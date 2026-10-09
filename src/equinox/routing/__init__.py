"""Pure cost-based routing decisions over a market snapshot.

Venue-agnostic by contract: this package never imports venue adapters or HTTP
machinery, and never names a specific venue — both enforced by structural
tests in the repository.

- :func:`route` — decide a venue for a simulated intent by all-in per-share
  cost; pure, deterministic, with tie-breaks and a staleness policy.
- :func:`decision_to_trace_line` — serialize a decision as one JSON line for
  the reasoning log; rejections are stated with reasons.
"""

from __future__ import annotations

from equinox.routing.router import (
    REJECTION_PREFIX,
    RouteDecision,
    RouteIntent,
    VenueQuote,
    rejection_line,
    route,
)
from equinox.routing.trace import decision_to_trace_line, rejected_from_rationale

__all__ = [
    "REJECTION_PREFIX",
    "RouteDecision",
    "RouteIntent",
    "VenueQuote",
    "decision_to_trace_line",
    "rejection_line",
    "rejected_from_rationale",
    "route",
]
