import copy
import tempfile
import unittest
from pathlib import Path
from unittest.mock import patch

import controller as c
import resource_agent
import adaptive_rollout
from test_controller import FakeAPI, csv_bytes, host


class AdaptiveImporterTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        root = Path(self.tmp.name)
        source = root / 'shops.csv'
        source.write_bytes(csv_bytes(3))
        self.api = FakeAPI()
        with patch.object(c.API, 'shops', return_value=[]):
            c.preview(source, self.api.target, 'test', root / 'run', 3)
        self.runner = c.Controller(root / 'run', self.api)
        self.api.config['mode'] = 'adaptive'

    def test_backend_wait_is_not_failure_and_importer_never_writes_frequency(self):
        original = self.api.control
        with patch.object(self.api, 'control', side_effect=lambda: {**original(), 'adaptive': {'admission_hold': 'budget waiting', 'multiplier': 4}}):
            self.runner.tick(host(self.api.now))
        self.assertEqual(self.api.registered, [])
        self.runner.tick(host(self.api.now + 60))
        self.assertEqual(len(self.api.registered), 1)
        self.assertTrue(all(path == '/shops' for _, path, _ in self.api.calls))
        self.assertEqual(self.runner.state['phase'], 'adaptive')

    def test_uncertain_post_is_reconciled_without_duplicate_or_quarantine(self):
        self.api.fail_after_create = True
        self.runner.tick(host(self.api.now))
        self.runner.tick(host(self.api.now + 60))
        ids = [row['id'] for row in self.api.registered]
        self.assertEqual(len(ids), len(set(ids)))
        self.assertEqual(self.api.config['quarantined_shops'], {})
        self.assertTrue(all(path != '/products/sync-control' for _, path, _ in self.api.calls))

    def test_host_payload_uses_cpu_delta_and_observed_resource_values(self):
        before = host(100)
        after = host(160, memory=.94, pressure=True)
        after['worker_id'] = 'worker'
        result = resource_agent.payload(after, before)
        self.assertAlmostEqual(result['cpu_ratio'], .1)
        self.assertEqual(result['worker_memory_ratio'], .94)
        self.assertEqual(result['upstream_events'], 1)
        self.assertNotIn('automatic_paused', result)

    def test_handoff_preserves_manual_isolation_and_only_restores_owned_pressure_rows(self):
        control = {'quarantined_shops': {'a': 'pressure', 'b': 'manual quarantine', 'c': 'manual override'}}
        state = {'shops': [{'platform_id': key, 'shop_id': key, 'status': 'pressure_paused', 'isolation_reason': 'pressure'} for key in 'abc'],
                 'batches': [{'number': 7, 'platform_ids': list('abc')}]}
        result = adaptive_rollout.handoff(control, state)
        self.assertEqual(result['pressure_paused_shops'], {'a': 'pressure'})
        self.assertEqual(result['quarantined_shops'], {'b': 'manual quarantine', 'c': 'manual override'})
        self.assertEqual(result['imported_batches'], {'a': 7, 'b': 7, 'c': 7})

    def test_acceptance_requires_quote_coverage_and_a_full_stable_cycle(self):
        now = 100000
        data = {'control': {'mode': 'adaptive', 'quarantined_shops': {}, 'automatic_paused': False},
                'adaptive': {'validated_since': c.iso(now - 7200), 'required_multiplier': 1},
                'shops': [{'shop_id': 'a', 'adaptive': {'first_catalog_at': c.iso(now - 7200), 'effective_count': 1, 'quote_seconds': 3600}}]}
        self.assertFalse(adaptive_rollout.summarize(data, now)['full_cycle_verified'])
        data['shops'][0]['adaptive']['first_quote_at'] = c.iso(now - 6000)
        self.assertTrue(adaptive_rollout.summarize(data, now)['full_cycle_verified'])
        data['adaptive']['validated_since'] = c.iso(now - 600)
        self.assertFalse(adaptive_rollout.summarize(data, now)['full_cycle_verified'])


if __name__ == '__main__':
    unittest.main()
