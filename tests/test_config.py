"""Config tests: defaults match the spec, validation traps are caught."""

from dataclasses import FrozenInstanceError
from datetime import timedelta

import pytest

from equinox.config import MatchConfig, RouterConfig
from equinox.model import FeeParams

# --- MatchConfig: defaults match the spec ---


def test_match_config_thresholds_match_spec():
    config = MatchConfig()
    assert config.mid_threshold == 0.55  # spec: score >= 0.55 becomes a candidate
    assert config.high_threshold == 0.70  # spec: >= 0.70 flags review_recommended


def test_match_config_blocking_defaults_match_spec():
    config = MatchConfig()
    assert config.rare_token_max_doc_freq == 0.05  # spec: doc frequency <= 5%
    assert config.date_block_window_days == 7  # spec: close dates within +/-7 days


def test_match_config_feature_weights_and_features():
    config = MatchConfig()
    # The four named features from the spec, with provisional defaults.
    assert config.weight_token_overlap == 0.40
    assert config.weight_idf_overlap == 0.30
    assert config.weight_numeric_agreement == 0.20
    assert config.weight_date_proximity == 0.10
    assert (
        config.weight_token_overlap
        + config.weight_idf_overlap
        + config.weight_numeric_agreement
        + config.weight_date_proximity
        == pytest.approx(1.0)
    )


# --- MatchConfig: validation ---


def test_match_config_rejects_weights_that_do_not_sum_to_one():
    with pytest.raises(ValueError, match="sum to 1.0"):
        MatchConfig(weight_date_proximity=0.20)  # total 1.1


def test_match_config_rejects_negative_weight():
    with pytest.raises(ValueError, match="non-negative"):
        MatchConfig(weight_idf_overlap=-0.30)


@pytest.mark.parametrize("doc_freq", [0.0, -0.5, 1.01])
def test_match_config_rejects_bad_doc_freq(doc_freq):
    with pytest.raises(ValueError, match="rare_token_max_doc_freq"):
        MatchConfig(rare_token_max_doc_freq=doc_freq)


def test_match_config_rejects_negative_date_window():
    with pytest.raises(ValueError, match="date_block_window_days"):
        MatchConfig(date_block_window_days=-1)


@pytest.mark.parametrize(
    ("kwargs", "message"),
    [
        ({"mid_threshold": 1.5}, "mid_threshold"),
        ({"high_threshold": -0.1}, "high_threshold"),
        ({"mid_threshold": 0.80, "high_threshold": 0.70}, "must not exceed"),
    ],
)
def test_match_config_rejects_bad_thresholds(kwargs, message):
    with pytest.raises(ValueError, match=message):
        MatchConfig(**kwargs)


def test_match_config_is_frozen():
    with pytest.raises(FrozenInstanceError):
        MatchConfig().mid_threshold = 0.9


# --- RouterConfig: defaults ---


def test_router_config_defaults():
    config = RouterConfig()
    assert config.max_quote_age == timedelta(minutes=5)
    assert len(config.fee_params_by_venue) == 0  # no fallback fees by default
    assert config.stale_cost_penalty == 0.01
    assert config.cost_tie_epsilon == 1e-9


def test_router_config_fee_lookup_normalizes_venue_slugs():
    kalshi_fees = FeeParams(model="p_curve", rate=0.07, source="config:2026-10-09")
    config = RouterConfig(fee_params_by_venue={"Kalshi": kalshi_fees})
    assert config.fee_params_for("kalshi") is kalshi_fees
    assert config.fee_params_for("  KALSHI ") is kalshi_fees
    assert config.fee_params_for("polymarket") is None


def test_router_config_fee_map_is_read_only():
    config = RouterConfig(
        fee_params_by_venue={
            "kalshi": FeeParams(model="flat", rate=0.01, source="config:2026-10-09")
        }
    )
    with pytest.raises(TypeError):
        config.fee_params_by_venue["polymarket"] = None  # type: ignore[index]


# --- RouterConfig: validation ---


@pytest.mark.parametrize(
    ("kwargs", "message"),
    [
        ({"max_quote_age": timedelta(0)}, "positive timedelta"),
        ({"max_quote_age": timedelta(minutes=-5)}, "positive timedelta"),
        ({"max_quote_age": 300}, "positive timedelta"),
        ({"stale_cost_penalty": -0.01}, "non-negative"),
        ({"cost_tie_epsilon": 0}, "must be positive"),
        ({"cost_tie_epsilon": -1e-9}, "must be positive"),
    ],
)
def test_router_config_rejects_bad_knobs(kwargs, message):
    with pytest.raises(ValueError, match=message):
        RouterConfig(**kwargs)


def test_router_config_rejects_non_fee_params_values():
    with pytest.raises(TypeError, match="FeeParams"):
        RouterConfig(fee_params_by_venue={"kalshi": "0.07"})  # type: ignore[dict-item]


def test_router_config_rejects_empty_venue_keys():
    with pytest.raises(ValueError, match="venue slugs"):
        RouterConfig(
            fee_params_by_venue={"   ": FeeParams(model="none", rate=None, source="s")}
        )


def test_router_config_is_frozen():
    with pytest.raises(FrozenInstanceError):
        RouterConfig().stale_cost_penalty = 0.5
