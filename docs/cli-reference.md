# Ember CLI reference

The binary is built as `ember`. Every command emits one JSON response on
stdout. Errors are written to stderr and return a non-zero process status.

## Global flags

These flags precede the command:

| Flag | Default | Meaning |
|---|---|---|
| `--state-dir PATH` | `$EMBER_STATE_DIR` or `.ember-state` | File-backed Ember state directory |
| `--quota BYTES` | `67108864` | Blob quota in bytes |
| `--listen ADDRESS` | `127.0.0.1:8080` | Loopback address for `serve` mode |

The shared `--scope ID` flag identifies the operator scope where shown. List
commands accept `--limit`; limits are validated against the repository bounds.

## Tenant commands

| Command | Flags |
|---|---|
| `tenant create` | `--id`, `--name` |
| `tenant list` | no command-specific flags |
| `tenant get` | `--scope`, `--id` |
| `tenant delete` | `--id`, `--confirm` |

Tenant registration is stored in `platform.db`. Each tenant's resources are
stored in its own `tenants/<tenant-id>/tenant.db` database and selected with the
`--tenant` flag on resource and operation commands where supported.

## Resource and Blob commands

| Command | Flags |
|---|---|
| `resource create` | `--tenant`, `--scope`, `--type`, `--name`, `--parent`, `--tags`, `--provider-namespace`, `--provider-type`, `--provider-version`, `--desired-state` |
| `resource get` | `--tenant`, `--scope`, `--id` |
| `resource list` | `--tenant`, `--scope`, `--limit` |
| `resource update-tags` | `--tenant`, `--scope`, `--id`, `--tags`, `--request-id`, `--correlation-id` |
| `resource delete` | `--tenant`, `--scope`, `--id`, `--confirm` |
| `resource lock acquire` | `--tenant`, `--scope`, `--id`, `--owner`, `--token` |
| `resource lock inspect` | `--tenant`, `--scope`, `--id` |
| `resource lock release` | `--tenant`, `--scope`, `--id`, `--owner`, `--token` |
| `blob put` | `--scope`, `--bucket`, `--key`, `--data` |
| `blob get` | `--scope`, `--bucket`, `--key`, optional paired `--start`, `--end` |
| `blob verify` | `--scope`, `--bucket`, `--key` |
| `blob recover` | `--scope`, `--bucket`, `--key`, `--expected-sha256`, `--data`, `--request-id`, `--correlation-id` |
| `blob list` | `--scope`, `--bucket`, `--limit` |
| `blob delete` | `--scope`, `--bucket`, `--key`, `--confirm` |

Object range reads use an inclusive start and exclusive end. `blob recover`
requires a trusted checksum and records an idempotent audited operation.

## Workloads and observability

| Command | Flags |
|---|---|
| `workload create` | `--scope`, `--name`, `--provider-namespace`, `--provider-type`, `--provider-version`, `--desired-state`, `--cpu-millis`, `--memory-bytes`, `--disk-bytes`, `--privileged`, `--allow-privilege-escalation` |
| `workload get` | `--scope`, `--id` |
| `workload inspect` | `--scope`, `--id`, `--limit` |
| `workload restart` | `--scope`, `--id` |
| `workload delete` | `--scope`, `--id`, `--confirm` |
| `workload volume attach` | `--scope`, `--id`, `--name`, `--max-bytes` |
| `workload volume list` | `--scope`, `--id`, `--limit` |
| `workload volume cleanup` | `--scope`, `--id`, `--confirm` |
| `observability metrics` | no command-specific flags |

## Operations, audit, deployment, and reset

| Command | Flags |
|---|---|
| `operation get` | `--tenant`, `--scope`, `--id` |
| `operation list` | `--tenant`, `--scope`, `--resource`, `--limit` |
| `audit list` | `--tenant`, `--scope`, `--resource`, `--limit` |
| `deployment plan` | `--scope`, one of `--document` or `--file`, repeatable `--parameter name=value` |
| `deployment apply` | `--scope`, one of `--document` or `--file`, repeatable `--parameter name=value`, `--request-id`, `--correlation-id`, `--confirm` |
| `deployment apply-progress get` | `--scope`, `--id` |
| `deployment apply-progress list` | `--scope`, `--limit` |
| `deployment apply-progress operation` | `--scope`, `--id` |
| `deployment recovery get` | `--scope`, `--id` |
| `deployment recovery list` | `--scope`, `--limit` |
| `deployment recovery run` | `--scope`, `--request-id`, `--apply-progress-id`, `--action` (`rollback` or `forward`) |
| `reset` | `--scope`, `--confirm` |

`deployment apply` refuses destructive changes without `--confirm`. Root reset
is confirmation-gated and scoped reset is denied. Unknown flags, positional
arguments, incomplete recovery commands, unpaired range flags, and invalid list
limits are rejected as invalid CLI requests.

## Verified examples

```text
ember --state-dir /tmp/ember-state resource create --type group --name platform
ember --state-dir /tmp/ember-state resource create --scope resource-00000001 --parent resource-00000001 --type bucket --name assets
ember --state-dir /tmp/ember-state blob put --scope resource-00000001 --bucket resource-00000002 --key greeting --data hello-ember
ember --state-dir /tmp/ember-state blob get --scope resource-00000001 --bucket resource-00000002 --key greeting
ember --state-dir /tmp/ember-state reset --confirm
```

The clean-machine smoke script runs this lifecycle against disposable state;
use `make smoke` or the [tenant/resource release evidence gate](tenant-resource-release-evidence.md)
to repeat it. The management page is started with `serve` and an explicit
loopback `--listen` address; it never binds a public interface.
