"""Kalshi adapter: trade-api v2 markets → the internal model.

Kalshi's public market-data endpoint (api.elections.kalshi.com, no keys,
evidence F3, probed live 2026-10-09) returns ``{cursor, markets[]}``
envelopes with cursor pagination — the cursor is opaque, so it is
followed verbatim, never computed.

Payload quirks this adapter owns:

- Quote fields are dollar-denominated *strings* (``"0.6100"``) — divided
  by 1.0 to the shared 0–1 scale under the model's bounds check.
- The listing endpoint accepts ``status=open`` as a server-side filter
  (``status=active`` is rejected as a query parameter even though the
  records themselves carry ``status: "active"``); records arriving with
  any other status are skipped defensively.
- NO-side quotes exist (``no_bid_dollars``/``no_ask_dollars``) but there
  is no NO last price on the payload; the NO outcome keeps the reported
  quotes and ``yes_price=None`` rather than a derived 1−p figure.
- Fresh MVE combo markets report explicit zero quotes (``"0.0000"``) —
  kept as zeros, because that is what the venue said. There is no
  liquidity field on listing payloads at all: liquidity stays None
  (unknown), never zero.
- Fees: no fee fields exist on market payloads. Per spec A1/F6 the
  schedule varies over time and by contract type, so the rate is a
  dated config default (``config:2026-10-09``) the constructor can
  override — never a constant hidden in logic.
"""

from __future__ import annotations

from collections.abc import Mapping
from datetime import datetime
from typing import Any, ClassVar

from equinox.model import FeeParams, Market, Outcome, normalize_dollars, parse_utc
from equinox.venues.base import (
    BaseVenueAdapter,
    EvidenceSink,
    FetchFailure,
    Transport,
    get_json_with_retries,
    require_field,
)

KALSHI_BASE_URL = "https://api.elections.kalshi.com/trade-api/v2"

KALSHI_DEFAULT_FEE_PARAMS = FeeParams(
    model="p_curve",
    rate=0.07,
    source="config:2026-10-09",
)
"""Kalshi's published schedule as of the recording date (spec A1): 7% of
contract value, i.e. rate·p·(1−p) per share on the 0–1 scale. The real
schedule varies over time and by contract type (F6) — this is a dated
config default, overridable per adapter instance. Reversal condition: a
live fee field on market payloads switches this to per-market data, the
way the Polymarket adapter already reads it."""


class KalshiAdapter(BaseVenueAdapter):
    """Trade-api v2 /markets ingest with cursor pagination, status=open."""

    venue = "kalshi"
    base_url: ClassVar[str] = KALSHI_BASE_URL
    # Cursor pagination: the pointer to the next page travels in the lost
    # body, so a failed page ends collection — there is nothing to advance.
    continue_after_page_failure = False

    def __init__(
        self,
        *,
        sink: EvidenceSink,
        transport: Transport | None = None,
        fee_params: FeeParams | None = None,
        **kwargs: Any,
    ) -> None:
        super().__init__(sink=sink, transport=transport, **kwargs)
        self._fee_params = KALSHI_DEFAULT_FEE_PARAMS if fee_params is None else fee_params

    def initial_page_state(self) -> None:
        return None  # no cursor yet

    def fetch_page(self, page_state: str | None, budget: int) -> tuple[list[Any], str | None]:
        cursor = page_state
        params = {"status": "open", "limit": str(budget)}
        if cursor:
            params["cursor"] = str(cursor)
        payload = get_json_with_retries(
            self._transport,
            f"{self.base_url}/markets",
            params,
            timeout=self._timeout,
            max_retries=self._max_retries,
            backoff_base=self._backoff_base,
            sleep=self._sleep,
        )
        if not isinstance(payload, Mapping):
            raise FetchFailure(
                f"unexpected payload shape {type(payload).__name__}, expected an object",
                attempts=1,
                retryable=False,
            )
        records = payload.get("markets", [])
        if not isinstance(records, list):
            raise FetchFailure(
                f"payload 'markets' is {type(records).__name__}, expected a list",
                attempts=1,
                retryable=False,
            )
        # An empty-string cursor ends pagination on the live API (observed
        # 2026-10-09); treat it the same as an absent one.
        next_cursor = payload.get("cursor") or None
        return records, next_cursor

    def pagination_done(self, next_state: str | None, records_in_page: int, budget: int) -> bool:
        return not next_state  # cursor exhausted

    def parse_record(self, raw: Any, *, raw_ref: str, fetched_at: datetime) -> Market:
        if not isinstance(raw, Mapping):
            raise ValueError(f"record is {type(raw).__name__}, expected an object")
        status = require_field(raw, "status")
        if status != "active":
            # The server-side filter is status=open; records that leak
            # through with any other lifecycle status are not routable.
            raise ValueError(f"status {status!r} is not 'active' (query filters status=open)")
        market_type = raw.get("market_type")
        if market_type is not None and market_type != "binary":
            raise ValueError(f"market_type {market_type!r} is not 'binary'")

        ticker = str(require_field(raw, "ticker"))
        title = str(require_field(raw, "title"))

        yes = Outcome(
            name="Yes",
            side="yes",
            yes_price=normalize_dollars(
                raw.get("last_price_dollars"), field_label="last_price_dollars"
            ),
            bid=normalize_dollars(raw.get("yes_bid_dollars"), field_label="yes_bid_dollars"),
            ask=normalize_dollars(raw.get("yes_ask_dollars"), field_label="yes_ask_dollars"),
        )
        # The payload carries NO quotes but no NO last price; unknown stays
        # unknown rather than a derived 1−p figure.
        no = Outcome(
            name="No",
            side="no",
            yes_price=None,
            bid=normalize_dollars(raw.get("no_bid_dollars"), field_label="no_bid_dollars"),
            ask=normalize_dollars(raw.get("no_ask_dollars"), field_label="no_ask_dollars"),
        )

        return Market(
            market_id=ticker,
            venue=self.venue,
            question=title,
            outcomes=(yes, no),
            fees=self._fee_params,
            closes_at=parse_utc(raw.get("close_time")),
            liquidity=None,  # not on listing payloads — unknown, never zero
            fetched_at=fetched_at,
            raw_ref=raw_ref,
        )


__all__ = ["KALSHI_BASE_URL", "KALSHI_DEFAULT_FEE_PARAMS", "KalshiAdapter"]
