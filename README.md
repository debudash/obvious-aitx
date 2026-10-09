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
