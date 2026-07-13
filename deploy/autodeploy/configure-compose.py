#!/usr/bin/env python3
"""Parameterize production image names in a Sub2API Compose file."""

from __future__ import annotations

import os
import re
import shutil
import sys
import tempfile
from pathlib import Path


TARGETS = {
    "sub2api": "SUB2API_IMAGE",
    "product-sync-worker": "PRODUCT_SYNC_WORKER_IMAGE",
}


def parameterize(path: Path) -> None:
    with path.open("r", encoding="utf-8") as handle:
        lines = handle.readlines()

    current_service: str | None = None
    found: set[str] = set()
    for index, line in enumerate(lines):
        service_match = re.match(r"^  ([A-Za-z0-9_-]+):\s*(?:#.*)?$", line)
        if service_match:
            current_service = service_match.group(1)
            continue
        if current_service not in TARGETS:
            continue

        image_match = re.match(
            r"^(    image:\s*)([^#\r\n]+?)(\s*(?:#.*)?\r?\n?)$", line
        )
        if not image_match:
            continue

        variable = TARGETS[current_service]
        value = image_match.group(2).strip()
        if "${" + variable in value:
            found.add(current_service)
            current_service = None
            continue
        if value.startswith(("'", '"')) and value.endswith(value[0]):
            value = value[1:-1]

        lines[index] = (
            f"{image_match.group(1)}${{{variable}:-{value}}}{image_match.group(3)}"
        )
        found.add(current_service)
        current_service = None

    missing = set(TARGETS) - found
    if missing:
        names = ", ".join(sorted(missing))
        raise RuntimeError(f"could not parameterize image for services: {names}")

    descriptor, temporary_name = tempfile.mkstemp(
        prefix=".compose-autodeploy-", dir=path.parent, text=True
    )
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="") as handle:
            handle.writelines(lines)
        shutil.copymode(path, temporary)
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def main() -> int:
    if len(sys.argv) != 2:
        print(f"usage: {Path(sys.argv[0]).name} COMPOSE_FILE", file=sys.stderr)
        return 2
    parameterize(Path(sys.argv[1]).resolve())
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
