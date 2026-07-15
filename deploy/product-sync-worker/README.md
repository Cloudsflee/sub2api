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
`/data/status.json`. Relevant optional settings are:

```env
SHOP_REQUEST_TIMEOUT_MILLISECONDS=20000
BROWSER_PROTOCOL_TIMEOUT_MILLISECONDS=45000
BACKEND_REQUEST_TIMEOUT_MILLISECONDS=10000
VERIFICATION_COOLDOWN_MILLISECONDS=900000
CHROME_PROFILE_DIRECTORY=/data/chrome-profile
PRODUCT_SYNC_CONCURRENCY=3
```

The worker leases up to three different shops per batch by default. The
backend lease prevents another browser page from receiving the same shop while
that job is active. Concurrency is capped at five to limit Chromium and ESA
pressure.

Authenticated SOCKS5 proxies are not supported by Chromium directly; use an
HTTP/HTTPS proxy for authenticated access.
