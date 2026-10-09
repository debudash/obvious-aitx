# Equinox

Cross-venue prediction market normalization and routing spike: pull live markets
from Polymarket and Kalshi behind one internal market model, detect candidate
equivalent markets deterministically, and compare simulated routing decisions
with legible reasoning traces. Strictly read-only against venues — no API keys,
no authentication, no order placement.

## Development

Python 3.11+.

```bash
pip install -e '.[dev]'
```

Run the test suite (live-API smoke tests are deselected by default):

```bash
pytest
```

Lint:

```bash
ruff check .
```

Opt in to the live-API smoke tests explicitly:

```bash
pytest -m live
```

## Findings — live run, 2026-10-09

The pipeline ran end to end against the public, unauthenticated market-data
APIs of both venues from the sandbox (transcript: `demo/transcript.md`;
aligned-slice evidence: `demo/probe/`). Every number below was measured in
that run, not assumed. Read-only throughout: no keys, no auth, no orders.

**TL;DR** — normalization is solved by the adapter layer; matching is limited
by corpus alignment and naming divergence, not by the matcher; routing is
simple code but the fee inputs decide outcomes, and the first live decision
was won by a fee-source difference, not a price difference.

### Q1 — Is cross-venue normalization achievable, and where does it hurt?

Achievable. Both venues' quirks were absorbed entirely inside the adapters:
Polymarket's stringified JSON arrays and 0–1 price scale, Kalshi's
dollar-denominated quotes and opaque cursor pagination, per-market fee
metadata on both sides. Ingest of 200 markets per venue completed with zero
skipped records and zero failed pages in ~1 second; 1,000 per venue (2,000
markets, 20 pages) in ~3 seconds.

Where it hurts — measured, in order of impact:

1. **Head-of-listing slices are disjoint.** The default listing of each venue
   samples different event classes entirely: Kalshi's first 1,000 markets are
   999/1,000 closing October 2026 and dominated by sports parlays ("over":
   455 questions, "goals": 267, "points": 231); Polymarket's are
   election-heavy ("president": 283, "election": 171, "governor": 115) with
   close dates from Nov 2025 to 2031. Candidate count on unaligned slices:
   **0 at 200/venue and 0 at 1,000/venue**. Bigger N does not fix this —
   category- or event-aligned ingestion is a prerequisite for cross-venue
   matching, not an optimization.
2. **Naming divergence caps lexical matching.** On category-aligned slices
   (Kalshi `KXSB` series vs Polymarket's `pro-football-2027-champion` event,
   both live), the matcher recalled **6 of 32** team markets above the 0.55
   threshold: exactly the six teams whose city names have two tokens —
   "Will Kansas City win the 2027 Pro Football Championship?" ↔ "Will the
   Kansas City Chiefs win the 2027 NFL league championship?" (0.578). Teams
   whose venue names share only one token (Arizona ↔ Cardinals, Chicago ↔
   Bears) fell below threshold: one shared content token cannot carry the
   weighted score. Mascot-only divergences (Kalshi "Buffalo wins" vs
   Polymarket "Bills vs. Rams") share no content token at all.
3. **Unpriced provisional markets flow through as data.** All 200 head-slice
   Kalshi markets carried `yes_ask = 0.0` — provisional parlay markets that
   have no quotes yet. The model has no provisional/liveliness flag, so a
   router reading these quotes raw would see free liquidity. Feed selection
   (or a model field) must exclude unpriced markets before routing.
4. **"Same event, different clocks."** The two venues' championship markets
   for the same season close **684.8 days** apart (Kalshi settles at the
   season window, Polymarket on 2027-04-01). Every matched pair's rationale
   flags this explicitly — date proximity contributed 0.0 to all six scores,
   yet they still cleared the threshold on token evidence.

### Q2 — What architectural trade-offs does the shared layer force?

- **Fee parameters as data with provenance proved load-bearing.** The first
  live routing decision was decided by a fee-source difference, not a price
  difference: Kalshi quoted the cheaper raw price (0.100 vs 0.104) but lost
  all-in (0.1113 vs 0.1098) because Polymarket's live per-market
  `feeSchedule.rate` beats Kalshi's config-dated rate. A router with hardcoded
  fees would have chosen the wrong venue.
- **Router purity cost nothing in practice.** No venue logic, no I/O, no
  clock inside `equinox.routing` — enforced by an import-graph CI test — and
  the router ran unchanged on live data. Tie-breaks and rejections carried
  explicit reasons ("no market in the snapshot resolves from this reference").
- **Skip-and-count resilience worked.** One Polymarket record in the aligned
  probe (33rd of the event) was skipped with a named reason — `missing
  required field 'outcomePrices'` — and the other 32 flowed through; the run
  summary reports skips and page failures per venue rather than aborting.
- **Pagination failure asymmetry.** Kalshi's cursor pagination cannot resume
  after a lost page (the pointer travels in the lost body), so a failed page
  ends that venue's collection; Polymarket's offset pagination continues.
  Both behaviors are explicit in the adapter contract and tested.

### Q3 — How complex does routing get once fees, spreads, and staleness are real?

The routing layer is one pure module (456 lines incl. docstrings) plus a
trace serializer; the complexity lives in its inputs, and the live run
showed every cost component mattering:

- The first live decision on a matched pair split the cost into
  price + spread impact + fee per venue and picked `polymarket` 0.1098 over
  `kalshi` 0.1113 — the fee flipped the ranking the raw prices set. Trace:
  `demo/probe/decision.json`.
- Staleness policy never triggered on freshly fetched snapshots — the
  interesting staleness cases (cross-venue snapshots taken hours apart) need
  a long-running process to occur naturally.
- Top-of-book (spec assumption A2) held at the simulated sizes: with 100
  shares the per-share ranking is unchanged, and the trace says so explicitly.
- What routing at production scale would add: depth-aware sizing once sizes
  exceed top-of-book, provisional-market filtering (finding 3), and a
  candidate-confirmation layer (LLM judge or rules) so router inputs are
  certified pairs rather than candidates.

### Assumptions — status after the live run

- **A1 (fee rates config-dated)** — held, and proved load-bearing: the
  fee-source difference decided the first live routing decision. Kalshi's
  public market-data API exposes no per-market fee schedule, so the
  config-dated rate stands; a reversal would need account-level endpoints.
- **A2 (top-of-book sufficient)** — held at simulated sizes; depth-aware
  quoting remains the documented next increment if sizes grow.
- **A3 (lexical recall adequate)** — mixed, with mechanism: 4/4 on the
  seeded equivalent pairs; 6/32 on live aligned slices (all six two-token
  city names); 0 on unaligned head slices because the corpora do not
  overlap. The 0.55 threshold sits close to the 0.578 ceiling the best live
  pairs reached — small threshold changes move these results. Embeddings
  behind the same `find_candidates` interface remain the follow-up for
  mascot-name paraphrase; for candidate detection with audit trails, lexical
  scoring did its job.

### Reproduce

```bash
pip install -e '.[dev]'
equinox demo --limit 200 --routes 8      # full pipeline, live, ~1s
equinox ingest --limit 200               # snapshot only, into runs/ingest
equinox match --snapshot runs/ingest     # ranked candidate pairs
equinox route --snapshot runs/ingest --ref <market-id-or-ticker> --shares 100
python3 demo/probe/run_probe.py          # aligned-slice diagnostic (offline)
pytest                                   # fixture-based suite, no network
```
