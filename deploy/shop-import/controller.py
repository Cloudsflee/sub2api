#!/usr/bin/env python3
"""Resumable, pressure-controlled shop import. Run on the verified operator host.

preview freezes the CSV and existing shops; run continues the same journal;
report is offline. All timings are real elapsed wall time, persisted on disk.
"""
import argparse
import csv
import datetime as dt
import hashlib
import io
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.parse
import urllib.request
import uuid

import observe

MINUTE = 60
TERMINAL = {'products', 'empty', 'no_sellable', 'closed', 'quarantined', 'pressure_paused', 'submit_failed'}


def timestamp(value):
    if not value:
        return 0.0
    return dt.datetime.fromisoformat(value.replace('Z', '+00:00')).timestamp()


def iso(value):
    return dt.datetime.fromtimestamp(value, dt.timezone.utc).isoformat()


def atomic_json(path, data):
    path = Path(path)
    path.parent.mkdir(parents=True, exist_ok=True)
    fd, name = tempfile.mkstemp(prefix='.' + path.name, dir=path.parent)
    try:
        with os.fdopen(fd, 'w', encoding='utf-8') as stream:
            json.dump(data, stream, ensure_ascii=False, indent=2)
            stream.write('\n')
            stream.flush()
            os.fsync(stream.fileno())
        os.replace(name, path)
    finally:
        if os.path.exists(name):
            os.unlink(name)


def platform_id(url):
    parsed = urllib.parse.urlsplit(url.strip())
    if (parsed.scheme.lower() not in ('http', 'https') or parsed.username or parsed.password
            or parsed.hostname not in ('wzyp.cn', 'pay.ldxp.cn')
            or parsed.port not in (None, 80, 443)):
        return None
    parts = [urllib.parse.unquote(part) for part in parsed.path.strip('/').split('/')]
    if len(parts) not in (2, 3) or parts[0] != 'shop' or not re.fullmatch(r'[A-Za-z0-9_-]+', parts[1]):
        return None
    if len(parts) == 3 and (not parts[2] or '/' in parts[2]):
        return None
    return parts[1]


def candidates(raw, existing):
    registered = {platform_id(shop['url']) for shop in existing}
    rows = list(csv.DictReader(io.StringIO(raw.decode('utf-8-sig'))))
    required = {'id', 'url', 'status', 'name', 'ai'}
    if not rows or not required.issubset(rows[0]):
        raise ValueError('CSV columns are not supported')
    counts = {'rows': len(rows), 'historical_success': 0, 'duplicate': 0, 'excluded': 0}
    result, seen = [], set()
    for index, row in enumerate(rows, 2):
        if row['status'].strip() != '已显示店铺':
            counts['excluded'] += 1
            continue
        counts['historical_success'] += 1
        key = platform_id(row['url'])
        if not key or key != row['id'].strip():
            raise ValueError(f'CSV identity mismatch at line {index}')
        if key in registered or key in seen:
            counts['duplicate'] += 1
            continue
        name = ' '.join(row['name'].split())
        if not name or len(name) > 80:
            raise ValueError(f'Invalid shop name at line {index}')
        seen.add(key)
        # Deliberately do not parse count/title/excerpt as inventory evidence.
        result.append({'platform_id': key, 'url': 'https://wzyp.cn/shop/' + key,
                       'name': name, 'ai': bool(row['ai'].strip()), 'source_line': index,
                       'status': 'unsubmitted', 'reason': 'awaiting batch admission'})
    result.sort(key=lambda row: (not row['ai'], row['source_line']))
    counts.update(candidates=len(result), ai=sum(row['ai'] for row in result),
                  unlabelled=sum(not row['ai'] for row in result))
    return result, counts


class API:
    def __init__(self, target, token=None):
        self.target = target.rstrip('/')
        parsed = urllib.parse.urlsplit(self.target)
        if parsed.hostname == 'api.sub2api.com' or parsed.username or parsed.password or parsed.query or parsed.fragment:
            raise ValueError('Invalid operator target')
        if parsed.scheme not in ('http', 'https') or parsed.path:
            raise ValueError('Use the explicit operator origin, without an API path')
        if parsed.scheme == 'http' and parsed.hostname not in ('127.0.0.1', 'localhost', '::1'):
            raise ValueError('Run on the host with a loopback target, or use HTTPS')
        self.token = token

    def request(self, method, path, data=None):
        if path.startswith('/products/sync-job'):
            raise ValueError('The controller must never claim sync jobs')
        headers = {'Accept': 'application/json'}
        if self.token:
            headers['Authorization'] = 'Bearer ' + self.token
        body = None
        if data is not None:
            body = json.dumps(data).encode()
            headers['Content-Type'] = 'application/json'
        request = urllib.request.Request(self.target + '/api/v1/public/account-import' + path,
                                         data=body, headers=headers, method=method)
        with urllib.request.urlopen(request, timeout=25) as response:
            result = json.load(response)
        if result.get('code') != 0:
            raise RuntimeError('Backend rejected ' + method + ' ' + path)
        return result['data']

    def shops(self):
        return self.request('GET', '/shops')['shops']

    def control(self):
        return self.request('GET', '/products/sync-control')


def worker_token():
    value = os.environ.get('PUBLIC_ACCOUNT_IMPORT_PRODUCT_SYNC_TOKEN')
    if value:
        return value
    slot = observe.command('/usr/local/sbin/sub2api-haproxy-config', '--current').strip()
    if slot not in ('blue', 'green'):
        raise RuntimeError('Cannot verify active operator slot')
    env = observe.docker('/containers/sub2api-' + slot + '/json')['Config']['Env']
    for entry in env:
        if entry.startswith('PUBLIC_ACCOUNT_IMPORT_PRODUCT_SYNC_TOKEN='):
            return entry.split('=', 1)[1]
    raise RuntimeError('Worker credential unavailable')


def preview(source, target, environment, directory, expected=None):
    directory = Path(directory)
    path = directory / 'state.json'
    raw = Path(source).read_bytes()
    digest = hashlib.sha256(raw).hexdigest()
    if path.exists():
        state = json.loads(path.read_text(encoding='utf-8'))
        if (state['source_sha256'], state['target'], state['environment']) != (digest, target.rstrip('/'), environment):
            raise ValueError('Existing journal belongs to another source or environment')
        return state
    existing = API(target).shops()
    rows, counts = candidates(raw, existing)
    if expected is not None and len(rows) != expected:
        raise ValueError(f'Expected {expected} candidates, found {len(rows)}; review before running')
    directory.mkdir(parents=True, exist_ok=True)
    (directory / 'source.csv').write_bytes(raw)
    atomic_json(directory / 'existing-shops.json', existing)
    atomic_json(directory / 'candidates.json', rows)
    state = {'version': 1, 'source_sha256': digest, 'target': target.rstrip('/'), 'environment': environment,
             'created_at': observe.utcnow(), 'counts': counts, 'shops': rows,
             'existing_ids': [shop['id'] for shop in existing], 'phase': 'baseline',
             'batch_size': 2, 'batches': [], 'history': [], 'baseline_due': None,
             'multiplier': 1, 'events': [], 'finished': False}
    atomic_json(path, state)
    return state


def continuous(history, seconds, predicate, now):
    """Require enough real samples, no missing minute, and the whole interval."""
    anchors = [index for index, item in enumerate(history) if item['time'] <= now - seconds]
    if not anchors:
        return False
    selected = history[anchors[-1]:]
    previous = None
    for item in selected:
        if not predicate(item) or (previous is not None and item['time'] - previous > 90):
            return False
        previous = item['time']
    return previous is not None and now - previous <= 90


def growing(history, now):
    window = [item for item in history if item['time'] >= now - 10 * MINUTE - 10]
    return (len(window) >= 11 and window[-1]['time'] - window[0]['time'] >= 10 * MINUTE
            and all(0 < b['time'] - a['time'] <= 90 and b['existing_due'] >= a['existing_due']
                    for a, b in zip(window, window[1:]))
            and window[-1]['existing_due'] > window[0]['existing_due'])


def pressure_sample(host, scheduler, state):
    now = timestamp(host['at'])
    previous = state['history'][-1] if state['history'] else None
    cpu = None
    if previous and host['cpu_total'] > previous['cpu_total']:
        cpu = 1 - (host['cpu_idle'] - previous['cpu_idle']) / (host['cpu_total'] - previous['cpu_total'])
    worker = scheduler['worker']
    existing = set(state['existing_ids'])
    due = sum(row['automatic_due'] for row in scheduler['shops'] if row['shop_id'] in existing)
    upstream = bool(host['events']['rate_limit'] or host['events']['access_denied']
                    or timestamp(worker.get('global_silence_until')) > now
                    or timestamp(worker.get('global_pressure_until')) > now
                    or any(timestamp(row.get('circuit_open_until')) > now for row in worker.get('egress_circuits', [])))
    base = state['baseline_due'] if state['baseline_due'] is not None else due
    result = {'time': now, 'cpu_total': host['cpu_total'], 'cpu_idle': host['cpu_idle'], 'cpu': cpu,
              'worker_memory': host['worker_memory_ratio'], 'host_available': host['host_available_ratio'],
              'lanes': worker['available_lane_count'], 'existing_due': due, 'upstream': upstream,
              'queue_pressure': due > base + 6, 'worker_id': host['worker_id'],
              'worker_started_at': host['worker_started_at'], 'app_started_at': host['app_started_at'],
              'worker_oom': host['worker_oom_kill'], 'host_oom': host['host_oom_kill'],
              'existing_max_wait_seconds': max((row['waiting_seconds'] for row in scheduler['shops']
                                               if row['shop_id'] in existing), default=0)}
    heavy = []
    if host['worker_memory_ratio'] is None:
        heavy.append('missing worker memory limit')
    if host['worker_oom_killed'] or (previous and (result['host_oom'] > previous['host_oom'] or
            (previous['worker_id'] == result['worker_id'] and result['worker_oom'] > previous['worker_oom']))):
        heavy.append('new OOM')
    if previous and now - previous['time'] <= 90 and result['worker_memory'] is not None and result['worker_memory'] >= .95 and previous.get('worker_memory') is not None and previous['worker_memory'] >= .95:
        heavy.append('worker memory >=95% in consecutive samples')
    if result['host_available'] < .10:
        heavy.append('host available memory <10%')
    if host['disk_free_bytes'] < 1024 ** 3:
        heavy.append('disk free <1 GiB')
    if not host['worker_running'] or now - timestamp(worker.get('updated_at')) > 120:
        heavy.append('worker status stale or stopped')
    result['heavy'] = heavy
    result['resource_light'] = (result['worker_memory'] is not None and result['worker_memory'] >= .90
                                or result['lanes'] < 5 or cpu is not None and cpu > .85)
    result['normal'] = (not heavy and not upstream and result['lanes'] >= 5
                        and result['worker_memory'] is not None and result['worker_memory'] < .90
                        and result['host_available'] >= .20 and not result['queue_pressure']
                        and (cpu is None or cpu <= .85))
    result['recovery'] = result['normal'] and result['worker_memory'] < .88
    return result


def report(state):
    rows = state['shops']
    summary = {'registered': sum(bool(row.get('shop_id')) for row in rows),
               'first_sync_complete': sum(bool(row.get('first_synced_at')) for row in rows),
               'with_products': sum(row.get('first_product_count', 0) > 0 for row in rows),
               'products_on_first_sync': sum(row.get('first_product_count', 0) for row in rows),
               'empty_catalog': sum(row.get('status') == 'empty' for row in rows),
               'no_sellable': sum(row.get('status') == 'no_sellable' for row in rows),
               'closed': sum(row.get('status') == 'closed' for row in rows),
               'isolated': sum(bool(row.get('isolation_reason')) for row in rows),
               'pressure_paused': sum(row.get('status') == 'pressure_paused' for row in rows),
               'unsubmitted': sum(not row.get('shop_id') for row in rows)}
    return {'source_sha256': state['source_sha256'], 'environment': state['environment'],
            'phase': state['phase'], 'finished': state['finished'], 'summary': summary,
            'resource_hold': state.get('resource_hold', False), 'pressure_hold_since': state.get('hold_since'),
            'interval_multiplier': state['multiplier'], 'baseline_due': state['baseline_due'],
            'verified_multiplier': state.get('verified_multiplier'), 'batches': state['batches'],
            'events': state['events'], 'shops': rows,
            'resume': 'python3 controller.py run --state <this-directory>' + (' --continue-after-boundary' if state['finished'] and state.get('boundary') else ''),
            'remaining': [row for row in rows if not row.get('first_synced_at')]}


class Controller:
    def __init__(self, directory, api, owner=None):
        self.directory = Path(directory)
        self.state = json.loads((self.directory / 'state.json').read_text(encoding='utf-8'))
        if hashlib.sha256((self.directory / 'source.csv').read_bytes()).hexdigest() != self.state['source_sha256']:
            raise ValueError('Frozen source checksum changed')
        if api.target != self.state['target']:
            raise ValueError('Target differs from the frozen manifest')
        self.api = api
        self.owner = owner or uuid.uuid4().hex
        self.control = None

    def save(self):
        atomic_json(self.directory / 'state.json', self.state)
        atomic_json(self.directory / 'report.json', report(self.state))

    def event(self, kind, **details):
        row = {'at': observe.utcnow(), 'kind': kind, **details}
        self.state['events'].append(row)
        print(json.dumps(row, ensure_ascii=True), flush=True)

    def change(self, **changes):
        self.control = self.api.request('PUT', '/products/sync-control', {
            'expected_revision': self.control['revision'], 'controller_id': self.owner,
            'lease_seconds': 180, **changes})
        self.state['multiplier'] = self.control['interval_multiplier']
        if changes:
            self.event('control_changed', **changes)
        self.save()

    def isolate(self, rows, reason, pressure=False):
        quarantine = dict(self.control['quarantined_shops'])
        for row in rows:
            if row.get('shop_id'):
                quarantine[row['shop_id']] = reason
        self.change(quarantined_shops=quarantine)
        for row in rows:
            row['isolation_reason'] = reason
            row['reason'] = reason
            row['status'] = 'pressure_paused' if pressure else ('closed' if row.get('last_failure_kind') == 'shop_closed' else 'quarantined')
        self.save()

    def reconcile(self, shops, scheduler, now):
        by_platform = {platform_id(shop['url']): shop for shop in shops}
        stats = {row['shop_id']: row for row in scheduler['shops']}
        for row in self.state['shops']:
            # Only reconcile our intended rows, never add all historical shops.
            shop = by_platform.get(row['platform_id'])
            if not row.get('shop_id') and shop:
                row.update(shop_id=shop['id'], registered_at=row.get('submitted_at', iso(now)),
                           created=None, status='registered', reason='reconciled from live platform identity')
                self.event('registration_reconciled', platform_id=row['platform_id'], shop_id=shop['id'])
            stat = stats.get(row.get('shop_id'))
            if not stat:
                continue
            row.update(last_failure_kind=stat.get('last_failure_kind'),
                       consecutive_failures=stat['consecutive_failures'], failure_count=stat['failure_count'],
                       last_error=stat.get('error'), last_attempt=stat.get('last_attempt'),
                       retry_not_before_at=stat.get('retry_not_before_at'))
            if stat['snapshot_complete'] and not row.get('first_synced_at'):
                row.update(first_synced_at=stat.get('first_synced_at') or stat['snapshot_updated_at'],
                           first_started_at=stat.get('first_started_at'),
                           first_product_count=stat['product_count'], source_product_count=stat['source_product_count'],
                           first_sync_elapsed_seconds=max(0, timestamp(stat.get('first_synced_at') or stat['snapshot_updated_at']) - timestamp(row['registered_at'])))
                if not row.get('isolation_reason'):
                    row['status'] = 'products' if stat['product_count'] else ('empty' if stat['source_product_count'] == 0 else 'no_sellable')
                    row['reason'] = 'complete authoritative snapshot saved'
                self.event('first_sync_completed', platform_id=row['platform_id'], count=stat['product_count'], source_count=stat['source_product_count'])
            if not row.get('first_synced_at') and not row.get('isolation_reason'):
                elapsed = now - timestamp(row['registered_at'])
                if stat['consecutive_failures'] >= 2 or elapsed >= 60 * MINUTE:
                    reason = 'two consecutive complete-task failures' if stat['consecutive_failures'] >= 2 else 'first-sync unresolved after 60 minutes'
                    self.isolate([row], reason)
        self.save()

    def current_batch(self):
        return self.state['batches'][-1] if self.state['batches'] else None

    def batch_rows(self, batch):
        keys = set(batch['platform_ids'])
        return [row for row in self.state['shops'] if row['platform_id'] in keys]

    def start_batch(self, now):
        pending = [row for row in self.state['shops'] if row['status'] == 'unsubmitted']
        if not pending or self.state.get('boundary'):
            self.state.update(phase='final_observation', validation_started=now)
            self.event('final_cycle_started', multiplier=self.state['multiplier'])
            self.save()
            return
        stage = pending[0]['ai']
        rows = [row for row in pending if row['ai'] == stage][:self.state['batch_size']]
        batch = {'number': len(self.state['batches']) + 1, 'started_at': iso(now),
                 'platform_ids': [row['platform_id'] for row in rows], 'size': len(rows), 'ai': stage}
        self.state['batches'].append(batch)
        self.state['phase'] = 'importing'
        self.event('batch_started', **batch)
        self.save()

    def submit(self, now):
        # Count all unresolved new shops, including any previous partial batch.
        unresolved = sum(bool(row.get('shop_id')) and row['status'] not in TERMINAL for row in self.state['shops'])
        batch = self.current_batch()
        for row in self.batch_rows(batch):
            if unresolved >= 2:
                break
            if row['status'] not in ('unsubmitted', 'submitting'):
                continue
            # Every attempt re-reads identity, including unknown POST outcomes.
            existing = next((shop for shop in self.api.shops() if platform_id(shop['url']) == row['platform_id']), None)
            if existing:
                row.update(shop_id=existing['id'], created=False, status='registered',
                           registered_at=row.get('submitted_at', iso(now)), reason='existing platform identity preserved')
                unresolved += 1
                self.save()
                continue
            # Renew target lease immediately before each external write.
            self.change()
            row.update(status='submitting', submitted_at=row.get('submitted_at', iso(now)),
                       reason='submission intent journaled before POST')
            self.save()
            try:
                result = self.api.request('POST', '/shops', {'name': row['name'], 'url': row['url']})
            except (OSError, ValueError, RuntimeError) as error:
                row['submit_errors'] = row.get('submit_errors', 0) + 1
                # Do not print request headers or response bodies containing secrets.
                row['reason'] = 'submission outcome uncertain; recheck identity next minute'
                self.event('submission_retry', platform_id=row['platform_id'], error_type=type(error).__name__)
                self.save()
                break
            row.update(shop_id=result['shop']['id'], created=result['created'], registered_at=row['submitted_at'],
                       status='registered', reason='awaiting automatic first sync')
            unresolved += 1
            self.state['last_submission'] = now
            self.event('shop_registered', platform_id=row['platform_id'], shop_id=row['shop_id'], created=row['created'])
            self.save()

    def roll_back_scale(self, now):
        for batch in reversed(self.state['batches']):
            rows = [row for row in self.batch_rows(batch) if row.get('shop_id') and not row.get('isolation_reason')]
            if rows:
                self.isolate(rows, 'pressure persisted at 4x; newest batch excluded from automatic collection', pressure=True)
                batch['pressure_rolled_back_at'] = iso(now)
                self.state.update(boundary=True, phase='final_observation', validation_started=now,
                                  pressure_since=None, hold_since=None)
                for row in self.state['shops']:
                    if not row.get('shop_id'):
                        row['reason'] = 'not submitted: verified scale boundary under evaluation'
                self.event('scale_reduced', batch=batch['number'])
                self.save()
                return
        self.state.update(boundary=True, phase='baseline_pressure', pressure_since=None)
        self.event('baseline_pressure_unresolved', reason='no imported batches remain to exclude; resource/upstream diagnosis required')
        self.save()

    def tick(self, host):
        state = self.state
        scheduler = self.api.control()
        self.control = scheduler['control']
        if state['finished']:
            return
        self.change()  # CAS and target-wide controller lease, before any writes.
        now = timestamp(host['at'])
        sample = pressure_sample(host, scheduler, state)
        previous = state['history'][-1] if state['history'] else None
        restarted = previous and any(sample[key] != previous[key] for key in ('worker_started_at', 'app_started_at'))
        missing = previous and now - previous['time'] > 90
        if restarted or missing:
            self.event('observation_reset', restart=bool(restarted), missing_samples=bool(missing),
                       worker_started_at=host['worker_started_at'], app_started_at=host['app_started_at'])
            state['history'] = []
            state['settle_until'] = now + 20 * MINUTE
            state['validation_started'] = now
            state['pressure_since'] = None
        state['history'].append(sample)
        state['history'] = [item for item in state['history'] if item['time'] >= now - 30 * MINUTE]
        with (self.directory / 'samples.jsonl').open('a', encoding='utf-8') as stream:
            stream.write(json.dumps({'host': host, 'scheduler': scheduler, 'pressure': sample}, ensure_ascii=False) + '\n')
        self.reconcile(self.api.shops(), scheduler, now)
        if state.get('baseline_started') is None:
            state['baseline_started'] = now
        if state['baseline_due'] is None and now - state['baseline_started'] >= 20 * MINUTE:
            window = state['history']
            if len(window) >= 20 and window[0]['time'] <= now - 20 * MINUTE:
                # Conservative reference: maximum observed pre-import due count.
                state['baseline_due'] = max(item['existing_due'] for item in window)
                self.event('baseline_completed', existing_due_max=state['baseline_due'], samples=len(window))
        history = state['history']
        queue_growing = growing(history, now)
        if queue_growing:
            sample['normal'] = sample['recovery'] = False
        normal15 = continuous(history, 15 * MINUTE, lambda item: item['normal'], now) and not queue_growing
        recovery15 = continuous(history, 15 * MINUTE, lambda item: item['recovery'], now) and not queue_growing
        light = sample['upstream'] or queue_growing or continuous(history, 5 * MINUTE, lambda item: item['resource_light'], now)
        if sample['heavy']:
            if not self.control['automatic_paused']:
                self.change(automatic_paused=True)
            if not state.get('resource_hold'):
                self.event('heavy_pressure', reasons=sample['heavy'])
            state.update(resource_hold=True, hold_since=state.get('hold_since') or now,
                         validation_started=now, batch_size=max(2, state['batch_size'] // 2), batch_reduced=True)
            self.save()
            return
        if state.get('resource_hold'):
            if not recovery15:
                self.save()
                return
            self.change(automatic_paused=False)
            state.update(resource_hold=False, hold_since=None, validation_started=now,
                         settle_until=now + 15 * MINUTE)
            self.event('resource_pressure_recovered', evidence='15 continuous minutes below 88% memory with lanes/host/queue recovered')
        if self.control['automatic_paused']:
            # A pause inherited from another operator/controller is never silently lifted.
            state['external_pause'] = True
            self.save()
            return
        request_pressure = sample['upstream'] or sample['queue_pressure'] or queue_growing
        if state.get('faster_trial_from') and (light or request_pressure):
            state['failed_faster_multiplier'] = state['multiplier']
            self.change(interval_multiplier=state.pop('faster_trial_from'))
            state['validation_started'] = now
            self.event('faster_interval_trial_failed', multiplier=state['failed_faster_multiplier'])
        if (light or sample['queue_pressure']) and not state.get('hold_since'):
            state['hold_since'] = now
            state['batch_size'] = max(2, state['batch_size'] // 2)
            state['batch_reduced'] = True
            self.event('light_pressure', upstream=sample['upstream'], queue_growing=queue_growing,
                       resource_light=sample['resource_light'], next_batch_size=state['batch_size'])
        if state.get('hold_since') and request_pressure:
            state['pressure_since'] = state.get('pressure_since') or now
        elif not request_pressure:
            state['pressure_since'] = None
        if state.get('pressure_since') and now - state['pressure_since'] >= 20 * MINUTE:
            if state['multiplier'] < 4:
                self.change(interval_multiplier=state['multiplier'] * 2)
                state.update(pressure_since=now, validation_started=now)
                self.event('uniform_interval_increased', evidence='upstream/queue pressure persisted 20 minutes without admissions')
            else:
                self.roll_back_scale(now)
            self.save()
            return
        if state.get('hold_since'):
            if recovery15:
                state.update(hold_since=None, pressure_since=None, validation_started=now)
                self.event('light_pressure_recovered', batch_size=state['batch_size'])
            else:
                self.save()
                return
        if state['baseline_due'] is None or now < state.get('settle_until', 0):
            self.save()
            return
        phase = state['phase']
        if phase == 'baseline' and normal15:
            self.start_batch(now)
        elif phase == 'importing':
            batch = self.current_batch()
            if all(row['status'] in TERMINAL for row in self.batch_rows(batch)):
                batch['finished_at'] = iso(now)
                batch['elapsed_seconds'] = now - timestamp(batch['started_at'])
                state['phase'] = 'batch_observation'
                self.event('batch_completed', number=batch['number'], elapsed_seconds=batch['elapsed_seconds'])
            elif sample['normal']:
                self.submit(now)
        elif phase == 'batch_observation':
            if now - timestamp(self.current_batch()['finished_at']) >= 15 * MINUTE and normal15:
                if not state.pop('batch_reduced', False):
                    state['batch_size'] = 5 if state['batch_size'] == 2 else 10
                self.start_batch(now)
        elif phase == 'final_observation':
            if not sample['normal'] or queue_growing:
                state['validation_started'] = now
            cycle = state['multiplier'] * 60 * MINUTE
            if now - state.get('validation_started', now) >= cycle and normal15:
                state['verified_multiplier'] = state['multiplier']
                state.pop('faster_trial_from', None)
                self.event('full_cycle_verified', multiplier=state['multiplier'], seconds=cycle)
                if state['multiplier'] > 1 and state.get('failed_faster_multiplier') != state['multiplier'] // 2:
                    state['faster_trial_from'] = state['multiplier']
                    self.change(interval_multiplier=state['multiplier'] // 2)
                    state['validation_started'] = now
                    self.event('faster_interval_trial_started', multiplier=state['multiplier'])
                else:
                    state.update(finished=True, phase='complete', completed_at=iso(now))
                    self.event('controller_completed', **report(state)['summary'])
        elif phase == 'baseline_pressure' and normal15:
            state.update(phase='final_observation', validation_started=now)
        self.save()

    def run(self):
        import fcntl  # Execution is deliberately host-local; preview/report are portable.
        with (self.directory / '.controller.lock').open('a') as lock:
            fcntl.flock(lock, fcntl.LOCK_EX | fcntl.LOCK_NB)
            previous_at = None
            try:
                while not self.state['finished']:
                    started = time.monotonic()
                    try:
                        host = observe.sample(previous_at)
                        self.tick(host)
                        previous_at = host['at']
                    except urllib.error.HTTPError as error:
                        if error.code in (401, 403, 409):
                            self.event('controller_stopped', http_status=error.code, reason='credential or controller lease conflict')
                            self.save()
                            raise
                        self.observation_failure(error)
                    except (OSError, ValueError, RuntimeError, KeyError, subprocess.SubprocessError) as error:
                        self.observation_failure(error)
                    if not self.state['finished']:
                        time.sleep(max(1, MINUTE - (time.monotonic() - started)))
            finally:
                if self.control and self.control.get('controller_id') == self.owner:
                    try:
                        self.api.request('PUT', '/products/sync-control', {
                            'expected_revision': self.control['revision'], 'controller_id': self.owner, 'lease_seconds': 0})
                    except (OSError, ValueError, RuntimeError):
                        pass  # Lease expires in at most three minutes.

    def observation_failure(self, error):
        self.event('observation_failed', error_type=type(error).__name__)
        self.state['history'] = []
        self.state['settle_until'] = time.time() + 20 * MINUTE
        self.state['validation_started'] = time.time()
        self.state['resource_hold'] = True
        if self.control and self.control.get('controller_id') == self.owner:
            try:
                self.change(automatic_paused=True)
            except (OSError, ValueError, RuntimeError):
                self.event('pause_failed', reason='control endpoint unavailable; no further admissions')
        self.save()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='command', required=True)
    preview_parser = sub.add_parser('preview')
    preview_parser.add_argument('--source', required=True)
    preview_parser.add_argument('--target', required=True)
    preview_parser.add_argument('--environment', required=True)
    preview_parser.add_argument('--expected-count', type=int)
    preview_parser.add_argument('--state', required=True)
    run_parser = sub.add_parser('run')
    run_parser.add_argument('--state', required=True)
    run_parser.add_argument('--continue-after-boundary', action='store_true',
                            help='Start a new baseline for unsubmitted candidates; existing isolation is preserved')
    sub.add_parser('report').add_argument('--state', required=True)
    args = parser.parse_args()
    if args.command == 'preview':
        state = preview(args.source, args.target, args.environment, args.state, args.expected_count)
        print(json.dumps({'source_sha256': state['source_sha256'], **state['counts']}, ensure_ascii=True))
    elif args.command == 'report':
        state = json.loads((Path(args.state) / 'state.json').read_text(encoding='utf-8'))
        print(json.dumps(report(state), ensure_ascii=True, indent=2))
    else:
        state = json.loads((Path(args.state) / 'state.json').read_text(encoding='utf-8'))
        controller = Controller(args.state, API(state['target'], worker_token()))
        if args.continue_after_boundary:
            if not state.get('boundary') or not state['finished']:
                raise ValueError('Only a completed, verified boundary may be explicitly continued')
            # This is saved after acquiring the local lock in run().
            controller.state.update(finished=False, boundary=False, phase='baseline', baseline_due=None,
                                    baseline_started=None, history=[], batch_size=2, hold_since=None,
                                    pressure_since=None, validation_started=None)
        controller.run()


if __name__ == '__main__':
    main()
