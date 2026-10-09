"""Shared internal market model: markets, outcomes, fee params, snapshots.

Pure data shapes and scalar normalization helpers consumed by every
downstream layer (matching, routing, CLI). This package performs no I/O:
no network, no filesystem, no clock reads — time exists only as data
(``fetched_at`` / ``closes_at``) handed in by the venue adapters.

Normalization rules (spec §Contracts):

- Polymarket quotes arrive on the 0–1 probability scale and are used as-is.
- Kalshi dollar-denominated quotes are already probability-equivalent
  ($0.62 is a YES price of 0.62); they pass through divided by 1.0 — a
  documented no-op marking the unit boundary — under the same bounds check.
- The bounds check plus a plausibility floor catch the classic unit traps:
  62 cents read as ``62`` (out of range) or as ``0.000062`` (below the
  floor; no venue book quotes that fine).
- Optional fields are None-tolerant: unknown is never zero.
- The YES side is made explicit at normalization time (``Outcome.side``),
  so matching and routing never re-derive venue label conventions.
"""

from __future__ import annotations

from collections.abc import Sequence
from dataclasses import dataclass
from datetime import UTC, datetime
from typing import Literal

Side = Literal["yes", "no", "other"]
"""Canonical outcome role, assigned by adapters at normalization time.

``yes``/``no`` mark the two sides of a binary market; ``other`` covers
multi-outcome markets. Downstream layers read this field — they never
re-derive YES/NO from the venue-native ``name`` label.
"""


class PriceScaleError(ValueError):
    """A price value is outside the valid probability scale or mis-scaled."""


class TimeParseError(ValueError):
    """A timestamp string could not be parsed as ISO-8601."""


def _as_price(value: object, field_label: str) -> float:
    """Validate *value* as a price on the 0–1 scale and return it as a float."""
    if isinstance(value, bool) or not isinstance(value, (int, float)):
        raise PriceScaleError(
            f"{field_label}: expected a real number, got {type(value).__name__}"
        )
    number = float(value)
    if number != number:  # NaN: the only float that fails every comparison
        raise PriceScaleError(f"{field_label}: {value!r} is not a finite number")
    if not 0.0 <= number <= 1.0:
        raise PriceScaleError(
            f"{field_label}: {value!r} is outside [0, 1] — cents/dollars unit mixup?"
        )
    # No magnitude floor below this: live Gamma longshots legitimately price
    # at 0.0005 (recorded in tests/fixtures 2026-10-09), which no threshold
    # can separate from a ÷1000 units error. Scale bugs are a cross-venue
    # sanity concern, not an ingest-rejection concern.
    return number


def _normalize_scale(value: float | str | None, *, field_label: str) -> float | None:
    """Shared body of the scale normalizers: string/None tolerance, then bounds."""
    if value is None:
        return None
    if isinstance(value, str):
        stripped = value.strip()
        if not stripped:
            return None  # an empty field is venue data for "unknown"
        try:
            value = float(stripped)
        except ValueError as exc:
            raise PriceScaleError(f"{field_label}: {value!r} is not a number") from exc
    return _as_price(value, field_label)


def normalize_probability(
    value: float | str | None, *, field_label: str = "price"
) -> float | None:
    """Normalize a quote already on the 0–1 probability scale, used as-is.

    Polymarket emits decimals (``0.62``) — sometimes as strings inside its
    JSON-encoded outcome arrays — so no rescaling happens; the value is only
    bounds-checked. ``None`` and blank strings pass through as ``None``
    (unknown is not zero). Raises :class:`PriceScaleError` on out-of-scale
    or mis-scaled input.
    """
    return _normalize_scale(value, field_label=field_label)


def normalize_dollars(
    value: float | str | None, *, field_label: str = "price"
) -> float | None:
    """Normalize a dollar-denominated quote to the 0–1 scale.

    Kalshi dollar quotes are already probability-equivalent — $0.62 is a
    YES price of 0.62 — so the conversion is a pass-through division by
    1.0. The no-op division is kept explicit to document the unit boundary:
    nothing may rescale dollar quotes downstream. Same bounds check and
    ``None`` tolerance as :func:`normalize_probability`.
    """
    normalized = _normalize_scale(value, field_label=field_label)
    if normalized is None:
        return None
    return normalized / 1.0  # explicit no-op: dollar quotes are already 0–1


def to_utc(value: datetime) -> datetime:
    """Return *value* as an aware UTC datetime; naive input is read as UTC."""
    if value.tzinfo is None:
        return value.replace(tzinfo=UTC)
    return value.astimezone(UTC)


def parse_utc(value: str | None) -> datetime | None:
    """Parse an ISO-8601 timestamp string into an aware UTC datetime.

    ``None`` and blank strings pass through as ``None``. Naive timestamps
    (both venues emit them at times) are interpreted as UTC. Malformed
    input raises :class:`TimeParseError` — adapters convert that into a
    skipped record; the model never guesses.
    """
    if value is None:
        return None
    if not isinstance(value, str):
        raise TimeParseError(f"expected an ISO-8601 string, got {type(value).__name__}")
    text = value.strip()
    if not text:
        return None
    try:
        parsed = datetime.fromisoformat(text)  # 3.11+ accepts a trailing 'Z'
    except ValueError as exc:
        raise TimeParseError(f"{value!r} is not a parsable ISO-8601 timestamp") from exc
    return to_utc(parsed)


@dataclass(frozen=True)
class Outcome:
    """One tradeable side of a market, under venue-native naming.

    ``yes_price`` is the price of buying *this* outcome, on the 0–1
    probability scale — for ``side="yes"`` that is the YES price, for
    ``side="no"`` the NO price. ``bid``/``ask`` are this outcome's best
    quotes on the same scale. ``None`` means unknown, never zero.
    """

    name: str
    side: Side
    yes_price: float | None = None
    bid: float | None = None
    ask: float | None = None

    def __post_init__(self) -> None:
        if not isinstance(self.name, str) or not self.name.strip():
            raise ValueError(f"Outcome.name must be a non-empty string, got {self.name!r}")
        if self.side not in ("yes", "no", "other"):
            raise ValueError(
                f"Outcome.side must be 'yes', 'no', or 'other', got {self.side!r}"
            )
        for attr in ("yes_price", "bid", "ask"):
            value = getattr(self, attr)
            if value is not None:
                object.__setattr__(
                    self, attr, _as_price(value, f"Outcome({self.name!r}).{attr}")
                )


FEE_MODELS = ("p_curve", "flat", "none")
"""Supported fee models.

``p_curve``: per-share fee = rate·p·(1−p), price-dependent (Polymarket's
published formula); ``flat``: a constant per-share rate; ``none``: fee-free.
The curve shape is shared, the parameters are per-venue data — that is how
"no hardcoded venue logic" survives contact with fees.
"""


@dataclass(frozen=True)
class FeeParams:
    """Fee parameters as data, never guessed at call time.

    ``source`` records provenance — e.g. ``"config:2026-10-09"`` for a
    config-dated Kalshi rate or ``"market:{condition_id}"`` for a rate read
    from live Polymarket market metadata — so a discrepancy can be audited.
    """

    model: str
    rate: float | None
    source: str

    def __post_init__(self) -> None:
        if self.model not in FEE_MODELS:
            raise ValueError(f"FeeParams.model must be one of {FEE_MODELS}, got {self.model!r}")
        if not isinstance(self.source, str) or not self.source.strip():
            raise ValueError(f"FeeParams.source must be a non-empty string, got {self.source!r}")
        if self.model == "none":
            if self.rate is not None:
                raise ValueError("FeeParams.model='none' takes no rate")
            return
        if self.rate is None:
            raise ValueError(f"FeeParams.model={self.model!r} requires a rate")
        if isinstance(self.rate, bool) or not isinstance(self.rate, (int, float)):
            raise TypeError(
                f"FeeParams.rate must be a real number, got {type(self.rate).__name__}"
            )
        if not 0.0 <= float(self.rate) <= 1.0:
            raise PriceScaleError(
                f"FeeParams.rate {self.rate!r} outside [0, 1] — a fee rate is a "
                "fraction, e.g. 7% is 0.07"
            )
        object.__setattr__(self, "rate", float(self.rate))

    def fee_per_share(self, price: float) -> float:
        """Per-share fee for a fill at *price* on the 0–1 scale.

        ``p_curve`` implements rate·p·(1−p); ``flat`` returns the constant
        rate; ``none`` returns 0.0. An order's total fee is per-share ×
        shares — the router multiplies out.
        """
        if self.model == "none":
            return 0.0
        p = _as_price(price, "fee price")
        if self.model == "flat":
            return float(self.rate)
        return float(self.rate) * p * (1.0 - p)


@dataclass(frozen=True)
class FetchError:
    """A page-level fetch failure that survived retries (adapter contract).

    Recorded, not raised: the snapshot stays honest about what it is
    missing, and :attr:`MarketSnapshot.degraded` names the affected venues.
    """

    venue: str
    url: str
    cause: str
    attempts: int

    def __post_init__(self) -> None:
        for attr in ("venue", "url", "cause"):
            value = getattr(self, attr)
            if not isinstance(value, str) or not value.strip():
                raise ValueError(f"FetchError.{attr} must be a non-empty string, got {value!r}")
        if (
            isinstance(self.attempts, bool)
            or not isinstance(self.attempts, int)
            or self.attempts < 1
        ):
            raise ValueError(
                f"FetchError.attempts must be a positive int, got {self.attempts!r}"
            )


@dataclass(frozen=True)
class Market:
    """One market on one venue, normalized into the shared model.

    ``question`` is the venue-native text, unmodified — matching works on
    it. The YES side is explicit: adapters set ``side`` on each outcome at
    normalization time and the ``yes_*`` / ``no_*`` properties resolve it,
    so downstream layers never re-derive venue conventions. ``liquidity``
    is ``None`` when the venue reports nothing — unknown is never zero,
    because an unreported book and an empty book are different facts.
    ``raw_ref`` points into the raw JSONL evidence dump.
    """

    market_id: str
    venue: str
    question: str
    outcomes: tuple[Outcome, ...]
    fees: FeeParams
    closes_at: datetime | None
    liquidity: float | None
    fetched_at: datetime
    raw_ref: str

    def __post_init__(self) -> None:
        for attr in ("market_id", "venue", "question", "raw_ref"):
            value = getattr(self, attr)
            if not isinstance(value, str) or not value.strip():
                raise ValueError(f"Market.{attr} must be a non-empty string, got {value!r}")
        if not isinstance(self.fetched_at, datetime):
            raise TypeError(
                f"Market.fetched_at must be a datetime, got {type(self.fetched_at).__name__}"
            )
        if self.closes_at is not None and not isinstance(self.closes_at, datetime):
            raise TypeError(
                f"Market.closes_at must be a datetime or None, got {type(self.closes_at).__name__}"
            )
        object.__setattr__(self, "fetched_at", to_utc(self.fetched_at))
        if self.closes_at is not None:
            object.__setattr__(self, "closes_at", to_utc(self.closes_at))
        object.__setattr__(self, "outcomes", tuple(self.outcomes))
        for outcome in self.outcomes:
            if not isinstance(outcome, Outcome):
                raise TypeError(
                    f"Market.outcomes must contain Outcome objects, got {type(outcome).__name__}"
                )
        for side_name in ("yes", "no"):
            count = sum(1 for outcome in self.outcomes if outcome.side == side_name)
            if count > 1:
                raise ValueError(
                    f"Market has {count} outcomes with side={side_name!r}; at most one is allowed"
                )
        if self.liquidity is not None:
            if isinstance(self.liquidity, bool) or not isinstance(
                self.liquidity, (int, float)
            ):
                raise TypeError(
                    f"Market.liquidity must be a number or None, got "
                    f"{type(self.liquidity).__name__}"
                )
            liquidity = float(self.liquidity)
            if liquidity != liquidity or liquidity < 0:
                raise ValueError(
                    f"Market.liquidity must be a finite non-negative number, "
                    f"got {self.liquidity!r}"
                )
            object.__setattr__(self, "liquidity", liquidity)

    def outcome_for(self, side: Side) -> Outcome | None:
        """The (at most one) outcome carrying *side*, or ``None``."""
        for outcome in self.outcomes:
            if outcome.side == side:
                return outcome
        return None

    def _side_attr(self, side: Side, attr: str) -> float | None:
        outcome = self.outcome_for(side)
        return getattr(outcome, attr) if outcome is not None else None

    @property
    def yes_price(self) -> float | None:
        """YES price on the 0–1 scale, when a YES side is declared."""
        return self._side_attr("yes", "yes_price")

    @property
    def yes_bid(self) -> float | None:
        """Best bid on the YES side, same scale."""
        return self._side_attr("yes", "bid")

    @property
    def yes_ask(self) -> float | None:
        """Best ask on the YES side, same scale."""
        return self._side_attr("yes", "ask")

    @property
    def no_price(self) -> float | None:
        """NO price, when a NO side is explicitly available."""
        return self._side_attr("no", "yes_price")

    @property
    def no_bid(self) -> float | None:
        """Best bid on the NO side, same scale."""
        return self._side_attr("no", "bid")

    @property
    def no_ask(self) -> float | None:
        """Best ask on the NO side, same scale."""
        return self._side_attr("no", "ask")


@dataclass(frozen=True)
class MarketSnapshot:
    """A point-in-time corpus of normalized markets across venues.

    ``fetched_at`` is passed in as data by the adapter layer — the model
    never reads a clock, so identical inputs build identical snapshots.
    Construction sorts fields into explicit total orders (spec §Resilience):
    markets by (venue, market_id), venues and degraded flags sorted and
    deduped, errors by (venue, url, cause, attempts). ``venues`` records
    the venues the snapshot attempted; ``degraded`` the ones that lost
    pages after retries; ``errors`` the matching failure records.
    """

    fetched_at: datetime
    venues: Sequence[str]
    markets: Sequence[Market] = ()
    degraded: Sequence[str] = ()
    errors: Sequence[FetchError] = ()

    def __post_init__(self) -> None:
        if not isinstance(self.fetched_at, datetime):
            raise TypeError(
                f"MarketSnapshot.fetched_at must be a datetime, got "
                f"{type(self.fetched_at).__name__}"
            )
        object.__setattr__(self, "fetched_at", to_utc(self.fetched_at))
        for venue in self.venues:
            if not isinstance(venue, str) or not venue.strip():
                raise ValueError(
                    f"MarketSnapshot.venues must be non-empty strings, got {venue!r}"
                )
        object.__setattr__(self, "venues", tuple(sorted(set(self.venues))))
        for market in self.markets:
            if not isinstance(market, Market):
                raise TypeError(
                    f"MarketSnapshot.markets must contain Market objects, got "
                    f"{type(market).__name__}"
                )
        object.__setattr__(
            self,
            "markets",
            tuple(sorted(self.markets, key=lambda m: (m.venue, m.market_id))),
        )
        for flag in self.degraded:
            if not isinstance(flag, str) or not flag.strip():
                raise ValueError(
                    f"MarketSnapshot.degraded must be non-empty venue slugs, got {flag!r}"
                )
        object.__setattr__(self, "degraded", tuple(sorted(set(self.degraded))))
        for error in self.errors:
            if not isinstance(error, FetchError):
                raise TypeError(
                    f"MarketSnapshot.errors must contain FetchError objects, got "
                    f"{type(error).__name__}"
                )
        object.__setattr__(
            self,
            "errors",
            tuple(
                sorted(
                    self.errors,
                    key=lambda e: (e.venue, e.url, e.cause, e.attempts),
                )
            ),
        )

    def markets_for_venue(self, venue: str) -> tuple[Market, ...]:
        """Markets from a single venue, in snapshot order."""
        return tuple(market for market in self.markets if market.venue == venue)

    def is_degraded(self, venue: str) -> bool:
        """True when *venue* lost pages after retries in this snapshot."""
        return venue in self.degraded

    def market_by_id(self, venue: str, market_id: str) -> Market | None:
        """Resolve a (venue, market_id) pair, or ``None`` when absent."""
        for market in self.markets:
            if market.venue == venue and market.market_id == market_id:
                return market
        return None


__all__ = [
    "FEE_MODELS",
    "FetchError",
    "FeeParams",
    "Market",
    "MarketSnapshot",
    "Outcome",
    "PriceScaleError",
    "Side",
    "TimeParseError",
    "normalize_dollars",
    "normalize_probability",
    "parse_utc",
    "to_utc",
]
