"""make ha-package (M8-D11): the component as a zip for a manual install, unpacked in Home Assistant's config directory.

usage: package.py <component dir> <out dir>; prints the zip's path, <out dir>/cadence-ha-<manifest version>.zip, which
holds custom_components/<component dir's name>/ and nothing else: no bytecode, no hidden file. Entries are sorted, dated
1980-01-01 and mode 644, so the same files make the same bytes.
"""

from __future__ import annotations

import json
import re
import sys
import zipfile
from pathlib import Path

STAMP = (1980, 1, 1, 0, 0, 0)
VERSION = re.compile(r"[0-9A-Za-z][0-9A-Za-z.+-]*")


def sources(component: Path) -> list[Path]:
    return sorted(
        (
            p
            for p in component.rglob("*")
            if p.is_file()
            and "__pycache__" not in p.relative_to(component).parts
            and p.suffix != ".pyc"
            and not any(part.startswith(".") for part in p.relative_to(component).parts)
        ),
        key=lambda p: p.relative_to(component).as_posix(),
    )


def build(component: Path, out: Path) -> Path:
    version = json.loads((component / "manifest.json").read_text(encoding="utf-8")).get("version")
    if not isinstance(version, str) or not VERSION.fullmatch(version):
        raise SystemExit("package: the manifest has no plain version")
    out.mkdir(parents=True, exist_ok=True)
    path = out / f"cadence-ha-{version}.zip"
    with zipfile.ZipFile(path, "w") as archive:
        for source in sources(component):
            entry = zipfile.ZipInfo(f"custom_components/{component.name}/{source.relative_to(component).as_posix()}")
            entry.date_time = STAMP
            entry.external_attr = 0o100644 << 16
            entry.compress_type = zipfile.ZIP_DEFLATED
            archive.writestr(entry, source.read_bytes())
    return path


def main(argv: list[str]) -> int:
    if len(argv) != 2:
        print(__doc__.partition("\n\n")[2].partition(";")[0], file=sys.stderr)
        return 2
    print(build(Path(argv[0]), Path(argv[1])))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
