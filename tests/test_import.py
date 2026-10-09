"""Smoke test: the package and every subpackage import cleanly."""

import pytest


def test_all_packages_import() -> None:
    import equinox
    import equinox.cli
    import equinox.matching
    import equinox.model
    import equinox.routing
    import equinox.venues

    assert equinox.__doc__


def test_cli_entry_point_is_a_declared_stub() -> None:
    from equinox.cli import main

    with pytest.raises(NotImplementedError, match="scaffolded"):
        main()
