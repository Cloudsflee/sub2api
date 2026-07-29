# Product synchronization proxy

`sub2api-product-proxy.service` runs a dedicated Mihomo process for product
catalog synchronization. It must not install a TUN interface, system proxy, or
global routing rules. Bind its HTTP/mixed listeners only to the Docker bridge
address and pass those listener URLs only to `product-sync-worker` through
`PRODUCT_SYNC_PROXY_URLS`.

For two lanes, configure two listeners that select distinct fixed-exit nodes,
then verify their public IP addresses differ before enabling the worker. Keep
the subscription URL and generated Mihomo configuration outside the repository
with root-only permissions. The service account only needs read access to the
generated configuration and write access to its state directory.

The production worker contract is deliberately bounded:

- one Chromium process and two isolated browser contexts at most;
- one product quote at a time per listener;
- one request per second per listener, with no burst;
- independent 1, 5, and 15 minute pressure backoff per listener.
