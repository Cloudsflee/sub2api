# Product synchronization proxy

`sub2api-product-proxy.service` runs a dedicated Mihomo process for product
catalog synchronization. It must not install a TUN interface, system proxy, or
global routing rules. Bind its HTTP/mixed listeners only to the Docker bridge
address and pass those listener URLs only to `product-sync-worker` through
`PRODUCT_SYNC_PROXY_URLS` and `PRODUCT_SYNC_PROXY_FALLBACK_URLS`.

For six lanes, configure primary listeners `17891-17896` and three lane-local
fallback listeners `17897-17899`. Keep subscription URLs, node credentials,
probe shop tokens, and generated Mihomo configuration outside the repository
and logs, with root-only permissions. The service account only needs read
access to the generated configuration and write access to its state directory.

## Six-active-exit gate

Probe candidates in subscription order through a separate temporary Mihomo
instance. Its configuration, subscription credentials, and protected shop
token must live in a root-only temporary directory. Do not alter the active
listeners while selecting candidates and do not print credentials or tokens.

A candidate qualifies only when all of these checks pass through its listener:

- `https://api.ipify.org` returns a parseable public IP address;
- primary addresses are different from every already selected primary exit;
- `https://wzyp.cn/` finishes with HTTP 200;
- the protected shop API returns JSON.

Prefer six primary exits from different node families. Pair the three fallbacks
with lanes 1-3 by position, preferring a different family, then a different
region, then a different IPv4
`/24` or IPv6 `/48` from the primary. Prefer globally distinct fallback IPs,
but require each fallback only to differ from its paired primary because the
fallback is inactive until that lane rotates. After all nine endpoints qualify,
generate the final listeners, validate the Mihomo configuration, back up the
active configuration, replace it atomically, and restart the dedicated service. Repeat
all four checks through each final listener. Restore the backup immediately and
keep the worker at two lanes if the six final primary listeners have fewer than
six unique public exits or a fallback shares its paired primary IP.

The production worker contract is deliberately bounded:

- one Chromium process and six isolated browser contexts at most;
- one product quote at a time per listener;
- 0.75 requests per second per listener and 4.5 requests per second globally,
  both with capacity one and no burst;
- independent 1, 5, and 15 minute pressure backoff per listener.

## Codex 292 listener discovery

`tools/discover_codex_ticket_pool.py` reuses the listeners declared in the
active Mihomo file without changing that file or the product worker. The
default config path is `/etc/sub2api-product-proxy/config.yaml`; only HTTP,
mixed, and SOCKS listeners bound to the application network are considered.
Each candidate must reference a complete node, return a public egress from the
egress probe, and return a successful status from the health probe. Egress
identities are recorded as stable hashes, and credentials are never printed.

Inspect the pool first:

```bash
python3 /opt/sub2api/tools/discover_codex_ticket_pool.py \
  --config /etc/sub2api-product-proxy/config.yaml \
  --network 172.18.0.0/16 \
  --output /var/lib/sub2api-product-proxy/codex-ticket-pool.json
```

After the report has been reviewed, pass an admin API token and explicitly
write the selected listeners. The PUT is a partial settings update, so the
application's settings coordinator performs the durable write and runtime
cache refresh. With more than one selected listener it also persists
`openai_codex_ticket_sync_business_proxy=false`, preserving the existing
business `proxy_id` values:

```bash
SUB2API_ADMIN_TOKEN='TOKEN' \
python3 /opt/sub2api/tools/discover_codex_ticket_pool.py \
  --write --api-url http://127.0.0.1:8080 \
  --config /etc/sub2api-product-proxy/config.yaml \
  --network 172.18.0.0/16
```

The script never targets `api.sub2api.com`. Set `CODEX_EGRESS_URL` and
`CODEX_HEALTH_URL` to operator-approved probe endpoints when the defaults are
not reachable. Keep the redacted report and the database settings/account
snapshot until every target account has a persisted HTTP 200, `gAAAAA`, exact
292 ticket; restore the prior setting through the same admin endpoint before
rolling back an image. The gateway emits a redacted pool status each harvest
cycle (`pool_size`, available/cooling counts, entry hash, HTTP status, state
length, and failure category such as `transport`, `length_312`, or
`incomplete`) and exposes the same snapshot through
`OpenAICodexTicketHarvestPoolStatus`. A bounded operator monitor can stop when
`OpenAICodexTicketHarvestCompletion.Complete` is true; the resident harvester
itself remains running for expiry refreshes.
