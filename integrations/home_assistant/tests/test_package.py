"""make ha-package's zip (M8-D11): the component alone, under custom_components/cadence, the same bytes every build."""

from __future__ import annotations

import json
import os
import shutil
import subprocess
import sys
import zipfile
from pathlib import Path

ROOT = Path(__file__).parents[1]
SCRIPT = ROOT / "scripts" / "package.py"
COMPONENT = ROOT / "custom_components" / "cadence"


def _component(tmp_path: Path) -> Path:
    """A copy of the component, with what a working tree also holds: bytecode and a hidden file."""
    component = tmp_path / "src" / "cadence"
    shutil.copytree(COMPONENT, component, ignore=shutil.ignore_patterns("__pycache__", "*.pyc"))
    (component / "__pycache__").mkdir()
    (component / "__pycache__" / "client.cpython-314.pyc").write_bytes(b"\x00")
    (component / "stale.pyc").write_bytes(b"\x00")
    (component / ".DS_Store").write_bytes(b"\x00")
    return component


def _package(component: Path, out: Path) -> Path:
    done = subprocess.run(  # noqa: S603
        [sys.executable, "-B", str(SCRIPT), str(component), str(out)],
        check=True,
        capture_output=True,
        text=True,
        timeout=30,
    )
    return Path(done.stdout.strip())


def test_the_package_holds_the_component_alone(tmp_path: Path) -> None:
    component = _component(tmp_path)
    version = json.loads((component / "manifest.json").read_text())["version"]
    out = tmp_path / "out"

    path = _package(component, out)

    assert path == out / f"cadence-ha-{version}.zip"
    sources = sorted(
        p.relative_to(component).as_posix()
        for p in component.rglob("*")
        if p.is_file() and "__pycache__" not in p.parts and p.suffix != ".pyc" and not p.name.startswith(".")
    )
    assert {"manifest.json", "__init__.py", "services.yaml", "strings.json", "translations/ru.json"} <= set(sources)
    with zipfile.ZipFile(path) as archive:
        assert archive.namelist() == [f"custom_components/cadence/{name}" for name in sources]
        for name in sources:
            assert archive.read(f"custom_components/cadence/{name}") == (component / name).read_bytes()


def test_the_package_is_the_same_for_the_same_files(tmp_path: Path) -> None:
    component = _component(tmp_path)
    first = _package(component, tmp_path / "a").read_bytes()
    os.utime(component / "client.py", (1_000_000_000, 1_000_000_000))

    second = _package(component, tmp_path / "b").read_bytes()

    assert first == second
