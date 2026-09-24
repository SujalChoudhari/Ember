# Ember deployment, recovery, and release guide

Use deployment documents when you want to compare desired state with the local
operator and then apply an approved plan. The path is inspectable and bounded;
it is not a hosted deployment service.

## Plan and apply

Create a plan without mutation:

```text
ember --state-dir /tmp/ember-state deployment plan --file deployment.json --parameter tier=test
```

Apply the same document with explicit request identity and destructive approval:

```text
ember --state-dir /tmp/ember-state deployment apply --file deployment.json --parameter tier=test --request-id deploy-1 --correlation-id deploy-correlation-1 --confirm
```

Secure parameters are resolved into redacted state and evidence. Apply progress
can be queried by record or operation:

```text
ember --state-dir /tmp/ember-state deployment apply-progress list --limit 10
ember --state-dir /tmp/ember-state deployment apply-progress operation --id operation-00000001
```

Replaying the same request ID returns the original bounded operation. An apply
that fails records redacted progress rather than copying payloads or provider
error details into inspection output.

## Recovery

List or inspect recovery records, then choose an explicit bounded action:

```text
ember --state-dir /tmp/ember-state deployment recovery list --limit 10
ember --state-dir /tmp/ember-state deployment recovery get --id recovery-00000001
ember --state-dir /tmp/ember-state deployment recovery run --request-id recovery-1 --apply-progress-id apply-progress-00000001 --action rollback
```

Supported recovery actions are `rollback` and `forward`. Recovery is
idempotent by request ID and retains only bounded private journal evidence.
Incomplete command shapes are rejected; no implicit rollback or forward action
runs during inspection.

## Release evidence

Run the complete local verification matrix with:

```text
./scripts/ember-release-evidence.sh
```

It runs tests, race checks, vet, build, package checksum verification, and
clean-machine smoke. The [release evidence guide](sprint-2-release-evidence.md)
describes each gate and its non-publication boundary. The gate does not deploy,
release, publish, change repository visibility, or enable the GitHub Wiki.
