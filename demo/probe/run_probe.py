"""Aligned-slice diagnostic: category-aligned slices from both venues, run
through the unmodified adapters, matcher, and router.

The default venue listings sample disjoint event classes (see README, finding
2), so this probe re-pulls the same live payloads filtered server-side /
client-side to ONE shared event class — the 2027 pro-football championship:

- Kalshi: ``series_ticker=KXSB`` page (committed verbatim as
  ``kxsb-kalshi-page1.json``, fetched live 2026-10-09)
- Polymarket: the ``pro-football-2027-champion`` event's markets (committed
  verbatim as ``polymarket-champion-event.json``)

Run offline (uses the committed payloads):

    python3 demo/probe/run_probe.py

Re-pull live (both endpoints are public-read, no keys):

    KX_URL='https://api.elections.kalshi.com/trade-api/v2/markets'
    PM_URL='https://gamma-api.polymarket.com/events'
    curl -s "$KX_URL?series_ticker=KXSB&status=open&limit=100" \
        > demo/probe/kxsb-kalshi-page1.json
    curl -s "$PM_URL?slug=pro-football-2027-champion-20260729185915366" \
        > demo/probe/polymarket-champion-event.json

Read-only diagnostic — no orders, no keys, no auth.
"""

import json
import sys
from datetime import UTC, datetime
from pathlib import Path

HERE = Path(__file__).parent
REPO_ROOT = HERE.parents[1]
sys.path.insert(0, str(REPO_ROOT / "src"))

from equinox.cli import pair_ref  # noqa: E402
from equinox.config import MatchConfig, RouterConfig  # noqa: E402
from equinox.matching import find_candidates, market_ref  # noqa: E402
from equinox.routing import RouteIntent, decision_to_trace_line, route  # noqa: E402
from equinox.venues.base import (  # noqa: E402
    MemoryEvidenceSink,
    TransportResponse,
    collect_results,
    snapshot_from_results,
)
from equinox.venues.kalshi import KalshiAdapter  # noqa: E402
from equinox.venues.polymarket import PolymarketAdapter  # noqa: E402


class OnePageTransport:
    """Serves one JSON payload to every GET — enough for a single page."""

    def __init__(self, payload: object) -> None:
        self._payload = payload

    def get(self, url: str, *, params: dict[str, str], timeout: float) -> TransportResponse:
        return TransportResponse(status_code=200, text=json.dumps(self._payload))


def main() -> int:
    kx_envelope = json.loads((HERE / "kxsb-kalshi-page1.json").read_text())
    kx_envelope.pop("cursor", None)  # single page: end pagination after it
    pm_event = json.loads((HERE / "polymarket-champion-event.json").read_text())
    pm_event = pm_event[0] if isinstance(pm_event, list) else pm_event
    pm_markets = pm_event["events"][0]["markets"] if "events" in pm_event else pm_event["markets"]

    kx_adapter = KalshiAdapter(sink=MemoryEvidenceSink(), transport=OnePageTransport(kx_envelope))
    pm_adapter = PolymarketAdapter(
        sink=MemoryEvidenceSink(), transport=OnePageTransport(pm_markets)
    )
    results = collect_results([kx_adapter, pm_adapter], limit=40)
    for result in results:
        print(f"collect {result.venue}: {len(result.markets)} markets, {result.skipped} skipped")

    # The 2026-10-09T18:40Z fetch time of the committed payloads; fixing it
    # keeps the diagnostic's output byte-identical offline.
    fetched_at = datetime(2026, 10, 9, 18, 40, tzinfo=UTC)
    snapshot = snapshot_from_results(results, fetched_at=fetched_at)

    pairs = find_candidates(snapshot, MatchConfig())  # default thresholds
    print(f"\ndefault-threshold candidates: {len(pairs)}")
    by_ref = {market_ref(m): m for m in snapshot.markets}

    for candidate in pairs[:8]:
        a, b = by_ref[candidate.a], by_ref[candidate.b]
        print(f"\n  score {candidate.score:.3f} [{candidate.threshold_band}]")
        print(f"    KX: {a.question[:70]} ({a.market_id})")
        print(f"    PM: {b.question[:70]} ({b.market_id[:20]}…)")
        print(f"    rationale: {candidate.rationale}")
        print(f"    features: { {k: round(v, 3) for k, v in candidate.features.items()} }")

    (HERE / "candidates.jsonl").write_text(
        "".join(json.dumps(candidate.as_dict()) + "\n" for candidate in pairs),
        encoding="utf-8",
    )

    if not pairs:
        return 0

    # Route the best pair on the committed live quotes — real fees, real spreads.
    best = pairs[0]
    intent = RouteIntent(market_ref=pair_ref(best.a, best.b), side="yes", shares=100)
    decision = route(intent, snapshot, RouterConfig())
    print(f"\nroute decision for {intent.market_ref}: chosen={decision.chosen}")
    for step in decision.rationale:
        print(f"    {step}")
    for quote in decision.quotes:
        print(f"    quote {quote.venue}: {json.dumps(quote.breakdown)} stale={quote.stale}")
    (HERE / "decision.json").write_text(
        json.dumps(json.loads(decision_to_trace_line(decision)), indent=2),
        encoding="utf-8",
    )
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
