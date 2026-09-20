#!/usr/bin/env python3
"""Compatibility entry point for the Mihomo Codex pool discovery utility."""

try:
    from tools.discover_codex_ticket_pool import main
except ModuleNotFoundError:  # execution as ``python tools/...`` from the repo root
    from discover_codex_ticket_pool import main


if __name__ == "__main__":
    raise SystemExit(main())
