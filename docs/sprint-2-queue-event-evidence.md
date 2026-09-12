# Ember Sprint 2 queue and event evidence

This guide records the bounded, operator-visible queue and event acceptance path
for the Day 2 closeout. It is local-first evidence only and does not claim distributed or exactly-once delivery semantics.

## Focused acceptance gate

Run the composed probe and the lower-level queue/event matrices:

```text
go test ./internal/ember -run TestSprintTwoQueueEventSurfacesShareBoundedRecoveryEvidence -count=1
go test ./internal/ember/queue ./internal/ember/events -count=1
```

`TestSprintTwoQueueEventSurfacesShareBoundedRecoveryEvidence` proves one
disposable state can cover:

- bounded queue retry and terminal dead-letter recording;
- successful redrive and idempotent replay without a duplicate consumer call;
- correlated queue receive/acknowledgement progress;
- event retry, terminal failure, dead-letter recovery, and correlation; and
- bounded delivery, retry, dead-letter, and recovery metrics.

The package-level tests additionally cover restart persistence, dead-letter
retention limits, concurrent redrive exclusion, payload/error redaction, stale
receipts, acknowledgement, topic subscriptions, and constant metric
cardinality.

## Verification matrix

The repository-wide gate is:

```text
./scripts/ember-release-evidence.sh
```

It runs full tests, race checks, vet, build, package checksum verification, and
clean-machine smoke. The focused commands make the queue/event acceptance
boundary explicit for review.

## Boundaries

- Retry counts, backoff, queue storage, dead-letter records, and metric
  cardinality are bounded by their package contracts.
- Failure reasons and correlation identifiers are retained as stable metadata;
  payloads and consumer error details are not persisted in dead-letter state.
- Redrive is idempotent per request identity and does not imply distributed
  exactly-once processing.
