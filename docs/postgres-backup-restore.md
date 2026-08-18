# PostgreSQL operational evidence

The intended Phase 1 operational profile is PostgreSQL 16 with max 10 connections, 5s connect timeout, 15s statement timeout, and 30s idle-transaction timeout. Startup must run the deterministic migration set and fail closed when `schema_meta` is missing or outside the compatible range.

The evidence command is `scripts/backup-restore-smoke.sh`. It runs a custom-format `pg_dump`, creates a disposable restore database, restores with `pg_restore --exit-on-error`, and drops the disposable database. The current verification host has PostgreSQL 16.14 and the required client/backup tools, so this smoke can run against a local PostgreSQL service. Docker is optional and unavailable on this host; the Compose path is not required and is not claimed as verified.

The in-memory adapter is test-only and does not replace PostgreSQL authority. Production startup explicitly refuses the memory adapter unless `--dev-memory-control-plane` is passed.
