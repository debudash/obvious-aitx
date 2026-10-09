"""Command-line interface: ingest, match, route, and the end-to-end demo.

The CLI is wiring (spec §Architecture, ``equinox.cli`` row): it builds the
venue adapters — the one venue-aware layer — stamps wall-clock fetch times,
and moves JSONL artifacts between the pure layers. Everything downstream of
``ingest`` is I/O-free computation: ``match`` and ``route`` call into
:mod:`equinox.matching` and :mod:`equinox.routing`, which never touch the
network, the filesystem, or a clock.

Artifact layout under an ingest ``--out-dir`` (that directory is also the
``--snapshot`` argument of ``match`` / ``route`` / ``demo``)::

    summary.json          run metadata + per-venue accounting (skips, errors)
    markets.jsonl         normalized markets, one JSON object per line
    raw/polymarket.jsonl  raw venue payloads (evidence; ``raw_ref`` targets)
    raw/kalshi.jsonl

JSONL is written with ``sort_keys=True`` and markets in the snapshot's
canonical order, so identical inputs produce byte-identical files (spec
§Resilience). ``route`` appends one trace line per invocation — JSONL is an
append log by design (spec §Routing, the reasoning log).

Exit codes: 0 on success — including a routing decision of ``chosen=None``
(the decision itself is the result, rejections included); 1 when
``ingest``/``demo`` collect no markets at all, or a local snapshot artifact
is missing or malformed.
"""

from __future__ import annotations

import argparse
import json
import sys
from collections import Counter
from collections.abc import Callable, Iterable, Mapping, Sequence
from datetime import UTC, datetime
from pathlib import Path
from typing import Any

from equinox.config import MatchConfig, RouterConfig
from equinox.matching import BAND_HIGH, BAND_MID, MatchCandidate, find_candidates
from equinox.model import (
    FeeParams,
    FetchError,
    Market,
    MarketSnapshot,
    Outcome,
    parse_utc,
)
from equinox.routing import RouteDecision, RouteIntent, decision_to_trace_line, route
from equinox.venues.base import (
    BaseVenueAdapter,
    CollectResult,
    JsonlFileSink,
    Transport,
    collect_results,
    snapshot_from_results,
)
from equinox.venues.kalshi import KalshiAdapter
from equinox.venues.polymarket import PolymarketAdapter

__all__ = [
    "SnapshotError",
    "main",
    "market_from_record",
    "market_to_record",
    "pair_ref",
]


class SnapshotError(ValueError):
    """A local snapshot artifact is missing, malformed, or inconsistent."""


# --- snapshot (de)serialization -------------------------------------------------
#
# The model package is intentionally I/O-free, so file (de)serialization of the
# shared shapes lives here, in the wiring layer. Records mirror ``Market`` 1:1;
# rebuilds delegate validation to the model constructors, so a malformed local
# snapshot raises the same error classes a venue record would.


def market_to_record(market: Market) -> dict[str, Any]:
    """JSON-safe record for one market; key set mirrors :class:`Market` 1:1."""
    return {
        "market_id": market.market_id,
        "venue": market.venue,
        "question": market.question,
        "outcomes": [
            {
                "name": outcome.name,
                "side": outcome.side,
                "yes_price": outcome.yes_price,
                "bid": outcome.bid,
                "ask": outcome.ask,
            }
            for outcome in market.outcomes
        ],
        "fees": {
            "model": market.fees.model,
            "rate": market.fees.rate,
            "source": market.fees.source,
        },
        "closes_at": market.closes_at.isoformat() if market.closes_at else None,
        "liquidity": market.liquidity,
        "fetched_at": market.fetched_at.isoformat(),
        "raw_ref": market.raw_ref,
    }


def _required_key(record: Mapping[str, Any], key: str) -> Any:
    value = record.get(key)
    if value is None or (isinstance(value, str) and not value.strip()):
        raise SnapshotError(f"record is missing required field {key!r}")
    return value


def market_from_record(record: Mapping[str, Any]) -> Market:
    """Rebuild a :class:`Market` from a :func:`market_to_record` record."""
    if not isinstance(record, Mapping):
        raise SnapshotError(f"market record must be a JSON object, got {type(record).__name__}")
    outcomes_raw = _required_key(record, "outcomes")
    if not isinstance(outcomes_raw, list):
        raise SnapshotError("market record 'outcomes' must be an array")
    fees_raw = _required_key(record, "fees")
    if not isinstance(fees_raw, Mapping):
        raise SnapshotError("market record 'fees' must be an object")
    fetched_at = parse_utc(_required_key(record, "fetched_at"))
    if fetched_at is None:
        raise SnapshotError("market record 'fetched_at' is required")

    outcomes: list[Outcome] = []
    for index, outcome_record in enumerate(outcomes_raw):
        if not isinstance(outcome_record, Mapping):
            raise SnapshotError(f"outcomes[{index}] must be a JSON object")
        outcomes.append(
            Outcome(
                name=str(_required_key(outcome_record, "name")),
                side=str(_required_key(outcome_record, "side")),
                yes_price=outcome_record.get("yes_price"),
                bid=outcome_record.get("bid"),
                ask=outcome_record.get("ask"),
            )
        )

    return Market(
        market_id=str(_required_key(record, "market_id")),
        venue=str(_required_key(record, "venue")),
        question=str(_required_key(record, "question")),
        outcomes=tuple(outcomes),
        fees=FeeParams(
            model=str(_required_key(fees_raw, "model")),
            rate=fees_raw.get("rate"),
            source=str(_required_key(fees_raw, "source")),
        ),
        closes_at=parse_utc(record.get("closes_at")),
        liquidity=record.get("liquidity"),
        fetched_at=fetched_at,
        raw_ref=str(_required_key(record, "raw_ref")),
    )


def write_jsonl(records: Iterable[Mapping[str, Any]], path: Path) -> int:
    """Write records as JSON lines (sorted keys); returns the record count."""
    path.parent.mkdir(parents=True, exist_ok=True)
    count = 0
    with path.open("w", encoding="utf-8") as stream:
        for record in records:
            stream.write(json.dumps(record, sort_keys=True) + "\n")
            count += 1
    return count


def read_jsonl(path: Path) -> list[dict[str, Any]]:
    """Parse a JSONL file; blank lines are skipped, malformed lines named."""
    records: list[dict[str, Any]] = []
    with path.open("r", encoding="utf-8") as stream:
        for line_number, line in enumerate(stream, start=1):
            stripped = line.strip()
            if not stripped:
                continue
            try:
                records.append(json.loads(stripped))
            except json.JSONDecodeError as exc:
                raise SnapshotError(f"{path}:{line_number}: malformed JSON line: {exc}") from exc
    return records


def build_summary(
    results: Sequence[CollectResult], *, fetched_at: datetime, limit: int
) -> dict[str, Any]:
    """Run metadata plus per-venue accounting — the ingest summary's shape."""
    return {
        "fetched_at": fetched_at.isoformat(),
        "limit_per_venue": limit,
        "venues": [result.venue for result in results],
        "degraded": [result.venue for result in results if result.degraded],
        "markets_total": sum(len(result.markets) for result in results),
        "per_venue": {
            result.venue: {
                "markets": len(result.markets),
                "skipped": len(result.skipped),
                "pages_fetched": result.pages_fetched,
                "pages_failed": result.pages_failed,
            }
            for result in results
        },
        "errors": [
            {
                "venue": error.venue,
                "url": error.url,
                "cause": error.cause,
                "attempts": error.attempts,
            }
            for result in results
            for error in result.errors
        ],
        "skipped": [
            {
                "venue": skipped.venue,
                "reason": skipped.reason,
                "raw_ref": skipped.raw_ref,
            }
            for result in results
            for skipped in result.skipped
        ],
    }


def read_snapshot_dir(snapshot_dir: Path) -> MarketSnapshot:
    """Load a snapshot from an ingest output directory.

    Reads ``summary.json`` (run metadata) plus ``markets.jsonl`` (markets).
    The raw evidence files are not re-read — ``raw_ref`` strings stay
    pointers, keeping snapshot loads cheap.
    """
    summary_path = snapshot_dir / "summary.json"
    markets_path = snapshot_dir / "markets.jsonl"
    for path in (summary_path, markets_path):
        if not path.is_file():
            raise SnapshotError(f"{path} not found — pass the ingest --out-dir as --snapshot")
    summary = json.loads(summary_path.read_text(encoding="utf-8"))
    if not isinstance(summary, Mapping):
        raise SnapshotError(f"{summary_path}: expected a JSON object")
    fetched_at = parse_utc(_required_key(summary, "fetched_at"))
    if fetched_at is None:
        raise SnapshotError(f"{summary_path}: 'fetched_at' is required")
    errors = [
        FetchError(
            venue=str(_required_key(error, "venue")),
            url=str(_required_key(error, "url")),
            cause=str(_required_key(error, "cause")),
            attempts=int(_required_key(error, "attempts")),
        )
        for error in summary.get("errors", [])
    ]
    return MarketSnapshot(
        fetched_at=fetched_at,
        venues=summary.get("venues", []),
        markets=[market_from_record(record) for record in read_jsonl(markets_path)],
        degraded=summary.get("degraded", []),
        errors=errors,
    )


# --- ingest core (shared by the ingest and demo commands) -----------------------


def build_adapters(
    evidence_dir: Path,
    *,
    transport: Transport | None = None,
    clock: Callable[[], datetime] | None = None,
) -> list[BaseVenueAdapter]:
    """Both venue adapters, writing raw evidence under *evidence_dir*.

    Test seam: *transport* and *clock* default to the real ones; tests inject
    a scripted transport and a frozen clock so the CLI wiring runs offline
    against recorded payloads.
    """
    return [
        PolymarketAdapter(
            sink=JsonlFileSink(evidence_dir / "polymarket.jsonl"),
            transport=transport,
            clock=clock,
        ),
        KalshiAdapter(
            sink=JsonlFileSink(evidence_dir / "kalshi.jsonl"),
            transport=transport,
            clock=clock,
        ),
    ]


def _run_ingest(
    out_dir: Path,
    limit: int,
    *,
    transport: Transport | None = None,
    clock: Callable[[], datetime] | None = None,
) -> tuple[dict[str, Any], MarketSnapshot]:
    """Collect both venues, write ``summary.json`` + ``markets.jsonl``.

    The only network-touching step of any command. Artifacts are overwritten
    per run; raw evidence under ``raw/`` appends (the sinks count existing
    lines, so ``raw_ref`` pointers stay unique across re-runs into the same
    directory).
    """
    adapters = build_adapters(out_dir / "raw", transport=transport, clock=clock)
    moment = clock() if clock is not None else datetime.now(UTC)
    results = collect_results(adapters, limit=limit)
    snapshot = snapshot_from_results(results, fetched_at=moment)
    summary = build_summary(results, fetched_at=moment, limit=limit)

    write_jsonl(
        (market_to_record(market) for market in snapshot.markets), out_dir / "markets.jsonl"
    )
    (out_dir / "summary.json").write_text(
        json.dumps(summary, indent=2, sort_keys=True) + "\n", encoding="utf-8"
    )
    return summary, snapshot


# --- command handlers ------------------------------------------------------------


def _print_ingest_summary(summary: Mapping[str, Any], snapshot: MarketSnapshot) -> None:
    print(f"ingest: {len(snapshot.markets)} market(s) at {summary['fetched_at']}")
    for venue, counts in summary["per_venue"].items():
        print(
            f"  {venue}: {counts['markets']} markets, {counts['skipped']} skipped, "
            f"pages {counts['pages_fetched']} fetched / {counts['pages_failed']} failed"
        )
    if summary["degraded"]:
        print(f"  degraded venues (page(s) lost after retries): {', '.join(summary['degraded'])}")
    for error in summary["errors"]:
        print(f"  error: {error['venue']}: {error['cause']} (attempts {error['attempts']})")


def cmd_ingest(args: argparse.Namespace) -> int:
    """``ingest``: fetch both venues into a normalized snapshot directory."""
    out_dir = Path(args.out_dir)
    summary, snapshot = _run_ingest(
        out_dir,
        args.limit,
        transport=getattr(args, "transport", None),
        clock=getattr(args, "clock", None),
    )
    _print_ingest_summary(summary, snapshot)
    return 0 if snapshot.markets else 1


def _print_match_summary(
    candidates: Sequence[MatchCandidate], snapshot: MarketSnapshot, out: Path
) -> None:
    bands = Counter(candidate.threshold_band for candidate in candidates)
    print(
        f"match: {len(candidates)} candidate pair(s) over {len(snapshot.markets)} market(s) "
        f"(high: {bands.get(BAND_HIGH, 0)}, mid: {bands.get(BAND_MID, 0)}) -> {out}"
    )
    for candidate in candidates[:10]:
        print(
            f"  {candidate.score:.4f} [{candidate.threshold_band}] "
            f"{candidate.a} <-> {candidate.b}"
        )


def cmd_match(args: argparse.Namespace) -> int:
    """``match``: ranked candidate pairs over a snapshot directory."""
    snapshot_dir = Path(args.snapshot)
    snapshot = read_snapshot_dir(snapshot_dir)
    candidates = find_candidates(snapshot, MatchConfig())
    out = Path(args.out) if args.out else snapshot_dir / "candidates.jsonl"
    write_jsonl((candidate.as_dict() for candidate in candidates), out)
    _print_match_summary(candidates, snapshot, out)
    return 0


def _print_decision_detail(decision: RouteDecision) -> None:
    for quote in decision.quotes:
        breakdown = quote.breakdown
        stale_note = " [stale]" if quote.stale else ""
        print(
            f"    {quote.venue}: all-in {quote.all_in_cost:.4f} = price "
            f"{breakdown['price']:.4f} + spread {breakdown['spread_impact']:.4f} "
            f"+ fee {breakdown['fee']:.4f}{stale_note}"
        )
    for step in decision.rationale:
        print(f"      | {step}")


def cmd_route(args: argparse.Namespace) -> int:
    """``route``: one simulated decision, appended to the trace JSONL."""
    snapshot_dir = Path(args.snapshot)
    snapshot = read_snapshot_dir(snapshot_dir)
    intent = RouteIntent(market_ref=args.ref, side=args.side, shares=args.shares)
    decision = route(intent, snapshot, RouterConfig())
    out = Path(args.out) if args.out else snapshot_dir / "route.jsonl"
    out.parent.mkdir(parents=True, exist_ok=True)
    with out.open("a", encoding="utf-8") as stream:
        stream.write(decision_to_trace_line(decision) + "\n")
    print(f"route: {decision.decision_id} -> {out}")
    print(f"  intent: {intent.market_ref} side={intent.side} shares={intent.shares}")
    _print_decision_detail(decision)
    print(f"  chosen: {decision.chosen if decision.chosen is not None else 'none'}")
    return 0


def pair_ref(a: str, b: str) -> str:
    """``pair:<id_a>:<id_b>`` router reference from two matcher refs.

    Matcher refs are ``"<venue>/<market_id>"``; the router's pair form
    carries the two venue-native market ids joined by ``:`` (safe for both
    venues: neither venue's ids contain colons).
    """
    return f"pair:{_market_id_of(a)}:{_market_id_of(b)}"


def _market_id_of(ref: str) -> str:
    return ref.split("/", 1)[1] if "/" in ref else ref


def write_traces(decisions: Sequence[RouteDecision], path: Path) -> int:
    """Write one JSON trace line per decision (the reasoning log)."""
    path.parent.mkdir(parents=True, exist_ok=True)
    with path.open("w", encoding="utf-8") as stream:
        for decision in decisions:
            stream.write(decision_to_trace_line(decision) + "\n")
    return len(decisions)


def cmd_demo(args: argparse.Namespace) -> int:
    """``demo``: ingest, match, route the top candidate pairs, summarize."""
    out_dir = Path(args.out_dir)
    print(
        f"== equinox demo: ingest({args.limit}/venue) -> match -> "
        f"route(top {args.routes} pairs) =="
    )
    summary, snapshot = _run_ingest(
        out_dir,
        args.limit,
        transport=getattr(args, "transport", None),
        clock=getattr(args, "clock", None),
    )
    _print_ingest_summary(summary, snapshot)
    if not snapshot.markets:
        print("demo: no markets ingested — nothing to match or route")
        return 1

    candidates = find_candidates(snapshot, MatchConfig())
    candidates_path = out_dir / "candidates.jsonl"
    write_jsonl((candidate.as_dict() for candidate in candidates), candidates_path)
    _print_match_summary(candidates, snapshot, candidates_path)

    decisions = [
        route(
            RouteIntent(
                market_ref=pair_ref(candidate.a, candidate.b),
                side="yes",
                shares=args.shares,
            ),
            snapshot,
            RouterConfig(),
        )
        for candidate in candidates[: args.routes]
    ]
    decisions_path = out_dir / "demo-decisions.jsonl"
    write_traces(decisions, decisions_path)
    print(
        f"route: {len(decisions)} pair intent(s) side=yes "
        f"shares={args.shares} -> {decisions_path}"
    )
    for index, decision in enumerate(decisions, start=1):
        print(f"  pair {index}/{len(decisions)}: {decision.intent.market_ref}")
        _print_decision_detail(decision)

    chosen = [decision.chosen for decision in decisions]
    for venue in sorted({name for name in chosen if name}):
        print(f"demo tally: chosen {venue}: {chosen.count(venue)}")
    if chosen.count(None):
        print(f"demo tally: chosen none (no routable venue): {chosen.count(None)}")
    print(
        f"demo artifacts in {out_dir}: summary.json, markets.jsonl, "
        f"candidates.jsonl, demo-decisions.jsonl, raw/*.jsonl"
    )
    return 0


# --- entry point -----------------------------------------------------------------


def _positive_int(text: str) -> int:
    try:
        value = int(text)
    except ValueError as exc:
        raise argparse.ArgumentTypeError(f"{text!r} is not an integer") from exc
    if value < 1:
        raise argparse.ArgumentTypeError(f"{text!r} must be >= 1")
    return value


def _build_parser() -> argparse.ArgumentParser:
    parser = argparse.ArgumentParser(
        prog="equinox",
        description=(
            "Cross-venue prediction market normalization and routing spike "
            "(strictly read-only: no keys, no auth, no orders)."
        ),
    )
    subparsers = parser.add_subparsers(dest="command", required=True)

    ingest = subparsers.add_parser(
        "ingest", help="fetch both venues into a normalized snapshot"
    )
    ingest.add_argument(
        "--limit", type=_positive_int, default=500, help="max raw records per venue (default 500)"
    )
    ingest.add_argument(
        "--out-dir", default="runs/ingest", help="artifact directory to write (default runs/ingest)"
    )
    ingest.set_defaults(func=cmd_ingest)

    match = subparsers.add_parser("match", help="rank candidate equivalent pairs over a snapshot")
    match.add_argument(
        "--snapshot", required=True, help="ingest --out-dir (summary.json + markets.jsonl)"
    )
    match.add_argument(
        "--out", default=None, help="candidates JSONL path (default <snapshot>/candidates.jsonl)"
    )
    match.set_defaults(func=cmd_match)

    route_cmd = subparsers.add_parser("route", help="simulate one routing decision")
    route_cmd.add_argument(
        "--snapshot", required=True, help="ingest --out-dir (summary.json + markets.jsonl)"
    )
    route_cmd.add_argument(
        "--ref", required=True, help="market id, or pair:<id_a>:<id_b> for a matched pair"
    )
    route_cmd.add_argument(
        "--side", choices=("yes", "no"), default="yes", help="intent side (default yes)"
    )
    route_cmd.add_argument(
        "--shares", type=_positive_int, default=100, help="simulated size in shares (default 100)"
    )
    route_cmd.add_argument(
        "--out",
        default=None,
        help="trace JSONL path (default <snapshot>/route.jsonl, appended)",
    )
    route_cmd.set_defaults(func=cmd_route)

    demo = subparsers.add_parser(
        "demo", help="end-to-end: ingest, match, route the top pairs, summarize"
    )
    demo.add_argument(
        "--limit", type=_positive_int, default=200, help="max raw records per venue (default 200)"
    )
    demo.add_argument(
        "--out-dir", default="runs/demo", help="artifact directory to write (default runs/demo)"
    )
    demo.add_argument(
        "--routes",
        type=_positive_int,
        default=5,
        help="how many top candidate pairs to route (default 5)",
    )
    demo.add_argument(
        "--shares", type=_positive_int, default=100, help="simulated size per intent (default 100)"
    )
    demo.set_defaults(func=cmd_demo)
    return parser


def main(argv: Sequence[str] | None = None) -> int:
    """Entry point: dispatch a subcommand; artifact errors exit 1, cleanly."""
    parser = _build_parser()
    args = parser.parse_args(argv)
    try:
        return int(args.func(args))
    except (ValueError, OSError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
