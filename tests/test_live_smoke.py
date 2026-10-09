"""Opt-in live smoke tests against the real venue APIs.

Run manually before merge — ``pytest -m live`` — never in CI (deselected
by default via pyproject ``addopts``; see the marker registration). These
are the human-runnable checks the spec's Verification table asks for: a
small live pull per venue asserting the adapters still match reality.
Read-only, no keys, market-data endpoints only.
"""

from datetime import UTC, datetime

import pytest

from equinox.venues.base import JsonlFileSink, collect_snapshot
from equinox.venues.kalshi import KalshiAdapter
from equinox.venues.polymarket import PolymarketAdapter

pytestmark = pytest.mark.live

LIVE_LIMIT = 25


def test_live_polymarket_ingest(tmp_path):
    adapter = PolymarketAdapter(sink=JsonlFileSink(tmp_path / "polymarket-live.jsonl"))
    result = adapter.collect(limit=LIVE_LIMIT)
    # A healthy day: markets come back, prices land on the shared scale.
    assert result.markets, "live Gamma returned no parseable markets"
    for market in result.markets:
        for outcome in market.outcomes:
            for value in (outcome.yes_price, outcome.bid, outcome.ask):
                assert value is None or 0 <= value <= 1


def test_live_kalshi_ingest(tmp_path):
    adapter = KalshiAdapter(sink=JsonlFileSink(tmp_path / "kalshi-live.jsonl"))
    result = adapter.collect(limit=LIVE_LIMIT)
    assert result.markets, "live Kalshi returned no parseable markets"
    for market in result.markets:
        for outcome in market.outcomes:
            for value in (outcome.yes_price, outcome.bid, outcome.ask):
                assert value is None or 0 <= value <= 1


def test_live_snapshot_spans_both_venues(tmp_path):
    snapshot = collect_snapshot(
        [
            PolymarketAdapter(sink=JsonlFileSink(tmp_path / "pm.jsonl")),
            KalshiAdapter(sink=JsonlFileSink(tmp_path / "kx.jsonl")),
        ],
        limit=LIVE_LIMIT,
        fetched_at=datetime.now(UTC),
    )
    assert set(snapshot.venues) == {"polymarket", "kalshi"}
    # Venues that answered are present with markets; anything degraded is
    # named on the snapshot rather than silently missing.
    healthy = set(snapshot.venues) - set(snapshot.degraded)
    assert healthy, f"both venues degraded: {snapshot.errors}"
    for market in snapshot.markets:
        assert market.venue in snapshot.venues
