"""Record trimmed, sanitized API fixtures for the adapter tests.

Run once against the live public APIs (read-only, no keys, no auth):

    python scripts/record_fixtures.py

Provenance of the fixtures currently in tests/fixtures/:

- Recorded 2026-10-09 from the live endpoints below.
- Polymarket: GET https://gamma-api.polymarket.com/markets
    ?limit=100&offset=0&closed=false
  Selected records: indices 0, 15 (bestBid null), 22, 54 (feesEnabled
  false), 70 of the response.
- Kalshi: GET https://api.elections.kalshi.com/trade-api/v2/markets
    ?event_ticker=KXNFLGAME-26OCT12BUFLAR   (two markets with live books)
  plus the first 3 records of GET .../markets?status=open&limit=100
  (fresh MVE combo markets with zero quotes); the envelope cursor is the
  real one from the status=open page.
- Trimming: a field whitelist (the adapter-parsed surface plus a few
  distractor fields that prove extra fields are ignored).
- Sanitizing: wallet addresses (submitted_by, resolvedBy,
  marketMakerAddress), image/icon URLs, and nested event/reward structures
  are dropped; long boilerplate (rules_secondary, description, sub-titles)
  is dropped to keep fixtures reviewable. Nothing personal exists in the
  kept fields (tickers, titles, prices, timestamps).
"""

import json
import urllib.request
from pathlib import Path

FIXTURES_DIR = Path(__file__).resolve().parents[1] / "tests" / "fixtures"

PM_URL = "https://gamma-api.polymarket.com/markets?limit=100&offset=0&closed=false"
PM_INDICES = (0, 15, 22, 54, 70)
PM_KEEP = (
    "id",
    "conditionId",
    "question",
    "slug",
    "outcomes",
    "outcomePrices",
    "clobTokenIds",
    "bestBid",
    "bestAsk",
    "lastTradePrice",
    "spread",
    "liquidity",
    "liquidityClob",
    "liquidityNum",
    "volumeNum",
    "makerBaseFee",
    "takerBaseFee",
    "feesEnabled",
    "feeType",
    "feeSchedule",
    "endDate",
    "startDate",
    "active",
    "closed",
    "acceptingOrders",
    "negRisk",
    "orderMinSize",
    "orderPriceMinTickSize",
)

KX_EVENT_URL = (
    "https://api.elections.kalshi.com/trade-api/v2/markets"
    "?event_ticker=KXNFLGAME-26OCT12BUFLAR"
)
KX_PAGE_URL = (
    "https://api.elections.kalshi.com/trade-api/v2/markets?status=open&limit=100"
)
KX_PAGE_RECORDS = 3
KX_KEEP = (
    "ticker",
    "event_ticker",
    "title",
    "market_type",
    "status",
    "close_time",
    "open_time",
    "created_time",
    "updated_time",
    "expiration_time",
    "expected_expiration_time",
    "yes_bid_dollars",
    "yes_ask_dollars",
    "no_bid_dollars",
    "no_ask_dollars",
    "last_price_dollars",
    "previous_yes_bid_dollars",
    "previous_yes_ask_dollars",
    "previous_price_dollars",
    "notional_value_dollars",
    "open_interest_fp",
    "volume_fp",
    "volume_24h_fp",
    "yes_bid_size_fp",
    "yes_ask_size_fp",
    "can_close_early",
    "is_provisional",
    "strike_type",
    "price_level_structure",
    "result",
    "expiration_value",
    "rules_primary",
)


def fetch(url: str) -> object:
    request = urllib.request.Request(url, headers={"User-Agent": "equinox-spike/0.1"})
    with urllib.request.urlopen(request, timeout=30) as response:
        return json.load(response)


def trim(record: dict, keep: tuple[str, ...]) -> dict:
    return {key: record[key] for key in keep if key in record}


def main() -> None:
    FIXTURES_DIR.mkdir(parents=True, exist_ok=True)

    # --- Polymarket -------------------------------------------------------
    pm_records = fetch(PM_URL)
    assert isinstance(pm_records, list) and len(pm_records) > max(PM_INDICES)
    chosen = [trim(pm_records[index], PM_KEEP) for index in PM_INDICES]
    assert any(market.get("feesEnabled") for market in chosen), "need a fee-enabled market"
    assert any(not market.get("feesEnabled") for market in chosen), "need a fee-free market"
    assert any(market.get("bestBid") is None for market in chosen), "need a quoteless book"
    assert all(isinstance(market.get("outcomes"), str) for market in chosen), (
        "outcomes must arrive as JSON-encoded strings — that is the shape being captured"
    )
    pm_path = FIXTURES_DIR / "polymarket_markets.json"
    pm_path.write_text(json.dumps(chosen, indent=2) + "\n", encoding="utf-8")
    print(f"wrote {pm_path} ({len(chosen)} records)")

    # --- Kalshi -----------------------------------------------------------
    event_body = fetch(KX_EVENT_URL)
    page_body = fetch(KX_PAGE_URL)
    assert isinstance(event_body, dict) and isinstance(page_body, dict)
    event_markets = [trim(market, KX_KEEP) for market in event_body["markets"]]
    page_markets = [trim(market, KX_KEEP) for market in page_body["markets"][:KX_PAGE_RECORDS]]
    assert any(
        market.get("yes_bid_dollars") not in ("0.0000", 0, None) for market in event_markets
    ), "the event markets should carry live books"
    assert any(
        market.get("yes_bid_dollars") == "0.0000" for market in page_markets
    ), "the open-page markets should include zero-quote records"
    envelope = {"cursor": page_body.get("cursor", ""), "markets": event_markets + page_markets}
    kx_path = FIXTURES_DIR / "kalshi_markets.json"
    kx_path.write_text(json.dumps(envelope, indent=2) + "\n", encoding="utf-8")
    print(f"wrote {kx_path} ({len(envelope['markets'])} records)")


if __name__ == "__main__":
    main()
