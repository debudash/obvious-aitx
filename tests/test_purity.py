"""Structural purity guards for the model, matching, and routing layers.

The PRD requires venue-free, I/O-free matching and routing, and the market
model must stay pure too (it is the representation every layer shares).
Rather than trust convention, the import graph is enforced statically: the
pure packages and modules must never import the venue adapters, HTTP
machinery, or the standard library's I/O surfaces.

Static AST analysis; dynamic ``importlib`` calls with computed names are out
of scope for the spike.
"""

import ast
from pathlib import Path

import pytest

REPO_ROOT = Path(__file__).resolve().parents[1]
SRC_ROOT = REPO_ROOT / "src" / "equinox"
PKG_ROOT = SRC_ROOT.parent  # src/, the root for resolving relative imports

PURE_PACKAGES = ("matching", "routing", "model")
PURE_MODULES = ("config",)  # top-level pure modules (src/equinox/<name>.py)
FORBIDDEN_IMPORTS = (
    "requests",
    "urllib",
    "http",
    "socket",
    "ftplib",
    "ssl",
    "subprocess",
    "os",
    "pathlib",
    "io",
    "shutil",
    "equinox.venues",
)


def _import_targets(path: Path) -> set[str]:
    """Full dotted module names imported by *path*, relatives resolved."""
    tree = ast.parse(path.read_text(encoding="utf-8"), filename=str(path))
    package = path.relative_to(PKG_ROOT).with_suffix("").parts[:-1]
    targets: set[str] = set()
    for node in ast.walk(tree):
        if isinstance(node, ast.Import):
            targets.update(alias.name for alias in node.names)
        elif isinstance(node, ast.ImportFrom):
            if node.level:  # relative import: resolve against this file's package
                base = package[: len(package) - (node.level - 1)]
                resolved = (*base, node.module) if node.module else base
                targets.add(".".join(resolved))
            elif node.module:
                targets.add(node.module)
    return targets


def _is_forbidden(module: str) -> bool:
    return any(module == name or module.startswith(name + ".") for name in FORBIDDEN_IMPORTS)


@pytest.mark.parametrize("package", PURE_PACKAGES)
def test_pure_packages_never_import_io_or_venues(package: str) -> None:
    package_dir = SRC_ROOT / package
    assert package_dir.is_dir(), f"pure package directory missing: {package_dir}"
    files = sorted(package_dir.rglob("*.py"))
    assert files, f"no python files found in pure package: {package_dir}"

    violations = [
        f"{path.relative_to(REPO_ROOT)} imports {module}"
        for path in files
        for module in sorted(_import_targets(path))
        if _is_forbidden(module)
    ]
    assert not violations, "forbidden imports in pure layer:\n" + "\n".join(violations)


@pytest.mark.parametrize("module_name", PURE_MODULES)
def test_pure_modules_never_import_io_or_venues(module_name: str) -> None:
    module_path = SRC_ROOT / f"{module_name}.py"
    assert module_path.is_file(), f"pure module missing: {module_path}"

    violations = [
        f"{module_path.relative_to(REPO_ROOT)} imports {module}"
        for module in sorted(_import_targets(module_path))
        if _is_forbidden(module)
    ]
    assert not violations, "forbidden imports in pure module:\n" + "\n".join(violations)
