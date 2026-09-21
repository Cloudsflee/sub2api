# 0.2.7 Integration Candidate

Baseline: production branch commit `4d1a9caa5` (parent production release
`72f160df9`). Upstream parent: `1c0a69c0c`.

The candidate merge commit is recorded in the repository history. The rollout
policy is intentionally harvest-only:

- `gateway.openai_codex_ticket.enabled=false`.
- `openai_codex_ticket_sync_business_proxy=false` for new and missing settings.
- Existing business proxy bindings are left unchanged.
- The 292-ticket pool is an observation/compatibility asset, never a release
  gate or a business route.
- Fingerprint convergence is opt-in. The rollout script changes only the two
  operator-supplied account IDs to `device`; it never writes a seed and checks
  that both accounts still use `proxy_id=1`.

Use `rollout-fingerprint.ps1` with `-CheckOnly` first. Its GET/POST responses
are reduced to account ID, proxy ID, and fingerprint mode. `rollback.ps1` sets
the same two accounts back to `off` and verifies the business proxy binding.

Run `preflight-snapshot.ps1` against the operator-supplied candidate endpoint
before a slot switch. It writes only redacted account metadata; database and
Docker capture are opt-in through their explicit parameters.

Do not place an admin token, account credentials, ticket state, proxy URLs, or
production hostnames in this directory.
