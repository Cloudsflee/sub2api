#!/usr/bin/env python3
"""Discover healthy Mihomo listeners for the Codex 292 harvest pool.

The script is intentionally independent of the product-sync worker. It reads
the active Mihomo configuration, validates listener/node structure without
printing credentials, probes each listener through its own route, and can
write the selected URLs through the Sub2API admin settings endpoint. A
multi-entry write always sets the business-proxy linkage switch to false;
the application coordinator applies the same rule transactionally.
"""

from __future__ import annotations

import argparse
import hashlib
import ipaddress
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import urllib.error
import urllib.parse
import urllib.request
from dataclasses import dataclass, asdict
from pathlib import Path
from typing import Any, Iterable

try:
    import yaml
except ImportError:  # pragma: no cover - exercised only on minimal hosts
    yaml = None


DEFAULT_CONFIG = "/etc/sub2api-product-proxy/config.yaml"
DEFAULT_NETWORKS = ("172.18.0.0/16",)
DEFAULT_EGRESS_URL = "https://api.ipify.org?format=json"
DEFAULT_HEALTH_URL = "https://chatgpt.com/cdn-cgi/trace"
MAX_POOL_ENTRIES = 256
PUBLIC_IP_RE = re.compile(r"(?<![0-9A-Fa-f:.])([0-9A-Fa-f:]{3,45})(?![0-9A-Fa-f:.])")


class DiscoveryError(RuntimeError):
    """A user-actionable discovery failure with no secret-bearing detail."""


class ProbeFailure(DiscoveryError):
    """A probe failure retaining only safe numeric HTTP status fields."""

    def __init__(self, message: str, http_status: int = 0, health_status: int = 0):
        super().__init__(message)
        self.http_status = http_status
        self.health_status = health_status


def _hash(prefix: str, value: str) -> str:
    digest = hashlib.sha256(value.encode("utf-8")).hexdigest()[:16]
    return f"{prefix}-{digest}"


def _mask_url(value: str) -> str:
    try:
        parsed = urllib.parse.urlsplit(value)
        if parsed.username is None:
            return value
        host = parsed.hostname or ""
        if ":" in host:
            host = f"[{host}]"
        if parsed.port:
            host = f"{host}:{parsed.port}"
        user = urllib.parse.quote(parsed.username, safe="")
        netloc = f"{user}:***@{host}"
        return urllib.parse.urlunsplit((parsed.scheme, netloc, parsed.path, parsed.query, parsed.fragment))
    except (TypeError, ValueError):
        return "<invalid-url>"


def _split_env(value: str | None, default: Iterable[str]) -> list[str]:
    if not value:
        return list(default)
    return [item.strip() for item in re.split(r"[,;\s]+", value) if item.strip()]


def _load_config(path: Path) -> dict[str, Any]:
    try:
        raw = path.read_text(encoding="utf-8")
    except OSError as exc:
        raise DiscoveryError("cannot read Mihomo config") from exc
    if yaml is not None:
        try:
            document = yaml.safe_load(raw)
        except Exception as exc:  # PyYAML errors can echo scalar values
            raise DiscoveryError("Mihomo config is not valid YAML") from exc
    else:
        try:
            document = json.loads(raw)
        except Exception as exc:
            raise DiscoveryError("PyYAML is required to read the Mihomo config") from exc
    if not isinstance(document, dict):
        raise DiscoveryError("Mihomo config root must be a mapping")
    return document


def _as_listener_items(value: Any) -> list[tuple[str, dict[str, Any]]]:
    if isinstance(value, list):
        result: list[tuple[str, dict[str, Any]]] = []
        for index, item in enumerate(value, 1):
            if isinstance(item, dict):
                result.append((str(item.get("name") or f"listener-{index}"), item))
        return result
    if isinstance(value, dict):
        result = []
        for name, item in value.items():
            if isinstance(item, dict):
                copy = dict(item)
                copy.setdefault("name", str(name))
                result.append((str(name), copy))
        return result
    return []


def _as_proxy_index(value: Any) -> dict[str, dict[str, Any]]:
    result: dict[str, dict[str, Any]] = {}
    if isinstance(value, list):
        for item in value:
            if isinstance(item, dict) and item.get("name"):
                result[str(item["name"])] = item
    elif isinstance(value, dict):
        for name, item in value.items():
            if isinstance(item, dict):
                copy = dict(item)
                copy.setdefault("name", str(name))
                result[str(name)] = copy
    return result


def _as_group_index(value: Any) -> dict[str, dict[str, Any]]:
    groups: dict[str, dict[str, Any]] = {}
    if isinstance(value, list):
        for item in value:
            if isinstance(item, dict) and item.get("name"):
                groups[str(item["name"])] = item
    elif isinstance(value, dict):
        for name, item in value.items():
            if isinstance(item, dict):
                copy = dict(item)
                copy.setdefault("name", str(name))
                groups[str(name)] = copy
    return groups


def _listener_host_port(listener: dict[str, Any]) -> tuple[str, int]:
    listen = listener.get("listen", listener.get("bind-address", listener.get("bind")))
    port = listener.get("port")
    if isinstance(listen, (list, tuple)) and len(listen) == 2:
        listen, port = listen
    if listen is None:
        raise DiscoveryError("listener has no listen address")
    host = str(listen).strip()
    if not host:
        raise DiscoveryError("listener has an empty listen address")
    if port is None:
        # Accept the compact address form used by a few hand-written configs.
        # Bracketed IPv6 addresses remain unambiguous.
        if host.startswith("[") and "]" in host:
            address, separator, suffix = host.partition("]")
            if separator and suffix.startswith(":"):
                host, port = address + "]", suffix[1:]
        elif host.count(":") == 1:
            host, port = host.rsplit(":", 1)
    try:
        port_number = int(port)
    except (TypeError, ValueError) as exc:
        raise DiscoveryError("listener port is invalid") from exc
    if not 1 <= port_number <= 65535:
        raise DiscoveryError("listener port is outside 1..65535")
    return host, port_number


def _node_is_complete(node: dict[str, Any]) -> bool:
    server = str(node.get("server") or "").strip()
    try:
        port = int(node.get("port"))
    except (TypeError, ValueError):
        return False
    if not server or not 1 <= port <= 65535:
        return False
    kind = str(node.get("type") or node.get("protocol") or "").lower()
    if kind in {"ss", "shadowsocks"}:
        return bool(str(node.get("cipher") or "").strip() and str(node.get("password") or "").strip())
    if kind in {"vmess", "vless", "tuic", "hysteria", "hysteria2", "wireguard"}:
        return bool(str(node.get("uuid") or node.get("password") or node.get("private-key") or "").strip())
    if kind in {"trojan"}:
        return bool(str(node.get("password") or "").strip())
    if kind in {"http", "https", "socks4", "socks5", "socks"}:
        return True
    if not kind:
        # A few generated Mihomo files omit the type for plain HTTP nodes.
        # Server/port is the complete connection identity in that form.
        return True
    # Unknown Mihomo node types still need a server/port and one credential
    # marker. This avoids selecting a half-rendered subscription entry.
    return bool(str(node.get("uuid") or node.get("password") or node.get("cipher") or "").strip())


@dataclass
class Candidate:
    name: str
    listener_url: str
    entry_id: str
    egress_id: str = ""
    health: str = "unprobed"
    http_status: int = 0
    health_status: int = 0
    selected: bool = False
    reason: str = ""

    def public(self) -> dict[str, Any]:
        data = asdict(self)
        data["listener_url"] = _mask_url(data["listener_url"])
        return data


def _public_ip_from_body(body: bytes) -> str:
    text = body.decode("utf-8", errors="replace").strip()
    try:
        value = json.loads(text)
        if isinstance(value, dict):
            for key in ("ip", "address", "origin"):
                if value.get(key):
                    text = str(value[key]).split(",", 1)[0].strip()
                    break
    except (TypeError, ValueError):
        pass
    if "=" in text:
        for line in text.splitlines():
            if line.startswith("ip="):
                text = line.split("=", 1)[1].strip()
                break
    try:
        address = ipaddress.ip_address(text)
    except ValueError:
        match = PUBLIC_IP_RE.search(text)
        if not match:
            raise DiscoveryError("egress probe did not return an IP address")
        try:
            address = ipaddress.ip_address(match.group(1))
        except ValueError as exc:
            raise DiscoveryError("egress probe returned an invalid IP address") from exc
    if not address.is_global:
        raise DiscoveryError("egress probe returned a non-public address")
    return str(address)


def _urllib_probe(proxy_url: str, target_url: str, timeout: float) -> tuple[int, bytes]:
    handler = urllib.request.ProxyHandler({"http": proxy_url, "https": proxy_url})
    opener = urllib.request.build_opener(handler)
    request = urllib.request.Request(
        target_url,
        headers={"Cache-Control": "no-cache", "User-Agent": "sub2api-codex-pool/1"},
    )
    try:
        with opener.open(request, timeout=timeout) as response:
            return int(response.status), response.read(128 * 1024)
    except urllib.error.HTTPError as exc:
        try:
            body = exc.read(128 * 1024)
        except OSError:
            body = b""
        return int(exc.code), body
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        raise DiscoveryError("proxy probe transport failed") from exc


def _curl_probe(proxy_url: str, target_url: str, timeout: float) -> tuple[int, bytes]:
    curl = shutil.which("curl")
    if not curl:
        raise DiscoveryError("SOCKS listener requires curl for probing")
    command = [
        curl,
        "--silent",
        "--show-error",
        "--location",
        "--max-time",
        str(max(1, int(timeout))),
        "--proxy",
        proxy_url,
        "--user-agent",
        "sub2api-codex-pool/1",
        "--dump-header",
        "-",
        target_url,
    ]
    try:
        completed = subprocess.run(command, check=False, capture_output=True, timeout=timeout + 2)
    except (OSError, subprocess.TimeoutExpired) as exc:
        raise DiscoveryError("proxy probe transport failed") from exc
    if completed.returncode != 0:
        raise DiscoveryError("proxy probe transport failed")
    raw = completed.stdout
    status_matches = list(re.finditer(rb"HTTP/\d(?:\.\d)?\s+(\d{3})", raw))
    if not status_matches:
        raise DiscoveryError("proxy probe returned no HTTP status")
    marker = b"\r\n\r\n"
    body_offset = raw.rfind(marker)
    body = raw[body_offset + len(marker) :] if body_offset >= 0 else b""
    return int(status_matches[-1].group(1)), body[: 128 * 1024]


def _probe(proxy_url: str, egress_url: str, health_url: str, timeout: float) -> tuple[int, int, str]:
    parsed = urllib.parse.urlsplit(proxy_url)
    probe = _curl_probe if parsed.scheme in {"socks", "socks5", "socks5h"} else _urllib_probe
    egress_status, body = probe(proxy_url, egress_url, timeout)
    if egress_status != 200:
        raise ProbeFailure("egress probe returned a non-success status", http_status=egress_status)
    try:
        ip_value = _public_ip_from_body(body)
    except DiscoveryError as exc:
        raise ProbeFailure(str(exc), http_status=egress_status) from exc
    health_status, _ = probe(proxy_url, health_url, timeout)
    if health_status != 200:
        raise ProbeFailure("health probe returned a non-success status", http_status=egress_status, health_status=health_status)
    return egress_status, health_status, _hash("e", ip_value)


def _candidate_list(document: dict[str, Any], networks: list[ipaddress._BaseNetwork], app_address: str, allow_wildcard: bool) -> list[Candidate]:
    proxies = _as_proxy_index(document.get("proxies"))
    groups = _as_group_index(document.get("proxy-groups"))
    candidates: list[Candidate] = []
    for name, listener in _as_listener_items(document.get("listeners")):
        listener_type = str(listener.get("type") or "").lower()
        if listener_type not in {"http", "mixed", "socks", "socks4", "socks5"}:
            continue
        fallback_id = _hash("l", f"{name}:{listener_type}")
        try:
            host, port = _listener_host_port(listener)
        except DiscoveryError as exc:
            candidates.append(Candidate(name=name, listener_url="", entry_id=fallback_id, health="invalid_listener", reason=str(exc)))
            continue
        if host in {"0.0.0.0", "::", "[::]"}:
            if not allow_wildcard:
                candidates.append(Candidate(name=name, listener_url="", entry_id=fallback_id, health="wildcard_listener", reason="wildcard bind is not an application address"))
                continue
            host = app_address
        try:
            address = ipaddress.ip_address(host.strip("[]"))
        except ValueError:
            candidates.append(Candidate(name=name, listener_url="", entry_id=fallback_id, health="invalid_listener", reason="listen address is not an IP address"))
            continue
        if not any(address in network for network in networks):
            candidates.append(Candidate(name=name, listener_url="", entry_id=fallback_id, health="outside_network", reason="listen address is outside the application network"))
            continue
        proxy_name = listener.get("proxy") or listener.get("proxy-name") or listener.get("proxy_name")
        node = proxy_name if isinstance(proxy_name, dict) else proxies.get(str(proxy_name))
        if node is None and str(proxy_name) in groups:
            group = groups[str(proxy_name)]
            members = group.get("proxies")
            if isinstance(members, list) and members:
                member_nodes = [proxies.get(str(member)) for member in members]
                if all(isinstance(member, dict) and _node_is_complete(member) for member in member_nodes):
                    # The listener is validated through the group endpoint;
                    # retaining one complete member here proves the group is
                    # backed by concrete node configuration without exposing
                    # any member credentials.
                    node = member_nodes[0]
            if node is None:
                candidates.append(Candidate(name=name, listener_url="", entry_id=fallback_id, health="incomplete_node", reason="listener proxy group has no complete direct nodes"))
                continue
        if not isinstance(node, dict) or not _node_is_complete(node):
            candidates.append(Candidate(name=name, listener_url="", entry_id=fallback_id, health="incomplete_node", reason="listener node is incomplete"))
            continue
        scheme = "socks5h" if listener_type in {"socks", "socks4", "socks5"} else "http"
        url_host = host.strip("[]")
        if ":" in url_host:
            url_host = f"[{url_host}]"
        listener_url = f"{scheme}://{url_host}:{port}"
        candidates.append(
            Candidate(
                name=name,
                listener_url=listener_url,
                entry_id=_hash("l", listener_url),
            )
        )
    return candidates


def _api_endpoint(raw: str) -> str:
    value = raw.rstrip("/")
    parsed = urllib.parse.urlsplit(value)
    if not parsed.scheme or not parsed.netloc:
        raise DiscoveryError("settings API URL must include a scheme and host")
    if parsed.path.endswith("/admin/settings"):
        return value
    if parsed.path.endswith("/api/v1"):
        return value + "/admin/settings"
    return value + "/api/v1/admin/settings"


def _api_request(endpoint: str, method: str, token: str, payload: dict[str, Any] | None = None) -> Any:
    body = None if payload is None else json.dumps(payload, separators=(",", ":")).encode("utf-8")
    headers = {"Accept": "application/json"}
    if token:
        headers["Authorization"] = f"Bearer {token}"
    if body is not None:
        headers["Content-Type"] = "application/json"
    request = urllib.request.Request(endpoint, data=body, headers=headers, method=method)
    try:
        with urllib.request.urlopen(request, timeout=15) as response:
            raw = response.read(256 * 1024)
    except (urllib.error.URLError, TimeoutError, OSError) as exc:
        raise DiscoveryError("settings API request failed") from exc
    try:
        return json.loads(raw.decode("utf-8")) if raw else {}
    except (TypeError, ValueError) as exc:
        raise DiscoveryError("settings API returned invalid JSON") from exc


def _unwrap_settings(value: Any) -> dict[str, Any]:
    if isinstance(value, dict) and isinstance(value.get("data"), dict):
        data = value["data"]
        if isinstance(data.get("settings"), dict):
            return data["settings"]
        return data
    return value if isinstance(value, dict) else {}


def _write_settings(endpoint: str, token: str, urls: list[str]) -> dict[str, Any]:
    payload = {
        "openai_codex_ticket_harvest_proxy_url": "\n".join(urls),
    }
    if len(urls) > 1:
        # The application coordinator repeats this invariant in the database
        # transaction; sending it here makes the operator intent explicit.
        payload["openai_codex_ticket_sync_business_proxy"] = False
    _api_request(endpoint, "PUT", token, payload)
    observed = _unwrap_settings(_api_request(endpoint, "GET", token))
    observed_pool = str(observed.get("openai_codex_ticket_harvest_proxy_url") or "")
    observed_count = len([line for line in observed_pool.splitlines() if line.strip()])
    linkage_ok = len(urls) <= 1 or observed.get("openai_codex_ticket_sync_business_proxy") is False
    if observed_count != len(urls) or not linkage_ok:
        raise DiscoveryError("settings API verification did not match the selected pool")
    return {"endpoint": endpoint, "entry_count": len(urls), "harvest_only": len(urls) > 1, "verified": True}


def discover(args: argparse.Namespace) -> dict[str, Any]:
    if not 1 <= args.max_entries <= MAX_POOL_ENTRIES:
        raise DiscoveryError(f"max entries must be between 1 and {MAX_POOL_ENTRIES}")
    try:
        networks = [ipaddress.ip_network(value, strict=False) for value in args.network]
        app_address = str(ipaddress.ip_address(args.app_address))
    except ValueError as exc:
        raise DiscoveryError("application network or address is invalid") from exc
    document = _load_config(Path(args.config))
    candidates = _candidate_list(document, networks, app_address, args.allow_wildcard)
    selected: list[Candidate] = []
    seen_egress: set[str] = set()
    for candidate in candidates:
        if candidate.health != "unprobed":
            continue
        if len(selected) >= args.max_entries:
            candidate.health = "pool_limit"
            candidate.reason = "pool limit reached"
            continue
        if args.no_probe:
            candidate.health = "unprobed"
            candidate.reason = "probe disabled"
            continue
        try:
            status, health_status, egress_id = _probe(candidate.listener_url, args.egress_url, args.health_url, args.timeout)
            candidate.http_status = status
            candidate.health_status = health_status
            candidate.egress_id = egress_id
        except DiscoveryError as exc:
            candidate.health = "unhealthy"
            candidate.reason = str(exc)
            if isinstance(exc, ProbeFailure):
                candidate.http_status = exc.http_status
                candidate.health_status = exc.health_status
            continue
        if candidate.egress_id in seen_egress and not args.allow_duplicate_egress:
            candidate.health = "duplicate_egress"
            candidate.reason = "egress identity already selected"
            continue
        candidate.health = "healthy"
        candidate.selected = True
        selected.append(candidate)
        seen_egress.add(candidate.egress_id)

    if args.write and args.no_probe:
        raise DiscoveryError("--write requires live probes; use --no-probe only for inspection")
    if args.write and not selected:
        raise DiscoveryError("no healthy listener qualified for the harvest pool")
    write_result: dict[str, Any] = {"requested": bool(args.write), "verified": False}
    if args.write:
        endpoint = _api_endpoint(args.api_url)
        token = args.api_token or os.environ.get("SUB2API_ADMIN_TOKEN", "")
        write_result = _write_settings(endpoint, token, [item.listener_url for item in selected])
        write_result["requested"] = True
    return {
        "config": args.config,
        "application_networks": [str(network) for network in networks],
        "pool_size": len(selected),
        "proxy_urls": [_mask_url(item.listener_url) for item in selected],
        "entries": [item.public() for item in candidates],
        "write": write_result,
    }


def _arguments(argv: list[str]) -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--config", default=os.environ.get("CODEX_MIHOMO_CONFIG", DEFAULT_CONFIG))
    parser.add_argument("--network", action="append", default=None, help="application CIDR; repeatable")
    parser.add_argument("--app-address", default=os.environ.get("CODEX_APP_ADDRESS", "172.18.0.1"))
    parser.add_argument("--allow-wildcard", action="store_true")
    parser.add_argument("--egress-url", default=os.environ.get("CODEX_EGRESS_URL", DEFAULT_EGRESS_URL))
    parser.add_argument("--health-url", default=os.environ.get("CODEX_HEALTH_URL", DEFAULT_HEALTH_URL))
    parser.add_argument("--timeout", type=float, default=12.0)
    parser.add_argument("--max-entries", type=int, default=MAX_POOL_ENTRIES)
    parser.add_argument("--allow-duplicate-egress", action="store_true")
    parser.add_argument("--no-probe", action="store_true", help="inspect listener structure without network probes")
    parser.add_argument("--write", action="store_true", help="write selected URLs through the admin settings API")
    parser.add_argument("--api-url", default=os.environ.get("SUB2API_ADMIN_URL", "http://127.0.0.1:8080"))
    parser.add_argument("--api-token", default="")
    parser.add_argument("--output", help="write the redacted JSON report atomically")
    args = parser.parse_args(argv)
    args.network = args.network or _split_env(os.environ.get("CODEX_APP_NETWORKS"), DEFAULT_NETWORKS)
    return args


def main(argv: list[str] | None = None) -> int:
    parsed = _arguments(sys.argv[1:] if argv is None else argv)
    try:
        report = discover(parsed)
    except DiscoveryError as exc:
        print(f"codex-ticket-pool: {exc}", file=sys.stderr)
        return 2
    rendered = json.dumps(report, ensure_ascii=True, indent=2, sort_keys=True) + "\n"
    if parsed.output:
        target = Path(parsed.output)
        target.parent.mkdir(parents=True, exist_ok=True)
        fd, temporary = tempfile.mkstemp(prefix=f".{target.name}.", dir=str(target.parent))
        try:
            with os.fdopen(fd, "w", encoding="utf-8") as handle:
                handle.write(rendered)
                handle.flush()
                os.fchmod(handle.fileno(), 0o600)
            os.replace(temporary, target)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)
    print(rendered, end="")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
