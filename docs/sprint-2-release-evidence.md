# Ember Sprint 2 release evidence

This document records the repeatable local verification gate for the Day 2
upgrade, rollback, and release-evidence scope. It is evidence for the
file-backed, local-first repository only; it is not a release, deployment, or
publication authorization.

## One-command gate

From the repository root, run:

```text
./scripts/ember-release-evidence.sh
```

The script uses disposable temporary directories and runs the complete local
matrix:

| Gate | Evidence |
|---|---|
| Full test suite | `go test ./...` |
| Race checks | `go test -race ./...` |
| Static analysis | `go vet ./...` |
| Build | trimmed `go build` of `./cmd/ember` |
| Package | platform-labelled binary and `.sha256` sidecar from `make package` |
| Provenance | `sha256sum --check` against the generated package sidecar |
| Clean-machine lifecycle | `scripts/ember-smoke.sh` against the packaged binary |

The script exits on the first failed gate and removes its temporary evidence
directory. It does not write a release, tag, deployment, Wiki, or public
publication artifact.

## Upgrade and rollback coverage

The upgrade package tests provide the bounded state-transition evidence used by
the gate:

- `TestUpgradeMigratesGoldenV1StateToCurrentVersion` proves the supported v1 to
  v2 migration and reopenable encoded state.
- `TestPreflightRejectsUnsupportedVersionsDeterministically` proves that an
  unsupported version is rejected before mutation.
- `TestUpgradeReportsBoundedRedactedFailureEvidence` and
  `TestUpgradeRejectsOversizedSnapshotsWithEvidence` prove stable failure
  classes without copying input values into evidence.
- `TestRollbackRestoresSafeStateAfterInjectedUpgradeFailure` proves restoration
  of the prepared safe snapshot after a bounded injected failure.
- `TestRollbackIsIdempotentAndRetentionIsExplicitlyBounded` proves replay safety,
  explicit discard, and bounded journal retention.
- `TestRollbackBoundsRetriesAndRedactsExecutorErrors` proves the retry bound and
  redaction of executor failures.
- `TestRunEvidenceCoversLifecycleAndArtifactProvenance` covers clean install,
  upgrade, rollback, reset, bounded checks, and artifact digest evidence.

The implementation bounds snapshots to 1 MiB and 100 resources, retains at most
16 rollback records, and permits at most 3 rollback attempts per request.
Rollback snapshots are retained privately by the journal and are not emitted
in status or list evidence.

## Boundaries

- State changes are file-backed and local-first; no cloud provider or network
  service is required.
- Package provenance is a SHA-256 check of the generated binary, not a claim of
  a signed or published release.
- The gate does not change repository visibility, enable the GitHub Wiki,
  deploy Ember, or publish artifacts.