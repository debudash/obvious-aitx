# Live demo transcript — 2026-10-09

Every command below ran from the sandbox against the public, unauthenticated
market-data APIs of both venues. Read-only throughout: no keys, no auth, no
order placement. Outputs are verbatim except where a step's output is noted
as summarized.

## 1. Full pipeline, default listing slices (200 markets/venue)

```
$ equinox demo --limit 200 --routes 8
== equinox demo: ingest(200/venue) -> match -> route(top 8 pairs) ==
ingest: 400 market(s) at 2026-10-09T18:38:02.089768+00:00
  polymarket: 200 markets, 0 skipped, pages 2 fetched / 0 failed
  kalshi: 200 markets, 0 skipped, pages 2 fetched / 0 failed
match: 0 candidate pair(s) over 400 market(s) (high: 0, mid: 0) -> runs/demo/candidates.jsonl
route: 0 pair intent(s) side=yes shares=100 -> runs/demo/demo-decisions.jsonl
demo artifacts in runs/demo: summary.json, markets.jsonl, candidates.jsonl, demo-decisions.jsonl, raw/*.jsonl
```

0 candidate pairs is the headline finding, not a failure — see README
(finding 1). Widening to 1,000 markets per venue does not change it:

```
$ equinox demo --limit 1000 --routes 8
ingest: 2000 market(s) at 2026-10-09T18:32:54.960007+00:00
  polymarket: 1000 markets, 0 skipped, pages 10 fetched / 0 failed
  kalshi: 1000 markets, 0 skipped, pages 10 fetched / 0 failed
match: 0 candidate pair(s) over 2000 market(s) (high: 0, mid: 0)
```

Below-threshold analysis (off-snapshot diagnostic, matching `min threshold`
set to 0): 392 cross-venue blocked pairs at 200/venue (max score 0.234),
14,313 at 1,000/venue (max score 0.321) — all parlay player-name noise
correctly under the 0.55 threshold. The slices share no real event.

## 2. Routing a single market from the live snapshot

```
$ equinox route --snapshot runs/demo --ref 0x02deb9538f5c123373adaa4ee6217b01745f1662bc902e46ac92f3fe6f8741e8 --shares 100
route: rt_cff50d0c01ab -> runs/demo/route.jsonl
  intent: 0x02deb9538f5c123373adaa4ee6217b01745f1662bc902e46ac92f3fe6f8741e8 side=yes shares=100
    polymarket: all-in 0.0377 = price 0.0320 + spread 0.0035 + fee 0.0022
      | 1 quotable venue(s) for 0x02deb9538f5c123373adaa4ee6217b01745f1662bc902e46ac92f3fe6f8741e8: venue=polymarket
      | venue=polymarket all-in 0.0377 = price 0.0320 + spread_impact 0.0035 + fee 0.0022 (fee source market:0x02deb9538f5c123373adaa4ee6217b01745f1662bc902e46ac92f3fe6f8741e8:feeSchedule.rate)
      | per-share all-in comparison under the top-of-book assumption (spec A2); shares=100 do not change the ranking
      | chosen: venue=polymarket
      | rejected venue='kalshi': no market in the snapshot resolves from this reference
  chosen: polymarket
```

## 3. Aligned-slice probe (cross-venue candidates on a shared event class)

The venue-default listings are disjoint, so this diagnostic re-pulls one
shared event class from each venue — the 2027 pro-football championship
(Kalshi `KXSB` series vs Polymarket's `pro-football-2027-champion` event) —
and runs the payloads through the unmodified adapters, matcher, and router.
Committed evidence: `demo/probe/` (raw payloads, candidates, decision trace);
re-pull commands and the offline run are documented in
`demo/probe/run_probe.py`.

```
$ python3 demo/probe/run_probe.py
collect kalshi: 32 markets, () skipped
collect polymarket: 32 markets, (SkippedRecord(venue='polymarket', reason="missing required field 'outcomePrices'", raw_ref='memory:33'),) skipped

default-threshold candidates: 6

  score 0.578 [mid]
    KX: Will Kansas City win the 2027 Pro Football Championship? (KXSB-27-KC)
    PM: Will the Kansas City Chiefs win the 2027 NFL league championship? (0xee307e0609e90bd64e7bb75135662091ff438b043031d79337a67b4d1f54763d)
    rationale: ("score 0.5780 in band 'mid' (thresholds: mid ≥ 0.55, high ≥ 0.70)", 'features: token_overlap=0.545, idf_overlap=0.533, numeric_agreement=1.000, date_proximity=0.000', "strongest shared tokens: 'city' (idf 3.50), 'kansas' (idf 3.50)", 'close dates differ by 684.8 days (beyond the 7-day blocking window) — verify the venues resolve the same window')
```

All six matches are the two-token city names (Kansas City, Las Vegas,
Green Bay, Tampa Bay, New England, New Orleans — scores 0.567–0.578, all
`mid` band, none `high`). Full candidate list: `demo/probe/candidates.jsonl`.

The probe then routes the best pair on the committed live quotes:

```
route decision for pair:KXSB-27-KC:0xee307e0609e90bd64e7bb75135662091ff438b043031d79337a67b4d1f54763d: chosen=polymarket
    venue=polymarket all-in 0.1098 = price 0.1040 + spread_impact 0.0030 + fee 0.0028 (fee source market:0xee307e0609e90bd64e7bb75135662091ff438b043031d79337a67b4d1f54763d:feeSchedule.rate)
    venue=kalshi all-in 0.1113 = price 0.1000 + spread_impact 0.0050 + fee 0.0063 (fee source config:2026-10-09)
    venue=polymarket leads venue=kalshi by 0.0015 on raw cost — largest contributor: price (-0.0040)
    per-share all-in comparison under the top-of-book assumption (spec A2); shares=100 do not change the ranking
    chosen: venue=polymarket
```

Kalshi had the cheaper raw price (0.100 vs 0.104) and still lost all-in on
fees. Trace: `demo/probe/decision.json`.

## 4. Snapshot artifacts (runs/demo, not committed)

Per-venue ingest summary (verbatim from `runs/demo/summary.json`, the 200/
venue run):

```json
{
  "degraded": [],
  "errors": [],
  "fetched_at": "2026-10-09T18:38:02.089768+00:00",
  "limit_per_venue": 200,
  "markets_total": 400,
  "per_venue": {
    "kalshi": {"markets": 200, "pages_failed": 0, "pages_fetched": 2, "skipped": 0},
    "polymarket": {"markets": 200, "pages_failed": 0, "pages_fetched": 2, "skipped": 0}
  },
  "skipped": [],
  "venues": ["polymarket", "kalshi"]
}
```

Run directories (`runs/`) are gitignored: they hold the full raw JSONL
evidence dumps, refreshed on every run. The committed evidence in `demo/`
is the aligned-slice probe plus this transcript.
