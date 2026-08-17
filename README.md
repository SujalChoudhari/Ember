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

## Important runtime boundary

`internal/ember` contains a deterministic in-memory control-plane adapter for the local contract tests. It is not a replacement for the approved PostgreSQL authority. The repository also includes PostgreSQL 16 migrations, schema compatibility checks, audit trigger/permissions, a Compose profile, and backup/restore scripts. The production wiring must refuse to start until PostgreSQL migration compatibility is verified.

The current host did not have `go`, Docker, PostgreSQL, or `pg_dump`, so those commands could not be run here. No build, test, vet, or backup result is claimed without the required tools.

## Local commands once the toolchain exists

```sh
# Private root setup; run as an operator, not from the source checkout.
sudo scripts/setup-root.sh

# Development/test server uses a dedicated temporary root explicitly.
go test ./...
go vet ./...
go build ./cmd/emberd ./cmd/ember
go run ./cmd/emberd --blob-root /var/lib/ember/blob --auth-file /etc/ember/auth.json

# PostgreSQL profile and evidence (only on a host with Docker/PostgreSQL tools).
docker compose --profile postgres up -d postgres
scripts/backup-restore-smoke.sh
```

The server does not use a current-directory fallback for Blob storage. Test fixtures use `t.TempDir()` and assert that writes remain under that root.

## Private boundary

Do not publish this repository, add a public remote, deploy it, contact external services, or create paid cloud resources as part of Phase 1.
