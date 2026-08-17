# Ember Phase 1

Ember is a **private, local-first, Azure-shaped learning platform**. Phase 1 is a deliberately small Go modular monolith that explores a Resource Manager boundary and an Ember-owned Blob boundary backed by local PostgreSQL and a private filesystem provider.

This is not Azure REST compatibility, an Azure emulator, or a claim of support for later Ember services. The HTTP contract uses Azure-shaped resource paths and concepts where they clarify the learning surface, while Ember owns the semantics and limits.

## Phase 1 boundary

The verified slice includes:

- Resource Manager group lifecycle and `Ember.Blob/bucket` lifecycle;
- synchronous exact-byte Blob object `PUT`, `GET`, `HEAD`, and `DELETE`;
- a 10 MiB synchronous object limit, 1 GiB logical quota, key validation, SHA-256 checksums, ETags, and provider-generated opaque paths;
- staged filesystem writes with fsync/rename, root confinement, integrity checks, and operator-assisted quarantine findings;
- PostgreSQL-backed resources, operations, idempotency records with 24-hour replay/conflict handling, locks, append-only redacted audit events, and schema compatibility checks;
- seeded owner/editor/reader bearer-token RBAC for local verification;
- versioned `/api/v1` HTTP endpoints and the HTTP-only `cmd/ember` CLI.

The approved persistence boundary is PostgreSQL for control-plane authority plus a private filesystem directory for Blob bytes. The in-memory `internal/ember` store remains an explicit deterministic adapter for contract tests and `--dev-memory-control-plane`; it is not the production authority.

See [the Phase 1 design diagrams](docs/ember-phase1-design.md) and [the verification record](docs/verification-2026-08-17.md) for the implementation boundary and evidence.

## Prerequisites

The verified local path requires:

- Go **1.22.2**;
- PostgreSQL **16.14**;
- PostgreSQL client/backup tools: `psql`, `createdb`, `dropdb`, `pg_dump`, and `pg_restore`.

Docker and Docker Compose are optional for the included Compose profile. They are unavailable on the verification host, so Docker/Compose was not run and no Docker result is claimed.

## Local commands

```sh
# Private root setup; run as an operator, not from the source checkout.
sudo scripts/setup-root.sh

# Development/test server uses an explicitly supplied temporary/private root.
export EMBER_TEST_DATABASE_URL='postgres:///ember_phase1?host=/var/run/postgresql'
go test ./... -count=1
make test-isolation
make fmt-check
go vet ./...
go build ./cmd/...
python3 scripts/contract-check.py
sh -n scripts/setup-root.sh scripts/backup-restore-smoke.sh

go run ./cmd/emberd \
  --blob-root /var/lib/ember/blob \
  --auth-file /etc/ember/auth.json \
  --database-url "$EMBER_TEST_DATABASE_URL"

# PostgreSQL backup/restore evidence against an installed local PostgreSQL.
export EMBER_DATABASE_URL="$EMBER_TEST_DATABASE_URL"
scripts/backup-restore-smoke.sh

# Optional Compose profile; not available on the current host.
docker compose --profile postgres up -d postgres
```

`emberd` requires an explicit Blob root, a mode-0600 auth file, and PostgreSQL via `--database-url` or `EMBER_DATABASE_URL`. It has no current-directory storage fallback. The in-memory control plane is opt-in only with `--dev-memory-control-plane` for local contract work.

## Private boundary and caveats

- Keep this repository private and use local PostgreSQL/filesystem resources only for Phase 1.
- Do not describe the API as Azure-compatible; it is Azure-shaped and Ember-owned.
- No later control-plane services, asynchronous transfers, cloud deployment, public remote, secrets, paid resource, or external service is part of this implementation.
- Test fixtures use temporary directories. Production-mode filesystem initialization refuses unsafe provider roots and rejects symlink escapes.
- A disposable verification database may retain test rows after checks; this does not change the repository state. Docker/Compose remains unverified because Docker is unavailable on the host.
