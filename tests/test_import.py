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


def test_cli_entry_point_parses_help() -> None:
    """The scaffold stub is gone: main() is a real argparse entry point."""
    from equinox.cli import main

    with pytest.raises(SystemExit) as excinfo:
        main(["--help"])
    assert excinfo.value.code == 0
