# Product synchronization proxy

`sub2api-product-proxy.service` runs a dedicated Mihomo process for product
catalog synchronization. It must not install a TUN interface, system proxy, or
global routing rules. Bind its HTTP/mixed listeners only to the Docker bridge
address and pass those listener URLs only to `product-sync-worker` through
`PRODUCT_SYNC_PROXY_URLS` and `PRODUCT_SYNC_PROXY_FALLBACK_URLS`.

For five lanes, configure primary listeners `17891-17895` and lane-local
fallback listeners `17896-17900`. Keep subscription URLs, node credentials,
probe shop tokens, and generated Mihomo configuration outside the repository
and logs, with root-only permissions. The service account only needs read
access to the generated configuration and write access to its state directory.

## Ten-exit gate

Probe candidates in subscription order through a separate temporary Mihomo
instance. Its configuration, subscription credentials, and protected shop
token must live in a root-only temporary directory. Do not alter the active
listeners while selecting candidates and do not print credentials or tokens.

A candidate qualifies only when all of these checks pass through its listener:

- `https://api.ipify.org` returns a parseable public IP address;
- the address is different from every already selected exit;
- `https://pay.ldxp.cn/` finishes with HTTP 200;
- the protected shop API returns JSON.

Prefer five primary exits from different node families. Pair fallbacks by lane,
preferring a different family, then a different region, then a different IPv4
`/24` or IPv6 `/48` from the primary. After ten candidates qualify, generate
the final listeners, validate the Mihomo configuration, back up the active
configuration, replace it atomically, and restart the dedicated service. Repeat
all four checks through each final listener. If the final check has fewer than
ten unique public exits, restore the backup immediately and keep the worker at
two lanes.

The production worker contract is deliberately bounded:

- one Chromium process and five isolated browser contexts at most;
- one product quote at a time per listener;
- 0.75 requests per second per listener and 3.75 requests per second globally,
  both with capacity one and no burst;
- independent 1, 5, and 15 minute pressure backoff per listener.
