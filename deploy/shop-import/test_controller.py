import copy
import csv
import io
import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import controller as c


def csv_bytes(count=9):
    out = io.StringIO()
    writer = csv.DictWriter(out, fieldnames=['id', 'url', 'status', 'name', 'ai', 'count'])
    writer.writeheader()
    for n in range(count):
        writer.writerow(dict(id=f'ID{n}', url=f'https://wzyp.cn/shop/ID{n}', status='已显示店铺',
                             name=f'Shop {n}', ai='AI' if n < 7 else '', count='broken 9e100'))
    return out.getvalue().encode('utf-8-sig')


class FakeAPI:
    target = 'http://127.0.0.1:8080'

    def __init__(self):
        self.config = {'version': 1, 'revision': 0, 'interval_multiplier': 1,
                       'automatic_paused': False, 'quarantined_shops': {}}
        self.registered = []
        self.stats = {}
        self.calls = []
        self.fail_after_create = False
        self.now = 100000

    def shops(self):
        return copy.deepcopy(self.registered)

    def control(self):
        return {'control': copy.deepcopy(self.config), 'shops': list(copy.deepcopy(self.stats).values()),
                'worker': {'available_lane_count': 6, 'updated_at': c.iso(self.now), 'egress_circuits': []}}

    def request(self, method, path, data=None):
        self.calls.append((method, path, copy.deepcopy(data)))
        if path == '/products/sync-control':
            if method == 'GET':
                return self.control()
            if data['expected_revision'] != self.config['revision']:
                raise RuntimeError('CAS conflict')
            self.config.update({k: v for k, v in data.items() if k not in ('expected_revision', 'lease_seconds')})
            self.config['revision'] += 1
            return copy.deepcopy(self.config)
        if path == '/shops' and method == 'POST':
            key = c.platform_id(data['url'])
            existing = next((row for row in self.registered if c.platform_id(row['url']) == key), None)
            if existing:
                return {'created': False, 'shop': existing}
            shop = dict(data, id='shop-' + key, trust_level='neutral')
            self.registered.append(shop)
            self.stats[shop['id']] = {'shop_id': shop['id'], 'platform_id': key, 'automatic_due': True,
                                     'snapshot_complete': False, 'consecutive_failures': 0, 'failure_count': 0,
                                     'waiting_seconds': 0, 'product_count': 0, 'source_product_count': 0}
            if self.fail_after_create:
                self.fail_after_create = False
                raise OSError('response lost after successful creation')
            return {'created': True, 'shop': shop}
        raise AssertionError((method, path))

    def complete(self, key, count=3):
        self.stats['shop-' + key].update(snapshot_complete=True, first_synced_at=c.iso(self.now),
                                        snapshot_updated_at=c.iso(self.now), source_product_count=count,
                                        product_count=count, automatic_due=False)


def host(now, memory=.5, pressure=False):
    return {'at': c.iso(now), 'worker_id': 'worker', 'worker_started_at': 'unchanged',
            'app_started_at': 'unchanged', 'cpu_total': now * 100, 'cpu_idle': now * 90,
            'worker_memory_ratio': memory, 'worker_oom_kill': 0, 'host_oom_kill': 0,
            'host_available_ratio': .5, 'worker_running': True, 'worker_oom_killed': False,
            'disk_free_bytes': 3 * 1024 ** 3, 'events': {'rate_limit': int(pressure), 'access_denied': 0}}


class ControllerTests(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name)
        source = self.path / 'original.csv'
        source.write_bytes(csv_bytes())
        self.api = FakeAPI()
        with patch.object(c.API, 'shops', return_value=[]):
            c.preview(source, self.api.target, 'test-environment', self.path, 9)
        self.controller = c.Controller(self.path, self.api, owner='test-owner')
        self.now = 100000
        self.quiet = patch('builtins.print')
        self.quiet.start()
        self.addCleanup(self.quiet.stop)

    def tick(self, **options):
        self.api.now = self.now
        self.controller.tick(host(self.now, **options))
        self.now += 60

    def baseline(self):
        for _ in range(21):
            self.tick()

    def test_domains_order_exclusions_and_ignored_counts(self):
        rows, counts = c.candidates(csv_bytes(), [{'url': 'https://pay.ldxp.cn/shop/ID0/category'}])
        self.assertEqual(counts['candidates'], 8)
        self.assertEqual(counts['ai'], 6)
        self.assertEqual([row['platform_id'] for row in rows], [f'ID{x}' for x in range(1, 9)])
        self.assertNotIn('count', rows[0])
        self.assertIsNone(c.platform_id('https://wzyp.cn.evil/shop/ID0'))
        self.assertIsNone(c.platform_id('https://user@wzyp.cn/shop/ID0'))
        self.assertEqual(c.platform_id('http://pay.ldxp.cn/shop/ID0/cat?x=1'), 'ID0')
        raw = csv_bytes().decode('utf-8-sig').replace('已显示店铺', '页面明确提示不存在', 1).encode()
        self.assertEqual(c.candidates(raw, [])[1]['excluded'], 1)

    def test_preview_is_repeatable_and_refuses_changed_source(self):
        old = json.loads((self.path / 'state.json').read_text())
        with patch.object(c.API, 'shops', side_effect=AssertionError('no need to re-query')):
            repeated = c.preview(self.path / 'original.csv', self.api.target, 'test-environment', self.path, 9)
        self.assertEqual(old, repeated)
        (self.path / 'original.csv').write_bytes(csv_bytes(2))
        with self.assertRaises(ValueError):
            c.preview(self.path / 'original.csv', self.api.target, 'test-environment', self.path)

    def test_uncertain_post_resume_and_two_first_sync_limit(self):
        self.baseline()
        self.api.fail_after_create = True
        self.tick()
        self.assertEqual(len(self.api.registered), 1)
        self.controller = c.Controller(self.path, self.api, owner='test-owner')
        self.tick()
        self.assertEqual(len(self.api.registered), 2)
        for _ in range(3):
            self.tick()
        self.assertEqual(len(self.api.registered), 2)
        posts = [call for call in self.api.calls if call[:2] == ('POST', '/shops')]
        self.assertEqual(len(posts), 2, 'uncertain success must reconcile instead of POST again')
        self.assertFalse(any('sync-job' in call[1] for call in self.api.calls))
        self.assertTrue(all(row['trust_level'] == 'neutral' for row in self.api.registered))

    def test_partial_failure_empty_and_observation_before_growth(self):
        self.baseline()
        self.tick()
        self.api.complete('ID0', count=0)
        self.api.stats['shop-ID1'].update(consecutive_failures=2, failure_count=2, last_failure_kind='shop_closed')
        self.tick()
        self.assertEqual(self.controller.state['shops'][0]['status'], 'empty')
        self.assertEqual(self.controller.state['shops'][1]['status'], 'closed')
        self.assertIn('shop-ID1', self.api.config['quarantined_shops'])
        self.assertEqual(c.report(self.controller.state)['summary']['with_products'], 0)
        for _ in range(14):
            self.tick()
        self.assertEqual(len(self.controller.state['batches']), 1)
        self.tick()
        self.assertEqual(self.controller.state['batches'][-1]['size'], 5)
        self.tick()
        self.assertEqual(len(self.api.registered), 4)

    def test_heavy_pause_and_real_recovery_window(self):
        self.baseline()
        self.tick(memory=.96)
        self.tick(memory=.96)
        self.assertTrue(self.api.config['automatic_paused'])
        self.assertTrue(self.controller.state['resource_hold'])
        for _ in range(15):
            self.tick(memory=.5)
        self.assertTrue(self.api.config['automatic_paused'])
        self.tick(memory=.5)
        self.assertFalse(self.api.config['automatic_paused'])
        self.assertFalse(self.controller.state['resource_hold'])
        self.assertEqual(self.controller.state['batch_size'], 2)

    def test_isolated_inflight_shops_still_occupy_first_sync_capacity(self):
        self.baseline()
        self.tick()
        for stat in self.api.stats.values():
            stat.update(active=True, consecutive_failures=2, failure_count=2)
        self.tick()
        self.assertEqual(len(self.api.config['quarantined_shops']), 2)
        self.assertEqual(self.controller.state['phase'], 'importing')
        for _ in range(16):
            self.tick()
        self.assertEqual(len(self.api.registered), 2)
        self.assertEqual(self.controller.state['phase'], 'importing')
        for stat in self.api.stats.values():
            stat['active'] = False
        self.tick()
        self.assertEqual(self.controller.state['phase'], 'batch_observation')

    def test_upstream_uniform_backoff_and_scale_rollback(self):
        self.baseline()
        self.tick()
        self.api.complete('ID0')
        self.api.complete('ID1')
        for _ in range(21):
            self.tick(pressure=True)
        self.assertEqual(self.api.config['interval_multiplier'], 2)
        self.assertEqual(len(self.api.registered), 2)
        for _ in range(20):
            self.tick(pressure=True)
        self.assertEqual(self.api.config['interval_multiplier'], 4)
        for _ in range(20):
            self.tick(pressure=True)
        self.assertTrue(self.controller.state['boundary'])
        self.assertEqual(len(self.api.config['quarantined_shops']), 2)
        self.assertEqual(self.controller.state['shops'][0]['first_product_count'], 3)

    def test_cpu_or_memory_does_not_increase_interval(self):
        self.baseline()
        self.controller.state['batch_size'] = 10
        for _ in range(30):
            self.tick(memory=.91)
        self.assertEqual(self.api.config['interval_multiplier'], 1)
        self.assertEqual(self.controller.state['batch_size'], 5)
        self.assertEqual(len(self.api.registered), 0)

    def test_gaps_restart_and_full_final_cycle(self):
        self.baseline()
        state = self.controller.state
        state.update(phase='final_observation', validation_started=self.now, boundary=True)
        for _ in range(59):
            self.tick()
        self.assertFalse(state['finished'])
        self.now += 120
        self.tick()
        self.assertFalse(state['finished'])
        self.assertTrue(any(event['kind'] == 'observation_reset' for event in state['events']))
        for _ in range(60):
            self.tick()
        self.assertTrue(state['finished'])
        self.assertEqual(state['verified_multiplier'], 1)

    def test_faster_trial_failure_returns_to_verified_frequency(self):
        self.baseline()
        state = self.controller.state
        state.update(phase='final_observation', validation_started=self.now, multiplier=2,
                     faster_trial_from=4, verified_multiplier=4, boundary=True)
        self.api.config['interval_multiplier'] = 2
        self.tick(pressure=True)
        self.assertEqual(self.api.config['interval_multiplier'], 4)
        self.assertEqual(state['failed_faster_multiplier'], 2)

    def test_continuous_rejects_missing_samples_and_short_windows(self):
        rows = [{'time': index * 60, 'ok': True} for index in range(16)]
        self.assertTrue(c.continuous(rows, 900, lambda item: item['ok'], 900))
        self.assertFalse(c.continuous(rows[1:], 900, lambda item: item['ok'], 900))
        del rows[6]
        self.assertFalse(c.continuous(rows, 900, lambda item: item['ok'], 900))


if __name__ == '__main__':
    unittest.main()
