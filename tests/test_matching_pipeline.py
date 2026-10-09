"""Pipeline tests over the recorded corpus: recall, determinism, bands.

This is the spike's acceptance centerpiece (spec §Verification, row 3):
a seeded set of genuinely equivalent cross-venue pairs drawn from real
venue text must surface above the MID threshold, reruns must be
byte-identical, and every emitted candidate must carry its audit trail.
"""

import json

from conftest import FROZEN_AT
from equinox.config import MatchConfig
from equinox.matching import find_candidates
from equinox.matching.score import BAND_HIGH, BAND_MID, FEATURE_NAMES
from equinox.model import MarketSnapshot


def pair_key(entry: dict) -> frozenset[str]:
    """Seed-pair key: the two ``venue/market_id`` refs, order-free."""
    return frozenset(
        f"{side['venue']}/{side['market_id']}" for side in (entry["a"], entry["b"])
    )


def candidate_key(candidate) -> frozenset[str]:
    return frozenset((candidate.a, candidate.b))


def serialize(candidates) -> str:
    """Byte-stable serialization of a candidate list."""
    return json.dumps(
        [c.as_dict() for c in candidates], sort_keys=True, separators=(",", ":")
    )


def keyed_serialization(candidates) -> list[tuple[frozenset[str], str]]:
    """Per-candidate bytes keyed by pair — order-independent comparisons."""
    return sorted((candidate_key(c), serialize([c])) for c in candidates)


def test_seeded_equivalents_all_recalled(matching_snapshot, seeded_pairs):
    candidates = find_candidates(matching_snapshot)
    found = {candidate_key(c) for c in candidates}
    missed = [
        seed["note"]
        for seed in seeded_pairs["equivalent"]
        if pair_key(seed) not in found
    ]
    assert not missed, f"seeded equivalents missed: {missed}"


def test_seeded_recall_at_least_spec_floor(matching_snapshot, seeded_pairs):
    # The spec's reversal trigger (A3) is recall below ~50%; assert the
    # measured value clears it with the observed number pinned.
    candidates = find_candidates(matching_snapshot)
    found = {candidate_key(c) for c in candidates}
    recall = sum(1 for seed in seeded_pairs["equivalent"] if pair_key(seed) in found) / len(
        seeded_pairs["equivalent"]
    )
    assert recall >= 0.5, "recall below the spec's A3 reversal floor"
    assert recall == 1.0, f"measured recall regressed to {recall:.2f}"


def test_reruns_are_byte_identical(matching_snapshot):
    first = serialize(find_candidates(matching_snapshot))
    second = serialize(find_candidates(matching_snapshot))
    assert first == second


def test_market_order_does_not_change_output(matching_corpus, matching_snapshot):
    # Rebuild the snapshot with reversed market and venue order: the same
    # candidates, byte-identical per pair.
    rebuilt = MarketSnapshot(
        fetched_at=FROZEN_AT,
        venues=["kalshi", "polymarket"],
        markets=list(reversed(matching_corpus)),
    )
    assert keyed_serialization(find_candidates(rebuilt)) == keyed_serialization(
        find_candidates(matching_snapshot)
    )


def test_candidates_respect_bands_and_total_order(matching_snapshot):
    config = MatchConfig()
    candidates = find_candidates(matching_snapshot, config)
    scores = [c.score for c in candidates]
    assert scores == sorted(scores, reverse=True)
    for candidate in candidates:
        assert config.mid_threshold <= candidate.score <= 1.0
        assert candidate.threshold_band in (BAND_MID, BAND_HIGH)
        if candidate.threshold_band == BAND_HIGH:
            assert candidate.score >= config.high_threshold
        else:
            assert candidate.score < config.high_threshold


def test_every_candidate_carries_audit_trail(matching_snapshot):
    candidates = find_candidates(matching_snapshot)
    assert candidates
    for candidate in candidates:
        assert candidate.rationale, f"empty rationale for {candidate.a} <-> {candidate.b}"
        assert all(isinstance(line, str) and line for line in candidate.rationale)
        assert tuple(candidate.features) == FEATURE_NAMES
        assert candidate.a.split("/", 1)[0] != candidate.b.split("/", 1)[0], (
            "same-venue pair leaked into candidates"
        )


def test_no_duplicate_pairs(matching_snapshot):
    candidates = find_candidates(matching_snapshot)
    keys = [candidate_key(c) for c in candidates]
    assert len(keys) == len(set(keys))


def test_high_band_near_miss_keeps_its_date_caveat(matching_snapshot, seeded_pairs):
    # Known limitation, pinned on purpose: verbatim-identical text with
    # incompatible close dates scores like a true equivalent — the score
    # cannot see resolution windows. The rationale must flag the gap so a
    # reviewer sees the caveat in the audit trail.
    nasty = next(
        seed
        for seed in seeded_pairs["near_miss"]
        if "incompatible close dates" in seed["note"]
    )
    candidates = find_candidates(matching_snapshot)
    match = next((c for c in candidates if candidate_key(c) == pair_key(nasty)), None)
    assert match is not None, "the verbatim-text/incompatible-date pair stopped being surfaced"
    assert match.threshold_band == BAND_HIGH
    assert any("close dates differ" in line for line in match.rationale), (
        "date-gap caveat missing from the audit trail"
    )


def test_strict_config_produces_subset(matching_snapshot):
    # Raising the thresholds under default weights can only shrink the
    # candidate set — never add pairs or reorder it.
    default = find_candidates(matching_snapshot)
    strict = find_candidates(
        matching_snapshot, MatchConfig(mid_threshold=0.85, high_threshold=0.95)
    )
    default_keys = {candidate_key(c) for c in default}
    strict_keys = {candidate_key(c) for c in strict}
    assert strict_keys
    assert strict_keys <= default_keys, "strict config emitted pairs the default dropped"


def test_empty_snapshot_yields_no_candidates():
    snapshot = MarketSnapshot(fetched_at=FROZEN_AT, venues=[], markets=[])
    assert find_candidates(snapshot) == []
