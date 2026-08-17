# Ember Phase 1 local verification — 2026-08-17

Repository: `/root/ember-work`
Remote: none

## Available checks

- `python3 scripts/contract-check.py` — exit 0; static contract checks passed for scope, storage, idempotency, recovery, HTTP, migrations, and private-repo assertions.
- `sh -n scripts/setup-root.sh scripts/backup-restore-smoke.sh` — exit 0.
- `python3 -m json.tool contracts/fixtures/auth.example.json` — exit 0.
- `git diff --check` — exit 0.

## Required checks blocked by host tooling

Each command was actually attempted from `/root/ember-work`:

- `go version` — exit 127: `go: not found`.
- `gofmt -l .` — exit 127: `gofmt: not found`.
- `go test ./...` — exit 127: `go: not found`.
- `go vet ./...` — exit 127: `go: not found`.
- `go build ./cmd/emberd ./cmd/ember` — exit 127: `go: not found`.
- `make check` — exit 2 at the Makefile Go prerequisite.
- `docker --version` — exit 127: `docker: not found`.
- `psql --version` — exit 127: `psql: not found`.
- `pg_dump --version` — exit 127: `pg_dump: not found`.

Therefore this artifact does **not** claim Go tests/build/vet, a Compose run, PostgreSQL migration execution, or custom-format backup/restore evidence. The implementation is the largest coherent private local subset that could be produced without installing or contacting external services: HTTP/CLI contract code, filesystem provider, in-memory test control-plane adapter, RBAC/audit/idempotency/recovery logic, PostgreSQL migration/policy artifacts, and resettable tests.

No public remote, push, deployment, external service, or paid resource was used.
