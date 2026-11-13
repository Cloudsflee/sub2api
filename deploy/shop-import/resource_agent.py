"""Publish host measurements to the adaptive backend; never claim jobs or set policy."""
import argparse
import json
import time

import observe
from controller import API, worker_token


def payload(host, previous):
    total = host['cpu_total'] - previous['cpu_total']
    idle = host['cpu_idle'] - previous['cpu_idle']
    if total <= 0 or idle < 0:
        raise ValueError('CPU counters restarted')
    return {
        'at': host['at'], 'worker_id': host['worker_id'],
        'worker_memory_ratio': host['worker_memory_ratio'],
        'host_available_ratio': host['host_available_ratio'],
        'cpu_ratio': max(0, min(1, 1 - idle / total)),
        'disk_free_bytes': host['disk_free_bytes'],
        'worker_oom_kill': host['worker_oom_kill'], 'host_oom_kill': host['host_oom_kill'],
        'upstream_events': host['events']['rate_limit'] + host['events']['access_denied'],
    }


def run(target):
    api = API(target, worker_token())
    previous = None
    while True:
        started = time.monotonic()
        try:
            host = observe.sample(previous['at'] if previous else None)
            if previous:
                result = api.request('POST', '/products/sync-resources', payload(host, previous))
                print(json.dumps({'at': host['at'], **result}), flush=True)
            previous = host
        except Exception as error:
            # Backend stops new automatic dispatch when this stream is stale.
            # Do not log credentials or upstream response bodies.
            print(json.dumps({'observation_failed': type(error).__name__}), flush=True)
            previous = None
        time.sleep(max(1, 60 - (time.monotonic() - started)))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--target', required=True)
    run(parser.parse_args().target)
