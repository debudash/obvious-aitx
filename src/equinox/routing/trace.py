"""One-line JSON serialization of routing decisions (spec §Routing).

Every decision serializes to a single JSON line — machine-appendable,
human-readable: quotes with cost breakdowns, the chosen venue, ordered
rationale steps, and every rejected venue stating why.

Rejections are carried in the rationale as steps marked with
:data:`~equinox.routing.router.REJECTION_PREFIX` (the decision shape stays
the five fields the spec pins); :func:`rejected_from_rationale` parses that
convention back into a structured ``rejected`` list here. The round-trip is
deterministic and covered by tests.
"""

from __future__ import annotations

import json
import re

from equinox.routing.router import RouteDecision

_REJECTION_PATTERN = re.compile(r"\Arejected venue='([^']+)': (.+)\Z")


def rejected_from_rationale(
    rationale: list[str] | tuple[str, ...],
) -> list[dict[str, str]]:
    """Structured rejections parsed from rationale steps carrying the prefix.

    Each parsed step yields ``{"venue": ..., "reason": ...}`` in rationale
    order; steps without the prefix are ignored.
    """
    rejected: list[dict[str, str]] = []
    for step in rationale:
        match = _REJECTION_PATTERN.match(step)
        if match:
            rejected.append({"venue": match.group(1), "reason": match.group(2)})
    return rejected


def decision_to_trace_line(decision: RouteDecision) -> str:
    """Serialize *decision* as one JSON line (no trailing newline).

    Key order is fixed (decision_id, intent, quotes, chosen, rejected,
    rationale) so identical decisions serialize byte-identically.
    """
    payload = {
        "decision_id": decision.decision_id,
        "intent": {
            "market_ref": decision.intent.market_ref,
            "side": decision.intent.side,
            "shares": decision.intent.shares,
        },
        "quotes": [
            {
                "venue": quote.venue,
                "all_in_cost": quote.all_in_cost,
                "breakdown": dict(quote.breakdown),
                "stale": quote.stale,
            }
            for quote in decision.quotes
        ],
        "chosen": decision.chosen,
        "rejected": rejected_from_rationale(decision.rationale),
        "rationale": list(decision.rationale),
    }
    return json.dumps(payload, ensure_ascii=False)


__all__ = ["decision_to_trace_line", "rejected_from_rationale"]
