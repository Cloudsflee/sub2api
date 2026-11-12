"""Read-only host pressure sample. Run on the operator's Docker host.

No job-claiming endpoint is used. Credentials never leave this process.
"""
import datetime as dt
import http.client
import json
import pathlib
import re
import shutil
import socket
import subprocess
import time


def utcnow():
    return dt.datetime.now(dt.timezone.utc).isoformat()


def command(*args):
    return subprocess.run(args, capture_output=True, text=True, check=True, timeout=25).stdout


class DockerHTTP(http.client.HTTPConnection):
    def connect(self):
        self.sock = socket.socket(socket.AF_UNIX, socket.SOCK_STREAM)
        self.sock.settimeout(25)
        self.sock.connect('/var/run/docker.sock')


def docker(path):
    conn = DockerHTTP('localhost', timeout=25)
    try:
        conn.request('GET', path)
        response = conn.getresponse()
        if response.status != 200:
            raise RuntimeError('Docker observation failed: ' + str(response.status))
        return json.load(response)
    finally:
        conn.close()


def sample(since=None):
    now = utcnow()
    worker = docker('/containers/sub2api-product-sync-worker/json')
    stats = docker('/containers/sub2api-product-sync-worker/stats?stream=false&one-shot=true')
    memory = stats['memory_stats']
    inactive = memory.get('stats', {}).get('inactive_file', memory.get('stats', {}).get('total_inactive_file', 0))
    working = max(0, memory.get('usage', 0) - inactive)
    host_mem = {k: int(v.split()[0]) * 1024 for k, v in
                (line.split(':', 1) for line in pathlib.Path('/proc/meminfo').read_text().splitlines())}
    cpu = list(map(int, pathlib.Path('/proc/stat').read_text().splitlines()[0].split()[1:9]))
    oom = {}
    pid = worker['State']['Pid']
    for line in pathlib.Path(f'/proc/{pid}/cgroup').read_text().splitlines():
        if line.startswith('0::'):
            event_path = pathlib.Path('/sys/fs/cgroup') / line[3:].lstrip('/') / 'memory.events'
            oom = dict((k, int(v)) for k, v in (row.split() for row in event_path.read_text().splitlines()))
    vmstat = dict(row.split() for row in pathlib.Path('/proc/vmstat').read_text().splitlines())
    events = {'rate_limit': 0, 'access_denied': 0, 'network': 0, 'synced': 0}
    if since:
        logs = subprocess.run(['docker', 'logs', '--since', since, '--until', now,
                               'sub2api-product-sync-worker'], capture_output=True, text=True, timeout=25, check=True)
        for line in (logs.stdout + logs.stderr).splitlines():
            if 'kind=rate_limit' in line:
                events['rate_limit'] += 1
            if 'kind=access_denied' in line:
                events['access_denied'] += 1
            if 'kind=network' in line:
                events['network'] += 1
            if re.search(r' synced .*: \d+/\d+ sellable products', line):
                events['synced'] += 1
    raw_status = json.loads(pathlib.Path('/opt/sub2api/product_worker_data/status.json').read_text())
    status_keys = ('state', 'updated_at', 'last_success_at', 'sync_concurrency', 'adaptive_rate_per_second',
                   'global_pressure_state', 'global_pressure_until', 'global_silence_until', 'last_pressure_kind')
    status = {key: raw_status.get(key) for key in status_keys}
    lane_keys = ('index', 'state', 'context_ready', 'updated_at', 'retry_at', 'challenge_state', 'egress_circuit_open_until')
    status['lanes'] = [{key: lane.get(key) for key in lane_keys} for lane in raw_status.get('lanes', [])]
    active = command('/usr/local/sbin/sub2api-haproxy-config', '--current').strip()
    app = docker('/containers/sub2api-' + active + '/json')
    return {
        'at': now, 'worker_id': worker['Id'], 'worker_started_at': worker['State']['StartedAt'],
        'worker_running': worker['State']['Running'], 'worker_oom_killed': worker['State']['OOMKilled'],
        'worker_working_set_bytes': working, 'worker_limit_bytes': memory.get('limit', 0),
        'worker_memory_ratio': working / memory['limit'] if memory.get('limit') else None,
        'worker_oom_kill': oom.get('oom_kill', 0), 'host_oom_kill': int(vmstat.get('oom_kill', 0)),
        'host_available_ratio': host_mem['MemAvailable'] / host_mem['MemTotal'],
        'host_available_bytes': host_mem['MemAvailable'], 'cpu_total': sum(cpu), 'cpu_idle': cpu[3] + cpu[4],
        'disk_free_bytes': shutil.disk_usage('/opt/sub2api').free, 'events': events, 'worker': status,
        'active_slot': active, 'app_image': app['Config']['Image'], 'app_started_at': app['State']['StartedAt'],
    }


if __name__ == '__main__':
    import argparse
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', type=pathlib.Path)
    parser.add_argument('--minutes', type=int, default=0)
    args = parser.parse_args()
    previous = None
    started = time.monotonic()
    while True:
        result = sample(previous)
        line = json.dumps(result, ensure_ascii=False)
        if args.output:
            with args.output.open('a', encoding='utf-8') as stream:
                stream.write(line + '\n')
        else:
            print(line, flush=True)
        previous = result['at']
        if time.monotonic() - started >= args.minutes * 60:
            break
        time.sleep(60)
