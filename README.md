# Ember

Ember is a local-first, single-node cloud computing platform for learning and
operating resource lifecycles on one machine. It stores its state in a directory you choose,
exposes the same contracts through the CLI and local HTTP management page, and
keeps destructive actions, scope boundaries, retries, and recovery explicit.
It does not require an external service or account.

## Who this is for

Use Ember when you want to understand what an operator does, reproduce a
lifecycle on a disposable machine, or build against a clear local contract.
The repository documents what is implemented and calls out work that is only a
proposal; names and interfaces do not imply hosted-provider, Kafka, or
secret-management features that are not present.

## Requirements

- Linux or another Go-supported system
- Go 1.22 or newer for a source build
- `sha256sum` and `install` for packaging and installation

No external service or network account is required.

## Build and install

```text
make test
make package
PREFIX="$HOME/.local" make install
```

`make package` creates a platform-labelled binary and SHA-256 sidecar in
`dist/`. `make install` verifies the sidecar when present and installs the
binary as `$PREFIX/bin/ember`. Set `GOOS`, `GOARCH`, `OUT_DIR`, `PREFIX`, or
`BINARY` to change these defaults.

## Run one real operation

The following commands create a disposable group and bucket, write and read an
object, then remove the state directory through Ember's confirmation-gated
reset command:

```text
ember --state-dir /tmp/ember-state resource create --type group --name platform
ember --state-dir /tmp/ember-state resource create --scope resource-00000001 --parent resource-00000001 --type bucket --name assets
ember --state-dir /tmp/ember-state blob put --scope resource-00000001 --bucket resource-00000002 --key greeting --data hello-ember
ember --state-dir /tmp/ember-state blob get --scope resource-00000001 --bucket resource-00000002 --key greeting
ember --state-dir /tmp/ember-state reset --confirm
```

For the packaged binary, run `make smoke`. The smoke script uses temporary
mode-0700 state, checks that the object can be read, confirms owned state is
removed, and repeats creation in the same disposable directory.

## Tenants, resources, and the local management page

Tenants isolate resource state in separate SQLite databases. Resources are
hierarchical: groups can own buckets, workloads, and network resources.

```text
ember --state-dir /tmp/ember-state tenant create --id alpha --name Alpha
ember --state-dir /tmp/ember-state resource create --tenant alpha --type group --name shared
ember --state-dir /tmp/ember-state resource list --tenant alpha --limit 10
```

Start the management page explicitly on loopback:

```text
ember --state-dir /tmp/ember-state --listen 127.0.0.1:8080 serve
```

The page exposes inspection and the same scoped operator actions as the CLI.
Tenant and resource deletion require a review and explicit confirmation. It
never binds a public interface by default.

## What happened?

Every state-changing operation can carry request and correlation identity and
produces bounded operation/audit evidence. Scope checks and read-only locks
explain why an action is refused. Queue and event paths retain retry,
dead-letter, acknowledgement, redrive, and delivery metrics without putting
payloads or receipts in URLs. Blob verification reports corruption; recovery
requires trusted bytes and the expected SHA-256.

The important safety rules are simple:

- state belongs to the selected `--state-dir` and tenant;
- list and payload sizes are bounded;
- destructive commands require `--confirm` or an equivalent review;
- retries and recovery are idempotent where request identity is accepted; and
- private paths, credentials, payloads, and provider internals are not exposed
  as user-facing evidence.

## Extend Ember

Start with the shared operator contracts in `internal/ember`, then read the
matching model, persistence, and adapter tests. Keep CLI, HTTP, and management
page behavior on one domain path. Add a focused contract test before changing
behavior and run the repository gates before proposing a change.

The [documentation index](docs/index.md) separates tutorials, operator
procedures, reference material, architecture, and verification receipts. The
[architecture guide](docs/architecture.md), [CLI reference](docs/cli-reference.md),
[lifecycle guide](docs/lifecycle-and-blobs.md), and [deployment guide](docs/deployment-and-recovery.md)
are the best next steps.

## Verification

Use the exact checks appropriate to the change:

```text
go test ./...
go test -race ./...
go vet ./...
make build
make smoke
```

For the complete local release check, run `./scripts/ember-release-evidence.sh`.
These commands verify the local artifact; they do not publish, deploy, change
repository visibility, or enable a Wiki.
