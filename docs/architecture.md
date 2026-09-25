# Ember architecture

Ember is a local-first, single-node cloud computing platform: it lets a person create, inspect, change,
and recover local resources without a hosted control plane. The executable in
`cmd/ember` parses process flags, opens a file-backed `Operator`, and passes the
remaining command to the shared `internal/ember` CLI contract.

This layering matters to operators and contributors. The CLI, HTTP adapter, and
management page should describe the same behavior, while the operator boundary
owns authorization, confirmation, identity, and audit decisions.

## Runtime layers

1. **CLI and HTTP adapters** — `RunCLI` and `NewHTTPHandler` translate command
   or request inputs into one operator contract. Both return bounded JSON
   response envelopes and stable error classes.
2. **Operator boundary** — `Operator` applies scope checks, confirmation gates,
   request/correlation identity, idempotent mutation handling, audit recording,
   observability, and lifecycle orchestration.
3. **Managers and domain models** — resource, workload, network, deployment,
   queue, event, operation, and upgrade packages hold bounded domain behavior.
4. **SQLite and file persistence** — platform resources and the tenant registry
   use `platform.db`; each tenant resource scope uses one validated
   `tenants/<tenant-id>/tenant.db` database. Operations, audit, workload/network
   state, recovery records, and Blob metadata remain bounded file-backed stores.
   Blob payloads live below the owned `blobs/objects` path; unrelated files are
   not part of reset or cleanup.

## State and request flow

The default state directory is `.ember-state`; `--state-dir` or
`EMBER_STATE_DIR` selects another directory. `--quota` bounds Blob bytes and
defaults to 64 MiB. A request enters through CLI or HTTP, receives an operator
principal and optional scope, is validated against model and list bounds, and
then reads or mutates the SQLite or file-backed stores owned by that state
directory.

Resource scopes are hierarchical: a group can own buckets, workloads, and
network resources. A scoped principal may inspect or mutate only resources in
its scope. HTTP uses `X-Ember-Scope`; CLI uses `--scope`. Destructive actions
require `--confirm`, `?confirm=true`, or deployment approval.

Mutations that accept request IDs are idempotent. The first accepted mutation
records an operation and audit entry with correlation identity; a replay returns
the original bounded result. Inspection paths are read-only and use explicit
`--limit` bounds. Tenant resources are selected by the tenant principal and are
never read through another tenant's database.

The pure-Go `modernc.org/sqlite` driver owns the SQLite path; no CGO, cloud
database, or web framework is required. A safe backup copies the complete state
directory only after Ember is stopped and all SQLite handles are closed. Schema
versions are checked on open and unsupported versions fail closed rather than
being silently migrated.

## Restart and recovery

Reopening the same state directory validates snapshots and restores resources,
operations, audit history, apply progress, recovery records, and Blob metadata.
Resource locks are intentionally process-local. Deployment recovery is explicit:
progress can be inspected, then a bounded `rollback` or `forward` action can be
requested. Blob recovery requires trusted content whose SHA-256 matches the
recorded object; corruption is not silently overwritten.

## Boundaries

Ember does not implement a hosted multi-node control plane, provider-specific
wire compatibility, external account authentication, multi-node coordination,
distributed locks, or exactly-once delivery. The management server is standard-library HTTP and
loopback-only. Packaging and smoke scripts verify local artifacts only; they do
not deploy, publish, change repository visibility, or enable the GitHub Wiki.

See the [compatibility matrix](azure-shaped-compatibility.md) for the supported
surface paired with its contract tests.
