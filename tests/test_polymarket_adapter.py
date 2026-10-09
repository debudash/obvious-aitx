"""Polymarket adapter tests against the recorded Gamma fixture.

The fixture (tests/fixtures/polymarket_markets.json) is a trimmed,
sanitized cut of a live Gamma response recorded 2026-10-09 — see
scripts/record_fixtures.py for provenance. No test here touches the
network: payloads ride a scripted transport.
"""

import json
from datetime import UTC, datetime

from conftest import FIXTURES_DIR, ScriptedTransport
from equinox.venues.base import (
    DEFAULT_TIMEOUT_SECONDS,
    MemoryEvidenceSink,
    TransportFailure,
    TransportResponse,
)
from equinox.venues.polymarket import GAMMA_BASE_URL, PolymarketAdapter

FROZEN_AT = datetime(2026, 10, 9, 12, 0, 0, tzinfo=UTC)


def fixture_text() -> str:
    return (FIXTURES_DIR / "polymarket_markets.json").read_text()


def fixture_records() -> list[dict]:
    return json.loads(fixture_text())


def make_adapter(records, **kwargs):
    transport = ScriptedTransport().enqueue(TransportResponse(200, json.dumps(records)))
    defaults = dict(
        sink=MemoryEvidenceSink(),
        transport=transport,
        clock=lambda: FROZEN_AT,
        sleep=lambda d: None,
    )
    defaults.update(kwargs)
    return PolymarketAdapter(**defaults), transport


def collect(records, **kwargs):
    adapter, transport = make_adapter(records, **kwargs)
    return adapter.collect(limit=len(records)), adapter, transport


class TestFixtureShape:
    def test_fixture_captures_the_stringified_array_quirk(self):
        """The whole reason this adapter exists (evidence F2): the live API
        wraps outcomes/prices/token ids in JSON-encoded strings."""
        for record in fixture_records():
            assert isinstance(record["outcomes"], str)
            assert isinstance(record["outcomePrices"], str)

    def test_fixture_covers_fee_on_fee_off_and_quoteless_books(self):
        records = fixture_records()
        assert any(record["feesEnabled"] for record in records)
        assert any(not record["feesEnabled"] for record in records)
        # Gamma omits bestBid entirely on quoteless books rather than sending
        # null — the adapter must read optional fields with .get().
        assert any("bestBid" not in record for record in records)


class TestParsing:
    def test_fixture_parses_to_five_markets(self):
        result, _, _ = collect(fixture_records())
        assert len(result.markets) == 5
        assert result.pages_fetched == 1
        assert result.errors == () and result.skipped == () and result.degraded is False

    def test_first_fixture_record_maps_field_by_field(self):
        record = fixture_records()[0]
        result, _, _ = collect([record])
        market = result.markets[0]
        assert market.market_id == record["conditionId"]
        assert market.venue == "polymarket"
        assert market.question == "Xi Jinping out before 2027?"
        assert [outcome.name for outcome in market.outcomes] == ["Yes", "No"]
        assert [outcome.side for outcome in market.outcomes] == ["yes", "no"]
        # Prices are 0..1 decimals on the wire — used as-is (F2).
        assert market.outcomes[0].yes_price == 0.0305
        assert market.outcomes[1].yes_price == 0.9695
        assert market.liquidity == 548424.11338
        assert market.closes_at is not None
        assert market.closes_at.isoformat() == "2027-01-01T04:59:00+00:00"
        assert market.fetched_at == FROZEN_AT

    def test_yes_side_carries_venue_quotes_no_side_stays_unset(self):
        record = fixture_records()[0]
        result, _, _ = collect([record])
        yes, no = result.markets[0].outcomes
        assert (yes.bid, yes.ask) == (0.03, 0.031)  # bestBid/bestAsk quote YES
        assert no.bid is None and no.ask is None  # venue reports no NO quotes

    def test_quoteless_book_maps_to_none_not_zero(self):
        record = fixture_records()[1]  # bestBid null in the live payload
        result, _, _ = collect([record])
        yes = result.markets[0].outcomes[0]
        assert yes.bid is None
        assert yes.ask == 0.001

    def test_fee_enabled_market_reads_fee_schedule(self):
        record = fixture_records()[0]
        result, _, _ = collect([record])
        fees = result.markets[0].fees
        assert fees.model == "p_curve"
        assert fees.rate == 0.04
        assert fees.source == f"market:{record['conditionId']}:feeSchedule.rate"

    def test_fee_free_market_maps_to_none_model(self):
        record = fixture_records()[3]  # feesEnabled false in the live payload
        result, _, _ = collect([record])
        fees = result.markets[0].fees
        assert fees.model == "none"
        assert fees.rate is None
        assert fees.source == f"market:{record['conditionId']}"


class TestContract:
    def test_closed_false_filter_and_request_shape(self):
        _, _, transport = collect([fixture_records()[0]])
        call = transport.calls[0]
        assert call["url"] == f"{GAMMA_BASE_URL}/markets"
        assert call["params"] == {"limit": "1", "offset": "0", "closed": "false"}
        assert call["timeout"] == DEFAULT_TIMEOUT_SECONDS

    def test_offset_pagination_spans_two_pages(self):
        records = fixture_records()  # 5 records
        transport = ScriptedTransport()
        transport.enqueue(
            TransportResponse(200, json.dumps(records[:3])),
            TransportResponse(200, json.dumps(records[3:])),
        )
        adapter = PolymarketAdapter(
            sink=MemoryEvidenceSink(),
            transport=transport,
            clock=lambda: FROZEN_AT,
            sleep=lambda d: None,
            page_size=3,
        )
        result = adapter.collect(limit=5)
        assert [market.market_id for market in result.markets] == [
            record["conditionId"] for record in records
        ]
        assert result.pages_fetched == 2
        assert transport.calls[1]["params"]["offset"] == "3"

    def test_transient_failure_is_absorbed_by_retries(self):
        """One transport failure inside the retry budget never surfaces."""
        records = fixture_records()[:3]
        transport = ScriptedTransport()
        transport.enqueue(
            TransportFailure("ReadTimeout: timed out"),
            TransportResponse(200, json.dumps(records)),
        )
        adapter = PolymarketAdapter(
            sink=MemoryEvidenceSink(),
            transport=transport,
            clock=lambda: FROZEN_AT,
            sleep=lambda d: None,
        )
        result = adapter.collect(limit=3)
        assert result.degraded is False
        assert result.errors == () and result.pages_failed == 0
        assert len(result.markets) == 3

    def test_lost_offset_page_degrades_but_collect_continues(self):
        """Four consecutive transport failures (initial + 3 retries) lose the
        page: the snapshot says degraded, and the next offset still fetches."""
        records = fixture_records()[:3]
        transport = ScriptedTransport()
        transport.enqueue(
            TransportFailure("ReadTimeout: timed out"),
            TransportFailure("ReadTimeout: timed out"),
            TransportFailure("ReadTimeout: timed out"),
            TransportFailure("ReadTimeout: timed out"),
            TransportResponse(200, json.dumps(records)),
        )
        adapter = PolymarketAdapter(
            sink=MemoryEvidenceSink(),
            transport=transport,
            clock=lambda: FROZEN_AT,
            sleep=lambda d: None,
        )
        result = adapter.collect(limit=3)
        assert result.degraded is True
        assert result.pages_failed == 1
        assert result.pages_fetched == 1
        assert len(result.markets) == 3


class TestSkips:
    def test_corrupt_outcomeprices_skips_record(self):
        records = fixture_records()[:3]
        records[0]["outcomePrices"] = "not json at all"
        result, _, _ = collect(records)
        assert len(result.markets) == 2
        assert "outcomePrices" in result.skipped[0].reason

    def test_out_of_scale_price_skips_record(self):
        records = fixture_records()[:3]
        records[0]["outcomePrices"] = '["62", "38"]'  # cents, not probabilities
        result, _, _ = collect(records)
        assert len(result.markets) == 2
        assert "outcomePrices[0]" in result.skipped[0].reason
        assert "outside [0, 1]" in result.skipped[0].reason

    def test_outcome_price_length_mismatch_skips(self):
        records = fixture_records()[:2]
        records[0]["outcomes"] = '["Yes"]'
        records[0]["outcomePrices"] = '["0.5", "0.5"]'
        result, _, _ = collect(records)
        assert len(result.markets) == 1
        assert "lengths differ" in result.skipped[0].reason

    def test_missing_question_skips_record(self):
        records = fixture_records()[:2]
        del records[0]["question"]
        result, _, _ = collect(records)
        assert len(result.markets) == 1
        assert "question" in result.skipped[0].reason

    def test_leaked_closed_market_is_skipped(self):
        records = [dict(fixture_records()[0], closed=True)]
        result, _, _ = collect(records)
        assert result.markets == () and len(result.skipped) == 1

    def test_duplicate_condition_id_is_skipped(self):
        record = fixture_records()[0]
        result, _, _ = collect([record, record])
        assert len(result.markets) == 1
        assert "duplicate" in result.skipped[0].reason

    def test_named_outcomes_keep_other_side(self):
        """Named two-way markets are not force-fit onto yes/no sides."""
        record = dict(fixture_records()[0])
        record["outcomes"] = '["Democrat", "Republican"]'
        record["outcomePrices"] = '["0.52", "0.48"]'
        result, _, _ = collect([record])
        sides = {outcome.name: outcome.side for outcome in result.markets[0].outcomes}
        assert sides == {"Democrat": "other", "Republican": "other"}


class TestEvidence:
    def test_raw_refs_point_into_the_sink(self):
        record = fixture_records()[0]
        result, adapter, _ = collect([record])
        assert result.markets[0].raw_ref == "memory:1"
        assert adapter._sink.records[0] == record
