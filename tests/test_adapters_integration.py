"""Cross-venue integration tests: both adapters, both fixtures, one snapshot.

These exercise the acceptance-criteria scenarios end to end — page
timeout/5xx surviving retries into a degraded snapshot, corrupt records
skipped and counted while the page continues, and one venue down leaving
the other untouched — using the recorded real-API fixtures, never the
network. The only network touchpoint in the repo is the opt-in live smoke
(tests/test_live_smoke.py, ``pytest -m live``) and the recorder script.

Ordering: MarketSnapshot applies the model's explicit total orders
(venues and degraded as sorted tuples, markets sorted by venue then
market_id), so every assertion here compares sets/sorted sequences.
"""

import json
from datetime import UTC, datetime

from conftest import FIXTURES_DIR, ScriptedTransport
from equinox.venues.base import (
    MemoryEvidenceSink,
    TransportFailure,
    TransportResponse,
    collect_snapshot,
)
from equinox.venues.kalshi import KalshiAdapter
from equinox.venues.polymarket import PolymarketAdapter

FROZEN_AT = datetime(2026, 10, 9, 12, 0, 0, tzinfo=UTC)
TIMEOUT_LINE = "ReadTimeout: timed out"


def polymarket_fixture() -> list:
    return json.loads((FIXTURES_DIR / "polymarket_markets.json").read_text())


def kalshi_fixture() -> dict:
    return json.loads((FIXTURES_DIR / "kalshi_markets.json").read_text())


def make_polymarket(records=None, *, script=None, **kwargs):
    """Build a polymarket adapter on a scripted transport.

    ``script`` replaces the default single good response carrying
    *records*; items are consumed in order and the last repeats when dry
    (conftest.ScriptedTransport).
    """
    if script is None:
        script = [TransportResponse(200, json.dumps(list(records)))]
    transport = ScriptedTransport().enqueue(*script)
    defaults = dict(
        sink=MemoryEvidenceSink(),
        transport=transport,
        clock=lambda: FROZEN_AT,
        sleep=lambda d: None,
    )
    defaults.update(kwargs)
    return PolymarketAdapter(**defaults), transport


def make_kalshi(records=None, *, script=None, **kwargs):
    if script is None:
        envelope = {"cursor": "C-1", "markets": list(records)}
        script = [TransportResponse(200, json.dumps(envelope))]
    transport = ScriptedTransport().enqueue(*script)
    defaults = dict(
        sink=MemoryEvidenceSink(),
        transport=transport,
        clock=lambda: FROZEN_AT,
        sleep=lambda d: None,
    )
    defaults.update(kwargs)
    return KalshiAdapter(**defaults), transport


class TestHealthySnapshot:
    def test_both_venues_ingest_into_one_snapshot(self):
        pm, _ = make_polymarket(polymarket_fixture())
        kx, _ = make_kalshi(kalshi_fixture()["markets"])
        snapshot = collect_snapshot([pm, kx], limit=10, fetched_at=FROZEN_AT)
        # Sorted tuple: the model's explicit total order, not insertion.
        assert snapshot.venues == ("kalshi", "polymarket")
        assert len(snapshot.markets) == 10  # 5 fixture records per venue
        assert snapshot.degraded == () and snapshot.errors == ()

    def test_market_ids_are_unique_across_venues(self):
        pm, _ = make_polymarket(polymarket_fixture())
        kx, _ = make_kalshi(kalshi_fixture()["markets"])
        snapshot = collect_snapshot([pm, kx], limit=10, fetched_at=FROZEN_AT)
        ids = [market.market_id for market in snapshot.markets]
        assert len(ids) == len(set(ids))
        assert {market.venue for market in snapshot.markets} == {"polymarket", "kalshi"}

    def test_both_scales_land_on_the_shared_0_1_scale(self):
        pm, _ = make_polymarket(polymarket_fixture())
        kx, _ = make_kalshi(kalshi_fixture()["markets"])
        snapshot = collect_snapshot([pm, kx], limit=10, fetched_at=FROZEN_AT)
        for market in snapshot.markets:
            for outcome in market.outcomes:
                for value in (outcome.yes_price, outcome.bid, outcome.ask):
                    if value is not None:
                        assert 0 <= value <= 1
        # Concretely: Polymarket decimals as-is, Kalshi dollars ÷ 1.0.
        by_venue = {market.venue: market for market in snapshot.markets}
        assert by_venue["polymarket"].outcomes[0].yes_price is not None
        assert 0 <= by_venue["polymarket"].outcomes[0].yes_price <= 1
        assert by_venue["kalshi"].outcomes[0].yes_price == 0.62


class TestFailureInjection:
    def test_timeout_exhausts_retries_into_a_degraded_snapshot(self):
        """Acceptance row: page timeout → retried ×3 → FetchError, snapshot
        degraded — for both pagination styles in one scenario. All-down
        scripts (only failures, repeated when dry): the offset adapter
        stops at its lost-page cap, the cursor adapter at the lost page.
        """
        pm, pm_transport = make_polymarket(
            page_size=2,
            script=[TransportFailure(TIMEOUT_LINE)],  # repeats when dry
        )
        kx, kx_transport = make_kalshi(
            page_size=2,
            script=[TransportFailure(TIMEOUT_LINE)],  # repeats when dry
        )
        snapshot = collect_snapshot([pm, kx], limit=4, fetched_at=FROZEN_AT)
        assert snapshot.markets == ()
        assert set(snapshot.degraded) == {"polymarket", "kalshi"}
        by_venue = {error.venue: error for error in snapshot.errors}
        assert by_venue["polymarket"].attempts == 4
        assert by_venue["kalshi"].attempts == 4
        assert "timed out" in by_venue["polymarket"].cause
        # Polymarket: four lost pages (the cap) × 4 attempts each.
        assert len(pm_transport.calls) == 16
        # Kalshi: one lost page — cursor pagination cannot advance.
        assert len(kx_transport.calls) == 4

    def test_5xx_degrades_the_same_way_after_retries(self):
        pm, _ = make_polymarket(
            page_size=2,
            script=[TransportResponse(500, '{"error":"boom"}')],  # repeats when dry
        )
        kx, _ = make_kalshi(kalshi_fixture()["markets"])
        snapshot = collect_snapshot([pm, kx], limit=10, fetched_at=FROZEN_AT)
        assert {market.venue for market in snapshot.markets} == {"kalshi"}
        assert len(snapshot.markets) == 5
        assert set(snapshot.degraded) == {"polymarket"}
        assert snapshot.errors[0].attempts == 4

    def test_corrupt_record_is_skipped_and_counted_while_the_page_continues(self):
        """Skip accounting lives on each CollectResult (the model snapshot
        carries markets/errors/degraded only), so this asserts there."""
        pm_records = polymarket_fixture()
        pm_records[1] = {"conditionId": "0xdamaged", "question": 42}  # wrong shapes
        kx_records = kalshi_fixture()["markets"]
        kx_records[1] = dict(kx_records[1], yes_bid_dollars="not-a-number")
        pm, _ = make_polymarket(pm_records)
        # Terminal empty cursor: exactly one page — no churn skips in the count.
        kx, _ = make_kalshi(
            kx_records,
            script=[
                TransportResponse(200, json.dumps({"cursor": "", "markets": kx_records}))
            ],
        )
        pm_result = pm.collect(limit=10)
        kx_result = kx.collect(limit=10)
        # The damaged member of each pair is skipped and counted; the rest
        # of the page continues (4 healthy markets per venue).
        assert len(pm_result.markets) == 4 and len(kx_result.markets) == 4
        assert len(pm_result.skipped) == 1 and len(kx_result.skipped) == 1
        assert pm_result.skipped[0].venue == "polymarket"
        assert kx_result.skipped[0].venue == "kalshi"
        # The damaged polymarket record fails at 'outcomes' (missing), the
        # damaged kalshi record at its quote scale.
        assert "outcomes" in pm_result.skipped[0].reason
        assert "yes_bid_dollars" in kx_result.skipped[0].reason

    def test_one_venue_down_leaves_the_other_proceeding(self):
        pm, _ = make_polymarket(
            script=[TransportFailure("connection refused")],  # repeats when dry
        )
        kx, _ = make_kalshi(kalshi_fixture()["markets"])
        snapshot = collect_snapshot([pm, kx], limit=10, fetched_at=FROZEN_AT)
        # Kalshi proceeds untouched; snapshot order is the model's total order.
        assert [market.market_id for market in snapshot.markets] == sorted(
            record["ticker"] for record in kalshi_fixture()["markets"]
        )
        assert set(snapshot.degraded) == {"polymarket"}
        assert snapshot.errors[0].venue == "polymarket"
        assert "connection refused" in snapshot.errors[0].cause

    def test_one_adapter_crashing_leaves_the_other_untouched(self):
        """A non-transport crash inside one adapter is an isolation-boundary
        event: recorded as a degraded venue with the crash in the cause,
        never propagated, never affecting the healthy venue."""

        class CrashingTransport:
            def get(self, url, params=None, timeout=None):
                raise RuntimeError("adapter bug: exploded")

        pm = PolymarketAdapter(
            sink=MemoryEvidenceSink(),
            transport=CrashingTransport(),
            clock=lambda: FROZEN_AT,
            sleep=lambda d: None,
        )
        kx, _ = make_kalshi(kalshi_fixture()["markets"])
        snapshot = collect_snapshot([pm, kx], limit=10, fetched_at=FROZEN_AT)
        assert [market.market_id for market in snapshot.markets] == sorted(
            record["ticker"] for record in kalshi_fixture()["markets"]
        )
        assert set(snapshot.degraded) == {"polymarket"}
        cause = snapshot.errors[0].cause
        assert cause.startswith("adapter crashed: RuntimeError")
        assert "exploded" in cause
        assert snapshot.errors[0].venue == "polymarket"


class TestEvidenceTrail:
    def test_every_market_points_at_its_venue_raw_record(self):
        pm_sink = MemoryEvidenceSink()
        kx_sink = MemoryEvidenceSink()
        pm, _ = make_polymarket(polymarket_fixture(), sink=pm_sink)
        # Terminal empty cursor: exactly one page, so the sink count is exact.
        kx, _ = make_kalshi(
            kalshi_fixture()["markets"],
            sink=kx_sink,
            script=[
                TransportResponse(
                    200, json.dumps({"cursor": "", "markets": kalshi_fixture()["markets"]})
                )
            ],
        )
        snapshot = collect_snapshot([pm, kx], limit=10, fetched_at=FROZEN_AT)
        for market in snapshot.markets:
            assert market.raw_ref.startswith("memory:")
            line = int(market.raw_ref.split(":")[1])
            sink = pm_sink if market.venue == "polymarket" else kx_sink
            assert 1 <= line <= len(sink.records)  # the ref resolves
        assert len(pm_sink.records) == 5 and len(kx_sink.records) == 5
