"""Candidate generation for cross-venue matching (spec §Matching).

Blocking is the two-stage design's first stage: cheap, conservative
candidate generation that keeps scoring near-linear over a day's corpus
(thousands of markets instead of the 50k × 50k all-pairs blowup).

A pair of markets becomes a blocking candidate when either:

- they share at least one **rare token** — a token whose document
  frequency across the snapshot is at most
  ``MatchConfig.rare_token_max_doc_freq`` (with a floor of one document
  so small fixture corpora still block), or
- their close dates fall within ``MatchConfig.date_block_window_days``
  of each other.

Only cross-venue pairs are emitted: two markets on one venue are two
listings of that venue, never an equivalence problem. Pairs are
canonicalized (smaller ref first) and returned in an explicit total
order — sorted by ``(a, b)`` — so identical input yields identical
output.
"""

from __future__ import annotations

import math
from collections.abc import Mapping, Sequence
from dataclasses import dataclass
from types import MappingProxyType

from equinox.config import MatchConfig
from equinox.model import Market

_SECONDS_PER_DAY = 86_400.0


def market_ref(market: Market) -> str:
    """Unambiguous reference for a market: ``"<venue>/<market_id>"``."""
    return f"{market.venue}/{market.market_id}"


@dataclass(frozen=True)
class CorpusStats:
    """Document frequencies and IDF weights over one snapshot's questions.

    Each market's normalized token set is one document. ``idf`` uses the
    smoothed form ``log(1 + N/df)`` — strictly positive (a token present
    in every document still contributes weight), so a pair agreeing only
    on ubiquitous tokens cannot earn a perfect ``idf_overlap``.
    """

    doc_count: int
    doc_freq: Mapping[str, int]
    rare_token_cap: int

    @classmethod
    def build(cls, token_sets: Sequence[frozenset[str]], config: MatchConfig) -> CorpusStats:
        """Count document frequencies over *token_sets* under *config*.

        The rare-token cap is ``ceil(rare_token_max_doc_freq × N)`` with
        a floor of two documents: a token shared by both sides of a
        candidate pair has document frequency ≥ 2 by construction, so a
        floor of one would make shared tokens never rare in tiny
        corpora and blocking could never fire.
        """
        doc_freq: dict[str, int] = {}
        for tokens in token_sets:
            for token in tokens:
                doc_freq[token] = doc_freq.get(token, 0) + 1
        cap = max(2, math.ceil(config.rare_token_max_doc_freq * len(token_sets)))
        return cls(
            doc_count=len(token_sets),
            doc_freq=MappingProxyType(doc_freq),
            rare_token_cap=cap,
        )

    def idf(self, token: str) -> float:
        """Smoothed inverse document frequency: ``log(1 + N/df)``.

        A token absent from the corpus (possible only for hand-built
        token sets, never for pairs drawn from it) is treated as df=1.
        """
        df = max(1, self.doc_freq.get(token, 0))
        return math.log(1.0 + self.doc_count / df)

    def is_rare(self, token: str) -> bool:
        """True when the token's document frequency is within the cap."""
        return self.doc_freq.get(token, 0) <= self.rare_token_cap

    def rare_shared_tokens(self, a: frozenset[str], b: frozenset[str]) -> tuple[str, ...]:
        """Shared tokens that qualify as rare, sorted for determinism."""
        return tuple(sorted(token for token in a & b if self.is_rare(token)))


def candidate_pairs(
    markets: Sequence[Market],
    tokens_by_ref: Mapping[str, frozenset[str]],
    stats: CorpusStats,
    config: MatchConfig,
) -> list[tuple[str, str]]:
    """Cross-venue candidate pairs under rare-token or close-date blocking.

    ``tokens_by_ref`` maps each market's :func:`market_ref` to its
    normalized token set (built once by the caller and shared with
    scoring). Returns canonically-oriented pairs sorted by ``(a, b)`` —
    never duplicates, never same-venue pairs.
    """
    pairs: set[tuple[str, str]] = set()
    markets_by_ref: dict[str, Market] = {market_ref(m): m for m in markets}

    # Rare-token blocking: invert rare tokens into posting lists, then
    # emit every cross-venue combination within a posting list. Posting
    # lists stay short precisely because the tokens are rare.
    postings: dict[str, list[str]] = {}
    for market in markets:
        ref = market_ref(market)
        for token in tokens_by_ref[ref]:
            if stats.is_rare(token):
                postings.setdefault(token, []).append(ref)
    for refs in postings.values():
        for i, ref_a in enumerate(refs):
            for ref_b in refs[i + 1 :]:
                if _venues_differ(markets_by_ref, ref_a, ref_b):
                    pairs.add(_canonical(ref_a, ref_b))

    # Close-date bucketing: sweep markets sorted by close time with a
    # two-pointer window instead of comparing all pairs.
    dated = sorted(
        (
            (m.closes_at, market_ref(m))
            for m in markets
            if m.closes_at is not None
        ),
        key=lambda item: (item[0], item[1]),
    )
    window_seconds = config.date_block_window_days * _SECONDS_PER_DAY
    for i, (close_a, ref_a) in enumerate(dated):
        for close_b, ref_b in dated[i + 1 :]:
            delta = (close_b - close_a).total_seconds()
            if delta > window_seconds:
                break
            if _venues_differ(markets_by_ref, ref_a, ref_b):
                pairs.add(_canonical(ref_a, ref_b))

    return sorted(pairs)


def _canonical(a: str, b: str) -> tuple[str, str]:
    return (a, b) if a < b else (b, a)


def _venues_differ(markets_by_ref: Mapping[str, Market], ref_a: str, ref_b: str) -> bool:
    return markets_by_ref[ref_a].venue != markets_by_ref[ref_b].venue


__all__ = ["CorpusStats", "candidate_pairs", "market_ref"]
