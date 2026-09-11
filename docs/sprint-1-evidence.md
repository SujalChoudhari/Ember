# Ember Sprint 1 evidence

## Scope

This receipt covers the Milestone 9 implementation gate in issue #155. It is
local-first evidence only: no cloud provider, live deployment, publication, or
release action is part of this gate.

## Shipped workstreams

- Compute: #156 and #157, merged through PRs #164, #165, and #172.
- Networking: #158 and #159, merged through PRs #166 and #167.
- Queue/event: #160 and #161, merged through PRs #168 and #169.
- Portal/operator UX: #162 and #163, merged through PRs #170 and #171.

## Integration evidence

`TestSprintOneSurfacesShareBoundedCleanState` exercises one disposable state
directory across:

- deployment plan/apply with operation correlation;
- workload health/readiness inspection;
- scoped network, port, endpoint, and cross-scope denial;
- bounded queue enqueue, receive, correlation, and acknowledgement; and
- acknowledged event delivery with bounded metrics.

The same test resets the operator and queue state and verifies that owned
resource, network, operation, apply-progress, and queue snapshots are removed.
Apply-progress persistence is included in the reset boundary so a clean rerun
cannot inherit stale portal evidence.

## Verification matrix

| Gate | Result |
|---|---|
| Focused reset regression | passed |
| Clean-state cross-surface acceptance | passed |
| `go test ./...` | passed |
| `go test -race ./...` | passed |
| `go vet ./...` | passed |
| `make build` | passed |
| `make package` | passed |
| `make smoke` | passed |
| `git diff --check` and secret scan | passed |

## Status

The implementation and local verification gates for the sprint integration
receipt are complete. Parent issue reconciliation and any final release decision
remain tracked by #155; this document does not authorize a release,
deployment, or publication.
