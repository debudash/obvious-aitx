"""Venue-literal guard for the venue-agnostic pure packages.

Matching and routing must stay venue-agnostic: adding venue #3 should
require zero edits under either package. Venue slugs appearing there is
the classic first smell, so the guard greps the package text directly
(case-insensitive).
"""

from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).resolve().parents[1]

FORBIDDEN_LITERALS = ("polymarket", "kalshi")
VENUE_AGNOSTIC_PACKAGES = ("matching", "routing")


@pytest.mark.parametrize("package", VENUE_AGNOSTIC_PACKAGES)
def test_no_venue_literals(package: str) -> None:
    package_root = REPO_ROOT / "src" / "equinox" / package
    assert package_root.is_dir(), f"package directory missing: {package_root}"

    violations: list[str] = []
    for path in sorted(package_root.rglob("*.py")):
        text = path.read_text(encoding="utf-8").lower()
        violations.extend(
            f"{path.relative_to(REPO_ROOT)} contains {literal!r}"
            for literal in FORBIDDEN_LITERALS
            if literal in text
        )

    assert not violations, (
        f"venue literals in {package} package:\n" + "\n".join(violations)
    )
