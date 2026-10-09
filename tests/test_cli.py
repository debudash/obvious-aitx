"""CLI wiring tests: artifact serialization, command behavior, determinism.

Everything runs on scripted transports serving the recorded fixture payloads —
no network in CI (the live path is ``pytest -m live`` plus the sandbox demo run
recorded under ``demo/``). The seam is explicit: ``_run_ingest`` and the
ingest/demo handlers accept injectable ``transport``/``clock``, defaulting to
the real ones; match/route only touch local snapshot artifacts.
"""

import json
from argparse import Namespace
from pathlib import Path

import pytest

from conftest import FROZEN_AT, MemoryClock, ScriptedTransport
from equinox.cli import (
    SnapshotError,
    _run_ingest,
    cmd_demo,
    cmd_ingest,
    cmd_match,
    cmd_route,
    main,
    market_from_record,
    market_to_record,
    pair_ref,
    read_jsonl,
    read_snapshot_dir,
)
from equinox.venues.base import MAX_PAGE_FAILURES, TransportResponse

FIXTURES_DIR = Path(__file__).parent / "fixtures"


class NotFoundTransport:
    """Always answers 404 — non-retryable, so no backoff sleeps in tests."""

    def get(self, url: str, *, params: dict[str, str], timeout: float) -> TransportResponse:
        return TransportResponse(status_code=404, text='{"error": "not found"}')


def _page(payload: object) -> TransportResponse:
    """HTTP 200 response wrapping a fixture payload — the transport's shape."""
    return TransportResponse(status_code=200, text=json.dumps(payload))


@pytest.fixture
def pm_page() -> list[dict]:
    return json.loads((FIXTURES_DIR / "polymarket_markets.json").read_text(encoding="utf-8"))


@pytest.fixture
def kx_envelope() -> dict:
    return json.loads((FIXTURES_DIR / "kalshi_markets.json").read_text(encoding="utf-8"))


@pytest.fixture
def scripted_transport(pm_page: list[dict], kx_envelope: dict) -> ScriptedTransport:
    """One Polymarket page then one Kalshi page; the last item repeats."""
    return ScriptedTransport().enqueue(_page(pm_page), _page(kx_envelope))


# --- serialization ----------------------------------------------------------


def test_market_record_round_trip(make_market) -> None:
    """A market survives record → model → record with identical field values."""
    market = make_market()
    assert market_from_record(market_to_record(market)) == market


def test_market_from_record_names_missing_fields() -> None:
    with pytest.raises(SnapshotError, match="missing required field"):
        market_from_record({"venue": "kalshi", "question": "?"})


def test_read_snapshot_dir_names_missing_artifacts(tmp_path: Path) -> None:
    with pytest.raises(SnapshotError, match="not found"):
        read_snapshot_dir(tmp_path / "nope")


# --- ingest -----------------------------------------------------------------


def test_ingest_writes_artifacts(tmp_path: Path, scripted_transport: ScriptedTransport) -> None:
    out_dir = tmp_path / "run"
    summary, snapshot = _run_ingest(
        out_dir, 5, transport=scripted_transport, clock=MemoryClock(FROZEN_AT)
    )

    assert len(snapshot.markets) == summary["markets_total"] == 10
    assert set(summary["per_venue"]) == {"polymarket", "kalshi"}
    assert summary["per_venue"]["polymarket"]["markets"] == 5
    assert summary["per_venue"]["polymarket"]["pages_fetched"] == 1
    assert summary["degraded"] == []
    assert summary["errors"] == []

    records = read_jsonl(out_dir / "markets.jsonl")
    assert [record["market_id"] for record in records] == [
        market.market_id for market in snapshot.markets
    ]
    # raw_ref pointers name the evidence file they were dumped to
    assert all(
        record["raw_ref"].startswith(str(out_dir / "raw")) for record in records
    )
    assert len((out_dir / "raw" / "polymarket.jsonl").read_text().splitlines()) == 5
    assert len((out_dir / "raw" / "kalshi.jsonl").read_text().splitlines()) == 5
    assert summary["fetched_at"] == FROZEN_AT.isoformat()


def test_ingest_exit_code_when_nothing_collected(tmp_path: Path, capsys) -> None:
    """A fully-down run exits 1 with the failure on the summary, not a raise."""
    args = Namespace(
        limit=5,
        out_dir=str(tmp_path / "run"),
        transport=NotFoundTransport(),
        clock=MemoryClock(FROZEN_AT),
    )
    assert cmd_ingest(args) == 1
    assert "degraded venues" in capsys.readouterr().out

    summary = json.loads((tmp_path / "run" / "summary.json").read_text(encoding="utf-8"))
    assert summary["markets_total"] == 0
    assert set(summary["degraded"]) == {"polymarket", "kalshi"}
    # Offset pagination keeps advancing past failed pages (bounded); cursor
    # pagination cannot, so Kalshi loses exactly one page before stopping.
    assert summary["per_venue"]["polymarket"]["pages_failed"] == MAX_PAGE_FAILURES
    assert summary["per_venue"]["kalshi"]["pages_failed"] == 1


# --- match ------------------------------------------------------------------


def test_match_command_writes_ranked_candidates(
    tmp_path: Path, scripted_transport: ScriptedTransport, capsys
) -> None:
    _run_ingest(tmp_path / "run", 5, transport=scripted_transport, clock=MemoryClock(FROZEN_AT))
    code = cmd_match(Namespace(snapshot=str(tmp_path / "run"), out=None))

    assert code == 0
    candidates = read_jsonl(tmp_path / "run" / "candidates.jsonl")
    for candidate in candidates:
        assert candidate["a"] < candidate["b"]  # explicit total order holds
        assert candidate["threshold_band"] in ("mid", "high")
        assert candidate["rationale"]
        assert set(candidate["features"]) == {
            "token_overlap",
            "idf_overlap",
            "numeric_agreement",
            "date_proximity",
        }
    assert "candidate pair(s)" in capsys.readouterr().out


def test_match_is_byte_identical_on_identical_input(
    tmp_path: Path, pm_page: list[dict], kx_envelope: dict
) -> None:
    """Two full ingest+match runs on the same payloads produce identical bytes."""
    outputs = []
    for run in ("a", "b"):
        out_dir = tmp_path / run
        transport = ScriptedTransport().enqueue(_page(pm_page), _page(kx_envelope))
        _run_ingest(out_dir, 5, transport=transport, clock=MemoryClock(FROZEN_AT))
        cmd_match(Namespace(snapshot=str(out_dir), out=None))
        outputs.append((out_dir / "candidates.jsonl").read_bytes())
    assert outputs[0] == outputs[1]


# --- route ------------------------------------------------------------------


def _routable_kalshi_market(snapshot) -> str:
    """First Kalshi market with a live (non-zero) ask — deterministic pick."""
    market = next(
        m
        for m in snapshot.markets
        if m.venue == "kalshi" and m.yes_ask is not None and m.yes_ask > 0
    )
    return market.market_id


def test_route_command_appends_deterministic_trace(
    tmp_path: Path, scripted_transport: ScriptedTransport
) -> None:
    _, snapshot = _run_ingest(
        tmp_path / "run", 5, transport=scripted_transport, clock=MemoryClock(FROZEN_AT)
    )
    ref = _routable_kalshi_market(snapshot)
    args = Namespace(
        snapshot=str(tmp_path / "run"), ref=ref, side="yes", shares=100, out=None
    )

    assert cmd_route(args) == 0
    assert cmd_route(args) == 0  # append mode: the log keeps both invocations

    lines = (tmp_path / "run" / "route.jsonl").read_text(encoding="utf-8").splitlines()
    assert len(lines) == 2
    assert lines[0] == lines[1]  # same inputs -> byte-identical decision
    trace = json.loads(lines[0])
    assert trace["intent"] == {"market_ref": ref, "side": "yes", "shares": 100}
    assert trace["chosen"] == "kalshi"
    assert any(step.startswith("chosen:") for step in trace["rationale"])
    # Single-market ref: the other venue cannot resolve and says why.
    assert trace["rejected"] == [
        {
            "venue": "polymarket",
            "reason": "no market in the snapshot resolves from this reference",
        }
    ]


def test_route_with_unresolvable_ref_still_decides(
    tmp_path: Path, scripted_transport: ScriptedTransport
) -> None:
    """A bogus reference is a decision with rejections, not a crash or exit 1."""
    _run_ingest(tmp_path / "run", 5, transport=scripted_transport, clock=MemoryClock(FROZEN_AT))
    args = Namespace(
        snapshot=str(tmp_path / "run"), ref="no-such-market", side="yes", shares=10, out=None
    )

    assert cmd_route(args) == 0
    trace = json.loads(
        (tmp_path / "run" / "route.jsonl").read_text(encoding="utf-8").splitlines()[0]
    )
    assert trace["chosen"] is None
    assert {rejection["venue"] for rejection in trace["rejected"]} == {
        "polymarket",
        "kalshi",
    }


def test_pair_ref_strips_venue_prefixes() -> None:
    assert pair_ref("kalshi/KX-1", "polymarket/0xabc") == "pair:KX-1:0xabc"


# --- demo -------------------------------------------------------------------


def test_demo_command_end_to_end(
    tmp_path: Path, pm_page: list[dict], kx_envelope: dict, capsys
) -> None:
    args = Namespace(
        limit=5,
        out_dir=str(tmp_path / "demo"),
        routes=3,
        shares=100,
        transport=ScriptedTransport().enqueue(_page(pm_page), _page(kx_envelope)),
        clock=MemoryClock(FROZEN_AT),
    )
    assert cmd_demo(args) == 0
    out = capsys.readouterr().out

    assert "ingest:" in out
    assert "match:" in out
    assert "route:" in out
    assert (tmp_path / "demo" / "candidates.jsonl").is_file()
    assert (tmp_path / "demo" / "demo-decisions.jsonl").is_file()
    # Every written trace is a well-formed JSON line with a rationale.
    for line in (tmp_path / "demo" / "demo-decisions.jsonl").read_text(
        encoding="utf-8"
    ).splitlines():
        trace = json.loads(line)
        assert trace["rationale"]
        assert trace["intent"]["side"] == "yes"


# --- entry point ------------------------------------------------------------


def test_main_help_exits_zero(capsys) -> None:
    with pytest.raises(SystemExit) as excinfo:
        main(["--help"])
    assert excinfo.value.code == 0
    assert "ingest" in capsys.readouterr().out


def test_main_reports_missing_snapshot_cleanly(tmp_path: Path, capsys) -> None:
    """A bad --snapshot is a one-line error and exit 1, not a traceback."""
    code = main(["match", "--snapshot", str(tmp_path / "nope")])
    assert code == 1
    assert "error:" in capsys.readouterr().err
