"""Trace serialization: one JSON line per decision, rejections stated with reasons."""

from __future__ import annotations

import json
from datetime import UTC, datetime

from equinox.config import RouterConfig
from equinox.model import FeeParams, Market, MarketSnapshot, Outcome
from equinox.routing import (
    RouteDecision,
    RouteIntent,
    decision_to_trace_line,
    rejected_from_rationale,
    rejection_line,
    route,
)

FROZEN_AT = datetime(2026, 10, 9, 12, 0, 0, tzinfo=UTC)


def _market(market_id: str, venue: str, *, ask: float, bid: float | None) -> Market:
    return Market(
        market_id=market_id,
        venue=venue,
        question="Will the router test pass?",
        outcomes=(Outcome(name="Yes", side="yes", yes_price=ask, bid=bid, ask=ask),),
        fees=FeeParams(model="none", rate=None, source="adapter-stamp"),
        closes_at=None,
        liquidity=1_000.0,
        fetched_at=FROZEN_AT,
        raw_ref=f"raw/{venue}.jsonl:1",
    )


def _routed() -> RouteDecision:
    """A two-venue decision with one rejection, on a fixed snapshot."""
    snapshot = MarketSnapshot(
        fetched_at=FROZEN_AT,
        venues=["v-a", "v-b"],
        markets=(
            _market("id-a", "v-a", ask=0.62, bid=0.61),
            _market("id-b", "v-b", ask=0.64, bid=None),  # no bid: spread unknown
        ),
    )
    return route(RouteIntent("pair:id-a:id-b", "yes", 100), snapshot, RouterConfig())


def test_trace_line_is_valid_single_line_json() -> None:
    line = decision_to_trace_line(_routed())
    assert "\n" not in line
    payload = json.loads(line)
    assert set(payload) == {
        "decision_id",
        "intent",
        "quotes",
        "chosen",
        "rejected",
        "rationale",
    }


def test_trace_line_carries_quotes_breakdowns_and_choice() -> None:
    payload = json.loads(decision_to_trace_line(_routed()))
    assert payload["intent"] == {
        "market_ref": "pair:id-a:id-b",
        "side": "yes",
        "shares": 100,
    }
    assert payload["chosen"] == "v-a"
    assert [q["venue"] for q in payload["quotes"]] == ["v-a"]  # v-b rejected, not quoted
    (quote,) = payload["quotes"]
    assert set(quote["breakdown"]) == {"price", "spread_impact", "fee"}
    assert quote["all_in_cost"] == sum(quote["breakdown"].values())


def test_trace_line_rejected_venues_state_why() -> None:
    payload = json.loads(decision_to_trace_line(_routed()))
    assert payload["rejected"] == [
        {
            "venue": "v-b",
            "reason": "no bid quoted for the 'yes' side — spread impact unknown",
        }
    ]


def test_rejections_parse_back_from_the_rationale_convention() -> None:
    rationale = [
        "chosen: venue=v-a",
        rejection_line("v-b", "no ask quoted for the 'yes' side"),
        rejection_line("v-c", "no market in the snapshot resolves from this reference"),
    ]
    assert rejected_from_rationale(rationale) == [
        {"venue": "v-b", "reason": "no ask quoted for the 'yes' side"},
        {
            "venue": "v-c",
            "reason": "no market in the snapshot resolves from this reference",
        },
    ]
    assert (
        rejected_from_rationale(["cost tie within epsilon 1e-09 — higher liquidity decided"])
        == []
    )


def test_repeated_serialization_is_byte_identical() -> None:
    first = decision_to_trace_line(_routed())
    second = decision_to_trace_line(_routed())
    assert first == second


def test_every_rationale_step_is_present_in_the_trace() -> None:
    decision = _routed()
    payload = json.loads(decision_to_trace_line(decision))
    assert payload["rationale"] == list(decision.rationale)
    assert any(step.startswith("chosen:") for step in payload["rationale"])
    assert payload["decision_id"] == decision.decision_id


def test_trace_of_a_no_quote_decision() -> None:
    snapshot = MarketSnapshot(
        fetched_at=FROZEN_AT,
        venues=["v-a"],
        markets=(_market("m", "v-a", ask=0.62, bid=0.61),),
    )
    decision = route(RouteIntent("missing", "yes", 100), snapshot, RouterConfig())
    payload = json.loads(decision_to_trace_line(decision))
    assert payload["chosen"] is None
    assert payload["quotes"] == []
    assert len(payload["rejected"]) == 1
