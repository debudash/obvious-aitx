"""Normalization unit tests: every rule the matcher depends on, per rule.

The fixture corpus (tests/fixtures/matching/) holds real venue text; a
few tests quote short excerpts from it. The gold property is *convergence*:
two venues' phrasings of the same event normalize to the same token set.
"""

from equinox.matching.normalize import (
    normalize_question,
    normalized_tokens,
    numeric_tokens,
)


def tokens(text: str) -> list[str]:
    # Order-preserving token list: normalized_tokens is a set, and its
    # list() order is hash-randomized — assertions on sequence need split().
    return normalize_question(text).split()


# --- lowercase, opener stripping, punctuation and whitespace ---


def test_lowercase_and_punctuation_collapse():
    # Duplicate tokens survive (token lists are multisets; frozensets dedupe
    # downstream) and only LEADING openers/articles are stripped.
    assert tokens("Will the Fed—maybe, maybe not—hold rates?") == [
        "fed",
        "maybe",
        "maybe",
        "not",
        "hold",
        "rates",
    ]


def test_will_opener_stripped():
    assert "will" not in tokens("Will the Fed hold rates in October?")
    assert "fed" in tokens("Will the Fed hold rates in October?")


def test_does_and_do_openers_stripped():
    assert "does" not in tokens("Does the Fed cut rates?")
    assert "do" not in tokens("Do Democrats keep the House?")


def test_contractions_expand():
    # "Won't" expands to "will not"; the leading "will" is then stripped
    # as boilerplate, leaving the negative visible for matching.
    assert tokens("Won't the Fed cut?") == ["not", "the", "fed", "cut"]
    assert tokens("Trump's the GOP nominee?") == ["trump", "the", "gop", "nominee"]


def test_whitespace_of_every_kind_collapses():
    assert normalize_question("Fed\tcuts\nrates   twice??") == "fed cuts rates twice"


def test_empty_and_blank_questions_normalize_to_empty():
    assert normalize_question("") == ""
    assert normalize_question("   \n\t ") == ""
    assert normalized_tokens("  ?!  ") == frozenset()


# --- embedded event dates ---


def test_month_day_year_dates_stripped():
    # The event date must not become a discriminative numeric token.
    a = numeric_tokens(normalized_tokens("Government shutdown on October 1, 2026?"))
    b = numeric_tokens(normalized_tokens("Government shutdown?"))
    assert a == b == frozenset()


def test_numeric_dates_stripped():
    assert numeric_tokens(normalized_tokens("Rate decision on 10/29/2026?")) == frozenset()


def test_date_words_left_when_no_date_attached():
    # "October" alone is a month word, not a date; keep the token, drop none.
    assert "october" in tokens("Fed decision in October?")
    assert numeric_tokens(normalized_tokens("Fed decision in October?")) == frozenset()


def test_abbreviated_months_expand_and_strip_as_dates():
    # "Sept 30" canonicalizes to "september 30", which is an embedded
    # event date and strips — same as the spelled-out form.
    assert tokens("Shutdown by Sept 30?") == ["shutdown", "by"]
    assert tokens("Shutdown by Jan. 31?") == ["shutdown", "by"]
    assert tokens("Shutdown by September 30?") == ["shutdown", "by"]


# --- currency, magnitude, percent ---


def test_currency_symbols_stripped_values_kept():
    assert numeric_tokens(normalized_tokens("Will debt exceed $38 trillion?")) == frozenset(
        {"38000000000000"}
    )


def test_currency_and_percent_normalize_to_plain_numerics():
    a = numeric_tokens(normalized_tokens("Will revenue exceed $100,000?"))
    b = numeric_tokens(normalized_tokens("Will revenue exceed 100000?"))
    assert a == b


def test_magnitude_words_expand():
    assert numeric_tokens(normalized_tokens("Debt over $1.5 million?")) == frozenset(
        {"1500000"}
    )
    assert numeric_tokens(normalized_tokens("Debt over 1.5 million?")) == frozenset(
        {"1500000"}
    )


def test_percent_symbol_stripped_value_kept():
    assert numeric_tokens(normalized_tokens("Will CPI hit 3.5%?")) == frozenset({"3.5"})


def test_percent_words_normalize():
    a = numeric_tokens(normalized_tokens("CPI at 3.5 percent?"))
    b = numeric_tokens(normalized_tokens("CPI at 3.5%?"))
    assert a == b


# --- diacritics and acronym expansion ---


def test_accent_insensitive():
    assert tokens("Will Lé Pen win?") == ["le", "pen", "win"]
    assert normalized_tokens("Lé Pen") == normalized_tokens("Le Pen")


def test_abbreviations_collapse_dots_to_words():
    # "U.S." dot-collapse makes dotted and undotted spellings collide;
    # spelled-out "United States" is its own phrasing — matching handles
    # the residual difference through partial overlap, not identity.
    assert normalized_tokens("US election") == normalized_tokens("U.S. election")
    assert normalized_tokens("US election") != normalized_tokens("United States election")


# --- convergence on real fixture text ---


def test_venue_phrasings_of_same_event_converge():
    # Same event, different wrappers: normalization leaves a high-overlap
    # token set — identity is not required, matching works on overlap.
    # Measured on this pair: 6 shared / 9 union (0.67) — the asymmetric
    # "win ... the" vs "winner" tails keep it below identity.
    a = normalized_tokens("Will David Lisnard win the 2027 French presidential election?")
    b = normalized_tokens("David Lisnard 2027 French presidential election winner")
    shared = len(a & b) / len(a | b)
    assert shared >= 0.6


def test_different_events_do_not_converge():
    a = normalized_tokens("Will the Fed hold rates in October?")
    b = normalized_tokens("Will the Fed cut rates in December?")
    assert a != b
