"""End-to-end fixture pipeline: CLI artifacts -> matching -> routing.

Drives the same handler chain the argv entry point runs. Ingest is replaced
by writing the recorded matching corpus through the CLI's own serialization
(``market_to_record`` -> ``markets.jsonl``), so the byte-level record path is
exercised end to end: the seeded equivalents must survive the snapshot
round trip, routing must quote both venues on a matched pair, and reruns
must be byte-identical. No network.
"""

import json
from argparse import Namespace
from pathlib import Path

import pytest

from conftest import FROZEN_AT
from equinox.cli import (
    cmd_match,
    cmd_route,
    market_to_record,
    pair_ref,
    read_jsonl,
    write_jsonl,
)


def _canonical_pair_ref(a: str, b: str) -> str:
    return pair_ref(*sorted((a, b)))


@pytest.fixture
def snapshot_dir(tmp_path: Path, matching_corpus: list) -> Path:
    """A snapshot directory holding the full recorded corpus, CLI-serialized."""
    out_dir = tmp_path / "snapshot"
    out_dir.mkdir()
    write_jsonl(
        (market_to_record(market) for market in matching_corpus),
        out_dir / "markets.jsonl",
    )
    (out_dir / "summary.json").write_text(
        json.dumps(
            {
                "fetched_at": FROZEN_AT.isoformat(),
                "venues": ["polymarket", "kalshi"],
                "markets_total": len(matching_corpus),
            }
        ),
        encoding="utf-8",
    )
    return out_dir


def test_seeded_equivalents_survive_the_artifact_round_trip(
    snapshot_dir: Path, seeded_pairs: dict
) -> None:
    assert cmd_match(Namespace(snapshot=str(snapshot_dir), out=None)) == 0

    candidates = read_jsonl(snapshot_dir / "candidates.jsonl")
    found = {
        _canonical_pair_ref(candidate["a"], candidate["b"]) for candidate in candidates
    }
    missed = [
        seed["note"]
        for seed in seeded_pairs["equivalent"]
        if _canonical_pair_ref(
            f"{seed['a']['venue']}/{seed['a']['market_id']}",
            f"{seed['b']['venue']}/{seed['b']['market_id']}",
        )
        not in found
    ]
    assert not missed, f"seeded equivalents missed through CLI artifacts: {missed}"


@pytest.fixture
def quotable_snapshot_dir(tmp_path: Path, make_market, make_outcome) -> Path:
    """Two-venue snapshot of one event with real quotes, CLI-serialized.

    The matching corpus carries neutral quote-free outcomes (matching needs
    no quotes), so routing tests get their own snapshot with prices.
    """
    markets = [
        make_market(
            venue="polymarket",
            market_id="0xfedpairpm",
            question="Will the Fed hold rates in October?",
            outcomes=(make_outcome(),),
        ),
        make_market(
            venue="kalshi",
            market_id="KXFED-26-OCT",
            question="Fed holds rates in October?",
            outcomes=(make_outcome(yes_price=0.61, bid=0.60, ask=0.62),),
            liquidity=99_000.0,
        ),
    ]
    out_dir = tmp_path / "quotable"
    out_dir.mkdir()
    write_jsonl(
        (market_to_record(market) for market in markets),
        out_dir / "markets.jsonl",
    )
    (out_dir / "summary.json").write_text(
        json.dumps({"fetched_at": FROZEN_AT.isoformat()}), encoding="utf-8"
    )
    return out_dir


def test_routing_top_pair_quotes_both_venues(quotable_snapshot_dir: Path) -> None:
    ref = pair_ref("polymarket/0xfedpairpm", "kalshi/KXFED-26-OCT")
    assert (
        cmd_route(
            Namespace(
                snapshot=str(quotable_snapshot_dir), ref=ref, side="yes", shares=100, out=None
            )
        )
        == 0
    )

    trace = json.loads(
        (quotable_snapshot_dir / "route.jsonl").read_text(encoding="utf-8").splitlines()[0]
    )
    assert trace["intent"] == {"market_ref": ref, "side": "yes", "shares": 100}
    assert trace["chosen"] in ("polymarket", "kalshi")
    assert len(trace["quotes"]) == 2  # both venues resolved from the pair ref
    assert {quote["venue"] for quote in trace["quotes"]} == {"polymarket", "kalshi"}
    assert any(step.startswith("chosen:") for step in trace["rationale"])


def test_pipeline_rerun_is_byte_identical(tmp_path: Path, matching_corpus: list) -> None:
    outputs = []
    for run in ("a", "b"):
        out_dir = tmp_path / run
        out_dir.mkdir()
        write_jsonl(
            (market_to_record(market) for market in matching_corpus),
            out_dir / "markets.jsonl",
        )
        (out_dir / "summary.json").write_text(
            json.dumps({"fetched_at": FROZEN_AT.isoformat()}), encoding="utf-8"
        )
        assert cmd_match(Namespace(snapshot=str(out_dir), out=None)) == 0
        outputs.append((out_dir / "candidates.jsonl").read_bytes())

    assert outputs[0] == outputs[1]
