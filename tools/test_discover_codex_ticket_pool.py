import json
import tempfile
import unittest
from pathlib import Path
from unittest import mock

from tools import discover_codex_ticket_pool as pool


class DiscoverCodexTicketPoolTests(unittest.TestCase):
    def write_config(self, text: str) -> Path:
        handle = tempfile.NamedTemporaryFile("w", suffix=".yaml", delete=False, encoding="utf-8")
        handle.write(text)
        handle.close()
        self.addCleanup(lambda: Path(handle.name).unlink(missing_ok=True))
        return Path(handle.name)

    def test_filters_network_and_incomplete_nodes(self):
        config = self.write_config(
            """
listeners:
  - name: good
    type: mixed
    listen: 172.18.0.1
    port: 17891
    proxy: node-a
  - name: outside
    type: mixed
    listen: 10.0.0.1
    port: 17892
    proxy: node-a
  - name: incomplete
    type: mixed
    listen: 172.18.0.1
    port: 17893
    proxy: node-b
proxies:
  - name: node-a
    type: socks5
    server: 198.51.100.10
    port: 443
  - name: node-b
    type: ss
    server: 198.51.100.11
    port: 443
"""
        )
        document = pool._load_config(config)
        candidates = pool._candidate_list(
            document,
            [pool.ipaddress.ip_network("172.18.0.0/16")],
            "172.18.0.1",
            False,
        )
        self.assertEqual(["good", "outside", "incomplete"], [candidate.name for candidate in candidates])
        self.assertEqual("unprobed", candidates[0].health)
        self.assertEqual("outside_network", candidates[1].health)
        self.assertEqual("incomplete_node", candidates[2].health)

    @mock.patch.object(pool, "_probe")
    def test_unique_egress_and_redacted_report(self, probe):
        config = self.write_config(
            """
listeners:
  - {name: one, type: mixed, listen: 172.18.0.1, port: 17891, proxy: a}
  - {name: two, type: mixed, listen: 172.18.0.1, port: 17892, proxy: b}
proxies:
  - {name: a, type: socks5, server: 198.51.100.10, port: 443}
  - {name: b, type: socks5, server: 198.51.100.11, port: 443}
"""
        )
        probe.side_effect = [(200, 200, "e-same"), (200, 200, "e-same")]
        args = pool._arguments(
            [
                "--config",
                str(config),
                "--network",
                "172.18.0.0/16",
                "--egress-url",
                "http://egress.test",
                "--health-url",
                "http://health.test",
            ]
        )
        report = pool.discover(args)
        self.assertEqual(1, report["pool_size"])
        self.assertEqual("duplicate_egress", report["entries"][1]["health"])
        self.assertNotIn("password", json.dumps(report))

    def test_mask_url_hides_userinfo(self):
        self.assertEqual(
            "http://user:***@listener.example:8080",
            pool._mask_url("http://user:secret@listener.example:8080"),
        )

    @mock.patch.object(pool, "_api_request")
    def test_single_entry_write_does_not_toggle_business_linkage(self, request):
        request.side_effect = [{}, {"data": {"openai_codex_ticket_harvest_proxy_url": "http://listener.example:17891", "openai_codex_ticket_sync_business_proxy": True}}]
        result = pool._write_settings("http://127.0.0.1:8080/api/v1/admin/settings", "TOKEN", ["http://listener.example:17891"])
        payload = request.call_args_list[0].args[3]
        self.assertNotIn("openai_codex_ticket_sync_business_proxy", payload)
        self.assertFalse(result["harvest_only"])

    @mock.patch.object(pool, "_api_request")
    def test_multi_entry_write_forces_harvest_only(self, request):
        request.side_effect = [{}, {"data": {"openai_codex_ticket_harvest_proxy_url": "a\nb", "openai_codex_ticket_sync_business_proxy": False}}]
        result = pool._write_settings("http://127.0.0.1:8080/api/v1/admin/settings", "TOKEN", ["a", "b"])
        payload = request.call_args_list[0].args[3]
        self.assertFalse(payload["openai_codex_ticket_sync_business_proxy"])
        self.assertTrue(result["harvest_only"])


if __name__ == "__main__":
    unittest.main()
