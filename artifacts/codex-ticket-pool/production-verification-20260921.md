# Production Codex Ticket Pool Verification

Date: 2026-09-21 (Asia/Shanghai)

The active blue/green slot is `green` at `HOST:8080`, running commit
`72f160df9df381d2cf2975a4a2c45592166eeb35`. The administrator key was read
from the local, ACL-restricted file outside the repository and was not added
to a source file, image, or commit.

## Baseline

- Remote snapshot: `/var/lib/sub2api-product-proxy/codex-ticket-pool-prewrite-20260921.json`
- Snapshot SHA-256: `560345fe49f6bcdaba666d3348ab8039e42e6406cf98fb17cacd73001ada0f12`
- Before the write, the persisted harvest setting was a single URL.
- Target account proxy bindings were `ACCOUNT_A -> 1` and `ACCOUNT_B -> 1`.
- Pool expansion snapshot: `/var/lib/sub2api-product-proxy/codex-ticket-pool-preexpand-20260921.json`
  (SHA-256 `c873ed8c0a0da036e09b6dd4db90f1f6657befa0a3a22e6a8b6d7abacb3187bd`).
- Final pre-temporary snapshot: `/var/lib/sub2api-product-proxy/codex-ticket-pool-pretemporary-20260921.json`
  (SHA-256 `fa2feec0251f97721127d3ad9656a6b2e1769e632d6aeb1d2f4b21044380a1ee`).

## Modified State

- `openai_codex_ticket_harvest_proxy_url`: 9 non-empty listener lines.
- `openai_codex_ticket_sync_business_proxy`: `false`.
- Persisted listener entry hashes, in round-robin order:
  `6b006669a61c2f6d`, `2f4836ca91300b54`, `973afb369ae83b32`,
  `85e16293c9c829fa`, `9808eeb192fb10b8`, `e6d13724ee59efb6`,
  `610b2d363cb3d84d`, `fce2a31f43eec63f`, `8bb894765d76b0a0`.
- Account proxy bindings remained `ACCOUNT_A -> 1` and `ACCOUNT_B -> 1`.
- The production release copy of the discovery tool matches the repository
  at SHA-256 `0c6b9e5de7578bb3b37db46402118291204c23234f95b1872e99fec9d34ce4be`.

## Verification Commands and Results

| Check | Result |
| --- | --- |
| Admin settings PUT through `HOST:8080` | HTTP `200`, response `code=0`, `success` |
| Admin settings GET | HTTP `200`, pool count `9`, linkage `False`, ticket enabled `True` |
| Application health | HTTP `200`, `{"status":"ok"}` |
| Containers | active `green`, product-sync worker, PostgreSQL, and Redis healthy; old `blue` retained stopped |
| Deployment state | `ACTIVE_SLOT=green`, deployed commit `72f160df9df381d2cf2975a4a2c45592166eeb35` |
| Ticket scope log | `eligible_accounts=2`, `eligible_pairs=2`, `ready_pairs=2` at the final cycle |
| ACCOUNT_A Astra ticket | prefix `gAAAAA`, state length `292`, persisted length `292`, unexpired |
| ACCOUNT_B Astra ticket | prefix `gAAAAA`, state length `292`, persisted length `292`, unexpired |
| Sol path | repeated `gpt-5.6-sol` requests logged `decision=allow`; Sol is outside the multi-entry gate |
| Temporary route cleanup | one transport-only test route was removed; relay, gateway, SSH tunnel, and port `19091` closed |

The final harvester cycle logged ACCOUNT_A harvested at length `292`, pool
size `9`, `available_entries=9`, `cooling_entries=0`, and
`ready_pairs=2`. This satisfies the configured stop condition for the current
Astra scope; the resident refresh loop remains enabled for expiry renewal.

## Rollback

Use the pre-temporary snapshot and
`artifacts/codex-ticket-pool/rollback.ps1`. The rollback helper now sends the
administrator credential in `x-api-key`, restores the prior URL value, and can
restore the single-entry business-linkage behavior with
`-RestoreBusinessLinkage`. Keep the snapshot until one complete renewal cycle
has been observed.
