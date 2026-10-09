"""Configuration for matching and routing (spec §Matching, §Routing).

Every knob tuning may touch lives here as data — thresholds, weights, and
penalties are config, never literals in logic. The defaults are the spike's
starting values from the spec; the seeded-pair calibration may revise them
through config, not code changes.
"""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass, field
from datetime import timedelta
from types import MappingProxyType

from equinox.model import FeeParams


@dataclass(frozen=True)
class MatchConfig:
    """Deterministic lexical matching knobs (spec §Matching).

    - **Blocking** — a pair is only scored when it shares a rare token
      (document frequency ≤ ``rare_token_max_doc_freq`` of the snapshot)
      or its close dates fall within ``date_block_window_days``.
    - **Score** — weighted sum of the four named features; weights must sum
      to 1.0 so scores keep their 0–1 meaning.
    - **Thresholds** — score ≥ ``mid_threshold`` becomes a ``MatchCandidate``
      (spec MID: 0.55); score ≥ ``high_threshold`` is additionally flagged
      ``review_recommended`` (spec HIGH: 0.70).
    """

    rare_token_max_doc_freq: float = 0.05
    date_block_window_days: int = 7
    weight_token_overlap: float = 0.40
    weight_idf_overlap: float = 0.30
    weight_numeric_agreement: float = 0.20
    weight_date_proximity: float = 0.10
    mid_threshold: float = 0.55
    high_threshold: float = 0.70

    def __post_init__(self) -> None:
        if not 0.0 < self.rare_token_max_doc_freq <= 1.0:
            raise ValueError(
                f"rare_token_max_doc_freq must be in (0, 1], got "
                f"{self.rare_token_max_doc_freq!r}"
            )
        if self.date_block_window_days < 0:
            raise ValueError(
                f"date_block_window_days must be >= 0, got {self.date_block_window_days!r}"
            )
        weights = {
            "token_overlap": self.weight_token_overlap,
            "idf_overlap": self.weight_idf_overlap,
            "numeric_agreement": self.weight_numeric_agreement,
            "date_proximity": self.weight_date_proximity,
        }
        for name, weight in weights.items():
            if isinstance(weight, bool) or not isinstance(weight, (int, float)):
                raise ValueError(f"weight_{name} must be a number, got {weight!r}")
            if weight < 0:
                raise ValueError(f"weight_{name} must be non-negative, got {weight!r}")
        total = sum(weights.values())
        if abs(total - 1.0) > 1e-9:
            raise ValueError(
                f"feature weights must sum to 1.0 (got {total:.6f}) — "
                "otherwise scores lose their 0-1 meaning"
            )
        for name, threshold in (
            ("mid_threshold", self.mid_threshold),
            ("high_threshold", self.high_threshold),
        ):
            if isinstance(threshold, bool) or not 0.0 <= threshold <= 1.0:
                raise ValueError(f"{name} must be within [0, 1], got {threshold!r}")
        if self.mid_threshold > self.high_threshold:
            raise ValueError(
                f"mid_threshold ({self.mid_threshold}) must not exceed "
                f"high_threshold ({self.high_threshold})"
            )


@dataclass(frozen=True)
class RouterConfig:
    """Pure cost-based routing knobs (spec §Routing).

    - ``max_quote_age`` — quotes whose age exceeds this are marked stale,
      penalized during selection, and called out in the reasoning trace;
      never silently chosen.
    - ``fee_params_by_venue`` — config-dated per-venue fallback fee params
      (assumption A1: Kalshi's schedule is config, not code). Adapters may
      also stamp fees per market; the router resolves per market first.
      Keys are lowercase venue slugs, stored read-only.
    - ``stale_cost_penalty`` — additive per-share cost applied to stale
      quotes during venue selection; the effect is recorded in the trace.
    - ``cost_tie_epsilon`` — all-in costs closer than this are treated as a
      tie, which triggers the explicit tie-break (higher liquidity, then
      venue slug alphabetically) instead of float luck.
    """

    max_quote_age: timedelta = timedelta(minutes=5)
    fee_params_by_venue: Mapping[str, FeeParams] = field(default_factory=dict)
    stale_cost_penalty: float = 0.01
    cost_tie_epsilon: float = 1e-9

    def __post_init__(self) -> None:
        if not isinstance(self.max_quote_age, timedelta) or self.max_quote_age <= timedelta(0):
            raise ValueError(
                f"max_quote_age must be a positive timedelta, got {self.max_quote_age!r}"
            )
        if isinstance(self.stale_cost_penalty, bool) or not isinstance(
            self.stale_cost_penalty, (int, float)
        ):
            raise ValueError(
                f"stale_cost_penalty must be a number, got {self.stale_cost_penalty!r}"
            )
        if self.stale_cost_penalty < 0:
            raise ValueError(
                f"stale_cost_penalty must be non-negative, got {self.stale_cost_penalty!r}"
            )
        if isinstance(self.cost_tie_epsilon, bool) or not isinstance(
            self.cost_tie_epsilon, (int, float)
        ):
            raise ValueError(
                f"cost_tie_epsilon must be a number, got {self.cost_tie_epsilon!r}"
            )
        if self.cost_tie_epsilon <= 0:
            raise ValueError(
                f"cost_tie_epsilon must be positive, got {self.cost_tie_epsilon!r}"
            )
        normalized: dict[str, FeeParams] = {}
        for venue, fee_params in self.fee_params_by_venue.items():
            if not isinstance(venue, str) or not venue.strip():
                raise ValueError(
                    f"fee_params_by_venue keys must be non-empty venue slugs, got {venue!r}"
                )
            if not isinstance(fee_params, FeeParams):
                raise TypeError(
                    f"fee_params_by_venue[{venue!r}] must be a FeeParams, got "
                    f"{type(fee_params).__name__}"
                )
            normalized[venue.strip().lower()] = fee_params
        object.__setattr__(self, "fee_params_by_venue", MappingProxyType(normalized))

    def fee_params_for(self, venue: str) -> FeeParams | None:
        """Config-dated fallback fee params for *venue*, if configured."""
        if not isinstance(venue, str) or not venue.strip():
            raise ValueError(f"venue must be a non-empty string, got {venue!r}")
        return self.fee_params_by_venue.get(venue.strip().lower())


__all__ = ["MatchConfig", "RouterConfig"]
