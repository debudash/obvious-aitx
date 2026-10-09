"""Polymarket adapter: Gamma markets API → the internal model.

Gamma is Polymarket's public, unauthenticated discovery API (evidence F2,
probed live 2026-10-09). The shapes that make this adapter earn its keep:

- ``outcomes``, ``outcomePrices``, and ``clobTokenIds`` arrive as *strings*
  containing JSON-encoded arrays (e.g. ``'["Yes", "No"]'``) — decoded here,
  never downstream.
- Prices are already on the 0–1 probability scale — used as-is under the
  model's bounds check.
- ``bestBid``/``bestAsk`` quote the YES-side book; the NO side's price comes
  from ``outcomePrices[1]`` with no venue-provided NO quotes (not derived —
  derived quotes would be fiction the router would trust).
- Fee data is per-market live metadata (F5): ``feeSchedule.rate`` is a
  fraction on [0,1] matching the p·(1−p) curve (observed 0.04 with
  ``exponent: 1``); legacy ``makerBaseFee``/``takerBaseFee`` are basis
  points (observed 1000 alongside rate 0.04) and serve only as the
  fallback when the schedule is absent. Provenance names the field used.
- ``liquidityNum`` is the reported liquidity; ``endDate`` the close time.

Pagination is offset-based: a lost page can be skipped past (offsets are
computable), so ``continue_after_page_failure`` is on — with the known
limitation that offset pagination under churn may duplicate or drop
records; duplicates are caught by the shared dedupe, and the skip reason
records it.
"""

from __future__ import annotations

import json
from collections.abc import Mapping
from dataclasses import replace
from datetime import datetime
from typing import Any, ClassVar

from equinox.model import FeeParams, Market, Outcome, normalize_probability, parse_utc
from equinox.venues.base import (
    BaseVenueAdapter,
    FetchFailure,
    get_json_with_retries,
    require_field,
)

GAMMA_BASE_URL = "https://gamma-api.polymarket.com"


def _stringified_array(raw: Mapping[str, Any], field_name: str) -> list[Any]:
    """Decode a Gamma field that arrives as a JSON-encoded array string (F2)."""
    value = require_field(raw, field_name)
    if isinstance(value, list):
        return value  # tolerate an already-decoded array
    if not isinstance(value, str):
        raise ValueError(
            f"field {field_name!r} is {type(value).__name__}, expected a JSON array string"
        )
    try:
        decoded = json.loads(value)
    except json.JSONDecodeError as exc:
        raise ValueError(
            f"field {field_name!r} is a malformed JSON-encoded array: {exc}"
        ) from exc
    if not isinstance(decoded, list):
        raise ValueError(
            f"field {field_name!r} decoded to {type(decoded).__name__}, expected an array"
        )
    return decoded


def _side_for_label(name: str) -> str:
    """Map a venue-native outcome label to the canonical side, honestly.

    "yes"/"no" (case-insensitive) get canonical sides; named two-way markets
    (e.g. party names) stay ``other`` rather than being force-fit.
    """
    lowered = name.strip().lower()
    if lowered == "yes":
        return "yes"
    if lowered == "no":
        return "no"
    return "other"


class PolymarketAdapter(BaseVenueAdapter):
    """Gamma /markets ingest with offset pagination and closed=false filter."""

    venue = "polymarket"
    base_url: ClassVar[str] = GAMMA_BASE_URL
    continue_after_page_failure = True

    def initial_page_state(self) -> int:
        return 0  # offset

    def fetch_page(self, page_state: int, budget: int) -> tuple[list[Any], int]:
        offset = page_state
        params = {"limit": str(budget), "offset": str(offset), "closed": "false"}
        payload = get_json_with_retries(
            self._transport,
            f"{self.base_url}/markets",
            params,
            timeout=self._timeout,
            max_retries=self._max_retries,
            backoff_base=self._backoff_base,
            sleep=self._sleep,
        )
        if not isinstance(payload, list):
            raise FetchFailure(
                f"unexpected payload shape {type(payload).__name__}, expected a list",
                attempts=1,
                retryable=False,
            )
        return payload, offset + len(payload)

    def pagination_done(self, next_state: int, records_in_page: int, budget: int) -> bool:
        return records_in_page < budget  # short page = last page

    def advance_after_failure(self, page_state: int, budget: int) -> int:
        # The lost page's record count is unknown; skip forward by the budget
        # rather than refetch — a conservative, deterministic advance.
        return page_state + budget

    def parse_record(
        self, raw: Any, *, raw_ref: str, fetched_at: datetime
    ) -> Market:
        if not isinstance(raw, Mapping):
            raise ValueError(f"record is {type(raw).__name__}, expected an object")
        if raw.get("closed"):
            raise ValueError("market is closed (closed=true) — ingest filters closed=false")
        condition_id = str(require_field(raw, "conditionId"))
        question = str(require_field(raw, "question"))

        outcomes = _stringified_array(raw, "outcomes")
        prices = _stringified_array(raw, "outcomePrices")
        if len(outcomes) != len(prices):
            raise ValueError(
                f"outcomes ({len(outcomes)}) and outcomePrices ({len(prices)}) lengths differ"
            )
        if raw.get("clobTokenIds") is not None:
            # Validated here so a broken encoding surfaces as a counted skip
            # now, not as a surprise when the CLOB book is read later (F4).
            _stringified_array(raw, "clobTokenIds")

        built = [
            Outcome(
                name=str(name),
                side=_side_for_label(str(name)),
                yes_price=normalize_probability(price, field_label=f"outcomePrices[{index}]"),
            )
            for index, (name, price) in enumerate(zip(outcomes, prices, strict=True))
        ]
        best_bid = normalize_probability(raw.get("bestBid"), field_label="bestBid")
        best_ask = normalize_probability(raw.get("bestAsk"), field_label="bestAsk")
        built = [
            replace(outcome, bid=best_bid, ask=best_ask)
            if outcome.side == "yes"
            else outcome
            for outcome in built
        ]

        return Market(
            market_id=condition_id,
            venue=self.venue,
            question=question,
            outcomes=tuple(built),
            fees=self._fee_params(raw, condition_id),
            closes_at=parse_utc(raw.get("endDate")),
            liquidity=self._liquidity(raw),
            fetched_at=fetched_at,
            raw_ref=raw_ref,
        )

    def _liquidity(self, raw: Mapping[str, Any]) -> float | None:
        value = raw.get("liquidityNum")
        if value is None:
            return None  # unknown is not zero
        try:
            return float(value)
        except (TypeError, ValueError) as exc:
            raise ValueError(f"liquidityNum {value!r} is not a number") from exc

    def _fee_params(self, raw: Mapping[str, Any], condition_id: str) -> FeeParams:
        """Fee params from live market metadata (F5), provenance recorded."""
        source_market = f"market:{condition_id}"
        if not raw.get("feesEnabled"):
            return FeeParams(model="none", rate=None, source=source_market)
        schedule = raw.get("feeSchedule")
        if isinstance(schedule, Mapping):
            rate = schedule.get("rate")
            if isinstance(rate, (int, float)) and not isinstance(rate, bool):
                return FeeParams(
                    model="p_curve",
                    rate=rate,
                    source=f"{source_market}:feeSchedule.rate",
                )
        taker = raw.get("takerBaseFee")
        if isinstance(taker, (int, float)) and not isinstance(taker, bool):
            # Legacy field, basis points (observed 1000 next to rate 0.04).
            return FeeParams(
                model="p_curve",
                rate=float(taker) / 10_000.0,
                source=f"{source_market}:takerBaseFee.bps",
            )
        raise ValueError(
            f"fees enabled but no parsable fee schedule "
            f"(feeSchedule={schedule!r}, takerBaseFee={taker!r})"
        )


__all__ = ["GAMMA_BASE_URL", "PolymarketAdapter"]
