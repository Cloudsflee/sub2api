"""Durable shadow handoff and full-cycle acceptance for the operator-managed stack."""
import argparse
import json
import subprocess
import time
from pathlib import Path

from controller import API, atomic_json, iso, platform_id, timestamp, worker_token


def handoff(control, state):
    """Only pressure isolation owned by this import run may be auto-recovered."""
    rows = {row['platform_id']: row for row in state['shops']}
    batches = dict(control.get('imported_batches') or {})
    pressure = dict(control.get('pressure_paused_shops') or {})
    quarantine = dict(control['quarantined_shops'])
    for batch in state['batches']:
        for key in batch['platform_ids']:
            row = rows[key]
            shop_id = row.get('shop_id')
            if not shop_id:
                continue
            batches[shop_id] = batch['number']
            if (row.get('status') == 'pressure_paused' and row.get('isolation_reason')
                    and quarantine.get(shop_id) == row['isolation_reason']):
                pressure[shop_id] = quarantine.pop(shop_id)
    return {'mode': 'adaptive', 'interval_multiplier': 1, 'imported_batches': batches,
            'pressure_paused_shops': pressure, 'quarantined_shops': quarantine}


def summarize(data, now):
    stats = [row.get('adaptive') or {} for row in data['shops']]
    s = data.get('adaptive') or {}
    present = lambda value: bool(value and not value.startswith('0001-'))
    longest = max((row.get('quote_seconds', 0) for row in stats if row.get('effective_count', 0) > 0), default=0)
    stable = timestamp(s.get('validated_since')) or now
    count = {
        'registered_supported': len(stats),
        'catalog_complete': sum(present(row.get('first_catalog_at')) for row in stats),
        'quote_complete': sum(present(row.get('first_quote_at')) for row in stats),
        'with_products': sum(row.get('effective_count', 0) > 0 for row in stats),
        'dormant': sum(row.get('state') == 'dormant' for row in stats),
        'exception': sum(row.get('state') == 'exception' for row in stats),
        'pressure_paused': sum(row.get('state') == 'pressure_paused' for row in stats),
        'manual_isolation': len(data['control']['quarantined_shops']),
    }
    eligible = [row for row in data['shops'] if row['shop_id'] not in data['control']['quarantined_shops']
                and (row.get('adaptive') or {}).get('state') not in ('exception', 'pressure_paused')]
    covered = all(present((row.get('adaptive') or {}).get('first_catalog_at')) and
                  ((row.get('adaptive') or {}).get('effective_count', 0) == 0 or
                   present((row.get('adaptive') or {}).get('first_quote_at'))) for row in eligible)
    complete = (data['control'].get('mode') == 'adaptive' and covered and longest > 0
                and now - stable >= longest and not s.get('automatic_hold')
                and s.get('required_multiplier', 1) <= s.get('admission_capacity_limit', 4.4)
                and not data['control']['automatic_paused'])
    return {'observed_at': iso(now), 'summary': count, 'mode': data['control'].get('mode'),
            'actual_multiplier': s.get('multiplier'), 'required_multiplier': s.get('required_multiplier'),
            'longest_quote_cycle_seconds': longest, 'stable_since': iso(stable),
            'stable_elapsed_seconds': max(0, now - stable), 'full_cycle_verified': complete,
            'automatic_hold': s.get('automatic_hold'), 'admission_hold': s.get('admission_hold')}


def run(args):
    directory = Path(args.directory)
    directory.mkdir(parents=True, exist_ok=True)
    api = API(args.target, worker_token())
    while True:
        now = time.time()
        data = api.control()
        control = data['control']
        mode = control.get('mode', 'legacy')
        if mode == 'legacy':
            # This service is explicitly installed for this approved handoff.
            # A previous completed handoff switched back to legacy is rollback.
            if (directory / 'handoff.json').exists():
                return
            if not (directory / 'previous-control.json').exists():
                atomic_json(directory / 'previous-control.json', control)
            api.request('PUT', '/products/sync-control', {'expected_revision': control['revision'],
                        'mode': 'shadow', 'controller_id': 'adaptive-rollout', 'lease_seconds': 0})
        elif mode == 'shadow':
            adaptive = data.get('adaptive') or {}
            since = timestamp(adaptive.get('shadow_since'))
            if (since and now - since >= 20 * 60 and not adaptive.get('automatic_hold')
                    and data['worker']['available_lane_count'] >= 5):
                subprocess.run(['systemctl', 'stop', args.import_service], check=True)
                state = json.loads((Path(args.import_directory) / 'run/state.json').read_text())
                change = handoff(control, state)
                marker = {'at': iso(now), 'shadow_seconds': now - since,
                          'imported_shops': len(change['imported_batches']),
                          'pressure_paused_shops': len(change['pressure_paused_shops'])}
                atomic_json(directory / 'handoff-intent.json', marker)
                api.request('PUT', '/products/sync-control', {'expected_revision': control['revision'],
                            'controller_id': 'adaptive-rollout', 'lease_seconds': 0, **change})
                atomic_json(directory / 'handoff.json', marker)
                subprocess.run(['systemctl', 'start', args.import_service], check=True)
                marker['importer_started_at'] = iso(time.time())
                atomic_json(directory / 'handoff.json', marker)
        elif mode == 'adaptive' and (directory / 'handoff-intent.json').exists():
            marker_path = directory / 'handoff.json'
            marker = json.loads((marker_path if marker_path.exists() else directory / 'handoff-intent.json').read_text())
            if not marker.get('importer_started_at'):
                subprocess.run(['systemctl', 'start', args.import_service], check=True)
                marker['importer_started_at'] = iso(time.time())
                atomic_json(marker_path, marker)
        result = summarize(data, now)
        report_path = Path(args.import_directory) / 'run/report.json'
        if report_path.exists():
            report = json.loads(report_path.read_text())
            result['import_summary'] = report.get('summary')
        atomic_json(directory / 'acceptance.json', result)
        print(json.dumps(result, ensure_ascii=True), flush=True)
        if result['full_cycle_verified']:
            return
        time.sleep(60)


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--target', required=True)
    parser.add_argument('--directory', required=True)
    parser.add_argument('--import-directory', required=True)
    parser.add_argument('--import-service', required=True)
    run(parser.parse_args())
