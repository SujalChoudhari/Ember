# Ember Phase 1 — private local vertical slice

This repository is private/local by design. It has no configured remote.

The implemented slice is a Go modular-monolith boundary for:

- Resource Manager group and Blob bucket lifecycle;
- synchronous exact-byte object PUT/GET/HEAD/DELETE with staged filesystem writes;
- opaque provider-generated object paths, checksum/ETag/version metadata, quota/key limits;
- operations and 24-hour idempotency replay/conflict handling;
- seeded owner/editor/reader bearer-token RBAC;
- locks, dependency-ordered deletion, redacted append-only audit;
- repair findings and explicit operator-assisted quarantine/delete boundary;
- versioned `/api/v1` HTTP API and an HTTP-only `cmd/ember` CLI.

## Prerequisites

The PostgreSQL-backed verification path requires Go 1.22.2 and PostgreSQL 16.14, including `psql`, `pg_dump`, `createdb`, `dropdb`, and `pg_restore`. The current private verification host has these tools installed. Docker is optional for the Compose profile; it is not installed on the current host, so Docker/Compose verification is not claimed.

## Important runtime boundary

`internal/ember` contains a deterministic in-memory control-plane adapter for the local contract tests. It is not a replacement for the approved PostgreSQL authority. The repository also includes PostgreSQL 16 migrations, schema compatibility checks, audit trigger/permissions, a Compose profile, and backup/restore scripts. The production wiring must refuse to start until PostgreSQL migration compatibility is verified.

The current host has Go 1.22.2, PostgreSQL 16.14, and the PostgreSQL client/backup tools installed. Docker remains optional for the Compose profile and is unavailable on this host, so the local PostgreSQL path is the verified path; no Docker/Compose result is claimed.

## Local commands

```sh
# Private root setup; run as an operator, not from the source checkout.
sudo scripts/setup-root.sh

# Development/test server uses a dedicated temporary root explicitly.
export EMBER_TEST_DATABASE_URL='postgres:///ember_phase1?host=/var/run/postgresql'
go test ./... -count=1
make test-isolation
go vet ./...
go build ./cmd/...
go run ./cmd/emberd --blob-root /var/lib/ember/blob --auth-file /etc/ember/auth.json

# PostgreSQL backup/restore evidence against an installed local PostgreSQL.
export EMBER_DATABASE_URL="$EMBER_TEST_DATABASE_URL"
scripts/backup-restore-smoke.sh

# Optional Docker Compose profile (Docker is not required for the local path).
docker compose --profile postgres up -d postgres
```

The server does not use a current-directory fallback for Blob storage. Test fixtures use `t.TempDir()` and assert that writes remain under that root.

## Private boundary

Do not publish this repository, add a public remote, deploy it, contact external services, or create paid cloud resources as part of Phase 1.
