# Ember Azure-shaped compatibility matrix

This matrix is generated from the implemented local-first operator, CLI, HTTP,
and persistence contracts in the current repository. “Azure-shaped” describes
resource and operator semantics that are useful for compatibility-oriented
examples; it does not claim Azure REST, ARM, cloud-provider, or multi-node
compatibility.

## Supported surfaces

| Surface | Supported contract | CLI / HTTP entry points | Contract tests |
|---|---|---|---|
| Resources and scopes | Groups and buckets, parent scope, deterministic IDs, bounded list, provider metadata, desired/observed state, scope denial | `resource create|get|list|update-tags|delete`; `POST/GET /v1/resources` and resource paths | `TestCLIUsesSharedScopedResourceAndOperationContract`, `TestCLIExposesCompleteResourceLifecycleAndLockSurface`, `TestHTTPResourceLifecycleUsesScopedOperatorContract` |
| Resource locks | Read-only ancestor lock, inspect, owner/token release, mutation refusal, process-local lock state | `resource lock acquire|inspect|release` | `TestCLIExposesCompleteResourceLifecycleAndLockSurface`, `TestOperatorExposesCompleteResourceLifecycleAndLockSurface`, `TestFileOperatorReopenPreservesResourcesAndProcessLocalLockContract` |
| Safe deletion | Dependent deletion refusal and explicit destructive confirmation | `resource delete --confirm`; `DELETE /v1/resources/{id}?confirm=true` | `TestCLIExposesCompleteResourceLifecycleAndLockSurface`, `TestOperatorExposesCompleteResourceLifecycleAndLockSurface` |
| Blobs | Bounded object metadata, quota, retention, sorted list, range reads, checksums, corruption inspection, explicit recovery and cleanup | `blob put|get|verify|recover|list|delete`; `/v1/buckets/{id}/objects/...` | `TestCLIExposesCompleteBlobLifecycle`, `TestCLIExposesBoundedBlobIntegrityAndAuditedRecovery`, `TestFileBlobStoreReportsTamperedContentAndRecoversExplicitly`, `TestFileBlobStoreCleansExpiredAndCorruptObjectsWithinOwnedBound` |
| Operations and audit | Correlation/request identity, idempotent replay, stable status, scoped inspection, redacted audit entries | `operation get|list`, `audit list`; `/v1/operations/...`, resource audit paths | `TestCLIUsesSharedScopedResourceAndOperationContract`, `TestHTTPOperationBlobAndResetFlow`, `TestOperatorCoordinatesIdempotentMutationAndAuditInspection` |
| Deployment and recovery | Plan/apply, destructive approval, bounded apply progress, recovery inspection and explicit rollback/forward action | `deployment plan|apply|apply-progress|recovery`; deployment HTTP paths | `TestCLIExposesDeploymentPlanAndApplyLifecycle`, `TestCLIAndHTTPExposeExplicitRecoveryAndBoundedLists`, `TestFileOperatorWiresDeploymentInspectionAndIdempotentRollback` |
| Workloads and observability | Provider metadata, bounded resource requirements, health/readiness, restart execution identity, bounded logs and metrics | `workload ...`, `observability metrics`; `/v1/workloads/...`, observability paths | `TestCLIExposesSafeWorkloadInspectionJourney`, `TestHTTPWorkloadAcceptanceWalkthroughExposesHealthLogsRestartAndCorrelation`, `TestCLIExposesBoundedMetricsSnapshot` |
| Network resources | Scoped networks, ports, endpoints, deterministic lifecycle and cross-scope redaction | HTTP `/v1/networks/...`, network port/endpoint paths | `TestHTTPExposesScopedNetworkLifecycle`, `TestNetworkManagerPublishesAndResolvesScopedEndpoints` |
| Queue and events | Bounded retry, dead-letter, acknowledgement, redrive, event outcomes, correlation and constant-cardinality metrics | Package contracts; composed evidence in `TestSprintTwoQueueEventSurfacesShareBoundedRecoveryEvidence` | `TestFileDeadLetterStoreRedrivesIdempotentlyAndSurvivesRestart`, `TestDeliverRecordsBoundedEventFailureAndPreservesIdentity`, `TestMetricsTracksBoundedDeliveryAndRecoveryOutcomes` |
| Reset and safety | Confirmation-gated root reset, owned-path cleanup, no implicit publication or deployment | `reset --confirm`; `POST /v1/reset?confirm=true` | `TestCLIDestructiveActionsRequireExplicitConfirmation`, `TestHTTPOperationBlobAndResetFlow`, `TestFileOperatorResetsOwnedStateAndReopensDeterministically` |

The listed tests are the contract evidence for the corresponding matrix rows;
the closeout evidence guides provide the clean-state commands for each bounded
surface.

## Common compatibility rules

- State is file-backed, local-first, single-node, and bounded under
  `--state-dir`.
- HTTP requests use JSON response envelopes and `X-Ember-Scope` for operator
  scope. CLI and HTTP calls share the same operator control-plane contracts.
- Destructive actions require explicit confirmation or approval. Read-only
  inspection does not mutate state.
- Request and correlation identifiers are preserved where a mutation or
  delivery contract supports them. Replays return the original bounded result.
- Error output uses stable class messages and does not expose secrets, payloads,
  provider error details, or cross-scope resource data.

## Explicitly unsupported

The repository does not implement or claim:

- Azure REST or ARM wire compatibility, Azure authentication, subscriptions,
  tenants, billing, or hosted cloud resources;
- multi-node coordination, distributed locks, or distributed/exactly-once
  delivery;
- implicit deployment, release, publication, repository visibility changes, or
  GitHub Wiki enablement; or
- provider-specific recovery payloads that are intentionally absent from
  redacted progress records.

New compatibility entries must be paired with a focused contract test and must
preserve these local-first and safety boundaries.
