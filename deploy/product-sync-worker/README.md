# Product catalog sync worker

The worker polls the public product-sync job endpoint independently of the
account-import page. It uses a persistent Playwright Chromium context that
must receive JSON from `pay.ldxp.cn`; Alibaba Cloud ESA may challenge
data-center IP addresses.

Configure a trusted HTTP/HTTPS egress proxy if the Playwright session receives
the ESA slider page:

```env
PRODUCT_SYNC_PROXY_URL=http://user:password@proxy.example:8080
```

Pass the variable to the `product-sync-worker` service without committing its
value. The worker writes its current state, last success, and last error to
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
PRODUCT_SYNC_CONCURRENCY=2
```

The shared token is required by both the worker and application. The worker
uses one persistent Chromium context with exactly two pages. Shop requests are
globally limited to 3 requests/second with a burst of two, and quote requests
have a global concurrency of two. A shop publishes only after every source
product is classified as sellable or explicitly unavailable.

Jobs send a heartbeat every 30 seconds. Missing heartbeats release a lease
after 90 seconds, while a single attempt can run for at most 20 minutes. HTTP
429 and verification challenges back off for 1, 5, then 15 minutes with
jitter. Completed batches poll again after one second; idle workers poll every
ten seconds.

The local Compose profile applies a 1 CPU, 768 MiB memory, and 256 PID limit,
plus `10 MiB x 3` JSON log rotation:

```sh
docker compose -f deploy/docker-compose.local.yml --profile public-account-import up -d
```

Authenticated SOCKS5 proxies are not supported by Chromium directly; use an
HTTP/HTTPS proxy for authenticated access.
