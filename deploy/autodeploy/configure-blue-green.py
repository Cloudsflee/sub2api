#!/usr/bin/env python3
"""Convert a legacy single-app Compose file to managed blue/green slots."""

from __future__ import annotations

import os
import re
import shutil
import sys
import tempfile
from pathlib import Path


SERVICE_RE = re.compile(r"^  ([A-Za-z0-9_-]+):\s*(?:#.*)?(?:\r?\n)?$")


def leading_spaces(line: str) -> int:
    return len(line) - len(line.lstrip(" "))


def service_ranges(lines: list[str]) -> dict[str, tuple[int, int]]:
    try:
        services_index = next(
            index for index, line in enumerate(lines) if line.strip() == "services:"
        )
    except StopIteration as error:
        raise RuntimeError("Compose file has no services section") from error

    starts: list[tuple[str, int]] = []
    section_end = len(lines)
    for index in range(services_index + 1, len(lines)):
        line = lines[index]
        if line.strip() and not line.startswith((" ", "#")):
            section_end = index
            break
        match = SERVICE_RE.match(line)
        if match:
            starts.append((match.group(1), index))

    ranges: dict[str, tuple[int, int]] = {}
    for position, (name, start) in enumerate(starts):
        end = starts[position + 1][1] if position + 1 < len(starts) else section_end
        ranges[name] = (start, end)
    return ranges


def remove_mapping_keys(
    block: list[str], indent: int, names: set[str]
) -> list[str]:
    output: list[str] = []
    index = 0
    key_pattern = re.compile(
        rf"^ {{{indent}}}({'|'.join(re.escape(name) for name in names)}):"
    )
    while index < len(block):
        line = block[index]
        if not key_pattern.match(line):
            output.append(line)
            index += 1
            continue
        index += 1
        while index < len(block):
            candidate = block[index]
            if candidate.strip() and leading_spaces(candidate) <= indent:
                break
            index += 1
    return output


def parameterized_image(line: str, variable: str) -> str:
    match = re.match(r"^(\s*image:\s*)([^#\r\n]+?)(\s*(?:#.*)?\r?\n?)$", line)
    if not match:
        return line
    value = match.group(2).strip()
    if "${" + variable in value:
        return line
    if value.startswith(("'", '"')) and value.endswith(value[0]):
        value = value[1:-1]
    return f"{match.group(1)}${{{variable}:-{value}}}{match.group(3)}"


def ensure_common_environment(common: list[str]) -> list[str]:
    trusted = (
        "    - SERVER_TRUSTED_PROXIES="
        "${SERVER_TRUSTED_PROXIES:-172.18.0.1/32}\n"
    )
    for index, line in enumerate(common):
        if re.match(r"^\s*-\s*SERVER_TRUSTED_PROXIES=", line):
            common[index] = trusted
            return common

    environment_index = next(
        (index for index, line in enumerate(common) if line.startswith("  environment:")),
        None,
    )
    if environment_index is None:
        raise RuntimeError("sub2api service has no environment mapping")

    insert_at = environment_index + 1
    for index in range(environment_index + 1, len(common)):
        line = common[index]
        if line.strip() and leading_spaces(line) <= 2:
            break
        insert_at = index + 1
        if re.match(r"^\s*-\s*SERVER_PORT=", line):
            insert_at = index + 1
            break
    common.insert(insert_at, trusted)
    return common


def build_common(app_block: list[str]) -> list[str]:
    body = remove_mapping_keys(
        app_block[1:], indent=4, names={"container_name", "ports", "profiles"}
    )
    common = ["x-sub2api-common: &sub2api-common\n"]
    for line in body:
        common.append(line[2:] if line.startswith("  ") else line)
    common = ensure_common_environment(common)
    for index, line in enumerate(common):
        if re.match(r"^  image:", line):
            common[index] = parameterized_image(line, "SUB2API_IMAGE")
            break
    else:
        raise RuntimeError("sub2api service has no image")
    return common


def slot_services() -> list[str]:
    return [
        "  sub2api-blue:\n",
        "    <<: *sub2api-common\n",
        "    container_name: sub2api-blue\n",
        "    ports:\n",
        '      - "127.0.0.1:${SUB2API_BLUE_PORT:-18080}:8080"\n',
        "\n",
        "  sub2api-green:\n",
        "    <<: *sub2api-common\n",
        "    container_name: sub2api-green\n",
        '    profiles: ["standby"]\n',
        "    ports:\n",
        '      - "127.0.0.1:${SUB2API_GREEN_PORT:-18081}:8080"\n',
    ]


def transform_worker(block: list[str]) -> list[str]:
    output: list[str] = []
    index = 0
    has_backend_url = False

    while index < len(block):
        line = block[index]
        if re.match(r"^\s*-\s*BACKEND_URL=", line):
            output.append("      - BACKEND_URL=http://host.docker.internal:8080\n")
            has_backend_url = True
            index += 1
            continue
        if line.startswith("    depends_on:"):
            index += 1
            while index < len(block):
                candidate = block[index]
                if candidate.strip() and leading_spaces(candidate) <= 4:
                    break
                index += 1
            continue
        if re.match(r"^    image:", line):
            line = parameterized_image(line, "PRODUCT_SYNC_WORKER_IMAGE")
        output.append(line)
        index += 1

    if not has_backend_url:
        raise RuntimeError("product-sync-worker has no BACKEND_URL")

    extra_hosts_index = next(
        (index for index, line in enumerate(output) if line.startswith("    extra_hosts:")),
        None,
    )
    host_gateway_pattern = re.compile(
        r"host\.docker\.internal\s*:\s*host-gateway", re.IGNORECASE
    )
    extra_hosts_end = extra_hosts_index + 1 if extra_hosts_index is not None else None
    if extra_hosts_index is not None:
        while extra_hosts_end < len(output):
            candidate = output[extra_hosts_end]
            if candidate.strip() and leading_spaces(candidate) <= 4:
                break
            extra_hosts_end += 1
    if extra_hosts_index is not None and any(
        host_gateway_pattern.search(line)
        for line in output[extra_hosts_index:extra_hosts_end]
    ):
        return output

    if extra_hosts_index is None:
        insertion = next(
            (
                index
                for index, line in enumerate(output)
                if line.startswith("    networks:")
            ),
            len(output),
        )
        output[insertion:insertion] = [
            "    extra_hosts:\n",
            '      - "host.docker.internal:host-gateway"\n',
        ]
        return output

    inline = re.match(
        r'^(    extra_hosts:\s*)(\[.*\])(\s*(?:#.*)?\r?\n?)$',
        output[extra_hosts_index],
    )
    if inline:
        values = inline.group(2)
        closing = values.rfind("]")
        contents = values[1:closing].strip()
        addition = '"host.docker.internal:host-gateway"'
        values = "[" + (contents + ", " if contents else "") + addition + "]"
        output[extra_hosts_index] = (
            inline.group(1) + values + inline.group(3)
        )
        return output

    insertion = extra_hosts_end
    children = output[extra_hosts_index + 1 : insertion]
    if any(re.match(r"^\s*-", child) for child in children if child.strip()):
        entry = '      - "host.docker.internal:host-gateway"\n'
    else:
        entry = "      host.docker.internal: host-gateway\n"
    output.insert(insertion, entry)
    return output


def atomic_write(path: Path, lines: list[str]) -> None:
    descriptor, temporary_name = tempfile.mkstemp(
        prefix=".compose-blue-green-", dir=path.parent, text=True
    )
    temporary = Path(temporary_name)
    try:
        with os.fdopen(descriptor, "w", encoding="utf-8", newline="") as handle:
            handle.writelines(lines)
        shutil.copymode(path, temporary)
        os.replace(temporary, path)
    finally:
        temporary.unlink(missing_ok=True)


def transform(path: Path, require_worker: bool = False) -> None:
    with path.open("r", encoding="utf-8", newline="") as handle:
        lines = handle.readlines()

    ranges = service_ranges(lines)
    already_blue_green = "sub2api-blue" in ranges and "sub2api-green" in ranges
    if already_blue_green:
        if not any(line.startswith("x-sub2api-common:") for line in lines):
            raise RuntimeError("blue/green services do not use x-sub2api-common")
    else:
        if "sub2api" not in ranges:
            raise RuntimeError("legacy sub2api service not found")
        start, end = ranges["sub2api"]
        common = build_common(lines[start:end])
        services_index = next(
            index for index, line in enumerate(lines) if line.strip() == "services:"
        )
        lines[start:end] = slot_services()
        lines[services_index:services_index] = common + ["\n"]

    ranges = service_ranges(lines)
    if "product-sync-worker" in ranges:
        start, end = ranges["product-sync-worker"]
        lines[start:end] = transform_worker(lines[start:end])
    elif require_worker:
        raise RuntimeError("product-sync-worker service not found")

    atomic_write(path, lines)


def main() -> int:
    args = sys.argv[1:]
    require_worker = False
    if args and args[0] == "--require-worker":
        require_worker = True
        args.pop(0)
    if len(args) != 1:
        print(
            f"usage: {Path(sys.argv[0]).name} [--require-worker] COMPOSE_FILE",
            file=sys.stderr,
        )
        return 2
    transform(Path(args[0]).resolve(), require_worker=require_worker)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
