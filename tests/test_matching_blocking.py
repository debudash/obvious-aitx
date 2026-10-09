"""Blocking unit tests: rare-token and close-date candidate generation.

Blocks must be conservative (few candidates), never same-venue, never
duplicated, and always in a deterministic canonical order.
"""

from datetime import UTC, datetime, timedelta

import pytest

from equinox.config import MatchConfig
from equinox.matching.blocking import CorpusStats, candidate_pairs, market_ref
from equinox.matching.normalize import normalized_tokens
from equinox.model import FeeParams, Market, Outcome

FROZEN_AT = datetime(2026, 10, 9, 12, 0, 0, tzinfo=UTC)
FEES = FeeParams(model="none", rate=None, source="fixture")
YES = (Outcome(name="Yes", side="yes", yes_price=0.5),)


def make_market(question: str, venue: str, market_id: str, closes_at=None) -> Market:
    return Market(
        market_id=market_id,
        venue=venue,
        question=question,
        outcomes=YES,
        fees=FEES,
        closes_at=closes_at,
        liquidity=None,
        fetched_at=FROZEN_AT,
        raw_ref=f"fixture:{market_id}",
    )


def tokens_for(markets: list[Market]) -> dict[str, frozenset[str]]:
    return {market_ref(m): normalized_tokens(m.question) for m in markets}


def blocked_pairs(
    markets: list[Market], config: MatchConfig | None = None
) -> list[tuple[str, str]]:
    config = config or MatchConfig()
    tokens = tokens_for(markets)
    stats = CorpusStats.build(list(tokens.values()), config)
    return candidate_pairs(markets, tokens, stats, config)


def test_shared_rare_token_blocks_cross_venue_pair():
    a = make_market("Will David Lisnard win the 2027 French election?", "alpha", "A1")
    b = make_market("David Lisnard 2027 French election winner", "beta", "B1")
    assert blocked_pairs([a, b]) == [("alpha/A1", "beta/B1")]


def test_common_only_tokens_do_not_block():
    # "fed" appears in every market of the corpus: above the rarity cap.
    markets = [make_market(f"Will the Fed act? extra{i}", "alpha", f"A{i}") for i in range(19)]
    markets.append(make_market("Will the Fed act?", "beta", "B0"))
    tokens = tokens_for(markets)
    stats = CorpusStats.build(list(tokens.values()), MatchConfig())
    assert not stats.is_rare("fed")
    assert candidate_pairs(markets, tokens, stats, MatchConfig()) == []


def test_rare_token_cap_has_a_floor_of_two_documents():
    # Regression: a token shared by BOTH sides of a pair has df ≥ 2 by
    # construction — a floor of one made shared tokens never rare in tiny
    # corpora, so blocking could never fire at all.
    stats = CorpusStats.build([frozenset({"a"}), frozenset({"b"})], MatchConfig())
    assert stats.rare_token_cap == 2
    assert stats.is_rare("a") and stats.is_rare("b")


def test_close_date_proximity_blocks_without_shared_tokens():
    close = datetime(2026, 10, 28, 18, 0, 0, tzinfo=UTC)
    a = make_market("Will the Fed cut rates?", "alpha", "A1", closes_at=close)
    b = make_market(
        "Fed policy decision outcome", "beta", "B1", closes_at=close + timedelta(days=3)
    )
    assert blocked_pairs([a, b]) == [("alpha/A1", "beta/B1")]


def test_dates_beyond_window_do_not_block():
    # Token-disjoint questions: only the date path could pair them, and
    # the dates are 9 days apart — beyond the 7-day window. (A shared
    # common word would now block in a 2-doc corpus, where every df=2
    # token counts as rare.)
    close = datetime(2026, 10, 28, 18, 0, 0, tzinfo=UTC)
    a = make_market("Will the Fed cut rates?", "alpha", "A1", closes_at=close)
    b = make_market(
        "Banana harvest quota outcome", "beta", "B1", closes_at=close + timedelta(days=9)
    )
    assert blocked_pairs([a, b]) == []


def test_same_venue_pairs_are_never_candidates():
    q = "Will David Lisnard win the 2027 French election?"
    a = make_market(q, "alpha", "A1")
    b = make_market(q, "alpha", "A2")
    assert blocked_pairs([a, b]) == []


def test_pairs_are_canonical_deduplicated_and_sorted():
    q = "Will David Lisnard win the 2027 French election?"
    a = make_market(q, "alpha", "A1")
    b1 = make_market(q + " winner", "beta", "B1")
    b2 = make_market(q + " odds", "beta", "B2")
    pairs = blocked_pairs([b2, a, b1])
    assert pairs == sorted(set(pairs))
    assert all(left < right for left, right in pairs)


def test_missing_close_dates_are_tolerated():
    # Token-disjoint questions and no close dates: no blocking path can
    # fire, so no pairs — and no crash on the missing dates.
    a = make_market("Will the Fed cut rates?", "alpha", "A1", closes_at=None)
    b = make_market("Banana harvest quota outcome", "beta", "B1", closes_at=None)
    assert blocked_pairs([a, b]) == []


def test_idf_is_positive_even_for_ubiquitous_tokens():
    sets = [frozenset({"fed", f"m{i}"}) for i in range(20)]
    stats = CorpusStats.build(sets, MatchConfig())
    assert stats.idf("fed") > 0.0


def test_recorded_corpus_blocks_cheaply_and_cross_venue_only(matching_corpus):
    # 1,200 recorded markets: blocking must cut the pairwise space
    # decisively — measured 7,296 candidates against 719,400 all-pairs,
    # a ~99x reduction. Assert the reduction stays at least 50x so a
    # blocking regression cannot hide here silently.
    all_pairs = len(matching_corpus) * (len(matching_corpus) - 1) // 2
    pairs = blocked_pairs(matching_corpus)
    assert pairs
    assert len(pairs) * 50 < all_pairs
    for left, right in pairs:
        assert left.split("/", 1)[0] != right.split("/", 1)[0]


@pytest.mark.parametrize(("df", "cap_is_rare"), [(1, True), (2, True), (3, False)])
def test_rarity_cap_boundary(df: int, cap_is_rare: bool):
    sets = [frozenset({"x"}) for _ in range(df)] + [
        frozenset({"other"}) for _ in range(40 - df)
    ]
    stats = CorpusStats.build(sets, MatchConfig())
    assert stats.is_rare("x") is cap_is_rare
