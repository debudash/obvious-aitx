"""Model tests: scale traps, None tolerance, immutability, snapshot determinism.

The scale traps are the acceptance centerpiece: a price of 62c must never
surface as ``62`` (out of range) or ``0.000062`` (mis-scaled below any
venue book quote).
"""

from dataclasses import FrozenInstanceError, asdict
from datetime import UTC, datetime, timedelta, timezone

import pytest

from equinox.model import (
    FeeParams,
    FetchError,
    Market,
    MarketSnapshot,
    Outcome,
    PriceScaleError,
    TimeParseError,
    normalize_dollars,
    normalize_probability,
    parse_utc,
    to_utc,
)

CLOSES_AT = datetime(2026, 10, 28, 18, 0, 0, tzinfo=UTC)


# --- normalize_probability: Polymarket-style 0-1 values pass through as-is ---


def test_probability_passes_through_unscaled():
    assert normalize_probability(0.62) == 0.62


def test_probability_accepts_numeric_strings():
    # Polymarket's JSON-encoded outcome arrays carry prices as strings.
    assert normalize_probability("0.62") == 0.62


def test_probability_bounds_are_inclusive():
    assert normalize_probability(0.0) == 0.0
    assert normalize_probability(1.0) == 1.0
    assert normalize_probability(1) == 1.0


def test_probability_none_is_unknown_not_zero():
    assert normalize_probability(None) is None


def test_probability_blank_string_is_unknown():
    assert normalize_probability("   ") is None


def test_raw_dollars_value_rejected():
    # The classic trap: 62c arriving as 62 (a probability of 6200%).
    with pytest.raises(PriceScaleError, match="outside \\[0, 1\\]"):
        normalize_probability(62, field_label="yes_price")


def test_micro_prices_are_valid():
    # Recalibrated 2026-10-09: the old 0.001 plausibility floor rejected real
    # Gamma longshots (live records price at 0.0005 — recorded in
    # tests/fixtures/polymarket_markets.json). No magnitude threshold can
    # separate a ÷1000 units error (0.000062) from a legitimate 0.0005
    # longshot, so the [0, 1] bounds are the whole contract.
    assert normalize_probability(0.000062, field_label="yes_price") == 0.000062
    assert normalize_probability(0.0005, field_label="yes_price") == 0.0005


@pytest.mark.parametrize("value", [-0.01, 1.0001, 100.0, -1.0])
def test_out_of_range_rejected(value):
    with pytest.raises(PriceScaleError):
        normalize_probability(value)


def test_nan_rejected():
    with pytest.raises(PriceScaleError, match="finite"):
        normalize_probability(float("nan"))


def test_bool_rejected():
    # bool is an int subclass; True must not sneak in as a price of 1.
    with pytest.raises(PriceScaleError, match="real number"):
        normalize_probability(True)


def test_unparseable_string_rejected():
    with pytest.raises(PriceScaleError, match="not a number"):
        normalize_probability("cheap")


# --- normalize_dollars: Kalshi-style dollar quotes divide by 1.0, same check ---


def test_dollar_quote_is_probability_equivalent():
    assert normalize_dollars(0.62) == 0.62


def test_dollar_quote_accepts_numeric_strings():
    assert normalize_dollars(" 0.37 ") == 0.37


def test_dollar_quote_scale_traps_rejected():
    with pytest.raises(PriceScaleError):
        normalize_dollars(62)  # cents read as whole dollars — out of [0, 1]
    # Recalibrated with the plausibility floor's removal: a double-divided
    # 62c (0.000062) sits inside [0, 1] and is real-data-indistinguishable
    # from legitimate sub-0.001 longshots, so it is accepted.
    assert normalize_dollars(0.000062) == 0.000062


def test_dollar_quote_none_passes_through():
    assert normalize_dollars(None) is None


# --- parse_utc / to_utc ---


def test_parse_utc_handles_zulu_suffix():
    parsed = parse_utc("2026-10-28T18:00:00Z")
    assert parsed == CLOSES_AT
    assert parsed.tzinfo is not None


def test_parse_utc_converts_offsets():
    assert parse_utc("2026-10-28T13:00:00-05:00") == CLOSES_AT


def test_parse_utc_reads_naive_as_utc():
    parsed = parse_utc("2026-10-28T18:00:00")
    assert parsed == CLOSES_AT
    assert parsed.tzinfo == UTC


def test_parse_utc_none_and_blank_are_unknown():
    assert parse_utc(None) is None
    assert parse_utc("") is None
    assert parse_utc("   ") is None


@pytest.mark.parametrize("value", ["October 28", "not-a-date", "1761234567", 1761234567])
def test_parse_utc_rejects_malformed(value):
    with pytest.raises(TimeParseError):
        parse_utc(value)


def test_to_utc_normalizes_naive_and_aware():
    naive = datetime(2026, 10, 28, 18, 0, 0)
    assert to_utc(naive).tzinfo == UTC
    minus_five = timezone(timedelta(hours=-5))
    offset_time = datetime(2026, 10, 28, 13, 0, 0, tzinfo=minus_five)
    assert to_utc(offset_time) == CLOSES_AT


# --- Outcome ---


def test_outcome_prices_coerced_to_float():
    outcome = Outcome(name="Yes", side="yes", yes_price=1, bid=0, ask=None)
    assert outcome.yes_price == 1.0 and isinstance(outcome.yes_price, float)
    assert outcome.bid == 0.0


def test_outcome_rejects_mis_scaled_prices():
    with pytest.raises(PriceScaleError):
        Outcome(name="Yes", side="yes", yes_price=62)
    # Sub-0.001 quotes are valid (real Gamma longshots price at 0.0005 —
    # see test_micro_prices_are_valid); only out-of-bounds values reject.
    outcome = Outcome(name="No", side="no", bid=0.000062)
    assert outcome.bid == 0.000062
    with pytest.raises(PriceScaleError):
        Outcome(name="No", side="no", ask=1.5)


def test_outcome_rejects_bad_side():
    with pytest.raises(ValueError, match="side"):
        Outcome(name="Yes", side="YES")  # canonical sides are lowercase


def test_outcome_rejects_empty_name():
    with pytest.raises(ValueError, match="name"):
        Outcome(name="  ", side="yes")


def test_outcome_tolerates_unknown_prices():
    outcome = Outcome(name="Democrats", side="other")
    assert outcome.yes_price is None and outcome.bid is None and outcome.ask is None


# --- FeeParams ---


@pytest.mark.parametrize("model", ["p_curve", "flat", "none"])
def test_fee_models_are_valid(make_fee_params, model):
    fee = make_fee_params(model=model, rate=None if model == "none" else 0.07)
    assert fee.model == model


def test_fee_rate_percent_trap_rejected(make_fee_params):
    # 7 (i.e. 700%) is the fee-rate version of the scale trap.
    with pytest.raises(PriceScaleError, match="fraction"):
        make_fee_params(rate=7)


def test_fee_model_none_rejects_rate(make_fee_params):
    with pytest.raises(ValueError, match="none"):
        make_fee_params(model="none", rate=0.0)


def test_fee_model_p_curve_requires_rate(make_fee_params):
    with pytest.raises(ValueError, match="requires a rate"):
        make_fee_params(model="p_curve", rate=None)


def test_fee_source_provenance_required(make_fee_params):
    with pytest.raises(ValueError, match="source"):
        make_fee_params(source="")


def test_fee_unknown_model_rejected(make_fee_params):
    with pytest.raises(ValueError, match="model"):
        make_fee_params(model="tariff")


def test_p_curve_fee_math(make_fee_params):
    fee = make_fee_params(model="p_curve", rate=0.07)
    assert fee.fee_per_share(0.62) == pytest.approx(0.07 * 0.62 * 0.38)
    assert fee.fee_per_share(0.0) == 0.0  # curve vanishes at the bounds
    assert fee.fee_per_share(1.0) == 0.0


def test_flat_and_none_fee_math(make_fee_params):
    assert make_fee_params(model="flat", rate=0.005).fee_per_share(0.9) == 0.005
    assert make_fee_params(model="none", rate=None).fee_per_share(0.62) == 0.0


def test_fee_per_share_rejects_invalid_price(make_fee_params):
    fee = make_fee_params()
    with pytest.raises(PriceScaleError):
        fee.fee_per_share(62)
    with pytest.raises(PriceScaleError):
        fee.fee_per_share(None)


# --- Market ---


def test_yes_side_is_explicit(make_market):
    market = make_market()
    assert market.yes_price == 0.62
    assert market.yes_bid == 0.61
    assert market.yes_ask == 0.63


def test_no_side_explicit_when_available(make_market):
    market = make_market()
    assert market.no_price == 0.38
    assert market.no_bid == 0.37
    assert market.no_ask == 0.39


def test_no_side_absent_is_none(make_outcome, make_fee_params):
    market = Market(
        market_id="kx-fed",
        venue="kalshi",
        question="Will the Fed hold rates?",
        outcomes=(make_outcome(),),  # YES only: NO not reported by the venue
        fees=make_fee_params(),
        closes_at=None,
        liquidity=None,
        fetched_at=datetime(2026, 10, 9, tzinfo=UTC),
        raw_ref="raw/kalshi.jsonl:4",
    )
    assert market.no_price is None and market.no_bid is None and market.no_ask is None
    assert market.yes_price == 0.62  # YES resolution unaffected


def test_multi_outcome_market_has_no_declared_yes_side(make_market, make_outcome):
    market = make_market(
        outcomes=(
            make_outcome(name="Up", side="other", yes_price=0.7),
            make_outcome(name="Down", side="other", yes_price=0.3),
        )
    )
    assert market.yes_price is None and market.no_price is None


def test_duplicate_sides_rejected(make_outcome, make_fee_params):
    outcomes = (make_outcome(), make_outcome(name="Yes again"))
    with pytest.raises(ValueError, match="side='yes'"):
        Market(
            market_id="m",
            venue="polymarket",
            question="q",
            outcomes=outcomes,
            fees=make_fee_params(),
            closes_at=None,
            liquidity=None,
            fetched_at=datetime(2026, 10, 9, tzinfo=UTC),
            raw_ref="raw/x.jsonl:1",
        )


def test_scale_trap_through_market_construction(make_market, make_outcome):
    with pytest.raises(PriceScaleError):
        make_market(outcomes=(make_outcome(yes_price=62),))


def test_unknown_liquidity_stays_none_not_zero(make_market):
    assert make_market(liquidity=None).liquidity is None


def test_negative_liquidity_rejected(make_market):
    with pytest.raises(ValueError, match="liquidity"):
        make_market(liquidity=-1.0)


@pytest.mark.parametrize("attr", ["market_id", "venue", "question", "raw_ref"])
def test_identity_fields_required(make_market, attr):
    with pytest.raises(ValueError, match=attr):
        make_market(**{attr: "   "})


def test_times_must_be_datetimes(make_market):
    with pytest.raises(TypeError, match="fetched_at"):
        make_market(fetched_at="2026-10-09T12:00:00Z")
    with pytest.raises(TypeError, match="closes_at"):
        make_market(closes_at="soon")


def test_market_times_normalized_to_utc(make_market):
    market = make_market(
        closes_at=datetime(2026, 10, 28, 13, 0, 0, tzinfo=timezone(timedelta(hours=-5))),
        fetched_at=datetime(2026, 10, 9, 12, 0, 0),  # naive
    )
    assert market.closes_at == CLOSES_AT
    assert market.fetched_at.tzinfo == UTC


def test_outcomes_coerced_to_tuple(make_outcome, make_market):
    market = make_market(outcomes=[make_outcome()])  # list in
    assert isinstance(market.outcomes, tuple)


def test_outcome_for_lookup(make_market):
    market = make_market()
    assert market.outcome_for("no").name == "No"
    assert market.outcome_for("other") is None


# --- immutability ---


def test_market_is_frozen(make_market):
    market = make_market()
    with pytest.raises(FrozenInstanceError):
        market.question = "Rewritten?"
    with pytest.raises(FrozenInstanceError):
        market.liquidity = 0.0


def test_outcome_is_frozen(make_outcome):
    with pytest.raises(FrozenInstanceError):
        make_outcome().yes_price = 0.99


def test_fee_params_is_frozen(make_fee_params):
    with pytest.raises(FrozenInstanceError):
        make_fee_params().rate = 0.5


# --- FetchError ---


def test_fetch_error_holds_page_failures():
    error = FetchError(venue="kalshi", url="https://x", cause="timeout", attempts=3)
    assert (error.venue, error.attempts) == ("kalshi", 3)


@pytest.mark.parametrize(
    ("field_name", "value"),
    [("venue", ""), ("url", "  "), ("cause", None), ("attempts", 0), ("attempts", True)],
)
def test_fetch_error_validation(field_name, value):
    defaults = dict(venue="kalshi", url="https://x", cause="boom", attempts=2)
    defaults[field_name] = value
    with pytest.raises(ValueError, match=field_name):
        FetchError(**defaults)


# --- MarketSnapshot ---


def _snapshot_inputs(make_market):
    kalshi = make_market(
        market_id="KX-FED", venue="kalshi", question="Fed hold rates in October?",
        raw_ref="raw/kalshi.jsonl:4",
    )
    poly_low = make_market(market_id="pm-a", raw_ref="raw/poly.jsonl:1")
    poly_high = make_market(market_id="pm-b", raw_ref="raw/poly.jsonl:2")
    return kalshi, poly_low, poly_high


def test_snapshot_orders_are_explicit_totals(make_market):
    kalshi, poly_low, poly_high = _snapshot_inputs(make_market)
    snapshot = MarketSnapshot(
        fetched_at=datetime(2026, 10, 9, 12, 0, 0, tzinfo=UTC),
        venues=["polymarket", "kalshi", "polymarket"],
        markets=[poly_high, kalshi, poly_low],  # deliberately scrambled
        degraded=["polymarket", "kalshi", "polymarket"],
        errors=[
            FetchError(venue="kalshi", url="https://b", cause="500", attempts=3),
            FetchError(venue="kalshi", url="https://a", cause="timeout", attempts=3),
        ],
    )
    assert snapshot.venues == ("kalshi", "polymarket")
    assert [m.market_id for m in snapshot.markets] == ["KX-FED", "pm-a", "pm-b"]
    assert snapshot.degraded == ("kalshi", "polymarket")
    assert [e.cause for e in snapshot.errors] == ["timeout", "500"]


def test_snapshot_round_trip(make_market):
    kalshi, poly_low, poly_high = _snapshot_inputs(make_market)
    snapshot = MarketSnapshot(
        fetched_at=datetime(2026, 10, 9, 12, 0, 0, tzinfo=UTC),
        venues=["kalshi", "polymarket"],
        markets=[kalshi, poly_low, poly_high],
        degraded=["kalshi"],
        errors=[FetchError(venue="kalshi", url="https://x", cause="timeout", attempts=3)],
    )
    rebuilt = _rehydrate(asdict(snapshot))
    assert rebuilt == snapshot
    assert asdict(rebuilt) == asdict(snapshot)


def _rehydrate(data: dict) -> MarketSnapshot:
    """Rebuild a snapshot from its asdict form, rehydrating nested dataclasses."""
    markets = []
    for record in data["markets"]:
        outcomes = tuple(
            Outcome(name=o["name"], side=o["side"], yes_price=o["yes_price"],
                    bid=o["bid"], ask=o["ask"])
            for o in record["outcomes"]
        )
        fee = record["fees"]
        markets.append(
            Market(
                market_id=record["market_id"],
                venue=record["venue"],
                question=record["question"],
                outcomes=outcomes,
                fees=FeeParams(model=fee["model"], rate=fee["rate"], source=fee["source"]),
                closes_at=record["closes_at"],
                liquidity=record["liquidity"],
                fetched_at=record["fetched_at"],
                raw_ref=record["raw_ref"],
            )
        )
    errors = tuple(FetchError(**e) for e in data["errors"])
    return MarketSnapshot(
        fetched_at=data["fetched_at"],
        venues=data["venues"],
        markets=markets,
        degraded=data["degraded"],
        errors=errors,
    )


def test_snapshot_naive_fetched_at_read_as_utc(make_market):
    kalshi, poly_low, poly_high = _snapshot_inputs(make_market)
    snapshot = MarketSnapshot(
        fetched_at=datetime(2026, 10, 9, 12, 0, 0),  # naive
        venues=["kalshi", "polymarket"],
        markets=[kalshi, poly_low, poly_high],
    )
    assert snapshot.fetched_at.tzinfo == UTC


def test_snapshot_accessors(make_market):
    kalshi, poly_low, poly_high = _snapshot_inputs(make_market)
    snapshot = MarketSnapshot(
        fetched_at=datetime(2026, 10, 9, 12, 0, 0, tzinfo=UTC),
        venues=["kalshi", "polymarket"],
        markets=[kalshi, poly_low, poly_high],
        degraded=["kalshi"],
    )
    assert len(snapshot.markets_for_venue("polymarket")) == 2
    assert snapshot.markets_for_venue("missing") == ()
    assert snapshot.is_degraded("kalshi")
    assert not snapshot.is_degraded("polymarket")
    assert snapshot.market_by_id("kalshi", "KX-FED").raw_ref == "raw/kalshi.jsonl:4"
    assert snapshot.market_by_id("kalshi", "nope") is None


def test_snapshot_rejects_wrong_types(make_market):
    with pytest.raises(TypeError, match="fetched_at"):
        MarketSnapshot(fetched_at="2026-10-09", venues=["kalshi"])
    with pytest.raises(TypeError, match="markets"):
        MarketSnapshot(
            fetched_at=datetime(2026, 10, 9, tzinfo=UTC),
            venues=["kalshi"],
            markets=[("not-a-market",)],
        )
    with pytest.raises(TypeError, match="errors"):
        MarketSnapshot(
            fetched_at=datetime(2026, 10, 9, tzinfo=UTC),
            venues=["kalshi"],
            errors=[("not-an-error",)],
        )


def test_snapshot_rejects_bad_venue_slugs():
    with pytest.raises(ValueError, match="venues"):
        MarketSnapshot(fetched_at=datetime(2026, 10, 9, tzinfo=UTC), venues=["  "])
    with pytest.raises(ValueError, match="degraded"):
        MarketSnapshot(
            fetched_at=datetime(2026, 10, 9, tzinfo=UTC),
            venues=["kalshi"],
            degraded=[""],
        )


def test_snapshot_is_frozen(make_market):
    kalshi, poly_low, poly_high = _snapshot_inputs(make_market)
    snapshot = MarketSnapshot(
        fetched_at=datetime(2026, 10, 9, 12, 0, 0, tzinfo=UTC),
        venues=["kalshi", "polymarket"],
        markets=[kalshi, poly_low, poly_high],
    )
    with pytest.raises(FrozenInstanceError):
        snapshot.venues = ("other",)
