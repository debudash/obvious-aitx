"""Shared factories: valid model objects with per-test overrides."""

from datetime import UTC, datetime

import pytest

from equinox.model import FeeParams, Market, Outcome

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
