#!/usr/bin/env python3
"""Parameterize production image names in a Sub2API Compose file."""

from __future__ import annotations

import os
import re
import shutil
import sys
import tempfile
from pathlib import Path


APP_SECTIONS = {"sub2api", "x-sub2api-common"}
WORKER_SECTION = "product-sync-worker"


def parameterize(path: Path) -> None:
    with path.open("r", encoding="utf-8") as handle:
        lines = handle.readlines()

    current_section: str | None = None
    found_app = False
    found_worker = False
    for index, line in enumerate(lines):
        extension_match = re.match(
            r"^(x-sub2api-common):\s*(?:&[A-Za-z0-9_-]+)?\s*(?:#.*)?$", line
        )
        if extension_match:
            current_section = extension_match.group(1)
            continue
        service_match = re.match(r"^  ([A-Za-z0-9_-]+):\s*(?:#.*)?$", line)
        if service_match:
            current_section = service_match.group(1)
            continue
        if current_section not in APP_SECTIONS | {WORKER_SECTION}:
            continue

        image_match = re.match(
            r"^(\s+image:\s*)([^#\r\n]+?)(\s*(?:#.*)?\r?\n?)$", line
        )
        if not image_match:
            continue

        is_app = current_section in APP_SECTIONS
        variable = "SUB2API_IMAGE" if is_app else "PRODUCT_SYNC_WORKER_IMAGE"
        value = image_match.group(2).strip()
        if "${" + variable in value:
            found_app = found_app or is_app
            found_worker = found_worker or not is_app
            current_section = None
            continue
        if value.startswith(("'", '"')) and value.endswith(value[0]):
            value = value[1:-1]

        lines[index] = (
            f"{image_match.group(1)}${{{variable}:-{value}}}{image_match.group(3)}"
        )
        found_app = found_app or is_app
        found_worker = found_worker or not is_app
        current_section = None

    missing = []
    if not found_app:
        missing.append("sub2api or x-sub2api-common")
    if not found_worker:
        missing.append(WORKER_SECTION)
    if missing:
        names = ", ".join(missing)
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
