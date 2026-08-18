# Ember Phase 1 local verification — 2026-08-17

Repository: `/root/ember-work`
Remote: none
Database: local PostgreSQL 16.14, disposable database `ember_phase1_20260817`, peer-authenticated service/root roles

## Debugging and remediation

- Repaired the syntax error in `internal/persistence/postgres/store.go` around `translateDBError`.
- Reproduced the original PostgreSQL CreateGroup failure through the real database: SQLSTATE `42703`, `column "resource_id" does not exist`.
- Root cause: `idempotency_records` has no `resource_id` column; the resource link is `operations.resource_id`.
- Fixed both group-create and bucket-create idempotency lookups to join `operations` through `operation_id`.
- Converted the focused PostgreSQL probe into `TestPostgresCreateGroupAndBucketIdempotency`; it was observed red before the fix and green after it.
- Corrected the PostgreSQL persistence golden test to restart its HTTP server against the reopened store rather than the closed pre-restart store.

## Passing checks

- `gofmt -l .` — exit 0.
- `go test ./... -count=1` with `EMBER_TEST_DATABASE_URL` — exit 0.
- `go vet ./...` — exit 0.
- `go build ./cmd/...` — exit 0.
- `make check` with `EMBER_TEST_DATABASE_URL` — exit 0; format, test, vet, and build gates passed.
- `python3 scripts/contract-check.py` — exit 0; static contract checks passed.
- `sh -n scripts/setup-root.sh scripts/backup-restore-smoke.sh` — exit 0.
- `git diff --check` — exit 0.
- Focused PostgreSQL package tests — exit 0, including CreateGroup/CreateBucket idempotent replay, HTTP persistence across reopen, and incompatible schema refusal.
- CLI-through-HTTP golden flow against PostgreSQL-backed `emberd` — passed group create/replay, bucket create, object PUT/GET/HEAD/DELETE.
- `runuser -u postgres -- env EMBER_DATABASE_URL=... RESTORE_DB=ember_restore_smoke_20260817 sh scripts/backup-restore-smoke.sh /tmp/ember-phase1-postgres.dump` — exit 0; custom-format dump, disposable restore, and cleanup passed.

## Environment notes

- The first backup-script invocation as the local `root` database role reached restore setup but failed because that role lacks `CREATEDB`; rerunning as the local PostgreSQL service account passed.
- Docker/Compose verification was not run because `docker` is not installed on this host. No remote, push, deployment, external service, secret, or paid resource was used.
