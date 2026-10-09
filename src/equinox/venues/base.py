"""Venue adapter contract: transport, resilience, evidence, and collection.

This package is the only venue-aware layer (spec §Architecture): HTTP,
pagination, payload→model mapping, and raw-JSONL evidence dumps live here;
everything downstream consumes only :mod:`equinox.model` shapes.

Resilience contract (spec §Resilience, adapter contract §Contracts):

- Every request carries a timeout — the wrapper passes one on every call.
- Transport failures and 5xx/429 responses retry exactly ``MAX_RETRIES``
  times with deterministic exponential backoff: delays are a pure function
  of the attempt number (no jitter, no clock reads).
- A 4xx response fails immediately — retrying cannot help.
- Page-level failures that survive retries become :class:`FetchError`
  records on the :class:`CollectResult`; they never raise out of
  :meth:`collect`.
- Record-level problems are skipped and counted, never raised; every raw
  record — parsed or skipped — lands in the evidence sink first, so a
  skip is always auditable against its payload.
"""

from __future__ import annotations

import json
import time
from collections.abc import Callable, Mapping, Sequence
from dataclasses import dataclass
from datetime import UTC, datetime
from pathlib import Path
from typing import Any, ClassVar, Protocol

from equinox.model import FetchError, Market, MarketSnapshot

DEFAULT_TIMEOUT_SECONDS = 10.0
"""Per-request timeout handed to every transport call."""

MAX_RETRIES = 3
"""Retries after the initial attempt (spec: "Retry ×3") — 4 attempts total."""

MAX_PAGE_FAILURES = 4
"""Failed pages one adapter tolerates inside a single collect() call before
returning early with what it has. Bounds a fully-down venue: without the cap,
an offset adapter that never charges the limit for a lost page would loop
forever. Four lost pages = the venue is down, say so and go home."""

BACKOFF_BASE_SECONDS = 0.5
"""Base delay; retry *n* waits ``base * 2**(n-1)`` seconds (0.5, 1.0, 2.0)."""

SKIP_EXCEPTIONS = (KeyError, TypeError, ValueError)
"""Exception types treated as record-level problems (skip + count).

``PriceScaleError`` and ``TimeParseError`` are ``ValueError`` subclasses, so
model normalization failures land here. Anything else escaping a parser is a
bug and propagates — the adapter contract contains data problems, not bugs.
"""


def _utc_now() -> datetime:
    """Default adapter clock: wall time at the moment of collection."""
    return datetime.now(UTC)


def require_field(record: Mapping[str, Any], field_name: str) -> Any:
    """Return *record[field_name]* or raise a skip-worthy ``ValueError``."""
    value = record.get(field_name)
    if value is None or (isinstance(value, str) and not value.strip()):
        raise ValueError(f"missing required field {field_name!r}")
    return value


class TransportFailure(Exception):
    """A transport-level failure (timeout, connection error) — retryable.

    Raised by :class:`Transport` implementations; the retry wrapper owns
    counting and converts it into a :class:`FetchFailure` after exhaustion.
    """


@dataclass(frozen=True)
class TransportResponse:
    """A transport-level HTTP response, venue-agnostic."""

    status_code: int
    text: str

    def json(self) -> Any:
        """Parse the body as JSON; ``ValueError`` signals a malformed body."""
        return json.loads(self.text)


class Transport(Protocol):
    """Minimal HTTP surface the adapters need (GET only — read-only spike)."""

    def get(
        self, url: str, *, params: Mapping[str, str], timeout: float
    ) -> TransportResponse: ...


class FetchFailure(Exception):
    """A page-level failure inside the retry wrapper.

    Carries what happened (:attr:`cause`), how many attempts were made
    (:attr:`attempts`), and whether it is :attr:`retryable`. ``collect``
    converts every surviving ``FetchFailure`` into a model
    :class:`~equinox.model.FetchError` record.
    """

    def __init__(self, cause: str, attempts: int, *, retryable: bool = True) -> None:
        super().__init__(cause)
        self.cause = cause
        self.attempts = attempts
        self.retryable = retryable


def get_json_with_retries(
    transport: Transport,
    url: str,
    params: Mapping[str, str],
    *,
    timeout: float = DEFAULT_TIMEOUT_SECONDS,
    max_retries: int = MAX_RETRIES,
    backoff_base: float = BACKOFF_BASE_SECONDS,
    sleep: Callable[[float], None] = time.sleep,
) -> Any:
    """GET *url* and return the parsed JSON body, retrying deterministically.

    Retryable: transport failures, HTTP 5xx and 429, and 200-bodies that fail
    to parse (a truncated body is a transport problem wearing a 200). Any
    other 4xx raises immediately. On exhaustion raises :class:`FetchFailure`
    with the total attempt count. Backoff before retry *n* is
    ``backoff_base * 2**(n-1)`` — no jitter, no clock reads — emitted through
    the injectable *sleep* so tests observe the schedule without waiting.
    """
    attempts = 0
    last_failure: FetchFailure | None = None
    while True:
        attempts += 1
        try:
            response = transport.get(url, params=dict(params), timeout=timeout)
        except TransportFailure as exc:
            last_failure = FetchFailure(f"transport failure: {exc}", attempts)
        else:
            code = response.status_code
            if 200 <= code < 300:
                try:
                    return response.json()
                except ValueError as exc:
                    last_failure = FetchFailure(f"malformed JSON in response: {exc}", attempts)
            elif code == 429 or code >= 500:
                last_failure = FetchFailure(f"HTTP {code} from {url}", attempts)
            else:
                raise FetchFailure(f"HTTP {code} from {url}", attempts, retryable=False)
        if attempts > max_retries:  # initial attempt + max_retries retries spent
            assert last_failure is not None  # a failure path always set it
            raise last_failure
        sleep(backoff_base * 2 ** (attempts - 1))


class EvidenceSink(Protocol):
    """Append-only raw-evidence dump; ``write`` returns a line pointer."""

    def write(self, record: Mapping[str, Any]) -> str: ...


class JsonlFileSink:
    """JSONL evidence file; raw refs are ``"<path>:<line>"`` pointers.

    Deterministic bytes for identical records (sorted keys); appends survive
    re-runs by counting existing lines so refs stay unique across runs.
    """

    def __init__(self, path: Path | str) -> None:
        self._path = Path(path)
        self._path.parent.mkdir(parents=True, exist_ok=True)
        self._lines = 0
        if self._path.exists():
            with self._path.open("rb") as existing:
                self._lines = sum(1 for _ in existing)

    @property
    def path(self) -> Path:
        """The evidence file this sink appends to."""
        return self._path

    def write(self, record: Mapping[str, Any]) -> str:
        self._lines += 1
        with self._path.open("a", encoding="utf-8") as stream:
            stream.write(json.dumps(record, sort_keys=True) + "\n")
        return f"{self._path}:{self._lines}"


class MemoryEvidenceSink:
    """In-memory sink for tests and dry runs; refs are ``"memory:<line>"``."""

    def __init__(self) -> None:
        self.records: list[dict[str, Any]] = []

    def write(self, record: Mapping[str, Any]) -> str:
        self.records.append(dict(record))
        return f"memory:{len(self.records)}"


@dataclass(frozen=True)
class SkippedRecord:
    """One raw record dropped at normalization, with the reason why.

    Kept per-record (not just a count) because the reason names the field
    that failed — the ingest summary becomes an audit trail, not a tally.
    """

    venue: str
    reason: str
    raw_ref: str

    def __post_init__(self) -> None:
        for attr in ("venue", "reason", "raw_ref"):
            value = getattr(self, attr)
            if not isinstance(value, str) or not value.strip():
                raise ValueError(f"SkippedRecord.{attr} must be a non-empty string, got {value!r}")


@dataclass(frozen=True)
class CollectResult:
    """Outcome of one adapter run: markets kept, why the rest did not make it.

    ``errors`` are page-level failures that survived retries — any non-empty
    ``errors`` means the venue is :attr:`degraded` and the snapshot says so.
    """

    venue: str
    markets: tuple[Market, ...] = ()
    errors: tuple[FetchError, ...] = ()
    skipped: tuple[SkippedRecord, ...] = ()
    pages_fetched: int = 0
    pages_failed: int = 0

    def __post_init__(self) -> None:
        if not isinstance(self.venue, str) or not self.venue.strip():
            raise ValueError(f"CollectResult.venue must be a non-empty string, got {self.venue!r}")
        for attr in ("markets", "errors", "skipped"):
            object.__setattr__(self, attr, tuple(getattr(self, attr)))
        for attr in ("pages_fetched", "pages_failed"):
            value = getattr(self, attr)
            if isinstance(value, bool) or not isinstance(value, int) or value < 0:
                raise ValueError(f"CollectResult.{attr} must be a non-negative int, got {value!r}")

    @property
    def degraded(self) -> bool:
        """True when any page was lost after retries."""
        return bool(self.errors)


class VenueAdapter(Protocol):
    """What every venue adapter promises (spec §Contracts, adapter layer).

    ``collect`` is the ingest entry point: it pulls up to *limit* raw
    records (markets plus skipped records count against it), maps them to
    the internal model, and reports page failures as data — never by
    raising. Only non-recoverable infrastructure problems (e.g. a failing
    evidence sink) propagate.
    """

    venue: str
    base_url: str

    def collect(self, limit: int = 500) -> CollectResult: ...


class BaseVenueAdapter:
    """Template-method base for adapters: pagination plumbing, skip accounting.

    Subclasses provide the venue specifics — ``venue``, ``base_url``, how to
    fetch one page (:meth:`fetch_page`), when pagination ends
    (:meth:`pagination_done`), and how one raw record maps to a
    :class:`~equinox.model.Market` (:meth:`parse_record`). Everything shared —
    timeouts, retries, evidence dumping, dedupe, skip counting, error
    containment — lives here so a third venue cannot get it wrong.

    Class attrs a subclass sets:

    - ``continue_after_page_failure`` — offset-paginated venues can advance
      past a lost page and keep going; cursor-paginated venues cannot (the
      cursor for the next page was in the lost body) and must stop.
    """

    venue: ClassVar[str]
    base_url: ClassVar[str]
    continue_after_page_failure: ClassVar[bool] = False

    def __init__(
        self,
        *,
        sink: EvidenceSink,
        transport: Transport | None = None,
        clock: Callable[[], datetime] | None = None,
        timeout: float = DEFAULT_TIMEOUT_SECONDS,
        max_retries: int = MAX_RETRIES,
        backoff_base: float = BACKOFF_BASE_SECONDS,
        sleep: Callable[[float], None] = time.sleep,
        page_size: int = 100,
    ) -> None:
        if not 0 < page_size <= 1000:
            raise ValueError(f"page_size must be in (0, 1000], got {page_size!r}")
        self._sink = sink
        self._transport = transport if transport is not None else RequestsTransport()
        self._clock = clock if clock is not None else _utc_now
        self._timeout = timeout
        self._max_retries = max_retries
        self._backoff_base = backoff_base
        self._sleep = sleep
        self._page_size = page_size

    def collect(self, limit: int = 500) -> CollectResult:
        """Pull up to *limit* raw records, mapped to the internal model."""
        if isinstance(limit, bool) or not isinstance(limit, int) or limit < 1:
            raise ValueError(f"limit must be a positive int, got {limit!r}")

        fetched_at = self._clock()
        markets: list[Market] = []
        skipped: list[SkippedRecord] = []
        errors: list[FetchError] = []
        seen_ids: set[str] = set()
        pages_fetched = 0
        pages_failed = 0
        consumed = 0
        page_state: Any = self.initial_page_state()

        while consumed < limit:
            budget = min(self._page_size, limit - consumed)
            try:
                records, next_state = self.fetch_page(page_state, budget)
            except FetchFailure as exc:
                # Page lost after retries: record it, keep the venue honest
                # (degraded), and either advance past it or stop pagination —
                # a subclass flag, because the next-page pointer determines it.
                # The lost page does not charge the caller's limit: the caller
                # asked for *limit* markets, and they may sit at the next
                # offset. MAX_PAGE_FAILURES bounds a venue that stays down.
                errors.append(
                    FetchError(
                        venue=self.venue,
                        url=self.base_url,
                        cause=exc.cause,
                        attempts=exc.attempts,
                    )
                )
                pages_failed += 1
                if not self.continue_after_page_failure or pages_failed >= MAX_PAGE_FAILURES:
                    break
                page_state = self.advance_after_failure(page_state, budget)
                continue

            pages_fetched += 1
            for raw in records:
                if consumed >= limit:
                    break  # fetched the page but the limit stops parsing here
                consumed += 1
                self._ingest(raw, fetched_at, seen_ids, markets, skipped)
            if self.pagination_done(next_state, len(records), budget):
                break
            page_state = next_state

        return CollectResult(
            venue=self.venue,
            markets=tuple(markets),
            errors=tuple(errors),
            skipped=tuple(skipped),
            pages_fetched=pages_fetched,
            pages_failed=pages_failed,
        )

    def _ingest(
        self,
        raw: Any,
        fetched_at: datetime,
        seen_ids: set[str],
        markets: list[Market],
        skipped: list[SkippedRecord],
    ) -> None:
        """Dump, parse, dedupe: every raw record lands in the sink first."""
        payload = raw if isinstance(raw, Mapping) else {"raw": raw}
        raw_ref = self._sink.write(payload)
        try:
            market = self.parse_record(raw, raw_ref=raw_ref, fetched_at=fetched_at)
        except SKIP_EXCEPTIONS as exc:
            reason = str(exc).strip() or type(exc).__name__
            skipped.append(SkippedRecord(venue=self.venue, reason=reason, raw_ref=raw_ref))
            return
        if market.market_id in seen_ids:
            skipped.append(
                SkippedRecord(
                    venue=self.venue,
                    reason=f"duplicate market_id {market.market_id!r} (pagination churn)",
                    raw_ref=raw_ref,
                )
            )
            return
        seen_ids.add(market.market_id)
        markets.append(market)

    # --- venue-specific hooks -------------------------------------------------

    def initial_page_state(self) -> Any:
        """The starting pagination pointer (offset 0, or no cursor)."""
        return None

    def fetch_page(self, page_state: Any, budget: int) -> tuple[list[Any], Any]:
        """Fetch one page; return (records, next state). Raises FetchFailure."""
        raise NotImplementedError

    def pagination_done(self, next_state: Any, records_in_page: int, budget: int) -> bool:
        """Whether pagination ends after the page just fetched."""
        raise NotImplementedError

    def advance_after_failure(self, page_state: Any, budget: int) -> Any:
        """Next pagination pointer after a lost page (offset venues only)."""
        return page_state

    def parse_record(self, raw: Any, *, raw_ref: str, fetched_at: datetime) -> Market:
        """Map one raw venue record to a Market; raise to skip with reason."""
        raise NotImplementedError


class RequestsTransport:
    """``requests``-backed transport — the only place requests is touched."""

    def __init__(self, session: Any = None) -> None:
        import requests

        self._requests = requests
        self._session = session

    def get(
        self, url: str, *, params: Mapping[str, str], timeout: float
    ) -> TransportResponse:
        try:
            if self._session is not None:
                response = self._session.get(url, params=params, timeout=timeout)
            else:
                response = self._requests.get(url, params=params, timeout=timeout)
        except self._requests.RequestException as exc:
            raise TransportFailure(f"{type(exc).__name__}: {exc}") from exc
        return TransportResponse(status_code=response.status_code, text=response.text)


def collect_snapshot(
    adapters: Sequence[VenueAdapter], *, limit: int, fetched_at: datetime
) -> MarketSnapshot:
    """Collect from every adapter into one snapshot, isolating venues.

    An adapter that raises (a bug, or a venue down at a deeper layer than
    pages) must not poison the others: its venue is recorded as degraded with
    a :class:`~equinox.model.FetchError` describing the crash, and the healthy
    venues proceed (spec §Resilience, "one venue fully down"). The broad
    except is the isolation boundary — the failure is surfaced as data on the
    snapshot, never swallowed.
    """
    markets: list[Market] = []
    errors: list[FetchError] = []
    degraded: list[str] = []
    venues: list[str] = []
    for adapter in adapters:
        venue = getattr(adapter, "venue", "unknown")
        venues.append(venue)
        try:
            result = adapter.collect(limit)
        except Exception as exc:  # isolation boundary: the failure is surfaced as data below
            base_url = getattr(adapter, "base_url", "unknown")
            errors.append(
                FetchError(
                    venue=venue,
                    url=str(base_url),
                    cause=f"adapter crashed: {type(exc).__name__}: {exc}",
                    attempts=1,
                )
            )
            degraded.append(venue)
            continue
        markets.extend(result.markets)
        errors.extend(result.errors)
        if result.degraded:
            degraded.append(result.venue)
    return MarketSnapshot(
        fetched_at=fetched_at,
        venues=venues,
        markets=markets,
        degraded=degraded,
        errors=errors,
    )


__all__ = [
    "BACKOFF_BASE_SECONDS",
    "DEFAULT_TIMEOUT_SECONDS",
    "MAX_PAGE_FAILURES",
    "MAX_RETRIES",
    "BaseVenueAdapter",
    "CollectResult",
    "EvidenceSink",
    "FetchFailure",
    "JsonlFileSink",
    "MemoryEvidenceSink",
    "RequestsTransport",
    "SkippedRecord",
    "Transport",
    "TransportFailure",
    "TransportResponse",
    "VenueAdapter",
    "collect_snapshot",
    "get_json_with_retries",
    "require_field",
]
