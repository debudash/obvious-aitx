"""Pure cost-based venue selection with a legible reasoning trail (spec §Routing).

:func:`route` decides which venue a simulated execution intent should use by
all-in per-share cost, and explains itself: every decision carries ordered
rationale steps, per-venue cost breakdowns, and explicit rejections. Routing
is simulated, never executed — the function touches no network and places no
orders.

Purity contract (enforced by structural tests in the repository):

- No clock: quote age is ``snapshot.fetched_at - market.fetched_at`` — both
  are data handed in by the adapter layer. No wall-clock reads anywhere.
- No network, no filesystem, no globals: the snapshot and config carry every
  fact, so identical inputs produce identical decisions, byte for byte.
- Venue-agnostic: venue slugs are data flowing through; no venue name is
  ever written here (the repository's venue-literal guard enforces this).

Reference-resolution contract: ``market_ref`` is either a snapshot
``market_id`` or ``pair:<id_a>:<id_b>`` — the two venue-native ids of a
matched candidate pair joined by ``:``. Any other form resolves to nothing,
and the decision comes back with ``chosen=None`` and the reason in the
trace, rather than a guess.

Fee-parameter precedence (spec assumption A1): a market's own ``FeeParams``
win — adapters stamp live metadata where the venue exposes it. When a market
declares fee model ``none`` (the adapter could not determine fees) and the
config carries a dated per-venue schedule, the config entry wins: the
operator's dated rate is more specific than a placeholder.
"""

from __future__ import annotations

import hashlib
from dataclasses import dataclass
from typing import cast

from equinox.config import RouterConfig
from equinox.model import FeeParams, Market, MarketSnapshot, Side

REJECTION_PREFIX = "rejected venue"
"""Rationale-step prefix marking a venue rejection; ``trace`` parses steps
carrying it back into structured rejections. Keep in sync with the pattern
in :mod:`equinox.routing.trace`."""

_REJECTION_LINE = "rejected venue='{venue}': {reason}"

_QUOTE_COMPONENTS = ("price", "spread_impact", "fee")
"""Exactly the keys a ``VenueQuote.breakdown`` carries — the trace's schema."""

_LIQUIDITY_UNKNOWN = -1.0
"""Selection rank for unreported liquidity: below every real book. Zero is a
real (empty) book; unknown is not zero (spec §Resilience)."""


@dataclass(frozen=True)
class RouteIntent:
    """A simulated execution request (spec §Routing).

    ``market_ref`` is a snapshot ``market_id`` or a ``pair:<id_a>:<id_b>``
    reference to a matched candidate pair. ``side`` accepts ``"yes"`` /
    ``"no"`` case-insensitively and is stored lowercase. ``shares`` sizes
    the simulation; under the top-of-book assumption (spec A2) per-share
    costs do not depend on size — the trace states that explicitly.
    """

    market_ref: str
    side: str
    shares: int

    def __post_init__(self) -> None:
        if not isinstance(self.market_ref, str) or not self.market_ref.strip():
            raise ValueError(
                f"RouteIntent.market_ref must be a non-empty string, got {self.market_ref!r}"
            )
        side = self.side.strip().lower() if isinstance(self.side, str) else ""
        if side not in ("yes", "no"):
            raise ValueError(
                f"RouteIntent.side must be 'yes' or 'no' (case-insensitive), got {self.side!r}"
            )
        object.__setattr__(self, "side", side)
        if (
            isinstance(self.shares, bool)
            or not isinstance(self.shares, int)
            or self.shares < 1
        ):
            raise ValueError(
                f"RouteIntent.shares must be a positive integer, got {self.shares!r}"
            )


@dataclass(frozen=True)
class VenueQuote:
    """One venue's simulated per-share cost for an intent (spec §Routing).

    ``all_in_cost`` is the raw per-share cost on the 0–1 scale —
    ``price + spread_impact + fee`` — comparable across venues. Staleness is
    reported, not baked in: selection penalizes stale quotes via
    ``RouterConfig.stale_cost_penalty`` and says so in the rationale.
    """

    venue: str
    all_in_cost: float
    breakdown: dict[str, float]
    stale: bool

    def __post_init__(self) -> None:
        if not isinstance(self.venue, str) or not self.venue.strip():
            raise ValueError(
                f"VenueQuote.venue must be a non-empty string, got {self.venue!r}"
            )
        if isinstance(self.all_in_cost, bool) or not isinstance(self.all_in_cost, (int, float)):
            raise TypeError(
                f"VenueQuote.all_in_cost must be a number, got {type(self.all_in_cost).__name__}"
            )
        if set(self.breakdown) != set(_QUOTE_COMPONENTS):
            raise ValueError(
                f"VenueQuote.breakdown must have exactly {sorted(_QUOTE_COMPONENTS)}, "
                f"got {sorted(self.breakdown)}"
            )
        for name in _QUOTE_COMPONENTS:
            value = self.breakdown[name]
            if (
                isinstance(value, bool)
                or not isinstance(value, (int, float))
                or value != value  # NaN
                or value < 0
            ):
                raise ValueError(
                    f"VenueQuote.breakdown[{name!r}] must be a finite non-negative number, "
                    f"got {value!r}"
                )


@dataclass(frozen=True)
class RouteDecision:
    """The outcome of one simulated routing decision (spec §Routing).

    ``quotes`` is sorted best-first by raw ``all_in_cost`` (ties: higher
    liquidity, then venue slug ascending — an explicit total order, never
    dict order). ``rationale`` is an ordered argument a human can audit:
    rejections state why (steps marked with :data:`REJECTION_PREFIX`),
    staleness states its effect, and the chosen venue is named last.
    ``decision_id`` is content-derived (sha256 of intent + quotes), not a
    call counter — a pure function may not count calls.
    """

    decision_id: str
    intent: RouteIntent
    quotes: list[VenueQuote]
    chosen: str | None
    rationale: list[str]

    def __post_init__(self) -> None:
        if not isinstance(self.decision_id, str) or not self.decision_id.strip():
            raise ValueError(
                f"RouteDecision.decision_id must be a non-empty string, "
                f"got {self.decision_id!r}"
            )
        if not isinstance(self.intent, RouteIntent):
            raise TypeError(
                f"RouteDecision.intent must be a RouteIntent, got {type(self.intent).__name__}"
            )
        object.__setattr__(self, "quotes", list(self.quotes))
        for quote in self.quotes:
            if not isinstance(quote, VenueQuote):
                raise TypeError(
                    f"RouteDecision.quotes must contain VenueQuote objects, "
                    f"got {type(quote).__name__}"
                )
        if self.chosen is not None and (
            not isinstance(self.chosen, str) or not self.chosen.strip()
        ):
            raise ValueError(
                f"RouteDecision.chosen must be a non-empty string or None, got {self.chosen!r}"
            )
        object.__setattr__(self, "rationale", list(self.rationale))
        for step in self.rationale:
            if not isinstance(step, str) or not step.strip():
                raise ValueError(
                    f"RouteDecision.rationale steps must be non-empty strings, got {step!r}"
                )


def rejection_line(venue: str, reason: str) -> str:
    """Format a rejection rationale step; :mod:`equinox.routing.trace` parses it."""
    return _REJECTION_LINE.format(venue=venue, reason=reason)


def _reference_ids(market_ref: str) -> tuple[str, ...]:
    """Split a market reference into snapshot market ids (deduped, ordered).

    ``<market_id>`` resolves to itself; ``pair:<id_a>:<id_b>`` to the two
    venue-native ids of a matched pair. A ``pair:`` prefix carrying a single
    composite token (a matcher slug that embeds the pair in one string)
    resolves to nothing routable — the decision explains that instead of
    guessing.
    """
    if market_ref.startswith("pair:"):
        parts = (part for part in market_ref[len("pair:") :].split(":") if part)
        return tuple(dict.fromkeys(parts))
    return (market_ref,)


def _resolve_markets(market_ref: str, snapshot: MarketSnapshot) -> tuple[Market, ...]:
    """Snapshot markets referenced by *market_ref*, in snapshot order."""
    wanted = set(_reference_ids(market_ref))
    return tuple(market for market in snapshot.markets if market.market_id in wanted)


def _fee_params_for(market: Market, config: RouterConfig) -> FeeParams:
    """Resolve fee params: per-market data first, config fallback per A1."""
    configured = config.fee_params_for(market.venue)
    if market.fees.model == "none" and configured is not None:
        return configured
    return market.fees


def _build_quote(
    market: Market, side: Side, snapshot: MarketSnapshot, config: RouterConfig
) -> tuple[VenueQuote | None, str]:
    """Per-share quote for one venue's market, or ``(None, rejection_reason)``.

    The quote buys *side* at the best ask; spread impact is half the
    bid-ask spread (top-of-book assumption A2 — book depth is out of
    scope). A missing ask or bid rejects the venue: unknown is never zero,
    so an unquoted book must not look like a free one.
    """
    outcome = market.outcome_for(side)
    if outcome is None:
        return None, f"no '{side}' side declared on market {market.market_id}"
    if outcome.ask is None:
        return None, f"no ask quoted for the '{side}' side — cannot price the fill"
    if outcome.bid is None:
        return None, f"no bid quoted for the '{side}' side — spread impact unknown"
    price = outcome.ask
    spread_impact = (outcome.ask - outcome.bid) / 2.0
    fee_params = _fee_params_for(market, config)
    fee = fee_params.fee_per_share(price)
    stale = (snapshot.fetched_at - market.fetched_at) > config.max_quote_age
    quote = VenueQuote(
        venue=market.venue,
        all_in_cost=price + spread_impact + fee,
        breakdown={"price": price, "spread_impact": spread_impact, "fee": fee},
        stale=stale,
    )
    return quote, f"fee source {fee_params.source}"


def _quote_line(quote: VenueQuote, fee_note: str) -> str:
    breakdown = quote.breakdown
    return (
        f"venue={quote.venue} all-in {quote.all_in_cost:.4f} = price "
        f"{breakdown['price']:.4f} + spread_impact {breakdown['spread_impact']:.4f} "
        f"+ fee {breakdown['fee']:.4f} ({fee_note})"
    )


def _format_liquidity(rank: float) -> str:
    return "unknown" if rank == _LIQUIDITY_UNKNOWN else f"{rank:.0f}"


def _decision_id(
    intent: RouteIntent, snapshot: MarketSnapshot, quotes: list[VenueQuote]
) -> str:
    """Content-derived id: identical inputs produce identical ids."""
    parts = [
        intent.market_ref,
        intent.side,
        str(intent.shares),
        snapshot.fetched_at.isoformat(),
        *[f"{q.venue}={q.all_in_cost:.10f}:{int(q.stale)}" for q in quotes],
    ]
    digest = hashlib.sha256("|".join(parts).encode("utf-8")).hexdigest()
    return f"rt_{digest[:12]}"


def route(
    intent: RouteIntent, snapshot: MarketSnapshot, config: RouterConfig
) -> RouteDecision:
    """Decide a venue for *intent* from *snapshot* — pure and deterministic.

    Same inputs, same decision: no clock, no network, no globals. The
    decision's ``rationale`` is an ordered, human-auditable argument;
    :func:`equinox.routing.trace.decision_to_trace_line` serializes it as
    one JSON line.
    """
    markets = _resolve_markets(intent.market_ref, snapshot)
    side = cast("Side", intent.side)

    # First market per venue in snapshot order (venue, market_id sorted);
    # extra hits on the same venue are noted, never fatal.
    by_venue: dict[str, Market] = {}
    duplicated: list[str] = []
    for market in markets:
        if market.venue in by_venue:
            duplicated.append(market.venue)
        else:
            by_venue[market.venue] = market

    rejections: list[str] = [
        rejection_line(
            venue, "no market in the snapshot resolves from this reference"
        )
        for venue in snapshot.venues
        if venue not in by_venue
    ]
    quotes: list[VenueQuote] = []
    fee_notes: dict[str, str] = {}
    for venue in sorted(by_venue):
        quote, note = _build_quote(by_venue[venue], side, snapshot, config)
        if quote is None:
            rejections.append(rejection_line(venue, note))
        else:
            quotes.append(quote)
            fee_notes[venue] = note

    # Explicit total order: raw cost, then liquidity, then venue slug.
    liquidity_rank = {
        venue: (
            market.liquidity if market.liquidity is not None else _LIQUIDITY_UNKNOWN
        )
        for venue, market in by_venue.items()
    }
    quotes.sort(key=lambda q: (q.all_in_cost, -liquidity_rank[q.venue], q.venue))

    rationale: list[str] = []
    if quotes:
        named = ", ".join(f"venue={q.venue}" for q in quotes)
        rationale.append(
            f"{len(quotes)} quotable venue(s) for {intent.market_ref}: {named}"
        )
    else:
        rationale.append(f"0 quotable venues for {intent.market_ref}")
        if not markets:
            rationale.append(
                f"no snapshot market resolves from reference '{intent.market_ref}'"
            )
    rationale.extend(_quote_line(q, fee_notes[q.venue]) for q in quotes)

    for quote in quotes:
        if not quote.stale:
            continue
        market = by_venue[quote.venue]
        age = (snapshot.fetched_at - market.fetched_at).total_seconds()
        rationale.append(
            f"venue={quote.venue} quote is stale: age {age:.0f}s exceeds max "
            f"{config.max_quote_age.total_seconds():.0f}s"
        )
    for venue in sorted(set(duplicated)):
        rationale.append(
            f"note: reference resolves to multiple markets on venue='{venue}'; "
            "using the first in snapshot order"
        )

    chosen: str | None = None
    if quotes:
        best, runner_up = quotes[0], (quotes[1] if len(quotes) > 1 else None)
        gap = (
            runner_up.all_in_cost - best.all_in_cost
            if runner_up is not None
            else None
        )
        if runner_up is not None and gap > config.cost_tie_epsilon:  # type: ignore[operator]
            deltas = {
                component: runner_up.breakdown[component] - best.breakdown[component]
                for component in _QUOTE_COMPONENTS
            }
            dominant = max(
                _QUOTE_COMPONENTS,
                key=lambda c: (abs(deltas[c]), -_QUOTE_COMPONENTS.index(c)),
            )
            rationale.append(
                f"venue={best.venue} leads venue={runner_up.venue} by {gap:.4f} on raw "
                f"cost — largest contributor: {dominant} ({deltas[dominant]:+.4f})"
            )
        if gap is not None and gap <= config.cost_tie_epsilon:  # type: ignore[operator]
            tie_group = [
                q
                for q in quotes
                if (q.all_in_cost - best.all_in_cost) <= config.cost_tie_epsilon
            ]
            named = ", ".join(f"venue={q.venue}" for q in tie_group)
            ranks = {liquidity_rank[q.venue] for q in tie_group}
            detail = ", ".join(
                f"venue={q.venue} liquidity={_format_liquidity(liquidity_rank[q.venue])}"
                for q in sorted(
                    tie_group, key=lambda q: (-liquidity_rank[q.venue], q.venue)
                )
            )
            if len(ranks) == 1:
                rationale.append(
                    f"cost tie within epsilon {config.cost_tie_epsilon:.1e} among "
                    f"{named} — liquidity also tied; venue slug ascending decided"
                )
            else:
                rationale.append(
                    f"cost tie within epsilon {config.cost_tie_epsilon:.1e} among "
                    f"{named} — higher liquidity decided"
                )
            rationale.append(f"tie-break detail: {detail}")
            winner = min(
                tie_group, key=lambda q: (-liquidity_rank[q.venue], q.venue)
            )
        else:
            winner = best
        rationale.append(
            f"per-share all-in comparison under the top-of-book assumption (spec A2); "
            f"shares={intent.shares} do not change the ranking"
        )
        chosen = winner.venue
        rationale.append(f"chosen: venue={chosen}")
    else:
        rationale.append("chosen: none — no routable venue for this reference")

    rationale.extend(rejections)

    return RouteDecision(
        decision_id=_decision_id(intent, snapshot, quotes),
        intent=intent,
        quotes=quotes,
        chosen=chosen,
        rationale=rationale,
    )


__all__ = [
    "REJECTION_PREFIX",
    "RouteDecision",
    "RouteIntent",
    "VenueQuote",
    "rejection_line",
    "route",
]
