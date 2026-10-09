"""Kalshi adapter tests against the recorded trade-api fixture.

The fixture (tests/fixtures/kalshi_markets.json) is a trimmed, sanitized
cut of live Kalshi responses recorded 2026-10-09 — two NFL game markets
with real books plus three fresh MVE combo markets with zero quotes; see
scripts/record_fixtures.py for provenance. No test here touches the
network: payloads ride a scripted transport.
"""

import json
from datetime import UTC, datetime

from conftest import FIXTURES_DIR, ScriptedTransport
from equinox.model import FeeParams
from equinox.venues.base import (
    DEFAULT_TIMEOUT_SECONDS,
    MemoryEvidenceSink,
    TransportFailure,
    TransportResponse,
)
from equinox.venues.kalshi import KALSHI_BASE_URL, KALSHI_DEFAULT_FEE_PARAMS, KalshiAdapter

FROZEN_AT = datetime(2026, 10, 9, 12, 0, 0, tzinfo=UTC)


def fixture_envelope() -> dict:
    return json.loads((FIXTURES_DIR / "kalshi_markets.json").read_text())


def make_adapter(records, **kwargs):
    envelope = {"cursor": "C-1", "markets": list(records)}
    transport = ScriptedTransport().enqueue(TransportResponse(200, json.dumps(envelope)))
    defaults = dict(
        sink=MemoryEvidenceSink(),
        transport=transport,
        clock=lambda: FROZEN_AT,
        sleep=lambda d: None,
    )
    defaults.update(kwargs)
    return KalshiAdapter(**defaults), transport


def collect(records, **kwargs):
    adapter, transport = make_adapter(records, **kwargs)
    return adapter.collect(limit=len(records)), adapter, transport


class TestFixtureShape:
    def test_fixture_captures_dollar_string_quotes(self):
        """Dollar-denominated quotes arrive as strings (evidence F3)."""
        for record in fixture_envelope()["markets"]:
            assert isinstance(record["yes_bid_dollars"], str)
            assert isinstance(record["yes_ask_dollars"], str)

    def test_fixture_covers_live_books_and_zero_quote_records(self):
        records = fixture_envelope()["markets"]
        assert any(float(record["yes_bid_dollars"]) > 0 for record in records)
        assert any(float(record["yes_bid_dollars"]) == 0 for record in records)
        assert fixture_envelope()["cursor"]  # a real, non-empty cursor


class TestParsing:
    def test_fixture_parses_to_five_markets(self):
        result, _, _ = collect(fixture_envelope()["markets"])
        assert len(result.markets) == 5
        assert result.pages_fetched == 1
        assert result.errors == () and result.skipped == () and result.degraded is False

    def test_first_event_market_maps_field_by_field(self):
        record = fixture_envelope()["markets"][0]
        result, _, _ = collect([record])
        market = result.markets[0]
        assert market.market_id == "KXNFLGAME-26OCT12BUFLAR-LAR"
        assert market.venue == "kalshi"
        assert market.question == "Los Angeles R wins"
        yes, no = market.outcomes
        # Dollar strings divide by 1.0 into the shared 0–1 scale (F3).
        assert (yes.yes_price, yes.bid, yes.ask) == (0.62, 0.61, 0.62)
        assert (no.yes_price, no.bid, no.ask) == (None, 0.38, 0.39)
        assert market.closes_at is not None
        assert market.closes_at.isoformat() == "2026-10-15T00:15:00+00:00"
        assert market.liquidity is None  # no liquidity field on listings
        assert market.fetched_at == FROZEN_AT

    def test_fees_come_from_the_dated_config_default(self):
        record = fixture_envelope()["markets"][0]
        result, _, _ = collect([record])
        assert result.markets[0].fees == KALSHI_DEFAULT_FEE_PARAMS
        assert result.markets[0].fees.source == "config:2026-10-09"

    def test_custom_fee_params_override_the_default(self):
        record = fixture_envelope()["markets"][0]
        override = FeeParams(model="flat", rate=0.0, source="test")
        result, _, _ = collect([record], fee_params=override)
        assert result.markets[0].fees == override

    def test_zero_quote_market_kept_as_explicit_zeros(self):
        """Fresh MVE combo markets report zero quotes — that is venue data,
        kept as zeros; the absent liquidity field stays unknown (None)."""
        records = fixture_envelope()["markets"]
        record = next(r for r in records if r["ticker"].startswith("KXMVECROSSCATEGORY"))
        result, _, _ = collect([record])
        market = result.markets[0]
        yes, no = market.outcomes
        assert (yes.bid, yes.ask) == (0.0, 0.0)
        assert (no.bid, no.ask) == (1.0, 1.0)
        assert market.liquidity is None  # unknown, never zero


class TestContract:
    def test_status_open_filter_and_request_shape(self):
        _, _, transport = collect([fixture_envelope()["markets"][0]])
        call = transport.calls[0]
        assert call["url"] == f"{KALSHI_BASE_URL}/markets"
        assert call["params"] == {"status": "open", "limit": "1"}
        assert "cursor" not in call["params"]
        assert call["timeout"] == DEFAULT_TIMEOUT_SECONDS

    def test_cursor_pagination_follows_and_terminates(self):
        records = fixture_envelope()["markets"][:2]
        transport = ScriptedTransport()
        transport.enqueue(
            TransportResponse(200, json.dumps({"cursor": "C-2", "markets": records[:1]})),
            TransportResponse(200, json.dumps({"cursor": "", "markets": records[1:]})),
        )
        adapter = KalshiAdapter(
            sink=MemoryEvidenceSink(),
            transport=transport,
            clock=lambda: FROZEN_AT,
            sleep=lambda d: None,
        )
        result = adapter.collect(limit=2)
        assert [market.market_id for market in result.markets] == [
            record["ticker"] for record in records
        ]
        assert result.pages_fetched == 2
        assert transport.calls[1]["params"]["cursor"] == "C-2"  # followed verbatim

    def test_empty_cursor_ends_pagination_after_one_page(self):
        record = fixture_envelope()["markets"][0]
        transport = ScriptedTransport()
        transport.enqueue(TransportResponse(200, json.dumps({"cursor": "", "markets": [record]})))
        adapter = KalshiAdapter(
            sink=MemoryEvidenceSink(),
            transport=transport,
            clock=lambda: FROZEN_AT,
            sleep=lambda d: None,
        )
        result = adapter.collect(limit=10)
        assert len(result.markets) == 1
        assert result.pages_fetched == 1
        assert len(transport.calls) == 1  # no second call on an empty cursor

    def test_missing_cursor_key_ends_pagination(self):
        record = fixture_envelope()["markets"][0]
        transport = ScriptedTransport()
        transport.enqueue(TransportResponse(200, json.dumps({"markets": [record]})))
        adapter = KalshiAdapter(
            sink=MemoryEvidenceSink(),
            transport=transport,
            clock=lambda: FROZEN_AT,
            sleep=lambda d: None,
        )
        result = adapter.collect(limit=10)
        assert result.pages_fetched == 1
        assert len(transport.calls) == 1

    def test_failed_cursor_page_ends_collection_degraded(self):
        """Cursor pagination cannot skip past a lost page — the pointer to
        the next page was in the lost body. Collection stops, degraded."""
        record = fixture_envelope()["markets"][0]
        transport = ScriptedTransport()
        transport.enqueue(
            TransportFailure("ReadTimeout: timed out"),
            TransportFailure("ReadTimeout: timed out"),
            TransportFailure("ReadTimeout: timed out"),
            TransportFailure("ReadTimeout: timed out"),
            TransportResponse(200, json.dumps({"cursor": "", "markets": [record]})),
        )
        adapter = KalshiAdapter(
            sink=MemoryEvidenceSink(),
            transport=transport,
            clock=lambda: FROZEN_AT,
            sleep=lambda d: None,
        )
        result = adapter.collect(limit=5)
        assert result.degraded is True
        assert result.pages_failed == 1
        assert result.pages_fetched == 0
        assert result.markets == ()
        assert result.errors[0].attempts == 4
        # All four transport calls (initial + 3 retries) are one page's
        # retry budget at the same cursor; the good page behind it in the
        # script is never reached — collection stops after a lost page.
        assert len(transport.calls) == 4


class TestSkips:
    def test_non_active_status_is_skipped(self):
        records = fixture_envelope()["markets"][:2]
        records[0] = dict(records[0], status="settled")
        result, _, _ = collect(records)
        assert len(result.markets) == 1
        assert "not 'active'" in result.skipped[0].reason

    def test_non_binary_market_type_is_skipped(self):
        records = fixture_envelope()["markets"][:2]
        records[0] = dict(records[0], market_type="neg_risk")
        result, _, _ = collect(records)
        assert len(result.markets) == 1
        assert "market_type" in result.skipped[0].reason

    def test_out_of_scale_dollar_quote_is_skipped(self):
        records = fixture_envelope()["markets"][:2]
        records[0] = dict(records[0], yes_bid_dollars="62")  # cents as dollars
        result, _, _ = collect(records)
        assert len(result.markets) == 1
        assert "yes_bid_dollars" in result.skipped[0].reason
        assert "outside [0, 1]" in result.skipped[0].reason

    def test_missing_ticker_skips_record(self):
        records = fixture_envelope()["markets"][:2]
        del records[0]["ticker"]
        result, _, _ = collect(records)
        assert len(result.markets) == 1
        assert "ticker" in result.skipped[0].reason

    def test_duplicate_ticker_is_skipped(self):
        record = fixture_envelope()["markets"][0]
        result, _, _ = collect([record, record])
        assert len(result.markets) == 1
        assert "duplicate" in result.skipped[0].reason


class TestEvidence:
    def test_raw_refs_point_into_the_sink(self):
        record = fixture_envelope()["markets"][0]
        result, adapter, _ = collect([record])
        assert result.markets[0].raw_ref == "memory:1"
        assert adapter._sink.records[0] == record
