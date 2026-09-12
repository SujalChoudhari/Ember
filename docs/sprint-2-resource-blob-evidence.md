# Ember Sprint 2 resource and Blob evidence

This guide records the clean-state evidence for the resource lifecycle, lock,
safe-deletion, and Blob-completeness scope. It covers local file-backed state
only and does not authorize a hosted deployment, release, publication, or Wiki
change.

## Focused acceptance gate

Run the composed clean-state probe and the lower-level persistence matrix:

```text
go test ./internal/ember -run 'TestSprintTwoResourceBlobSurfacesShareBoundedCleanState|TestOperatorExposesCompleteResourceLifecycleAndLockSurface|TestOperatorExposesCompleteBlobLifecycle' -count=1
go test ./internal/ember/persistence -run 'TestFileBlobStore' -count=1
```

`TestSprintTwoResourceBlobSurfacesShareBoundedCleanState` proves one disposable
operator state can cover:

- group → bucket scope and a read-only ancestor lock;
- deterministic mutation refusal while locked and explicit lock release;
- Blob write, range read, on-disk corruption inspection, and explicit checksum-
  verified recovery; and
- reset removal of owned resource and Blob state.

The resource-manager and Blob-store tests add deterministic coverage for
dependent deletion, scope isolation, list bounds, quotas, retention, checksum
and metadata tampering, recovery idempotency, corruption cleanup, and restart
behavior. Blob payloads and unrelated files are not included in inspection
records or removed by an owned reset.

## Verification matrix

The repository-wide gate is the repeatable local command:

```text
./scripts/ember-release-evidence.sh
```

It runs the full tests, race checks, vet, build, package checksum verification,
and clean-machine smoke. The focused commands above make the resource/Blob
acceptance boundary explicit for review.

## Boundaries

- Resource locks are process-local by design; persisted resource state is
  reopened and validated independently.
- Blob object keys, metadata, payload sizes, quotas, list output, and cleanup
  passes are bounded by the persistence contract.
- Recovery requires trusted content whose SHA-256 matches the recorded object;
  corruption is never silently overwritten.
- Reset removes only Ember-owned paths and leaves unrelated state-directory
  residue untouched.
