# Codex Ticket Pool Verification

This record covers the local workspace implementation. It does not target an
external production hostname.

## Artifacts

| Role | Path |
| --- | --- |
| Discovery/modified artifact | `tools/discover_codex_ticket_pool.py` |
| Compatibility entry point | `tools/codex_clash_pool_discover.py` |
| Runtime pool implementation | `backend/internal/service/openai_codex_ticket.go` |
| Transactional linkage guard | `backend/internal/service/codex_ticket_proxy_sync.go` |
| Runnable settings rollback | `artifacts/codex-ticket-pool/rollback.ps1` |
| Source diff | `git diff` from the workspace root |

## Commands

| Command | Result |
| --- | --- |
| `python -m unittest tools.test_discover_codex_ticket_pool -v` | 5 tests passed; exit 0 |
| `python -m py_compile tools/discover_codex_ticket_pool.py tools/codex_clash_pool_discover.py` | exit 0 |
| `go test ./... -run '^$' -count=0` (from `backend`) | compile-only pass; exit 0 |
| `go test ./internal/repository -count=1 -timeout=240s` | pass; exit 0 |
| `go test ./internal/handler/admin -count=1 -timeout=180s` | pass; exit 0 |
| `go test -race ./internal/service -run 'Test(CodexTicketHarvesterStopCancelsInFlightWork|OpenAICodexTicketHarvestStatusTracksFailureCategoriesWithoutURL|CodexTicketProxySyncMultiEntryPoolIsHarvestOnly)' -count=1` | pass; exit 0 |
| `pnpm run typecheck` (from `frontend`) | pass; exit 0 |
| `pnpm run test:run -- src/views/admin/__tests__/SettingsView.spec.ts` | 40 tests passed; exit 0 |
| `pnpm run build` (from `frontend`) | pass; exit 0 |

The broad service package still contains a pre-existing parallel-sensitive
failure in `TestCanonicalOpenAIAccountSchedulingModelMatchesForwardSemantics/
Grok_OAuth_does_not_inherit_OpenAI_Codex_aliases`; the ticket-focused and race
tests above pass independently.

## Behavioral checks

- Listener discovery records rejected listeners, requires an application-network
  bind and complete node, probes a public egress and a separate HTTP endpoint,
  and rejects duplicate egress hashes.
- A multi-entry write is persisted as harvest-only in the settings transaction;
  no account proxy writer or scheduler outbox event is invoked.
- Single-entry linkage remains compatible with the existing coordinator.
- Runtime entry state records round-robin allocation, cooldown, HTTP status,
  state length, `transport`, `length_312`, `incomplete`, and other categories.
- Completion counts only persisted, unexpired, exact-292 `gAAAAA` tickets in
  the active OAuth/Astra scope; Sol remains outside a multi-entry gate.
