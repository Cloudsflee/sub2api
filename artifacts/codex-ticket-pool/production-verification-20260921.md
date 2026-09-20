# Production Codex Ticket Pool Verification

Date: 2026-09-21 (Asia/Shanghai)

The active blue/green slot is `green` at `HOST:8080`, running commit
`dc391257aa19b1a85f76a696d961f683ac9fff04`. The administrator key was read
from the local, ACL-restricted file outside the repository and was not added
to a source file, image, or commit.

## Baseline

- Remote snapshot: `/var/lib/sub2api-product-proxy/codex-ticket-pool-prewrite-20260921.json`
- Snapshot SHA-256: `560345fe49f6bcdaba666d3348ab8039e42e6406cf98fb17cacd73001ada0f12`
- Before the write, the persisted harvest setting was a single URL.
- Target account proxy bindings were `ACCOUNT_A -> 1` and `ACCOUNT_B -> 1`.

## Modified State

- `openai_codex_ticket_harvest_proxy_url`: 8 non-empty listener lines.
- `openai_codex_ticket_sync_business_proxy`: `false`.
- Persisted listener entry hashes, in round-robin order:
  `6b006669a61c2f6d`, `2f4836ca91300b54`, `973afb369ae83b32`,
  `9808eeb192fb10b8`, `e6d13724ee59efb6`, `610b2d363cb3d84d`,
  `fce2a31f43eec63f`, `8bb894765d76b0a0`.
- Account proxy bindings remained `ACCOUNT_A -> 1` and `ACCOUNT_B -> 1`.

## Verification Commands and Results

| Check | Result |
| --- | --- |
| Admin settings PUT through `HOST:8080` | HTTP `200`, response `code=0`, `success` |
| Admin settings GET | HTTP `200`, pool count `8`, linkage `False`, ticket enabled `True` |
| Application health | HTTP `200`, `{"status":"ok"}` |
| Containers | `green`, product-sync worker, PostgreSQL, and Redis healthy |
| Deployment state | `ACTIVE_SLOT=green`, deployed commit `dc391257aa19b1a85f76a696d961f683ac9fff04` |
| Ticket scope log | `eligible_accounts=2`, `eligible_pairs=2`, `ready_pairs=2` |
| ACCOUNT_A Astra ticket | prefix `gAAAAA`, state length `292`, persisted length `292` |
| ACCOUNT_B Astra ticket | prefix `gAAAAA`, state length `292`, persisted length `292` |
| Sol ticket | existing `gAAAAA`/`292` record retained and excluded from the multi-entry gate |

The harvester logged all eight entries available with zero cooling entries
after the successful probe. This satisfies the configured stop condition for
the current Astra scope; the resident refresh loop remains enabled for expiry
renewal.

## Rollback

Use the pre-write snapshot and
`artifacts/codex-ticket-pool/rollback.ps1`. The rollback helper now sends the
administrator credential in `x-api-key`, restores the prior URL value, and can
restore the single-entry business-linkage behavior with
`-RestoreBusinessLinkage`. Keep the snapshot until one complete renewal cycle
has been observed.

