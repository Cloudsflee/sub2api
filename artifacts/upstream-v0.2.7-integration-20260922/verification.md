# Candidate Verification

Code candidate: `9347f0a10` (the 0.2.7 merge is `dad02d559`; the follow-up
fix makes product-sync attempt IDs unique). Baseline: `4d1a9caa5`.

All commands below were run from the repository root unless a working
directory is shown. Exit status is recorded explicitly.

| Check | Command | Result |
| --- | --- | --- |
| Merge parents | `git show -s --format='%H %P' dad02d559` | `dad02d559  4d1a9caa5  1c0a69c0c` (0) |
| Migration diff policy | `git diff --name-status 4d1a9caa5 9347f0a10 -- backend/migrations` | only additive `238b`, `239`, and their tests (0) |
| Migration unit tests | `cd backend; go test ./migrations -count=1` | `ok github.com/Wei-Shaw/sub2api/migrations` (0) |
| Backend candidate tests | `cd backend; go test ./internal/service ./internal/repository ./internal/handler/admin ./cmd/server -count=1` | all four packages `ok` (0) |
| Backend all-package compile | `cd backend; go test ./... -run '^$' -count=1` | completed with exit 0 |
| Wire generation | `cd backend; go generate ./cmd/server` | generated `cmd/server/wire_gen.go` (0) |
| Frontend typecheck | `cd frontend; pnpm exec vue-tsc --noEmit` | completed without diagnostics (0) |
| Frontend lint | `cd frontend; pnpm run lint:check` | completed without diagnostics (0) |
| YAML parse | `python -c "import yaml; yaml.safe_load(open('deploy/config.example.yaml',encoding='utf-8'))"` | `config-yaml-ok` (0) |
| CI YAML parse | `python -c "import yaml; yaml.safe_load(open('.github/workflows/backend-ci.yml'))"` | `backend-ci-yaml-ok` (0) |
| Rollout script parse | PowerShell `[scriptblock]::Create(...)` for both scripts | `powershell-parse-ok` (0) |
| Rollout dry-run | local mock API + `rollout-fingerprint.ps1 -CheckOnly -Mode device` | verified two IDs, `proxy_id=1`, mode `device` (0) |
| Rollback dry-run | local mock API + `rollback.ps1 -CheckOnly` | verified two IDs, `proxy_id=1`, mode `off` (0) |
| Preflight snapshot dry-run | local mock API + `preflight-snapshot.ps1 -SkipDatabase -SkipDocker` | wrote redacted settings/accounts snapshots and SHA256 manifest (0) |
| Whitespace check | `git diff --check 4d1a9caa5 9347f0a10 -- . ':(exclude)artifacts/upstream-v0.2.7-integration-20260922'` | `final-code-diff-check-ok` (0) |

The first combined package run exposed a deterministic attempt-ID collision in
the pre-existing product refresh flow. `94c0d848d` adds an atomic sequence to
the hash input; the rerun above is green. No production endpoint was contacted.

The exact baseline/candidate file hashes are in `baseline-hashes.tsv`; the
generated binary patch is `changes.patch` (the artifact directory itself is
excluded from that self-referential diff).

The upstream release-helper unittest was also attempted locally. Its Python
fixtures require UTF-8 locale and its subprocess tests pass POSIX paths to
`bash`; on this Windows shell it stopped at locale/path conversion before
exercising the helper logic. The CI workflow runs that suite on Ubuntu.
