# Tenant and resource release evidence

This document closes the local, two-day tenant/resource integration gate tracked by
[issue #198](https://github.com/SujalChoudhari/Ember/issues/198). It records
repository evidence only. It is not authorization to publish, deploy, release,
change repository visibility, or enable the attached GitHub Wiki.

## Supported state boundary

The approved storage layout is:

```text
<state-dir>/platform.db
<state-dir>/tenants/<tenant-id>/tenant.db
<state-dir>/blobs/objects/...
<state-dir>/operations.json
<state-dir>/audit.json
<state-dir>/apply-progress.json
<state-dir>/recoveries.json
<state-dir>/networks.json
<state-dir>/workloads.json
```

`platform.db` owns platform resources, the tenant registry, and versioned schema
metadata. Each registered tenant owns one SQLite database below its validated
identifier directory. Tenant resource IDs are prefixed with the tenant ID so
that identical names remain distinguishable while storage and authorization
remain isolated. The SQLite driver is the pure-Go `modernc.org/sqlite` driver;
CGO, PostgreSQL, MySQL, cloud databases, and a web framework are not part of
this slice.

The management surface is a standard-library Go HTTP handler and server. It is
loopback-only, serves a responsive HTML/CSS page with mobile breakpoints, keeps
platform and tenant resource paths separate, and requires a confirmed POST for
destructive tenant or resource mutation. It does not provide hosted auth or a
public deployment surface.

## Fresh-machine and restart evidence

The release test `TestTenantResourceReleaseGatePreservesIsolationAcrossRestoreAndWeb`
proves, from an empty state directory, that:

- platform registration and two tenants create `platform.db` plus one
  `tenants/<id>/tenant.db` database per tenant;
- equal resource names in different tenants produce isolated IDs and deny
  cross-tenant reads;
- closing and reopening the operator preserves platform and tenant resources;
- a closed-state copy can be restored as a complete owned state directory;
- the browser-facing HTML flow registers a tenant, creates a scoped resource,
  serves the responsive stylesheet, and refuses an unconfirmed tenant delete;
- a confirmed tenant delete removes that tenant's database directory.

The focused SQLite tests additionally cover fresh open, reopen, schema metadata,
unsupported schema rejection, tenant path validation, and resource persistence.
An unsupported schema version fails closed; this release gate does not perform an
unreviewed database migration.

The clean-machine smoke path also exercises CLI tenant registration, equal-name
resources in two tenants, cross-tenant ID isolation, reset cleanup, and repeat
platform creation:

```text
./scripts/ember-release-evidence.sh
```

That command runs documentation validation, the full and race-tested Go suite,
`go vet`, a trimmed build, package checksum verification, and the disposable
smoke path. It produces no release, deployment, or publication artifact.

## Backup and restore boundary

A safe local backup is a copy of the complete owned state directory while Ember
is stopped and all SQLite handles are closed. The copy must include `platform.db`,
every `tenants/<tenant-id>/tenant.db`, the file-backed JSON stores, and the
`blobs/` directory. Restoring only one database is not a complete Ember backup:
operations, audit history, workload/network state, and Blob payloads have
separate owned files.

The release test copies the closed state directory to a fresh location and
reopens it before exercising the web flow. Live SQLite files must not be copied
while Ember is running, and no cloud or external backup service is implied.
Restored databases still pass the current schema checks; unknown schema versions
are rejected rather than silently rewritten.

## Remaining boundaries

- This is local-first, single-node evidence, not hosted Azure compatibility.
- The server must bind to a loopback address; public binding and deployment are
  rejected or out of scope.
- Blob payload bytes remain on the owned filesystem path in this slice; metadata
  is persisted separately.
- The milestone remains open until this issue's verified PR/merge evidence is
  reconciled by the project owner.
