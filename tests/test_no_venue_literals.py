"""Venue-literal guard for the routing package.

Routing must stay venue-agnostic: adding venue #3 should require zero edits
under ``equinox/routing/``. Venue slugs appearing there is the classic first
smell, so the guard greps the package text directly (case-insensitive).
"""

from pathlib import Path

REPO_ROOT = Path(__file__).resolve().parents[1]
ROUTING_ROOT = REPO_ROOT / "src" / "equinox" / "routing"

FORBIDDEN_LITERALS = ("polymarket", "kalshi")


def test_no_venue_literals_in_routing() -> None:
    assert ROUTING_ROOT.is_dir(), f"routing package directory missing: {ROUTING_ROOT}"

    violations: list[str] = []
    for path in sorted(ROUTING_ROOT.rglob("*.py")):
        text = path.read_text(encoding="utf-8").lower()
        violations.extend(
            f"{path.relative_to(REPO_ROOT)} contains {literal!r}"
            for literal in FORBIDDEN_LITERALS
            if literal in text
        )

    assert not violations, "venue literals in routing package:\n" + "\n".join(violations)
