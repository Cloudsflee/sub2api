# Product catalog sync worker

The worker polls the public product-sync job endpoint independently of the
account-import page. Each lane uses an isolated Playwright Chromium context
that must receive JSON from `pay.ldxp.cn`; Alibaba Cloud ESA may challenge
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
PRODUCT_SYNC_CHALLENGE_AUTO_SOLVE=true
PRODUCT_SYNC_CHALLENGE_SESSION_DIR=/data/challenge-sessions
PRODUCT_SYNC_CHALLENGE_TIMEOUT_MILLISECONDS=90000
```

`PRODUCT_SYNC_PROXY_URL` remains supported for a single-lane deployment. Pass
proxy values to the `product-sync-worker` service without committing secrets.
Fallback endpoints are optional, cannot outnumber the lanes, and all configured
endpoints (up to twelve) must be distinct. Pools are paired by position: lane 1
uses primary 1/fallback 1, and so on; trailing lanes without a positional
fallback remain primary-only. The worker writes lane states, active proxy pool
positions, actual browser context/page counts, configured lane/global rates,
challenge state, last success, and last error to `/data/status.json`. Configure
the required shared token first:

```env
PUBLIC_ACCOUNT_IMPORT_PRODUCT_SYNC_TOKEN=<shared-random-token>
```

Relevant optional settings are:

```env
SHOP_REQUEST_TIMEOUT_MILLISECONDS=20000
BROWSER_PROTOCOL_TIMEOUT_MILLISECONDS=45000
BACKEND_REQUEST_TIMEOUT_MILLISECONDS=10000
PRODUCT_SYNC_CONCURRENCY=1
PRODUCT_SYNC_REQUEST_RATE_PER_LANE=1
PRODUCT_SYNC_CHALLENGE_AUTO_SOLVE=false
PRODUCT_SYNC_CHALLENGE_SESSION_DIR=/data/challenge-sessions
PRODUCT_SYNC_CHALLENGE_TIMEOUT_MILLISECONDS=90000
```

The shared token is required by both the worker and application. Automatic
challenge recovery is disabled by default in deployment templates and must be
explicitly enabled in production. Every mode uses one Chromium process with
one isolated context and one page per lane. Each lane has
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
429/502/520, proxy failures, and browser transport failures back off
independently per lane for 1, 5, then 15 minutes with jitter. Verification
failures use a separate 15-minute, 60-minute, then 6-hour backoff. Unsupported
verification types use the 6-hour tier immediately. After a completed pressure
backoff, a lane with a fallback closes its old context before opening one
context and one page on the alternate endpoint, then continues the same
in-memory shop snapshot. The other lanes are not restarted or sped up, and the
worker never has more active contexts than configured lanes.
Multi-lane startup and context replacement are supervised per lane. Each proxy
gets two short initialization attempts before that lane tries its positional
fallback; exhausting one lane pool leaves the other browser contexts running.
Completed batches and idle workers poll again after ten seconds.

## Challenge recovery

The built-in provider registry supports Alibaba Cloud ESA (`http_custom`, ESA
scripts/DOM, and verification copy) and generic visible slide-to-end controls
on pages already classified as verification pages. It does not attempt puzzle,
click-selection, SMS, or ordinary page sliders. Detection scans the main page
and child frames, including mobile/H5 layouts. A normal page accompanied only
by `denied by intelligent_cc_acl` is accepted.

The live slider geometry determines the drag distance. For example, a 360 px
track with a 40 px handle produces a 320 px movement; no fixed distance is
configured. Each attempt uses 36-60 nonlinear steps over 900-1600 ms with
small vertical variation and an end correction. A context gets at most two
drag attempts and reloads the verification page before the second. All lanes
share one cancellable lock, so only one lane can drag at a time. After dragging,
the manager requests the home page again and requires the verification DOM,
title, copy, and `http_custom` denial to be gone.

At startup, each context restores the storage state for its provider, target
origin, and proxy identity before validating the home page. Successful states
are written atomically under `/data/challenge-sessions` with directory mode
`0700` and file mode `0600`; filenames are SHA-256 digests and never expose
proxy credentials. Invalid files are removed and rebuilt. Images, fonts, and
media are allowed only while solving, then the normal resource block is
restored. If a shop API returns HTML during a sync, the lane solves in the same
context and retries only that failed API call, preserving completed catalog
work. After two failed drags it moves directly to its lane-local fallback.

`/data/status.json` exposes `challenge_auto_solve_enabled` globally and
`challenge_provider`, `challenge_state`, `challenge_attempt`,
`challenge_started_at`, `challenge_solved_at`, and `session_restored` for each
lane.

The local Compose profile applies a 1 CPU, configurable memory, and 256 PID
limit, plus `10 MiB x 3` JSON log rotation. Its health check requires a fresh
status file and at least one ready browser context/page; stopped, globally
errored, and stale workers are unhealthy. Zero-context status is accepted only
while automatic challenge recovery is actively queued/solving or every lane is
in a verification-specific backoff with a future `retry_at`; backoff status is
refreshed every 30 seconds to prevent restart loops. The Compose template
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
and challenge variables, and recreate only the worker. The first lane must be
ready within three minutes and all six lanes within twelve minutes.
`/data/status.json` must
report six lanes, six contexts, six pages, a per-lane rate of `0.75`, a
global rate of `4.5`, challenge auto-solve enabled, and no container restart.

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
Disable `PRODUCT_SYNC_CHALLENGE_AUTO_SOLVE` when rolling back challenge
recovery; saved session files can remain on disk.
Configuration-only rollback can resume both timers immediately. Image rollback
must leave autodeploy paused until a fix or revert passes CI and advances
`deploy/custom`, preventing the failed image from being deployed again.
