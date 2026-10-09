"""Router behavior: cost math, tie-breaks, staleness policy, resolution, purity.

All decisions run on fixed snapshots — no clock, no network. Quote age is
always ``snapshot.fetched_at - market.fetched_at`` with both stamps handed
in as data.
"""

from __future__ import annotations

from datetime import UTC, datetime, timedelta
from pathlib import Path

import pytest

from equinox.config import RouterConfig
from equinox.model import FeeParams, Market, MarketSnapshot, Outcome
from equinox.routing import (
    REJECTION_PREFIX,
    RouteIntent,
    decision_to_trace_line,
    route,
)

FROZEN_AT = datetime(2026, 10, 9, 12, 0, 0, tzinfo=UTC)
STALE_AT = FROZEN_AT - timedelta(minutes=10)  # older than the default max_quote_age

ROUTING_ROOT = Path(__file__).resolve().parents[1] / "src" / "equinox" / "routing"


def _market(
    market_id: str,
    venue: str,
    *,
    ask: float,
    bid: float,
    liquidity: float | None = 1_000.0,
    fee_model: str = "none",
    fee_rate: float | None = None,
    fee_source: str = "config:2026-10-09",
    fetched_at: datetime | None = None,
    side: str = "yes",
) -> Market:
    """A single-outcome market quoting *side* at the given best bid/ask."""
    if fee_model == "none":
        fees = FeeParams(model="none", rate=None, source="adapter-stamp")
    else:
        fees = FeeParams(model=fee_model, rate=fee_rate, source=fee_source)
    return Market(
        market_id=market_id,
        venue=venue,
        question="Will the router test pass?",
        outcomes=(Outcome(name=side.upper(), side=side, yes_price=ask, bid=bid, ask=ask),),
        fees=fees,
        closes_at=None,
        liquidity=liquidity,
        fetched_at=fetched_at or FROZEN_AT,
        raw_ref=f"raw/{venue}.jsonl:1",
    )


def _snapshot(*markets: Market, venues: list[str] | None = None) -> MarketSnapshot:
    return MarketSnapshot(
        fetched_at=FROZEN_AT,
        venues=venues if venues is not None else sorted({m.venue for m in markets}),
        markets=markets,
    )


def _config(**overrides) -> RouterConfig:
    params: dict = {
        "max_quote_age": timedelta(minutes=5),
        "stale_cost_penalty": 0.01,
        "cost_tie_epsilon": 1e-9,
    }
    params.update(overrides)
    return RouterConfig(**params)


# --------------------------------------------------------------------------
# Fee math (per fee model, at representative probabilities)
# --------------------------------------------------------------------------


@pytest.mark.parametrize(
    ("ask", "expected_fee"),
    [
        (0.62, 0.07 * 0.62 * 0.38),  # typical two-sided market
        (0.50, 0.07 * 0.25),  # curve maximum: p(1-p) peaks at 0.25
        (0.90, 0.07 * 0.09),  # near-certain: curve nearly free
        (0.02, 0.07 * 0.02 * 0.98),  # longshot
    ],
)
def test_p_curve_fee_math(ask: float, expected_fee: float) -> None:
    market = _market("m", "v-a", ask=ask, bid=ask, fee_model="p_curve", fee_rate=0.07)
    decision = route(RouteIntent("m", "yes", 100), _snapshot(market), _config())
    quote = decision.quotes[0]
    assert quote.breakdown["fee"] == pytest.approx(expected_fee)
    assert quote.all_in_cost == pytest.approx(ask + expected_fee)  # bid == ask: no spread


@pytest.mark.parametrize("ask", [0.30, 0.62, 0.88])
def test_flat_fee_math(ask: float) -> None:
    market = _market("m", "v-a", ask=ask, bid=ask, fee_model="flat", fee_rate=0.02)
    decision = route(RouteIntent("m", "yes", 100), _snapshot(market), _config())
    assert decision.quotes[0].breakdown["fee"] == pytest.approx(0.02)


def test_none_fee_model_is_free() -> None:
    market = _market("m", "v-a", ask=0.62, bid=0.61, fee_model="none")
    decision = route(RouteIntent("m", "yes", 100), _snapshot(market), _config())
    quote = decision.quotes[0]
    assert quote.breakdown["fee"] == pytest.approx(0.0)
    assert quote.all_in_cost == pytest.approx(0.62 + 0.005)


# --------------------------------------------------------------------------
# Spread impact: half the bid-ask spread (top-of-book assumption A2)
# --------------------------------------------------------------------------


@pytest.mark.parametrize(
    ("ask", "bid", "expected"), [(0.63, 0.61, 0.01), (0.70, 0.60, 0.05), (0.62, 0.62, 0.0)]
)
def test_spread_impact_is_half_spread(ask: float, bid: float, expected: float) -> None:
    market = _market("m", "v-a", ask=ask, bid=bid)
    decision = route(RouteIntent("m", "yes", 100), _snapshot(market), _config())
    assert decision.quotes[0].breakdown["spread_impact"] == pytest.approx(expected)


# --------------------------------------------------------------------------
# Fee-parameter precedence (assumption A1: per-market data, config fallback)
# --------------------------------------------------------------------------


def test_config_fee_fallback_applies_when_market_declares_none() -> None:
    market = _market("m", "v-a", ask=0.62, bid=0.61, fee_model="none")
    config = _config(
        fee_params_by_venue={
            "v-a": FeeParams(model="p_curve", rate=0.07, source="config:2026-10-09")
        }
    )
    decision = route(RouteIntent("m", "yes", 100), _snapshot(market), config)
    quote = decision.quotes[0]
    assert quote.breakdown["fee"] == pytest.approx(0.07 * 0.62 * 0.38)
    assert any("fee source config:2026-10-09" in step for step in decision.rationale)


def test_market_fees_win_over_config_entry() -> None:
    market = _market("m", "v-a", ask=0.62, bid=0.61, fee_model="p_curve", fee_rate=0.05)
    config = _config(
        fee_params_by_venue={
            "v-a": FeeParams(model="p_curve", rate=0.07, source="config:2026-10-09")
        }
    )
    decision = route(RouteIntent("m", "yes", 100), _snapshot(market), config)
    assert decision.quotes[0].breakdown["fee"] == pytest.approx(0.05 * 0.62 * 0.38)


def test_market_none_without_config_is_free() -> None:
    market = _market("m", "v-a", ask=0.62, bid=0.61, fee_model="none")
    decision = route(RouteIntent("m", "yes", 100), _snapshot(market), _config())
    assert decision.quotes[0].breakdown["fee"] == pytest.approx(0.0)


# --------------------------------------------------------------------------
# Selection: cheapest all-in wins; explicit total order over quotes
# --------------------------------------------------------------------------


def test_cheapest_venue_wins_and_quotes_sorted_best_first() -> None:
    snapshot = _snapshot(
        _market("m", "v-a", ask=0.62, bid=0.61),
        _market("m", "v-b", ask=0.65, bid=0.64),
    )
    decision = route(RouteIntent("m", "yes", 100), snapshot, _config())
    assert decision.chosen == "v-a"
    assert [q.venue for q in decision.quotes] == ["v-a", "v-b"]
    assert any("largest contributor: price" in step for step in decision.rationale)


def test_quotes_total_order_under_equal_raw_cost() -> None:
    snapshot = _snapshot(
        _market("m", "v-a", ask=0.62, bid=0.61, liquidity=100.0),
        _market("m", "v-b", ask=0.62, bid=0.61, liquidity=5_000.0),
    )
    decision = route(RouteIntent("m", "yes", 100), snapshot, _config())
    assert [q.venue for q in decision.quotes] == ["v-b", "v-a"]


# --------------------------------------------------------------------------
# Tie-breaks: cost -> liquidity -> venue slug (each tier exercised)
# --------------------------------------------------------------------------


def test_tie_break_tier_liquidity() -> None:
    snapshot = _snapshot(
        _market("m", "v-a", ask=0.62, bid=0.61, liquidity=100.0),
        _market("m", "v-b", ask=0.62, bid=0.61, liquidity=5_000.0),
    )
    decision = route(RouteIntent("m", "yes", 100), snapshot, _config())
    assert decision.chosen == "v-b"
    assert any("higher liquidity decided" in step for step in decision.rationale)


def test_tie_break_tier_venue_slug() -> None:
    snapshot = _snapshot(
        _market("m", "v-b", ask=0.62, bid=0.61, liquidity=100.0),
        _market("m", "v-a", ask=0.62, bid=0.61, liquidity=100.0),
    )
    decision = route(RouteIntent("m", "yes", 100), snapshot, _config())
    assert decision.chosen == "v-a"  # "v-a" < "v-b" ascending; never dict order
    assert any("venue slug ascending decided" in step for step in decision.rationale)


def test_tie_break_within_epsilon_treats_near_costs_as_equal() -> None:
    # Raw costs differ by 5e-10 < cost_tie_epsilon (1e-9): a tie, so the
    # cheaper-raw venue does NOT win by float luck — liquidity does.
    snapshot = _snapshot(
        _market("m", "v-a", ask=0.63, bid=0.63, liquidity=100.0),
        _market("m", "v-b", ask=0.63 + 5e-10, bid=0.63 + 5e-10, liquidity=900.0),
    )
    decision = route(RouteIntent("m", "yes", 100), snapshot, _config())
    assert decision.chosen == "v-b"
    assert any("cost tie within epsilon" in step for step in decision.rationale)


def test_unknown_liquidity_loses_to_any_known_book() -> None:
    # None liquidity must not look like free liquidity: unknown ranks below
    # even a reported empty book (liquidity 0).
    snapshot = _snapshot(
        _market("m", "v-a", ask=0.62, bid=0.61, liquidity=None),
        _market("m", "v-b", ask=0.62, bid=0.61, liquidity=0.0),
    )
    decision = route(RouteIntent("m", "yes", 100), snapshot, _config())
    assert decision.chosen == "v-b"
    assert any("liquidity=unknown" in step for step in decision.rationale)


# --------------------------------------------------------------------------
# Staleness policy: mark, downweight, state the effect; never silent
# --------------------------------------------------------------------------


def test_stale_penalty_flips_the_outcome() -> None:
    snapshot = _snapshot(
        _market("m", "v-stale", ask=0.61, bid=0.60, fetched_at=STALE_AT),
        _market("m", "v-fresh", ask=0.615, bid=0.610),
    )
    decision = route(RouteIntent("m", "yes", 100), snapshot, _config())
    stale_quote, fresh_quote = decision.quotes  # raw-sorted: stale is cheaper
    assert stale_quote.stale is True
    assert fresh_quote.stale is False
    # Downweighting happens at selection: raw order is unchanged, choice flips.
    assert decision.quotes[0].venue == "v-stale"
    assert decision.chosen == "v-fresh"
    assert any("stale penalty changed the outcome" in step for step in decision.rationale)
    assert any("+0.0100" in step for step in decision.rationale)


def test_stale_still_wins_with_freshness_warning() -> None:
    snapshot = _snapshot(
        _market("m", "v-stale", ask=0.61, bid=0.60, fetched_at=STALE_AT, liquidity=1.0)
    )
    decision = route(RouteIntent("m", "yes", 100), snapshot, _config())
    assert decision.chosen == "v-stale"
    assert decision.quotes[0].stale is True
    rationale = "\n".join(decision.rationale)
    assert "still cheapest after the penalty" in rationale
    assert "freshness warning" in rationale


def test_both_stale_still_decides_with_warning() -> None:
    snapshot = _snapshot(
        _market("m", "v-a", ask=0.61, bid=0.60, fetched_at=STALE_AT),
        _market("m2", "v-b", ask=0.615, bid=0.610, fetched_at=STALE_AT),
    )
    decision = route(RouteIntent("pair:m:m2", "yes", 100), snapshot, _config())
    assert decision.chosen == "v-a"  # decision still made, on penalized costs
    assert all(q.stale for q in decision.quotes)
    assert any(
        "every quotable venue's quote is stale" in step for step in decision.rationale
    )


def test_fresh_quotes_carry_no_stale_lines() -> None:
    market = _market("m", "v-a", ask=0.62, bid=0.61)
    decision = route(RouteIntent("m", "yes", 100), _snapshot(market), _config())
    assert decision.quotes[0].stale is False
    assert not any("stale" in step for step in decision.rationale)


# --------------------------------------------------------------------------
# chosen=None paths and rejections that state why
# --------------------------------------------------------------------------


def test_chosen_none_for_unresolvable_reference() -> None:
    market = _market("m", "v-a", ask=0.62, bid=0.61)
    decision = route(RouteIntent("missing-id", "yes", 100), _snapshot(market), _config())
    assert decision.chosen is None
    assert decision.quotes == []
    assert any("no snapshot market resolves" in step for step in decision.rationale)
    assert any(
        step.startswith(REJECTION_PREFIX) and "v-a" in step for step in decision.rationale
    )


def test_chosen_none_for_unresolvable_composite_pair_slug() -> None:
    # A matcher slug embedding the pair in one token resolves to nothing —
    # the router explains rather than guessing (reference-resolution contract).
    market = _market("m", "v-a", ask=0.62, bid=0.61)
    decision = route(
        RouteIntent("pair:KX-251030/some-slug", "yes", 100), _snapshot(market), _config()
    )
    assert decision.chosen is None


def test_chosen_none_when_ask_missing() -> None:
    market = Market(
        market_id="m",
        venue="v-a",
        question="Will the router test pass?",
        outcomes=(Outcome(name="Yes", side="yes", yes_price=0.62, bid=0.61, ask=None),),
        fees=FeeParams(model="none", rate=None, source="adapter-stamp"),
        closes_at=None,
        liquidity=1_000.0,
        fetched_at=FROZEN_AT,
        raw_ref="raw/v-a.jsonl:1",
    )
    decision = route(RouteIntent("m", "yes", 100), _snapshot(market), _config())
    assert decision.chosen is None
    assert any("no ask quoted" in step for step in decision.rationale)


def test_chosen_none_when_bid_missing() -> None:
    market = Market(
        market_id="m",
        venue="v-a",
        question="Will the router test pass?",
        outcomes=(Outcome(name="Yes", side="yes", yes_price=0.62, bid=None, ask=0.63),),
        fees=FeeParams(model="none", rate=None, source="adapter-stamp"),
        closes_at=None,
        liquidity=1_000.0,
        fetched_at=FROZEN_AT,
        raw_ref="raw/v-a.jsonl:1",
    )
    decision = route(RouteIntent("m", "yes", 100), _snapshot(market), _config())
    assert decision.chosen is None
    assert any("spread impact unknown" in step for step in decision.rationale)


def test_chosen_none_when_side_not_declared() -> None:
    market = _market("m", "v-a", ask=0.38, bid=0.37, side="no")
    decision = route(RouteIntent("m", "yes", 100), _snapshot(market), _config())
    assert decision.chosen is None
    assert any("no 'yes' side declared" in step for step in decision.rationale)


def test_one_venue_down_other_proceeds() -> None:
    market = _market("m", "v-a", ask=0.62, bid=0.61)
    decision = route(
        RouteIntent("m", "yes", 100), _snapshot(market, venues=["v-a", "v-down"]), _config()
    )
    assert decision.chosen == "v-a"
    joined = "\n".join(decision.rationale)
    assert "rejected venue='v-down'" in joined
    assert "no market in the snapshot resolves from this reference" in joined


# --------------------------------------------------------------------------
# Reference forms and duplicate resolution
# --------------------------------------------------------------------------


def test_pair_reference_quotes_both_venues() -> None:
    snapshot = _snapshot(
        _market("id-a", "v-a", ask=0.62, bid=0.61),
        _market("id-b", "v-b", ask=0.64, bid=0.63),
    )
    decision = route(RouteIntent("pair:id-a:id-b", "yes", 100), snapshot, _config())
    assert decision.chosen == "v-a"
    assert {q.venue for q in decision.quotes} == {"v-a", "v-b"}


def test_duplicate_resolution_picks_first_in_snapshot_order() -> None:
    snapshot = _snapshot(
        _market("x-1", "v-a", ask=0.62, bid=0.61),
        _market("x-2", "v-a", ask=0.66, bid=0.65),
    )
    decision = route(RouteIntent("pair:x-1:x-2", "yes", 100), snapshot, _config())
    assert decision.chosen == "v-a"
    assert decision.quotes[0].breakdown["price"] == pytest.approx(0.62)  # x-1, snapshot order
    assert any("multiple markets on venue='v-a'" in step for step in decision.rationale)


# --------------------------------------------------------------------------
# NO-side routing and intent validation
# --------------------------------------------------------------------------


def test_no_side_routes_against_no_ask() -> None:
    market = _market("m", "v-a", ask=0.38, bid=0.37, side="no")
    decision = route(RouteIntent("m", "no", 100), _snapshot(market), _config())
    assert decision.chosen == "v-a"
    assert decision.quotes[0].breakdown["price"] == pytest.approx(0.38)


def test_intent_side_normalized_case_insensitively() -> None:
    assert RouteIntent("m", "YES", 10).side == "yes"
    assert RouteIntent("m", "No", 10).side == "no"


@pytest.mark.parametrize(
    ("field", "value"),
    [("side", "buy"), ("side", ""), ("market_ref", "  "), ("shares", 0), ("shares", True)],
)
def test_intent_validation_rejects_bad_values(field: str, value: object) -> None:
    kwargs: dict = {"market_ref": "m", "side": "yes", "shares": 100}
    kwargs[field] = value
    with pytest.raises(ValueError):
        RouteIntent(**kwargs)


# --------------------------------------------------------------------------
# Determinism: content-derived ids, byte-identical traces, no clock
# --------------------------------------------------------------------------


def _build_snapshot_twice() -> tuple[MarketSnapshot, MarketSnapshot]:
    """Two independently built but identical snapshots."""

    def build() -> MarketSnapshot:
        return _snapshot(
            _market("id-a", "v-a", ask=0.62, bid=0.61, liquidity=900.0),
            _market("id-b", "v-b", ask=0.64, bid=0.63, liquidity=100.0),
        )

    return build(), build()


def test_repeat_calls_are_byte_identical() -> None:
    first, second = _build_snapshot_twice()
    lines = {
        decision_to_trace_line(
            route(RouteIntent("pair:id-a:id-b", "yes", 100), snap, _config())
        )
        for snap in (first, second)
        for _ in range(2)
    }
    assert len(lines) == 1  # byte-identical across rebuilds and repeat calls


def test_decision_id_is_content_derived() -> None:
    first, second = _build_snapshot_twice()
    intent = RouteIntent("pair:id-a:id-b", "yes", 100)
    config = _config()
    a = route(intent, first, config)
    b = route(intent, second, config)
    assert a.decision_id == b.decision_id
    assert a == b
    different_shares = route(RouteIntent("pair:id-a:id-b", "yes", 200), first, config)
    assert a.decision_id != different_shares.decision_id


def test_no_clock_reads_in_routing_sources() -> None:
    forbidden = ("datetime.now", "utcnow", "time.time", "monotonic", "perf_counter")
    for path in sorted(ROUTING_ROOT.rglob("*.py")):
        text = path.read_text(encoding="utf-8")
        hits = [token for token in forbidden if token in text]
        assert not hits, f"{path.name} references clock reads: {hits}"
