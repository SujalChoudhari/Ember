# Ember lifecycle and Blob guide

This guide explains how an operator moves a resource through its lifecycle and
what Ember protects at each step. Ember keeps Blob content behind a bucket
resource; the lifecycle is local, explicit, and scope-checked rather than a
cloud-provider provisioning workflow.

## Resource lifecycle

1. Create a root `group`.
2. Create child `bucket`, `workload`, or network resources with `--scope` and
   `--parent` where the model requires a parent.
3. Inspect or list within the current scope. Provider metadata and desired /
   observed state remain part of the resource contract.
4. Apply bounded tag mutations with request and correlation IDs. Replaying a
   request ID returns the original operation instead of applying a second
   mutation.
5. Acquire a read-only ancestor lock when inspection must be isolated. While
   held, mutation, create, and delete operations are refused; release requires
   the matching owner and token.
6. Delete leaves before parents, with explicit `--confirm`. A parent with
   dependents is refused rather than implicitly cascading.

The [resource and Blob evidence guide](sprint-2-resource-blob-evidence.md)
contains the focused clean-state acceptance command and reset boundaries.

## Blob lifecycle

A bucket owns object metadata and payloads. `blob put` validates the key and
quota, writes content, and records size plus SHA-256 metadata. `blob get` reads
the full object; paired `--start` and `--end` flags perform a bounded range
read. `blob list --limit N` returns deterministic bounded metadata.

`blob verify` compares on-disk bytes and metadata and reports corruption without
silently repairing it. `blob recover` requires trusted content and the expected
SHA-256, then records an idempotent audited operation. `blob delete --confirm`
removes one object. Retention and corruption cleanup remove only owned objects
within the configured bounds.

## HTTP equivalents

The HTTP adapter exposes the same operator contract with JSON envelopes and
`X-Ember-Scope`:

- `POST/GET /v1/resources` and `/v1/resources/{id}` for resource lifecycle;
- `/v1/buckets/{bucket}/objects/{key}` for Blob write/read/delete and range
  query parameters;
- `/v1/operations/{id}` and resource audit paths for operation history; and
- `/v1/reset?confirm=true` for the confirmation-gated root reset.

The [compatibility matrix](azure-shaped-compatibility.md) pairs these routes
with their contract tests. Ember does not claim Azure Blob Storage wire
compatibility, hosted persistence, or distributed consistency.
