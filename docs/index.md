# Ember documentation

Ember's versioned, code-coupled documentation lives in this directory. The
repository is the source of truth for commands, flags, response shapes, and
behavior; documentation links back to the implementation and verified
repository workflows rather than maintaining a second contract.

## Start here

1. Follow the [clean-machine quickstart](../README.md#golden-clean-machine-lifecycle).
2. Read the [build, package, and install notes](../README.md#build-package-and-install).
3. Use the navigation below to distinguish behavior that exists today from
   roadmap work that has not been implemented.

## Learner path

- **Quickstart:** [Golden clean-machine lifecycle](../README.md#golden-clean-machine-lifecycle)
- **Architecture:** [Architecture and conceptual model](architecture.md)
- **Resource safety:** [Lifecycle, scopes, locks, and safe deletion](lifecycle-and-blobs.md)
- **Blob behavior:** [Blob, quota, checksum, range, and cleanup guide](lifecycle-and-blobs.md)

## Contributor path

- **Build and test:** [Build, package, and install](../README.md#build-package-and-install)
- **CLI contract:** [Versioned CLI reference](cli-reference.md)
- **Azure-shaped compatibility:** [Compatibility matrix](azure-shaped-compatibility.md)
- **Deployment and recovery:** [Deployment, recovery, and release guide](deployment-and-recovery.md)
- **Upgrade and release evidence:** [Sprint 2 release evidence](sprint-2-release-evidence.md)
- **Resource and Blob evidence:** [Sprint 2 resource and Blob evidence](sprint-2-resource-blob-evidence.md)
- **Queue and event evidence:** [Sprint 2 queue and event evidence](sprint-2-queue-event-evidence.md)
- **Documentation validation:** `scripts/ember-docs-check.sh`
- **Project work:** [Open issues](https://github.com/SujalChoudhari/Ember/issues) and [recent changes](https://github.com/SujalChoudhari/Ember/commits/main)

## Documentation status

### Implemented behavior covered by the current repository

- Local-first, single-node operation with bounded state under `--state-dir`.
- Resource creation and inspection, including the resource → bucket hierarchy
  used by the smoke path.
- Blob write and read through the CLI smoke path.
- Reproducible Go tests, platform-labelled packaging, checksum verification
  during installation, and confirmation-gated reset.
- Repeatable upgrade, rollback, package-provenance, race, static-analysis, and
  clean-machine release evidence through the [Sprint 2 release evidence](sprint-2-release-evidence.md)
  gate.
- Resource scopes, read-only locks, safe deletion, Blob integrity, bounded
  cleanup, and reset evidence through the [Sprint 2 resource and Blob evidence](sprint-2-resource-blob-evidence.md)
  guide.
- Queue retry, dead-letter, redrive, acknowledgement, event outcomes, and
  bounded metrics through the [Sprint 2 queue and event evidence](sprint-2-queue-event-evidence.md)
  guide.
- Azure-shaped resource, CLI, HTTP, operation, workload, network, queue, and
  safety semantics through the [compatibility matrix](azure-shaped-compatibility.md),
  with explicit hosted-Azure non-goals.
- Source-grounded architecture, CLI, lifecycle, deployment, recovery, and
  release navigation through the guides linked above. The attached GitHub Wiki
  remains pending the explicit visibility/Wiki authorization gate.

See the README quickstart and the source-linked issue-specific guides before
assuming behavior beyond these verified paths.

### Planned or separately documented

The detailed guides are checked against the implementation by
`scripts/ember-docs-check.sh` and the repository test/release gates. Planned
product areas must not be presented as available behavior.

### Deliberate non-goals and boundaries

- Ember is not a hosted Azure service, a cloud account, or a multi-node control
  plane.
- The current repository does not authorize public visibility, Wiki enablement,
  deployment, release, or publication changes. Wiki navigation remains gated by
  [issue #149](https://github.com/SujalChoudhari/Ember/issues/149).
- This documentation surface does not implement roadmap product features or
  create a separate `Ember.wiki` repository.
