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
- **Architecture:** [Architecture and conceptual model](https://github.com/SujalChoudhari/Ember/issues/144) (planned)
- **Resource safety:** [Lifecycle, scopes, locks, and safe deletion](https://github.com/SujalChoudhari/Ember/issues/146) (planned)
- **Blob behavior:** [Blob, quota, checksum, range, and cleanup guide](https://github.com/SujalChoudhari/Ember/issues/147) (planned)

## Contributor path

- **Build and test:** [Build, package, and install](../README.md#build-package-and-install)
- **CLI contract:** [Versioned CLI reference](https://github.com/SujalChoudhari/Ember/issues/145) (planned)
- **Upgrade and release evidence:** [Sprint 2 release evidence](sprint-2-release-evidence.md)
- **Resource and Blob evidence:** [Sprint 2 resource and Blob evidence](sprint-2-resource-blob-evidence.md)
- **Queue and event evidence:** [Sprint 2 queue and event evidence](sprint-2-queue-event-evidence.md)
- **Deployment and recovery:** [Operations and release evidence](https://github.com/SujalChoudhari/Ember/issues/148) (planned)
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

See the README quickstart and the source-linked issue-specific guides before
assuming behavior beyond these verified paths.

### Planned or separately documented

The detailed architecture, CLI, lifecycle, Blob, deployment, and release guides
are tracked as separate issues so each can be checked against the implementation
without turning this index into a duplicate command reference. Planned product
areas must not be presented as available behavior.

### Deliberate non-goals and boundaries

- Ember is not a hosted Azure service, a cloud account, or a multi-node control
  plane.
- The current repository does not authorize public visibility, Wiki enablement,
  deployment, release, or publication changes. Wiki navigation remains gated by
  [issue #149](https://github.com/SujalChoudhari/Ember/issues/149).
- This documentation surface does not implement roadmap product features or
  create a separate `Ember.wiki` repository.
