# Ember documentation

This index helps a reader choose the next useful document. The repository is
the source of truth for commands, flags, response shapes, limits, and behavior;
the guides explain those contracts without replacing them.

## Choose a path

### I want to understand Ember

1. Read the [README](../README.md) for the product boundary and one complete
   local operation.
2. Read [Architecture](architecture.md) for the layers and state flow.
3. Read [Lifecycle and Blobs](lifecycle-and-blobs.md) for scopes, locks,
   deletion, object integrity, and cleanup.

### I want to operate Ember

- [CLI reference](cli-reference.md) lists commands, flags, output, and safe
  examples.
- [Deployment and recovery](deployment-and-recovery.md) explains plan, apply,
  progress, rollback, and forward actions.
- [Azure-shaped compatibility](azure-shaped-compatibility.md) distinguishes
  local contracts from hosted-provider behavior.

### I want to extend Ember

- Read the implementation and tests in `internal/ember` together; adapters
  should call the shared operator contract rather than create a second domain
  path.
- Use the [CLI reference](cli-reference.md) and compatibility matrix to find
  stable interfaces.
- Run `go test ./...`, `go test -race ./...`, `go vet ./...`, `make build`, and
  `make smoke` for a runtime or packaging change.

### I want to verify a release

- [Sprint 2 release evidence](sprint-2-release-evidence.md)
- [Tenant and resource release evidence](tenant-resource-release-evidence.md)
- [Resource and Blob evidence](sprint-2-resource-blob-evidence.md)
- [Queue and event evidence](sprint-2-queue-event-evidence.md)
- [Sprint 2 closeout](sprint-2-closeout.md)
- `scripts/ember-docs-check.sh` for documentation references

These receipts record exact checks and results. They are verification material,
not a substitute for the operator guides.

## What is implemented

The current repository includes local single-node state, tenant isolation,
resource and Blob lifecycle operations, locks and safe deletion, workload and
network contracts, deployment planning and recovery, queue retry and
acknowledgement, event subscriptions and delivery outcomes, bounded metrics,
and the local management page. The CLI, HTTP adapter, and page use the same
operator boundary.

The repository does not implement a hosted Azure control plane, cloud
provisioning, Kafka, Key Vault, public deployment, multi-node coordination, or
distributed exactly-once delivery. Do not infer those capabilities from names
that resemble a provider API. Planned work must remain labeled as planned.

## Vocabulary

- **Platform** is the local Ember process and its root resources.
- **Tenant** is an isolated resource database.
- **Scope** is the group boundary used for authorization and listing.
- **Resource** is a managed group, bucket, workload, or network object.
- **Operation** records a state-changing request; **audit** records its outcome.
- **Queue/event** features are Ember's local delivery paths, not Kafka.
- **Recovery** is an explicit rollback or forward action over recorded progress.

For project status, use the [open issues](https://github.com/SujalChoudhari/Ember/issues)
and [recent commits](https://github.com/SujalChoudhari/Ember/commits/main).
