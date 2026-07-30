# Product catalog sync worker

The worker polls the public product-sync job endpoint independently of the
account-import page. It uses a persistent Playwright Chromium context that
must receive JSON from `pay.ldxp.cn`; Alibaba Cloud ESA may challenge
data-center IP addresses.

Configure trusted HTTP/HTTPS egress proxies if the Playwright session receives
the ESA slider page. A multi-lane worker requires one distinct primary endpoint
per lane and can pair the leading lanes with distinct positional fallbacks. The
six-lane production configuration is:

```env
PRODUCT_SYNC_CONCURRENCY=6
PRODUCT_SYNC_REQUEST_RATE_PER_LANE=0.75
PRODUCT_SYNC_PROXY_URLS=http://172.18.0.1:17891,http://172.18.0.1:17892,http://172.18.0.1:17893,http://172.18.0.1:17894,http://172.18.0.1:17895,http://172.18.0.1:17896
PRODUCT_SYNC_PROXY_FALLBACK_URLS=http://172.18.0.1:17897,http://172.18.0.1:17898,http://172.18.0.1:17899
PRODUCT_SYNC_WORKER_MEMORY_LIMIT=1.75g
```

`PRODUCT_SYNC_PROXY_URL` remains supported for a single-lane deployment. Pass
proxy values to the `product-sync-worker` service without committing secrets.
Fallback endpoints are optional, cannot outnumber the lanes, and all configured
endpoints (up to twelve) must be distinct. Pools are paired by position: lane 1
uses primary 1/fallback 1, and so on; trailing lanes without a positional
fallback remain primary-only. The worker
writes lane states, active proxy pool positions, actual browser context/page
counts, configured lane/global rates, last success, and last error to
`/data/status.json`. Configure the required shared token first:

```env
PUBLIC_ACCOUNT_IMPORT_PRODUCT_SYNC_TOKEN=<shared-random-token>
```

Relevant optional settings are:

```env
SHOP_REQUEST_TIMEOUT_MILLISECONDS=20000
BROWSER_PROTOCOL_TIMEOUT_MILLISECONDS=45000
BACKEND_REQUEST_TIMEOUT_MILLISECONDS=10000
CHROME_PROFILE_DIRECTORY=/data/chrome-profile
PRODUCT_SYNC_CONCURRENCY=1
PRODUCT_SYNC_REQUEST_RATE_PER_LANE=1
```

The shared token is required by both the worker and application. Single-lane
mode without a fallback uses one persistent Chromium context; a configured
fallback uses one rotatable isolated context instead. Multi-lane mode uses one
Chromium process with one isolated context and one page per lane. Each lane has
a capacity-one token bucket at `PRODUCT_SYNC_REQUEST_RATE_PER_LANE`; every
request then passes through a capacity-one shared bucket at `lane count x lane
rate`. Blocked lanes never transfer their unused allowance to active lanes, and
quotes remain sequential within each lane. Each shop publishes immediately
after every source product is classified as sellable or explicitly unavailable.

Jobs send a heartbeat every 30 seconds and refresh the worker status timestamp.
Missing heartbeats release a lease after 90 seconds, while a single attempt can
run for at most 30 minutes. A heartbeat HTTP 409 immediately cancels the stale
attempt, closes only that lane context to interrupt an in-flight browser call,
and rebuilds the lane. Stale attempts are never submitted as failures. HTTP
429/502/520, verification challenges, proxy failures, and browser transport
failures back off independently per lane for 1, 5, then 15 minutes with jitter.
After a completed pressure backoff, a lane with a fallback closes its old
context before opening one context and one page on the alternate endpoint, then
continues the same in-memory shop snapshot. The other lanes are not restarted or
sped up, and the worker never has more active contexts than configured lanes.
Multi-lane startup and context replacement are supervised per lane. Each proxy
gets two short initialization attempts before that lane tries its positional
fallback; exhausting one lane pool leaves the other browser contexts running.
Completed batches and idle workers poll again after ten seconds.

The local Compose profile applies a 1 CPU, configurable memory, and 256 PID
limit, plus `10 MiB x 3` JSON log rotation. Its health check requires a fresh
status file and at least one ready browser context/page; stopped, globally
errored, stale, and zero-context workers are unhealthy. The Compose template
defaults to `1g`; six-lane production uses a `1.75g` memory limit:

```sh
docker compose -f deploy/docker-compose.local.yml --profile public-account-import up -d
```

Authenticated SOCKS5 proxies are not supported by Chromium directly; use an
HTTP/HTTPS proxy for authenticated access.

## Rollout

Before building, protect the current and previous immutable images and remove
only unused images and BuildKit cache; never prune volumes. Abort the release
when the root filesystem has less than 10 GiB free. Back up the production
environment, Compose customization, proxy configuration, and timer state.
Pause the autodeploy timer before advancing `custom`, and pause the
health-restart timer immediately before the live switch.

Deploy the compatible worker image first with the existing two-lane/four-port
configuration and the new `1.75g` limit. After its health check passes, stop the
worker, pass the six-active-exit proxy gate, atomically update the six-lane
variables, and recreate only the worker. Within three minutes,
`/data/status.json` must
report six lanes, six contexts, six pages, a per-lane rate of `0.75`, a
global rate of `4.5`, and no container restart.

Observe two complete catalog cycles. A no-pressure cycle must finish within 23
minutes; steady RSS must remain below 1.40 GiB and peak RSS below 1.60 GiB; host
available memory must remain above 800 MiB; PID count must remain below 220;
host five-minute CPU must remain below 80%; and expired shops must remain below
10%. Require no OOM or restart, and keep the pressure-error rate at no more than
twice the two-cycle pre-switch baseline.

For a resource or throughput failure, keep the new code and nine listeners but
restore `PRODUCT_SYNC_CONCURRENCY=2`, the `0.75` per-lane rate, primaries
`17891,17892`, and fallbacks `17897,17898`. For a proxy failure, restore the
previous proxy configuration and its two-lane port layout (`17891,17892` plus
`17893,17894`). For an image failure, restore the two-lane environment and
previous Compose file before rolling back to the immutable
`32e39af6e51f9c9764ebeb72c36651dc117707a6` image.
Configuration-only rollback can resume both timers immediately. Image rollback
must leave autodeploy paused until a fix or revert passes CI and advances
`deploy/custom`, preventing the failed image from being deployed again.
