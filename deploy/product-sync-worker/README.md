# Product catalog sync worker

The worker polls the public product-sync job endpoint independently of the
account-import page. Each lane uses an isolated persistent Playwright context
backed by Camoufox/Firefox in production; Alibaba Cloud ESA may challenge
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
PRODUCT_SYNC_BROWSER=camoufox
CAMOUFOX_PATH=/opt/camoufox/camoufox
CAMOUFOX_FIREFOX_VERSION=152.0
PRODUCT_SYNC_CHALLENGE_AUTO_SOLVE=true
PRODUCT_SYNC_CHALLENGE_NATIVE_DRAG=true
PRODUCT_SYNC_CHALLENGE_NATIVE_DRAG_DEBUG=false
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

The application reads the same host-mounted file at
`/app/data/product-sync-worker/status.json` and includes a sanitized six-lane
summary in the public `/api/v1/public/account-import/products` response. The
shop page shows every lane to anonymous visitors as **Available** or
**Unavailable**; unavailable lanes include the worker error, challenge state,
or missing/stale heartbeat reason. Set
`PUBLIC_ACCOUNT_IMPORT_PRODUCT_SYNC_STATUS_FILE` on the application only when
the shared mount uses a different path. Proxy credentials are never included
in the public summary.

Relevant optional settings are:

```env
SHOP_REQUEST_TIMEOUT_MILLISECONDS=20000
BROWSER_PROTOCOL_TIMEOUT_MILLISECONDS=45000
BACKEND_REQUEST_TIMEOUT_MILLISECONDS=10000
PRODUCT_SYNC_CONCURRENCY=1
PRODUCT_SYNC_REQUEST_RATE_PER_LANE=1
PRODUCT_SYNC_CHALLENGE_AUTO_SOLVE=false
PRODUCT_SYNC_CHALLENGE_NATIVE_DRAG=false
PRODUCT_SYNC_CHALLENGE_NATIVE_DRAG_DEBUG=false
PRODUCT_SYNC_CHALLENGE_SESSION_DIR=/data/challenge-sessions
PRODUCT_SYNC_CHALLENGE_TIMEOUT_MILLISECONDS=90000
PRODUCT_SYNC_BROWSER_PROFILE_DIR=/data/browser-profiles
PRODUCT_SYNC_BROWSER=camoufox
CAMOUFOX_PATH=/opt/camoufox/camoufox
CAMOUFOX_FIREFOX_VERSION=152.0
```

The shared token is required by both the worker and application. Automatic
challenge recovery is disabled by default in deployment templates and must be
explicitly enabled in production. Each lane uses one headed, persistent
Camoufox profile with one context and one page; the container supplies Xvfb so
no host display is required. This avoids the incognito fingerprint that ESA
rejects. Set `PRODUCT_SYNC_BROWSER=chromium` only for a rollback; the image
retains Chrome as a compatibility fallback. The worker entrypoint removes only
display 99's stale Xvfb lock and socket, then waits for a successful X11 request
before starting Node, so a Docker restart can safely reuse the container
filesystem. Camoufox disables Firefox's back/forward document cache and content
process prelaunch and caps its memory cache at 16 MiB, preventing a solved ESA
document from remaining resident beside each lane's normal shop page.
Each lane retains its capacity-one token bucket at
`PRODUCT_SYNC_REQUEST_RATE_PER_LANE`. Every request then passes through a
no-burst adaptive global limiter that starts at `1.5 req/s`, floors at `0.5`,
and caps at `2.0`. A 403 or 429 halves the global rate. After 100 consecutive
successful API responses and five pressure-free minutes, the rate rises by
`0.1 req/s`. Quotes remain sequential within a shop and share a worker-wide
concurrency limit of two. Each shop publishes immediately
after every source product is classified as sellable or explicitly unavailable.

A non-JSON 403 is reviewed against the shop home page once. A clear home page
classifies the response as ESA/CC access denial, opens that egress circuit for
10 minutes, waits 60 seconds, and selects a lane-local fallback whose circuit
is closed. Two different egresses denied within 60 seconds force a 120-second
global silence and the `0.5 req/s` floor. HTTP 429 waits for `Retry-After` when
present. TLS/connect timeouts are counted per egress and rotate after two
consecutive failures; a successful response clears only that egress counter.
Transaction-closed shops keep their last successful snapshot and are deferred
for 60 minutes instead of publishing an empty catalog.

Jobs send a heartbeat every 30 seconds and refresh the worker status timestamp.
Missing heartbeats release a lease after 90 seconds, while a single attempt can
run for at most 30 minutes. A heartbeat HTTP 409 immediately cancels the stale
attempt and retains that lane's browser context and verified ESA session. An
in-flight shop request keeps browser `fetch` and `response.text()` under one
configured request deadline. A browser-side timeout reports the API path and
`fetch` or `response_body` phase as ordinary network pressure; only the outer
Playwright protocol guard recreates a lane when `page.evaluate` itself cannot
return. After the job deadline, the expired result is discarded before the
lane polls another job. Stale attempts are never submitted as failures. HTTP
429/502/520, proxy failures, and browser transport failures back off
independently per lane for 1, 5, then 15 minutes with jitter. Verification
failures use a separate 15-minute, 60-minute, then 6-hour backoff. Unsupported
verification types use the 6-hour tier immediately. After the first pressure
backoff the lane reloads its current verified page. If the same operation fails
again, a lane with a fallback closes its old context before opening one context
and one page on the alternate endpoint, then continues the same in-memory shop
snapshot. The other lanes are not restarted or sped up, and the worker never
has more active contexts than configured lanes.
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
configured. Before pressing, the pointer pauses near the right side of the
track, approaches the handle through 30-42 native events over 1100-1700 ms,
and settles on the handle for 800-1400 ms. The pressed movement supports 36-60
steps (50-60 by default); its main 320 px phase takes 1000-1400 ms, follows a
measured late-acceleration curve with smooth vertical variation, then moves
24-42 px beyond the clamped track and holds for 550-850 ms before release.
Movement uses absolute deadlines within each phase, so browser protocol
latency does not accumulate once per step. These ranges come from a successful
server-side noVNC recording: replaying the complete pre-drag history passed,
and the parameterized model then passed fresh ESA challenges with two distinct
Camoufox fingerprints. A context gets at most two drag attempts and reloads the
verification page before the second. All lanes share one cancellable lock, so
only one lane can drag at a time. The 90-second
per-context recovery budget starts after that lock is acquired; queued lanes
remain immediately cancellable without losing their own solve budget. After
dragging, the manager requests the home page again and requires the verification
DOM, title, copy, and `http_custom` denial to be gone.

When `PRODUCT_SYNC_CHALLENGE_NATIVE_DRAG=true`, the headed Camoufox window is
focused and the same trajectory is emitted through X11 with `xdotool`. All six
Xvfb windows occupy the same screen coordinates, so `page.bringToFront()` alone
is not sufficient: the worker maps the persistent profile to its parent
Camoufox PID through `/proc`, finds that PID's visible X11 window, raises it,
and waits for focus before pressing the mouse. The viewport-to-screen offset is
estimated from the live window geometry and then calibrated with one harmless
trusted native move (so X11 decorations/toolbars are accounted for); repeated
pixel coordinates are skipped. If native input is unavailable, the worker
falls back to Playwright's protocol mouse without dropping the lane.

Immediately after launch, each PID-owned window is resized to `1024x824`
without raising or focusing it, producing a real `1024x768` content viewport.
This operation remains outside the challenge input path and cannot steal focus
from the lane holding the challenge lock. Firefox ignores both persisted
`xulstore.json` geometry and `--width`/`--height` under the worker's Xvfb setup,
so the resize is applied to the live X11 window. An isolated cgroup measurement
reduced one representative browser from 312.6 MB above baseline to 273.5 MB,
about 39.1 MB per lane.

At startup, each context restores the storage state for its provider, target
origin, and proxy identity before validating the home page. Successful states
are written atomically under `/data/challenge-sessions` with directory mode
`0700` and file mode `0600`; filenames are SHA-256 digests and never expose
proxy credentials. Camoufox fingerprint seeds are derived from the lane and
hashed proxy identity rather than the ephemeral container hostname, so a
recreated worker keeps the profile identity it previously verified. Each state
carries a non-secret summary of its provider,
target origin, proxy server, and hashed proxy identity; invalid or mismatched
files are removed and rebuilt. Images, fonts, and
media are allowed only while solving, then the normal resource block is
restored. New lanes navigate and post directly to the canonical
`https://wzyp.cn` origin. Sessions saved for the legacy `https://pay.ldxp.cn`
origin remain reusable when they contain a valid canonical-origin clearance
cookie. If a shop API returns HTML during a sync, the lane solves in the same
context and preserves completed catalog work. When ESA's verified callback
completes the original API request on `https://wzyp.cn`, the worker consumes
that same-path JSON response directly instead of discarding the one-time
approval and replaying the request. Other callback shapes retain the normal
retry path. Protected shop requests use form encoding so ESA's callback retains
the original token and request fields. Trusted shop-to-callback 301/302/303
handoffs are exposed to the browser as 307 so both hops remain POST requests.
While a restored page remains on the legacy origin, later API requests use the
same browser context's cookie-sharing request client and manually preserve POST
bodies across redirects between the two trusted origins. Product links returned
on either origin are canonicalized to the exact
`wzyp.cn/item/<goods_key>` form before publication. Existing stored legacy shop
links remain valid inputs and are left unchanged; their refreshed product links
use the canonical origin.
Each chained verification stage permits two failed drags; a completed API call
resets that budget before the next shop endpoint. Exhausting a stage moves
directly to the lane-local fallback.
ESA callback navigation is allowed up to 25 seconds. Firefox
`NS_BINDING_ABORTED` during a callback handoff is treated as an ESA-owned
navigation and followed by a fresh document inspection instead of an immediate
lane failure.

`/data/status.json` exposes `challenge_auto_solve_enabled` globally and
`challenge_provider`, `challenge_state`, `challenge_attempt`,
`challenge_started_at`, `challenge_solved_at`, and `session_restored` for each
lane. It also publishes `adaptive_rate_per_second`, `global_pressure_state`,
`global_pressure_until`, `global_silence_until`, active `egress_circuits`, and
the last sanitized `waf_response_fingerprint`. Lane entries include an opaque
`egress_id` and `egress_circuit_open_until`; proxy credentials, cookies, tokens,
and signature parameters are excluded.

The local Compose profile applies a 1 CPU, configurable memory, and a 1024 PID
limit (override with `PRODUCT_SYNC_WORKER_PIDS_LIMIT` for smaller deployments),
plus `10 MiB x 3` JSON log rotation. Its health check requires a fresh
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
report six lanes, six contexts, six pages, an adaptive global rate within
`0.5-2.0`, global quote concurrency of two, challenge auto-solve enabled, and
no container restart.

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
