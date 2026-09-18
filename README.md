# Ember

Ember is a private, local-first, single-node Azure-like platform. The command
line binary stores bounded state below the directory passed with
`--state-dir` (or `.ember-state` by default). Platform resources and the tenant
registry live in `platform.db`; each tenant has an isolated
`tenants/<tenant-id>/tenant.db` database.

## Requirements

- Linux or another Go-supported clean machine
- Go 1.22 or newer for a source build
- `sha256sum` and `install` for packaging and installation

No network service, cloud account, or external runtime is required.

## Documentation

Start with the [golden clean-machine quickstart](#golden-clean-machine-lifecycle),
the [tenant/resource release evidence](docs/tenant-resource-release-evidence.md),
or use the [documentation index](docs/index.md) for the learner and contributor
navigation. The index separates implemented behavior from planned work and
non-goals.

## Build, package, and install

```text
make test
make package
PREFIX="$HOME/.local" make install
```

`make package` writes a platform-labelled binary and SHA-256 sidecar below
`dist/`. `make install` verifies the sidecar when present and installs the
binary as `$PREFIX/bin/ember`; it does not modify the source tree or a live
service. Set `GOOS`, `GOARCH`, `OUT_DIR`, `PREFIX`, or `BINARY` to override
these defaults.

## Golden clean-machine lifecycle

Run the bounded smoke path after packaging:

```text
make smoke
```

The smoke path creates and reads a group, creates a bucket, writes and reads an
object, resets Ember with explicit confirmation, asserts that owned state is
removed, and repeats creation in the same disposable state directory. The
fixture is temporary, mode `0700`, and contains no secret values.

For an explicit reset of a state directory:

```text
ember --state-dir /path/to/state reset --confirm
```

Reset is intentionally confirmation-gated and removes only Ember-owned state.

## Tenant and resource management

The local management slice supports platform-root resources plus isolated tenant
resources:

```text
ember --state-dir /tmp/ember-state tenant create --id alpha --name Alpha
ember --state-dir /tmp/ember-state tenant create --id beta --name Beta
ember --state-dir /tmp/ember-state resource create --tenant alpha --type group --name shared
ember --state-dir /tmp/ember-state resource list --tenant alpha --limit 10
```

The standard-library management page is available only on an explicit loopback
address:

```text
ember --state-dir /tmp/ember-state --listen 127.0.0.1:8080 serve
```

Tenant and resource deletion remains confirmation-gated. The complete clean
machine, restart, backup-boundary, SQLite, and web evidence is documented in
[`docs/tenant-resource-release-evidence.md`](docs/tenant-resource-release-evidence.md)
and runs through `./scripts/ember-release-evidence.sh`.

## Event handling

Ember includes a bounded, inspectable event path rather than treating delivery as
an invisible background detail:

- topics and subscriptions with scope, owner, name, event-type, and correlation
  filters;
- queue receive and acknowledgement with restart-persistent state;
- bounded retry and terminal dead-letter recording;
- redrive with request-identity idempotency and concurrent-recovery exclusion;
- event outcomes, correlation identifiers, and constant-cardinality delivery,
  retry, dead-letter, and recovery metrics;
- redacted failure evidence: dead-letter state keeps stable failure metadata and
  payload digests without persisting payloads or consumer error details.

The event surface is deliberately local-first. It does not claim distributed
exactly-once delivery, cross-node coordination, or cloud-provider semantics. The
focused evidence lives in `docs/sprint-2-queue-event-evidence.md` and the
`internal/ember/events` and `internal/ember/queue` packages.

## Why Ember should exist next to Azure

Ember should not try to beat Azure on global scale or managed-service breadth. Its
advantage can be a system that is easier to understand, reproduce, and trust on a
single machine:

- **Failure-readable by default:** every retry, dead-letter, recovery, and reset
  boundary should be inspectable without reading server logs or guessing hidden
  state.
- **Safe by construction:** destructive actions require explicit confirmation;
  scope, locks, redaction, bounded state, and idempotency are part of the core
  contract rather than optional operational add-ons.
- **Offline and owner-controlled:** the same artifact can be learned, tested,
  packaged, and run without a cloud account, subscription, network service, or
  vendor control plane.
- **Reproducible learning:** clean-machine smoke, package checksums, source-linked
  documentation, and deterministic bounded fixtures make behavior easier to
  verify than a large hosted system.
- **Explainable operations:** a learner or operator should be able to ask why an
  operation was denied, retried, recovered, or refused without reconstructing the
  answer from distributed telemetry.

These are product principles, not claims that Ember currently matches Azure's
scale, availability, or service breadth.

## Candidate next differentiators

The following are proposals, not implemented behavior. They should become separate
bounded issues with focused contract tests before implementation:

1. **Explain mode:** return a structured decision trace for scope denial, lock
   refusal, quota rejection, retry exhaustion, and safe-delete refusal.
2. **Deterministic event replay:** replay a bounded event journal against a test
   state directory with a fixed clock and compare the resulting state and metrics.
3. **Local policy packs:** let owners declare quotas, retention, allowed providers,
   and destructive-action rules as versioned, inspectable policy files.
4. **Causal operation timelines:** link request, operation, audit, event delivery,
   retry, dead-letter, and recovery records into one readable timeline.
5. **Provider conformance kits:** make a provider implement a small contract suite
   and produce the same safety/recovery evidence before it can be selected.
6. **A small operator UI/TUI:** show resources, event flow, dead letters, locks,
   recovery decisions, and explanations without hiding the underlying JSON/CLI
   contract.

The strongest next differentiator is probably **explain mode plus deterministic
replay**: it would make Ember unusually teachable and unusually good at answering
“what happened, and can I reproduce it?” without pretending to be a hosted Azure
replacement.
