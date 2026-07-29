# Product catalog sync worker

The worker polls the public product-sync job endpoint independently of the
account-import page. It uses a persistent Playwright Chromium context that
must receive JSON from `pay.ldxp.cn`; Alibaba Cloud ESA may challenge
data-center IP addresses.

Configure trusted HTTP/HTTPS egress proxies if the Playwright session receives
the ESA slider page. A two-lane worker requires two distinct endpoints whose
upstream exits have been verified to use different IP addresses:

```env
PRODUCT_SYNC_CONCURRENCY=2
PRODUCT_SYNC_PROXY_URLS=http://proxy-a.internal:17891,http://proxy-b.internal:17892
```

`PRODUCT_SYNC_PROXY_URL` remains supported for a single-lane deployment. Pass
proxy values to the `product-sync-worker` service without committing secrets.
The worker writes its lane states, last success, and last error to
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
```

The shared token is required by both the worker and application. Single-lane
mode uses one persistent Chromium context. Two-lane mode uses one Chromium
process with two isolated contexts and one page per context. Each lane remains
on its own proxy for an entire shop, is limited to 1 request/second without a
burst, and quotes sequentially. Each shop publishes immediately after every
source product is classified as sellable or explicitly unavailable.

Jobs send a heartbeat every 30 seconds. Missing heartbeats release a lease
after 90 seconds, while a single attempt can run for at most 20 minutes. HTTP
429, verification challenges, proxy failures, and browser transport failures
back off independently per lane for 1, 5, then 15 minutes with jitter.
Completed batches and idle workers poll again after ten seconds.

The local Compose profile applies a 1 CPU, 768 MiB memory, and 256 PID limit,
plus `10 MiB x 3` JSON log rotation:

```sh
docker compose -f deploy/docker-compose.local.yml --profile public-account-import up -d
```

Authenticated SOCKS5 proxies are not supported by Chromium directly; use an
HTTP/HTTPS proxy for authenticated access.
