"""Scoring unit tests: feature math, bands, validation, and rationale."""

import json
import math
from dataclasses import FrozenInstanceError
from datetime import UTC, datetime, timedelta

import pytest

from equinox.config import MatchConfig
from equinox.matching.blocking import CorpusStats
from equinox.matching.score import (
    BAND_HIGH,
    BAND_MID,
    FEATURE_NAMES,
    NEUTRAL_SCORE,
    MatchCandidate,
    band_for_score,
    build_rationale,
    compute_features,
    date_proximity,
    find_candidates,
    idf_overlap,
    numeric_agreement,
    score_pair,
    token_overlap,
)
from equinox.model import MarketSnapshot

FROZEN_AT = datetime(2026, 10, 9, 12, 0, 0, tzinfo=UTC)
CLOSE = datetime(2026, 10, 28, 18, 0, 0, tzinfo=UTC)


def stats_for(sets: list[frozenset[str]]) -> CorpusStats:
    return CorpusStats.build(sets, MatchConfig())


# --- feature math ---


def test_token_overlap_is_jaccard():
    a = frozenset({"fed", "cut", "october"})
    b = frozenset({"fed", "hold", "october"})
    assert token_overlap(a, b) == pytest.approx(2 / 4)


def test_idf_overlap_weights_rare_tokens_more():
    # "x" is common in the corpus (df 3/4); "lisnard" is rare (df 1).
    # Absent fillers take the df-floor weight — the same weight a rare
    # real token gets — so the denominator structures match and the
    # comparison isolates the shared token's idf.
    corpus = [
        frozenset({"fed", "lisnard"}),
        frozenset({"x"}),
        frozenset({"x"}),
        frozenset({"x"}),
    ]
    stats = stats_for(corpus)
    rare_pair = idf_overlap(
        frozenset({"lisnard", "q1"}), frozenset({"lisnard", "q2"}), stats
    )
    common_pair = idf_overlap(frozenset({"x", "q1"}), frozenset({"x", "q2"}), stats)
    assert rare_pair > common_pair
    assert idf_overlap(frozenset({"fed"}), frozenset({"fed"}), stats) == pytest.approx(1.0)


def test_numeric_agreement_cases():
    silent = numeric_agreement(frozenset({"fed"}), frozenset({"rates"}))
    one_sided = numeric_agreement(frozenset({"100000"}), frozenset({"fed"}))
    agreeing = numeric_agreement(frozenset({"100000"}), frozenset({"100000"}))
    disagreeing = numeric_agreement(frozenset({"100000"}), frozenset({"150000"}))
    assert silent == 1.0
    assert one_sided == NEUTRAL_SCORE
    assert agreeing == 1.0
    assert disagreeing < 1.0


def test_date_proximity_math_and_neutral():
    scale = 7.0
    assert date_proximity(CLOSE, CLOSE, scale) == pytest.approx(1.0)
    assert date_proximity(CLOSE, CLOSE + timedelta(days=7), scale) == pytest.approx(
        math.exp(-1.0)
    )
    assert date_proximity(None, CLOSE, scale) == NEUTRAL_SCORE
    assert date_proximity(CLOSE, None, scale) == NEUTRAL_SCORE


def test_compute_features_covers_all_named_features(make_market):
    market_a = make_market(question="Will the Fed cut rates in October?")
    market_b = make_market(
        question="Fed October rate decision", venue="kalshi", market_id="KX-FED-1030"
    )
    tokens_a = frozenset({"fed", "cut", "rates", "october"})
    tokens_b = frozenset({"fed", "october", "rate", "decision"})
    features = compute_features(
        market_a, market_b, tokens_a, tokens_b, stats_for([]), MatchConfig()
    )
    assert tuple(features) == FEATURE_NAMES


# --- weighted sum and bands ---


def test_score_pair_is_the_config_weighted_sum():
    config = MatchConfig()
    features = dict.fromkeys(FEATURE_NAMES, 0.5)
    assert score_pair(features, config) == pytest.approx(0.5)  # weights sum to 1.0


def test_weights_sum_to_one_by_default():
    config = MatchConfig()
    total = (
        config.weight_token_overlap
        + config.weight_idf_overlap
        + config.weight_numeric_agreement
        + config.weight_date_proximity
    )
    assert total == pytest.approx(1.0)


@pytest.mark.parametrize(
    ("score", "expected"),
    [
        (0.55, BAND_MID),
        (0.6999, BAND_MID),  # between MID and HIGH: still mid, not below
        (0.70, BAND_HIGH),
        (0.90, BAND_HIGH),
        (0.10, "below"),
    ],
)
def test_band_for_score_boundaries(score, expected):
    assert band_for_score(score, MatchConfig()) == expected


# --- candidate validation ---


def _candidate(**overrides) -> MatchCandidate:
    defaults = dict(
        a="alpha/A1",
        b="beta/B1",
        score=0.6,
        features=dict.fromkeys(FEATURE_NAMES, 0.5),
        threshold_band=BAND_MID,
        rationale=("score 0.6000 in band 'mid'", "features: ..."),
    )
    defaults.update(overrides)
    return MatchCandidate(**defaults)


def test_candidate_is_frozen():
    candidate = _candidate()
    with pytest.raises(FrozenInstanceError):
        candidate.score = 0.99  # type: ignore[misc]


def test_candidate_rejects_wrong_orientation():
    with pytest.raises(ValueError, match="oriented a < b"):
        _candidate(a="beta/B1", b="alpha/A1")


def test_candidate_rejects_out_of_range_score():
    with pytest.raises(ValueError, match="outside"):
        _candidate(score=1.5)


def test_candidate_rejects_wrong_feature_keys():
    with pytest.raises(ValueError, match="must carry exactly"):
        _candidate(features={"wrong": 0.5, "keys": 0.4, "here": 0.3, "too": 0.2})


def test_candidate_rejects_feature_out_of_range():
    with pytest.raises(ValueError, match="outside"):
        _candidate(features=dict.fromkeys(FEATURE_NAMES, 7.0))


def test_candidate_rejects_bad_band_or_empty_rationale():
    with pytest.raises(ValueError, match="threshold_band"):
        _candidate(threshold_band="huge")
    with pytest.raises(ValueError, match="rationale"):
        _candidate(rationale=())


def test_as_dict_is_json_safe_and_stable():
    payload = _candidate().as_dict()
    json.dumps(payload, sort_keys=True)  # round-trips to JSON
    assert tuple(payload["features"]) == FEATURE_NAMES


# --- rationale content ---


def test_rationale_names_shared_evidence_and_flags_date_gaps(make_market):
    market_a = make_market(question="Will the Fed cut rates in October?")
    market_b = make_market(
        question="Fed October rate decision",
        venue="kalshi",
        market_id="KX-FED",
        closes_at=CLOSE + timedelta(days=30),
    )
    tokens_a = frozenset({"fed", "cut", "october"})
    tokens_b = frozenset({"fed", "october", "decision"})
    stats = stats_for(
        [tokens_a, tokens_b] + [frozenset({"noise"}) for _ in range(38)]
    )
    config = MatchConfig()
    features = compute_features(market_a, market_b, tokens_a, tokens_b, stats, config)
    lines = build_rationale(
        tokens_a,
        tokens_b,
        features,
        0.6,
        BAND_MID,
        stats,
        config,
        market_a.closes_at,
        market_b.closes_at,
    )
    assert any("score 0.6000" in line for line in lines)
    assert any("'fed'" in line and "idf" in line for line in lines)
    assert any("30.0 days" in line for line in lines)


# --- end-to-end on tiny snapshots ---


def test_find_candidates_emits_sorted_banded_candidates(make_market):
    markets = [
        make_market(question="Will David Lisnard win the 2027 French election?"),
        make_market(
            question="David Lisnard 2027 French election winner",
            venue="kalshi",
            market_id="KXFRENCHPRES-27-DLIS",
        ),
        make_market(question="Will it snow in Miami tomorrow?", market_id="pm-snow"),
        make_market(
            question="Snowfall in Miami", venue="kalshi", market_id="KXMIAMISNOW"
        ),
    ]
    snapshot = MarketSnapshot(
        fetched_at=FROZEN_AT, venues=["polymarket", "kalshi"], markets=markets
    )
    candidates = find_candidates(snapshot)
    assert candidates, "constructed equivalents must clear the MID threshold"
    scores = [c.score for c in candidates]
    assert scores == sorted(scores, reverse=True)
    assert all(c.a < c.b for c in candidates)
    assert all(tuple(c.features) == FEATURE_NAMES for c in candidates)
    assert all(c.rationale for c in candidates)
    for c in candidates:
        if c.threshold_band == BAND_HIGH:
            assert c.score >= 0.70
        else:
            assert c.score >= 0.55


def test_find_candidates_respects_config_thresholds(make_market):
    markets = [
        make_market(question="Will the Fed cut rates in October?"),
        make_market(
            question="Fed October rate decision", venue="kalshi", market_id="KX-FED"
        ),
    ]
    snapshot = MarketSnapshot(
        fetched_at=FROZEN_AT, venues=["polymarket", "kalshi"], markets=markets
    )
    loose = find_candidates(snapshot, MatchConfig(mid_threshold=0.0, high_threshold=0.0))
    strict = find_candidates(
        snapshot,
        MatchConfig(
            mid_threshold=0.99,
            high_threshold=0.995,
            weight_token_overlap=0.25,
            weight_idf_overlap=0.25,
            weight_numeric_agreement=0.25,
            weight_date_proximity=0.25,
        ),
    )
    assert loose, "zero thresholds emit every blocked pair"
    assert len(strict) < len(loose)
