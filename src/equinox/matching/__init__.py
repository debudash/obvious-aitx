"""Deterministic candidate-equivalence detection over the shared model.

Two-stage pipeline (spec §Matching): :mod:`equinox.matching.normalize`
canonicalizes question text, :mod:`equinox.matching.blocking` generates
cross-venue candidate pairs from rare tokens or close-date proximity,
and :mod:`equinox.matching.score` ranks them into
:class:`~equinox.matching.MatchCandidate` objects with per-feature
breakdowns and audit rationale.

The public entry point is :func:`find_candidates`::

    from equinox.matching import find_candidates

    candidates = find_candidates(snapshot)  # MatchConfig defaults
    top = candidates[0]
    print(top.a, top.b, top.score, top.threshold_band, top.rationale)

Pure layer: no I/O, no clock, no venue names — enforced by test.
"""

from __future__ import annotations

from equinox.matching.blocking import CorpusStats, candidate_pairs, market_ref
from equinox.matching.normalize import (
    normalize_question,
    normalized_tokens,
    numeric_tokens,
)
from equinox.matching.score import (
    BAND_HIGH,
    BAND_MID,
    FEATURE_NAMES,
    MatchCandidate,
    band_for_score,
    compute_features,
    find_candidates,
    score_pair,
)

__all__ = [
    "BAND_HIGH",
    "BAND_MID",
    "FEATURE_NAMES",
    "CorpusStats",
    "MatchCandidate",
    "band_for_score",
    "candidate_pairs",
    "compute_features",
    "find_candidates",
    "market_ref",
    "normalize_question",
    "normalized_tokens",
    "numeric_tokens",
    "score_pair",
]
