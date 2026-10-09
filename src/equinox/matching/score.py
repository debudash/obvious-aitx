"""Deterministic scoring and candidate emission (spec §Matching).

The second stage of the pipeline: every blocking pair is scored as a
weighted sum of four named features, and pairs clearing the MID
threshold become :class:`MatchCandidate` objects — ranked candidates
with a per-feature breakdown and audit rationale, never certified
matches.

Determinism contract (spec §Resilience):

- every feature is a pure function of its inputs; IDF weights come from
  the snapshot corpus (built once in :func:`find_candidates`);
- the score is the weighted sum with weights from ``MatchConfig``;
- output is sorted by ``(-score, a, b)`` — an explicit total order, so
  identical input produces byte-identical output;
- no clock, no network, no globals. Close dates and fetch times are
  data carried on the markets.
"""

from __future__ import annotations

import math
from collections.abc import Mapping
from dataclasses import dataclass
from datetime import datetime
from types import MappingProxyType
from typing import Literal

from equinox.config import MatchConfig
from equinox.matching.blocking import CorpusStats, candidate_pairs, market_ref
from equinox.matching.normalize import normalized_tokens, numeric_tokens
from equinox.model import Market, MarketSnapshot

FEATURE_NAMES = ("token_overlap", "idf_overlap", "numeric_agreement", "date_proximity")
"""The named features every candidate's breakdown carries, in fixed order."""

ThresholdBand = Literal["mid", "high"]
"""Bands of emitted candidates. Scores below MID never become candidates."""

BAND_MID: ThresholdBand = "mid"
BAND_HIGH: ThresholdBand = "high"

NEUTRAL_SCORE = 0.5
"""Feature value when evidence is missing rather than disagreeing.

Unknown is not zero and not one: a missing close date or a number
mentioned on only one side neither confirms nor refutes equivalence.
"""

_SECONDS_PER_DAY = 86_400.0

_MAX_EVIDENCE_TOKENS = 3
"""Shared rare tokens named in the rationale, most informative first."""


@dataclass(frozen=True)
class MatchCandidate:
    """One cross-venue candidate equivalence, with its audit trail.

    ``a``/``b`` are ``"<venue>/<market_id>"`` refs oriented so ``a < b``;
    ``features`` is the per-feature breakdown in :data:`FEATURE_NAMES`
    order; ``threshold_band`` is ``"mid"`` or ``"high"`` (``"high"`` is
    additionally flagged review-recommended by the spec); ``rationale``
    is ordered human-readable lines a reviewer can audit.
    """

    a: str
    b: str
    score: float
    features: Mapping[str, float]
    threshold_band: ThresholdBand
    rationale: tuple[str, ...]

    def __post_init__(self) -> None:
        if not self.a or not self.b:
            raise ValueError("MatchCandidate pair refs must be non-empty")
        if not self.a < self.b:
            raise ValueError(
                f"MatchCandidate refs must be oriented a < b, got ({self.a!r}, {self.b!r})"
            )
        if isinstance(self.score, bool) or not isinstance(self.score, (int, float)):
            raise TypeError(f"MatchCandidate.score must be a number, got {self.score!r}")
        if not 0.0 <= self.score <= 1.0:
            raise ValueError(f"MatchCandidate.score {self.score!r} outside [0, 1]")
        if tuple(self.features) != FEATURE_NAMES:
            raise ValueError(
                f"MatchCandidate.features must carry exactly {FEATURE_NAMES} in order, "
                f"got {tuple(self.features)}"
            )
        for name, value in self.features.items():
            if isinstance(value, bool) or not isinstance(value, (int, float)):
                raise TypeError(f"feature {name!r} must be a number, got {value!r}")
            if not 0.0 <= value <= 1.0:
                raise ValueError(f"feature {name!r} value {value!r} outside [0, 1]")
        if self.threshold_band not in (BAND_MID, BAND_HIGH):
            raise ValueError(
                f"threshold_band must be {BAND_MID!r} or {BAND_HIGH!r}, "
                f"got {self.threshold_band!r}"
            )
        if not self.rationale or any(not isinstance(line, str) for line in self.rationale):
            raise ValueError("MatchCandidate.rationale must be non-empty text lines")
        object.__setattr__(
            self, "features", MappingProxyType({k: float(v) for k, v in self.features.items()})
        )
        object.__setattr__(self, "score", float(self.score))

    def as_dict(self) -> dict[str, object]:
        """JSON-safe representation with stable key order."""
        return {
            "a": self.a,
            "b": self.b,
            "score": self.score,
            "features": {name: self.features[name] for name in FEATURE_NAMES},
            "threshold_band": self.threshold_band,
            "rationale": list(self.rationale),
        }


def token_overlap(a: frozenset[str], b: frozenset[str]) -> float:
    """Plain Jaccard overlap of the two token sets."""
    union = len(a | b)
    return len(a & b) / union if union else 0.0


def idf_overlap(a: frozenset[str], b: frozenset[str], stats: CorpusStats) -> float:
    """IDF-weighted Jaccard: shared tokens counted by rarity.

    A shared rare proper noun ("lisnard") moves this far more than a
    shared "the" — the snapshot corpus supplies the weights.
    """
    union_weight = sum(stats.idf(token) for token in a | b)
    if union_weight <= 0.0:
        return 0.0
    shared_weight = sum(stats.idf(token) for token in a & b)
    return shared_weight / union_weight


def numeric_agreement(a: frozenset[str], b: frozenset[str]) -> float:
    """Agreement between the two sides' numeric tokens.

    Both silent → 1.0 (nothing disagrees); exactly one side cites
    numbers → :data:`NEUTRAL_SCORE`; both cite → Jaccard over the
    numeric tokens (``$100,000`` vs ``$150,000`` is disagreement).
    """
    numbers_a = numeric_tokens(a)
    numbers_b = numeric_tokens(b)
    if not numbers_a and not numbers_b:
        return 1.0
    if not numbers_a or not numbers_b:
        return NEUTRAL_SCORE
    union = len(numbers_a | numbers_b)
    return len(numbers_a & numbers_b) / union if union else 0.0


def date_proximity(
    closes_a: datetime | None, closes_b: datetime | None, scale_days: float
) -> float:
    """Exponential closeness of the two close dates: ``exp(-days/scale)``.

    Same instant → 1.0; a week apart at the default 7-day scale →
    ≈ 0.368; a month apart → ≈ 0.014. Either side unknown →
    :data:`NEUTRAL_SCORE`. On real venue data close dates often differ
    by convention (one venue closes at the event, the other at the end
    of a resolution window) — the feature reads ~0 there, which is the
    spike's honest measurement, not a bug.
    """
    if closes_a is None or closes_b is None:
        return NEUTRAL_SCORE
    gap_days = abs((closes_b - closes_a).total_seconds()) / _SECONDS_PER_DAY
    return math.exp(-gap_days / scale_days)


def compute_features(
    market_a: Market,
    market_b: Market,
    tokens_a: frozenset[str],
    tokens_b: frozenset[str],
    stats: CorpusStats,
    config: MatchConfig,
) -> dict[str, float]:
    """The four named features for one candidate pair, in fixed order."""
    return {
        "token_overlap": token_overlap(tokens_a, tokens_b),
        "idf_overlap": idf_overlap(tokens_a, tokens_b, stats),
        "numeric_agreement": numeric_agreement(tokens_a, tokens_b),
        "date_proximity": date_proximity(
            market_a.closes_at, market_b.closes_at, config.date_proximity_scale_days
        ),
    }


def score_pair(features: Mapping[str, float], config: MatchConfig) -> float:
    """Weighted sum of the features under *config* weights."""
    return (
        config.weight_token_overlap * features["token_overlap"]
        + config.weight_idf_overlap * features["idf_overlap"]
        + config.weight_numeric_agreement * features["numeric_agreement"]
        + config.weight_date_proximity * features["date_proximity"]
    )


def band_for_score(score: float, config: MatchConfig) -> ThresholdBand | Literal["below"]:
    """Classify *score*: ``high`` (≥ HIGH), ``mid`` (≥ MID), else ``below``."""
    if score >= config.high_threshold:
        return BAND_HIGH
    if score >= config.mid_threshold:
        return BAND_MID
    return "below"


def build_rationale(
    tokens_a: frozenset[str],
    tokens_b: frozenset[str],
    features: Mapping[str, float],
    score: float,
    band: ThresholdBand,
    stats: CorpusStats,
    config: MatchConfig,
    closes_a: datetime | None,
    closes_b: datetime | None,
) -> tuple[str, ...]:
    """Ordered audit lines: verdict, breakdown, evidence, caveats."""
    lines = [
        (
            f"score {score:.4f} in band '{band}' "
            f"(thresholds: mid ≥ {config.mid_threshold:.2f}, high ≥ {config.high_threshold:.2f})"
        ),
        "features: " + ", ".join(f"{name}={features[name]:.3f}" for name in FEATURE_NAMES),
    ]
    evidence = stats.rare_shared_tokens(tokens_a, tokens_b)[:_MAX_EVIDENCE_TOKENS]
    if evidence:
        idf_bits = ", ".join(f"{token!r} (idf {stats.idf(token):.2f})" for token in evidence)
        lines.append(f"strongest shared tokens: {idf_bits}")
    else:
        lines.append("no shared rare tokens; pair reached scoring via close-date blocking")
    gap_days = _close_gap_days(closes_a, closes_b)
    if gap_days is not None and gap_days > config.date_block_window_days:
        lines.append(
            f"close dates differ by {gap_days:.1f} days (beyond the "
            f"{config.date_block_window_days}-day blocking window) — verify the "
            "venues resolve the same window"
        )
    return tuple(lines)


def find_candidates(
    snapshot: MarketSnapshot, config: MatchConfig | None = None
) -> list[MatchCandidate]:
    """Ranked cross-venue candidate equivalences for a snapshot.

    Pipeline (spec §Matching): normalize every question once, block,
    score, drop pairs below the MID threshold, and sort by
    ``(-score, a, b)``. Pure: same snapshot, same config, byte-identical
    output.
    """
    if config is None:
        config = MatchConfig()

    markets = list(snapshot.markets)
    markets_by_ref = {market_ref(m): m for m in markets}
    tokens_by_ref = {ref: normalized_tokens(m.question) for ref, m in markets_by_ref.items()}
    stats = CorpusStats.build(
        [tokens_by_ref[market_ref(m)] for m in markets], config
    )

    candidates: list[MatchCandidate] = []
    for ref_a, ref_b in candidate_pairs(markets, tokens_by_ref, stats, config):
        market_a, market_b = markets_by_ref[ref_a], markets_by_ref[ref_b]
        tokens_a = tokens_by_ref[ref_a]
        tokens_b = tokens_by_ref[ref_b]
        features = compute_features(market_a, market_b, tokens_a, tokens_b, stats, config)
        score = score_pair(features, config)
        band = band_for_score(score, config)
        if band == "below":
            continue
        rationale = build_rationale(
            tokens_a,
            tokens_b,
            features,
            score,
            band,
            stats,
            config,
            market_a.closes_at,
            market_b.closes_at,
        )
        candidates.append(
            MatchCandidate(
                a=ref_a,
                b=ref_b,
                score=score,
                features=features,
                threshold_band=band,
                rationale=rationale,
            )
        )

    candidates.sort(key=lambda c: (-c.score, c.a, c.b))
    return candidates


def _close_gap_days(closes_a: datetime | None, closes_b: datetime | None) -> float | None:
    if closes_a is None or closes_b is None:
        return None
    return abs((closes_b - closes_a).total_seconds()) / _SECONDS_PER_DAY


__all__ = [
    "BAND_HIGH",
    "BAND_MID",
    "FEATURE_NAMES",
    "MatchCandidate",
    "band_for_score",
    "build_rationale",
    "compute_features",
    "date_proximity",
    "find_candidates",
    "idf_overlap",
    "numeric_agreement",
    "score_pair",
    "token_overlap",
]
