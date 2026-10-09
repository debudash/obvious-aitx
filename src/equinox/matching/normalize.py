"""Question-text normalization for cross-venue matching (spec §Matching).

Venues describe the same real-world event in house style: one leans on
"Will ..." constructions with embedded dates and $ amounts; another
writes out resolution criteria with "U.S."-style acronyms and
dollar-denominated levels. Matching compares tokens, so the text is
reduced to a canonical token stream first:

1. Unicode NFKD decomposition strips accents (``Núñez`` → ``nunez``) so
   both spellings collide.
2. Lowercase; currency/percent symbols and digit-group commas normalize
   to plain numerics (``$1,250,000`` → ``1250000``, ``62%`` → ``62``);
   magnitudes expand in both word and suffix form (``$1.5 million`` and
   ``$1.5M`` → ``1500000``); decimals are preserved as single tokens
   (``3.5%`` → ``3.5``).
3. Frequent contractions expand (``won't`` → ``will not``) and possessive
   ``'s`` drops (``Trump's`` → ``trump``) so the same entity tokenizes
   the same way on both sides.
4. Acronym dots collapse (``U.S.`` → ``us``) — dots inside single-letter
   runs are abbreviation style, not word separators.
5. Month abbreviations canonicalize (``Oct`` → ``october``).
6. Embedded event dates are stripped (ISO forms, numeric ``m/d/y``,
   ``month day[, year]``, ``day month year``, ``month year``). A bare
   month name survives — it is a strong lexical token, and the close-date
   features carry date semantics from metadata instead.
7. Punctuation and whitespace collapse to single spaces; leading
   question boilerplate (``Will the ...``) is dropped from the token
   list.

The result is deterministic and idempotent: ``normalize_question`` is a
pure function with no clock, no network, no randomness.
"""

from __future__ import annotations

import re
import unicodedata
from collections.abc import Sequence

QUESTION_OPENERS = frozenset(
    {
        "will",
        "does",
        "do",
        "did",
        "can",
        "could",
        "should",
        "would",
        "is",
        "are",
        "was",
        "were",
        "has",
        "have",
        "had",
        "am",
        "may",
        "might",
        "must",
    }
)
"""Leading interrogative/auxiliary verbs stripped as venue boilerplate.

Applied uniformly to every venue's text, so even a sentence where the
word is a proper noun ("Will Smith wins ...") is mangled the same way on
both sides of a pair — harmless for matching.
"""

_ARTICLES = frozenset({"the", "a", "an"})

_CURRENCY_OR_PERCENT = re.compile(r"[$€£¥₹¢%]")

_DIGIT_GROUP_COMMAS = re.compile(r"(?<=\d),(?=\d)")

_MAGNITUDE_SUFFIX = re.compile(r"\b(\d+(?:\.\d+)?)([kmb])\b")
_MAGNITUDE_MULTIPLIERS = {"k": 1_000, "m": 1_000_000, "b": 1_000_000_000}

_WORD_MAGNITUDE = re.compile(
    r"\b(\d+(?:\.\d+)?)\s*(thousand|million|billion|trillion)\b", re.IGNORECASE
)
_WORD_MULTIPLIERS = {
    "thousand": 1_000,
    "million": 1_000_000,
    "billion": 1_000_000_000,
    "trillion": 1_000_000_000_000,
}

_ACRONYM_DOTS = re.compile(r"\b(?:[a-z]\.)+(?=[a-z]\b)")

# A dot between digits is a decimal point, not sentence punctuation: guard
# it with a private-use sentinel through the punctuation collapse and
# restore it afterwards, so ``3.5`` stays one token.
_DECIMAL_DOT = re.compile(r"(?<=\d)\.(?=\d)")
_DECIMAL_GUARD = "\ue000"

_CONTRACTIONS = {
    "won't": "will not",
    "can't": "can not",
    "don't": "do not",
    "doesn't": "does not",
    "didn't": "did not",
    "isn't": "is not",
    "aren't": "are not",
    "wasn't": "was not",
    "weren't": "were not",
    "hasn't": "has not",
    "haven't": "have not",
    "hadn't": "had not",
    "couldn't": "could not",
    "shouldn't": "should not",
    "wouldn't": "would not",
    "shan't": "shall not",
    "ain't": "is not",
}
_CONTRACTION_RE = re.compile(
    r"\b(?:" + "|".join(re.escape(c) for c in _CONTRACTIONS) + r")\b", re.IGNORECASE
)
_POSSESSIVE_S = re.compile(r"(?<=\w)'s\b")

_MONTHS = (
    "january",
    "february",
    "march",
    "april",
    "may",
    "june",
    "july",
    "august",
    "september",
    "october",
    "november",
    "december",
)
_MONTH_ABBREVIATIONS = {
    "jan": "january",
    "feb": "february",
    "mar": "march",
    "apr": "april",
    "jun": "june",
    "jul": "july",
    "aug": "august",
    "sep": "september",
    "sept": "september",
    "oct": "october",
    "nov": "november",
    "dec": "december",
}
_MONTH_ALTERNATION = "|".join(_MONTHS)

# Ordered most-specific first: each form must run before the looser ones
# could partially consume it.
_DATE_PATTERNS = (
    re.compile(rf"\b(?:{_MONTH_ALTERNATION})\s+\d{{1,2}}(?:st|nd|rd|th)?\s*,?\s+\d{{4}}\b"),
    re.compile(rf"\b\d{{1,2}}(?:st|nd|rd|th)?\s+(?:{_MONTH_ALTERNATION})\s*,?\s+\d{{4}}\b"),
    re.compile(rf"\b(?:{_MONTH_ALTERNATION})\s+\d{{4}}\b"),
    re.compile(rf"\b(?:{_MONTH_ALTERNATION})\s+\d{{1,2}}(?:st|nd|rd|th)?(?:\s*[-–]\s*\d{{1,2}}(?:st|nd|rd|th)?)?\b"),
    re.compile(r"\b\d{4}[-/]\d{1,2}[-/]\d{1,2}\b"),
    re.compile(r"\b\d{1,2}/\d{1,2}/\d{2,4}\b"),
)

_NON_WORD = re.compile(r"[^a-z0-9\s\ue000]+")
_WHITESPACE = re.compile(r"\s+")

_CONTAINS_DIGIT = re.compile(r"\d")


def _strip_accents(text: str) -> str:
    """NFKD-decompose and drop combining marks: ``Núñez`` → ``Nunez``."""
    decomposed = unicodedata.normalize("NFKD", text)
    return "".join(ch for ch in decomposed if not unicodedata.combining(ch))


def _expand_magnitudes(text: str) -> str:
    """Expand magnitudes into plain numerics: ``100k`` → ``100000``,
    ``1.5 million`` → ``1500000`` — word and suffix forms agree."""

    def _expand_word(match: re.Match[str]) -> str:
        number = float(match.group(1))
        multiplier = _WORD_MULTIPLIERS[match.group(2).lower()]
        expanded = number * multiplier
        return str(int(expanded)) if expanded == int(expanded) else f"{expanded:g}"

    def _expand_suffix(match: re.Match[str]) -> str:
        number = float(match.group(1))
        multiplier = _MAGNITUDE_MULTIPLIERS[match.group(2)]
        expanded = number * multiplier
        return str(int(expanded)) if expanded == int(expanded) else f"{expanded:g}"

    text = _WORD_MAGNITUDE.sub(_expand_word, text)
    return _MAGNITUDE_SUFFIX.sub(_expand_suffix, text)


def _expand_contractions(text: str) -> str:
    """Expand the frequent negative contractions and drop possessive 's
    so entity tokens collide across phrasings (``Trump's`` → ``trump``)."""
    text = _CONTRACTION_RE.sub(lambda m: _CONTRACTIONS[m.group(0).lower()], text)
    return _POSSESSIVE_S.sub("", text)


_ABBREVIATION = r"jan|feb|mar|apr|jun|jul|aug|sep|sept|oct|nov|dec"
# A dot directly after a month abbreviation is abbreviation style, not a
# separator: drop it first ("jan. 31" → "jan 31"), or the date patterns
# never see the month next to its day.
_MONTH_ABBREV_DOT = re.compile(rf"\b({_ABBREVIATION})\.(?=\s|$)")


def _canonicalize_months(text: str) -> str:
    """Replace month abbreviations with full month names."""
    text = _MONTH_ABBREV_DOT.sub(lambda m: m.group(1), text)

    def _expand(match: re.Match[str]) -> str:
        return _MONTH_ABBREVIATIONS[match.group(0)]

    return re.sub(rf"\b(?:{_ABBREVIATION})\b", _expand, text)


def normalize_question(text: str) -> str:
    """Reduce a venue question to its canonical normalized token string.

    Pure: same input, same output, no I/O, no clock. Returns ``""`` for
    blank or wholly-punctuation input. Idempotent — normalizing an
    already-normalized string is a no-op.
    """
    if not isinstance(text, str):
        raise TypeError(f"question text must be a str, got {type(text).__name__}")

    text = _strip_accents(text).lower()
    text = _expand_contractions(text)
    text = _CURRENCY_OR_PERCENT.sub(" ", text)
    text = _DIGIT_GROUP_COMMAS.sub("", text)
    text = _expand_magnitudes(text)
    text = _ACRONYM_DOTS.sub(lambda m: m.group(0).replace(".", ""), text)
    text = _canonicalize_months(text)
    for pattern in _DATE_PATTERNS:
        text = pattern.sub(" ", text)
    text = _DECIMAL_DOT.sub(_DECIMAL_GUARD, text)
    text = _NON_WORD.sub(" ", text)
    text = _WHITESPACE.sub(" ", text).strip()
    text = text.replace(_DECIMAL_GUARD, ".")

    tokens = text.split(" ") if text else []
    while tokens and (tokens[0] in QUESTION_OPENERS or tokens[0] in _ARTICLES):
        tokens.pop(0)
    return " ".join(tokens)


def normalized_tokens(text: str) -> frozenset[str]:
    """The distinct normalized tokens of a question — the unit matching compares."""
    return frozenset(normalize_question(text).split())


def numeric_tokens(tokens: Sequence[str]) -> frozenset[str]:
    """Tokens containing a digit — the evidence :mod:`score` compares for
    ``numeric_agreement``. Dates have already been stripped by
    :func:`normalize_question`, so these are levels, thresholds, and
    bare years."""
    return frozenset(token for token in tokens if _CONTAINS_DIGIT.search(token))


__all__ = [
    "QUESTION_OPENERS",
    "normalize_question",
    "normalized_tokens",
    "numeric_tokens",
]
