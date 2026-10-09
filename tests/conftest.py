"""Shared factories: valid model objects with per-test overrides."""

from datetime import UTC, datetime
from pathlib import Path

import pytest

from equinox.model import FeeParams, Market, Outcome
from equinox.venues.base import TransportResponse

FROZEN_AT = datetime(2026, 10, 9, 12, 0, 0, tzinfo=UTC)
"""Fixed stamp standing in for the adapter's fetch time — no clock reads."""


@pytest.fixture
def make_fee_params():
    """Factory for FeeParams with per-test overrides."""

    def _make(**overrides) -> FeeParams:
        defaults = dict(model="p_curve", rate=0.07, source="config:2026-10-09")
        defaults.update(overrides)
        return FeeParams(**defaults)

    return _make


@pytest.fixture
def make_outcome():
    """Factory for a YES-side Outcome with per-test overrides."""

    def _make(**overrides) -> Outcome:
        defaults = dict(name="Yes", side="yes", yes_price=0.62, bid=0.61, ask=0.63)
        defaults.update(overrides)
        return Outcome(**defaults)

    return _make


@pytest.fixture
def make_market(make_fee_params, make_outcome):
    """Factory for a binary Market (YES + NO outcomes) with per-test overrides."""

    def _make(**overrides) -> Market:
        defaults = dict(
            market_id="pm-fed-october",
            venue="polymarket",
            question="Will the Fed hold rates in October?",
            outcomes=(
                make_outcome(),
                make_outcome(name="No", side="no", yes_price=0.38, bid=0.37, ask=0.39),
            ),
            fees=make_fee_params(),
            closes_at=datetime(2026, 10, 28, 18, 0, 0, tzinfo=UTC),
            liquidity=12_500.0,
            fetched_at=FROZEN_AT,
            raw_ref="raw/polymarket-2026-10-09.jsonl:17",
        )
        defaults.update(overrides)
        return Market(**defaults)

    return _make


# --- venue adapter test doubles -------------------------------------------

FIXTURES_DIR = Path(__file__).parent / "fixtures"
"""Recorded real-API payloads (trimmed and sanitized); see scripts/record_fixtures.py."""


class ScriptedTransport:
    """Transport double serving a scripted sequence of responses.

    Each ``get`` pops the next scripted item — a :class:`TransportResponse`
    or an exception instance to raise. When the script runs dry the last
    item repeats, so exhaustion tests enqueue a single failure. Records every
    call (url, params, timeout) for assertions.
    """

    def __init__(self) -> None:
        self.script: list[object] = []
        self.calls: list[dict[str, object]] = []

    def enqueue(self, *items: object) -> "ScriptedTransport":
        self.script.extend(items)
        return self

    def get(self, url: str, *, params: dict[str, str], timeout: float) -> object:
        self.calls.append({"url": url, "params": dict(params), "timeout": timeout})
        if not self.script:
            raise AssertionError("ScriptedTransport received a call with an empty script")
        item = self.script.pop(0) if len(self.script) > 1 else self.script[0]
        if isinstance(item, Exception):
            raise item
        assert isinstance(item, TransportResponse), f"bad scripted item: {item!r}"
        return item


class MemoryClock:
    """Clock returning the same frozen instant on every call."""

    def __init__(self, moment: datetime) -> None:
        self.moment = moment
        self.reads = 0

    def __call__(self) -> datetime:
        self.reads += 1
        return self.moment


class SleepRecorder:
    """Stand-in for time.sleep: records delays instead of waiting."""

    def __init__(self) -> None:
        self.delays: list[float] = []

    def __call__(self, delay: float) -> None:
        self.delays.append(delay)
