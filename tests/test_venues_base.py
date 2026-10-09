"""Failure injection and contract tests for the venue adapter base layer.

Every failure path is exercised through a scripted transport or a stub
adapter — no test in the default suite touches the live network; the opt-in
smoke test lives in ``test_live_smoke.py``.
"""

import json
from datetime import UTC, datetime

import pytest
import requests as requests_lib

from conftest import ScriptedTransport, SleepRecorder
from equinox.model import FeeParams, Market, MarketSnapshot, Outcome
from equinox.venues.base import (
    BACKOFF_BASE_SECONDS,
    DEFAULT_TIMEOUT_SECONDS,
    MAX_PAGE_FAILURES,
    BaseVenueAdapter,
    CollectResult,
    FetchFailure,
    JsonlFileSink,
    MemoryEvidenceSink,
    RequestsTransport,
    TransportFailure,
    TransportResponse,
    collect_snapshot,
    get_json_with_retries,
)

FROZEN_AT = datetime(2026, 10, 9, 12, 0, 0, tzinfo=UTC)
TEST_FEES = FeeParams(model="flat", rate=0.0, source="test")


def ok_json(payload: object) -> TransportResponse:
    return TransportResponse(status_code=200, text=json.dumps(payload))


class StubAdapter(BaseVenueAdapter):
    """Minimal adapter over canned pages — exercises the shared plumbing."""

    venue = "stub"
    base_url = "https://stub.example/v1"
    continue_after_page_failure = True

    def __init__(
        self,
        *,
        pages: list[list[dict]],
        fail_records: set[int] | None = None,
        **kwargs,
    ) -> None:
        super().__init__(**kwargs)
        self._pages = pages
        self._page_index = 0
        self._fail_records = fail_records or set()

    def fetch_page(self, page_state, budget):
        if self._page_index >= len(self._pages):
            return [], None
        records = self._pages[self._page_index]
        self._page_index += 1
        return records, None

    def pagination_done(self, next_state, records_in_page, budget):
        return self._page_index >= len(self._pages)

    def parse_record(self, raw, *, raw_ref, fetched_at):
        index = raw.get("n", 0)
        if index in self._fail_records:
            raise ValueError(f"record {index} is corrupt")
        return Market(
            market_id=f"stub-{index}",
            venue=self.venue,
            question=f"Stub question {index}?",
            outcomes=(Outcome(name="Yes", side="yes", yes_price=0.5),),
            fees=TEST_FEES,
            closes_at=None,
            liquidity=None,
            fetched_at=fetched_at,
            raw_ref=raw_ref,
        )


def make_adapter(**kwargs) -> StubAdapter:
    defaults: dict = dict(
        sink=MemoryEvidenceSink(), clock=lambda: FROZEN_AT, sleep=lambda d: None
    )
    defaults.update(kwargs)
    return StubAdapter(**defaults)


class CrashingAdapter:
    """Adapter whose collect raises — the one-venue-down isolation case."""

    venue = "crashing"
    base_url = "https://crashing.example"

    def collect(self, limit: int = 500) -> CollectResult:
        raise RuntimeError("venue exploded")


class FailingPageAdapter(StubAdapter):
    """Every page fails after retries (transport exhaustion upstream)."""

    def fetch_page(self, page_state, budget):
        raise FetchFailure("HTTP 503 from stub", attempts=4)


class FailingOnceCursorAdapter(StubAdapter):
    """Cursor venue: first page OK, second page lost — no way to continue."""

    continue_after_page_failure = False

    def __init__(self, **kwargs) -> None:
        super().__init__(**kwargs)
        self._calls = 0

    def fetch_page(self, page_state, budget):
        self._calls += 1
        if self._calls == 1:
            return self._pages[0], "cursor-2"
        raise FetchFailure("HTTP 500 from stub", attempts=4)

    def pagination_done(self, next_state, records_in_page, budget):
        return next_state is None


class TestRetryWrapper:
    def test_retries_transport_failure_then_succeeds(self):
        transport = ScriptedTransport()
        sleeps = SleepRecorder()
        transport.enqueue(
            TransportFailure("ReadTimeout: timed out"),
            TransportFailure("ConnectionError: reset by peer"),
            ok_json({"fine": True}),
        )
        payload = get_json_with_retries(transport, "https://stub.example/v1", {}, sleep=sleeps)
        assert payload == {"fine": True}
        assert sleeps.delays == [0.5, 1.0]  # base * 2**(attempt-1): deterministic

    def test_timeout_exhausts_retries(self):
        transport = ScriptedTransport().enqueue(TransportFailure("ReadTimeout: timed out"))
        with pytest.raises(FetchFailure) as excinfo:
            get_json_with_retries(transport, "https://stub.example/x", {}, sleep=lambda d: None)
        assert excinfo.value.attempts == 4  # initial attempt + MAX_RETRIES retries
        assert "transport failure" in excinfo.value.cause

    def test_http_500_retried_then_exhausted(self):
        transport = ScriptedTransport().enqueue(TransportResponse(500, "boom"))
        with pytest.raises(FetchFailure) as excinfo:
            get_json_with_retries(transport, "https://stub.example/x", {}, sleep=lambda d: None)
        assert excinfo.value.attempts == 4
        assert "HTTP 500" in excinfo.value.cause

    def test_http_404_fails_immediately(self):
        transport = ScriptedTransport().enqueue(TransportResponse(404, "nope"))
        sleeps = SleepRecorder()
        with pytest.raises(FetchFailure) as excinfo:
            get_json_with_retries(transport, "https://stub.example/x", {}, sleep=sleeps)
        assert excinfo.value.attempts == 1
        assert excinfo.value.retryable is False
        assert len(transport.calls) == 1  # no retry on a client error
        assert sleeps.delays == []

    def test_http_429_is_retried(self):
        transport = ScriptedTransport().enqueue(TransportResponse(429, "slow down"))
        with pytest.raises(FetchFailure) as excinfo:
            get_json_with_retries(transport, "https://stub.example/x", {}, sleep=lambda d: None)
        assert excinfo.value.attempts == 4

    def test_malformed_json_body_is_retried(self):
        transport = ScriptedTransport().enqueue(TransportResponse(200, "<html>gateway</html>"))
        with pytest.raises(FetchFailure) as excinfo:
            get_json_with_retries(transport, "https://stub.example/x", {}, sleep=lambda d: None)
        assert "malformed JSON" in excinfo.value.cause
        assert excinfo.value.attempts == 4

    def test_backoff_schedule_is_deterministic(self):
        delays_per_run = []
        for _ in range(2):
            transport = ScriptedTransport().enqueue(TransportFailure("ReadTimeout: x"))
            sleeps = SleepRecorder()
            with pytest.raises(FetchFailure):
                get_json_with_retries(
                    transport,
                    "https://stub.example/x",
                    {},
                    backoff_base=BACKOFF_BASE_SECONDS,
                    sleep=sleeps,
                )
            delays_per_run.append(sleeps.delays)
        assert delays_per_run[0] == delays_per_run[1] == [0.5, 1.0, 2.0]

    def test_every_request_carries_the_timeout(self):
        transport = ScriptedTransport().enqueue(ok_json([]))
        get_json_with_retries(transport, "https://stub.example/x", {}, timeout=7.5)
        assert transport.calls[0]["timeout"] == 7.5


class TestCollectPlumbing:
    def test_pages_are_consumed_until_limit(self):
        pages = [[{"n": 1}, {"n": 2}], [{"n": 3}, {"n": 4}], [{"n": 5}]]
        result = make_adapter(pages=pages, page_size=2).collect(limit=4)
        assert [m.market_id for m in result.markets] == ["stub-1", "stub-2", "stub-3", "stub-4"]
        assert result.pages_fetched == 2

    def test_corrupt_record_is_skipped_and_counted(self):
        pages = [[{"n": 1}, {"n": 2}, {"n": 3}]]
        result = make_adapter(pages=pages, fail_records={2}).collect(limit=10)
        assert [m.market_id for m in result.markets] == ["stub-1", "stub-3"]
        assert len(result.skipped) == 1
        assert result.skipped[0].reason == "record 2 is corrupt"
        assert result.skipped[0].raw_ref == "memory:2"
        assert result.pages_fetched == 1

    def test_duplicate_market_id_is_skipped(self):
        result = make_adapter(pages=[[{"n": 1}, {"n": 1}]]).collect(limit=10)
        assert len(result.markets) == 1
        assert "duplicate" in result.skipped[0].reason

    def test_page_failure_becomes_fetcherror_and_degrades(self):
        adapter = FailingPageAdapter(
            sink=MemoryEvidenceSink(),
            clock=lambda: FROZEN_AT,
            sleep=lambda d: None,
            pages=[],
        )
        result = adapter.collect(limit=10)
        assert result.markets == ()
        assert result.degraded is True
        assert result.errors[0].attempts == 4
        assert result.errors[0].cause == "HTTP 503 from stub"
        # The offset venue keeps advancing until MAX_PAGE_FAILURES says the
        # venue is down — four lost pages, then stop and report.
        assert result.pages_failed == MAX_PAGE_FAILURES
        assert len(result.errors) == MAX_PAGE_FAILURES

    def test_cursor_adapter_stops_after_page_failure(self):
        adapter = FailingOnceCursorAdapter(
            sink=MemoryEvidenceSink(),
            clock=lambda: FROZEN_AT,
            sleep=lambda d: None,
            pages=[[{"n": 1}]],
        )
        result = adapter.collect(limit=10)
        assert [m.market_id for m in result.markets] == ["stub-1"]
        assert result.pages_failed == 1
        assert result.pages_fetched == 1  # the failed page ends pagination


class TestCollectSnapshot:
    def test_one_adapter_raising_leaves_the_other_untouched(self):
        healthy = make_adapter(pages=[[{"n": 1}, {"n": 2}]])
        snapshot = collect_snapshot(
            [CrashingAdapter(), healthy], limit=10, fetched_at=FROZEN_AT
        )
        assert isinstance(snapshot, MarketSnapshot)
        assert [m.market_id for m in snapshot.markets] == ["stub-1", "stub-2"]
        assert set(snapshot.venues) == {"crashing", "stub"}
        assert snapshot.is_degraded("crashing")
        assert not snapshot.is_degraded("stub")
        error = next(e for e in snapshot.errors if e.venue == "crashing")
        assert error.cause == "adapter crashed: RuntimeError: venue exploded"

    def test_degraded_page_marks_venue_on_snapshot(self):
        adapter = FailingOnceCursorAdapter(
            sink=MemoryEvidenceSink(),
            clock=lambda: FROZEN_AT,
            sleep=lambda d: None,
            pages=[[{"n": 1}]],
        )
        snapshot = collect_snapshot([adapter], limit=10, fetched_at=FROZEN_AT)
        assert snapshot.is_degraded("stub")
        assert len(snapshot.markets) == 1  # the healthy page survived

    def test_healthy_snapshot_is_not_degraded(self):
        adapter = make_adapter(pages=[[{"n": 1}]])
        snapshot = collect_snapshot([adapter], limit=10, fetched_at=FROZEN_AT)
        assert snapshot.degraded == ()
        assert snapshot.errors == ()


class TestSinks:
    def test_jsonl_sink_round_trips(self, tmp_path):
        sink = JsonlFileSink(tmp_path / "evidence" / "stub.jsonl")
        ref1 = sink.write({"n": 1, "q": "first?"})
        ref2 = sink.write({"n": 2, "q": "second?"})
        assert ref1.endswith(":1") and ref2.endswith(":2")
        lines = (tmp_path / "evidence" / "stub.jsonl").read_text().splitlines()
        assert [json.loads(line) for line in lines] == [
            {"n": 1, "q": "first?"},
            {"n": 2, "q": "second?"},
        ]

    def test_jsonl_sink_refs_continue_across_runs(self, tmp_path):
        path = tmp_path / "stub.jsonl"
        JsonlFileSink(path).write({"n": 1})
        ref = JsonlFileSink(path).write({"n": 2})
        assert ref.endswith(":2")  # append, don't clobber existing evidence

    def test_memory_sink_returns_line_refs(self):
        sink = MemoryEvidenceSink()
        assert sink.write({"a": 1}) == "memory:1"
        assert sink.records == [{"a": 1}]


class TestLimits:
    @pytest.mark.parametrize("bad_limit", [0, -1, True, "5", 1.0])
    def test_nonpositive_limit_rejected(self, bad_limit):
        with pytest.raises(ValueError):
            make_adapter(pages=[]).collect(limit=bad_limit)

    @pytest.mark.parametrize("bad_page_size", [0, -1, 1001])
    def test_bad_page_size_rejected(self, bad_page_size):
        with pytest.raises(ValueError):
            make_adapter(pages=[], page_size=bad_page_size)


class TestRequestsTransport:
    def test_maps_request_exceptions_to_transport_failure(self):
        class ExplodingSession:
            def get(self, url, params=None, timeout=None):
                raise requests_lib.Timeout("timed out")

        transport = RequestsTransport(session=ExplodingSession())
        with pytest.raises(TransportFailure, match="Timeout"):
            transport.get("https://stub.example", params={}, timeout=1.0)

    def test_returns_transport_response(self):
        class StubSession:
            def get(self, url, params=None, timeout=None):
                assert timeout == 3.0
                response = requests_lib.Response()
                response.status_code = 200
                response._content = b'{"ok": true}'
                return response

        transport = RequestsTransport(session=StubSession())
        response = transport.get("https://stub.example", params={"a": "1"}, timeout=3.0)
        assert response.status_code == 200
        assert response.json() == {"ok": True}


def test_default_timeout_matches_the_spec():
    assert DEFAULT_TIMEOUT_SECONDS == 10.0
