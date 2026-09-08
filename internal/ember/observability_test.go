package ember

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/events"
	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
	"github.com/SujalChoudhari/Ember/internal/ember/queue"
)

func TestObservabilityManagerExposesBoundedWorkloadHealthLogsAndMetrics(t *testing.T) {
	ctx := context.Background()
	resources, err := NewResourceManager(newObservabilityResourceStore(t))
	if err != nil {
		t.Fatalf("NewResourceManager() error = %v", err)
	}
	registry, err := NewWorkloadProviderRegistry()
	if err != nil {
		t.Fatalf("NewWorkloadProviderRegistry() error = %v", err)
	}
	metadata := workloadProviderMetadata("observability-v1")
	provider := &observabilityWorkloadProvider{
		statuses: []models.WorkloadStatus{
			{ObservedState: models.ResourceStatePending, Health: models.WorkloadHealthUnknown, Readiness: models.WorkloadReadinessNotReady, Reason: "starting", ExecutionID: "execution-0"},
			{ObservedState: models.ResourceStateReady, Health: models.WorkloadHealthHealthy, Readiness: models.WorkloadReadinessReady, Reason: "ready", ExecutionID: "execution-1"},
			{ObservedState: models.ResourceStateFailed, Health: models.WorkloadHealthUnhealthy, Readiness: models.WorkloadReadinessNotReady, Reason: "api-key=super-secret", ExecutionID: "token=super-secret"},
			{ObservedState: models.ResourceStateReady, Health: models.WorkloadHealthHealthy, Readiness: models.WorkloadReadinessReady, Reason: "recovered", ExecutionID: "execution-3"},
		},
		logs: []models.WorkloadLog{
			{Timestamp: time.Unix(1, 0).UTC(), Stream: "stderr", Message: "token=super-secret", ExecutionID: "execution-3"},
			{Timestamp: time.Unix(2, 0).UTC(), Stream: "stdout", Message: "recovered", ExecutionID: "execution-3"},
		},
	}
	if err := registry.Register(metadata, provider); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	workloads, err := NewWorkloadManager(resources, registry)
	if err != nil {
		t.Fatalf("NewWorkloadManager() error = %v", err)
	}
	metrics := events.NewMetrics()
	deadLetters, err := queue.NewFileDeadLetterStore(filepath.Join(t.TempDir(), "dead-letters.json"), queue.DeadLetterStoreOptions{MaxRecords: 2})
	if err != nil {
		t.Fatalf("NewFileDeadLetterStore() error = %v", err)
	}
	if _, err := events.DeliverWithMetrics(ctx, events.Event{ID: "event-1", CorrelationID: "correlation-1", Type: "resource.updated"}, queue.RetryPolicy{}, func(context.Context, events.Event) error { return nil }, func(context.Context, time.Duration) error { return nil }, deadLetters, metrics); err != nil {
		t.Fatalf("DeliverWithMetrics() error = %v", err)
	}
	observer, err := NewObservabilityManager(workloads, metrics)
	if err != nil {
		t.Fatalf("NewObservabilityManager() error = %v", err)
	}

	root, err := resources.CreateResource(ctx, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "compute"})
	if err != nil {
		t.Fatalf("CreateResource() error = %v", err)
	}
	created, err := workloads.CreateWorkload(ctx, root.ID, workloadResourceSpec(root.ID, "api", metadata))
	if err != nil {
		t.Fatalf("CreateWorkload() error = %v", err)
	}

	first, err := observer.InspectWorkload(ctx, root.ID, created.Resource.ID, 1)
	if err != nil {
		t.Fatalf("InspectWorkload(healthy) error = %v", err)
	}
	if first.Status.Health != models.WorkloadHealthHealthy || first.Status.Readiness != models.WorkloadReadinessReady {
		t.Fatalf("InspectWorkload(healthy) status = %#v, want healthy and ready", first.Status)
	}
	if len(first.Logs) != 1 || first.Logs[0].Message != "[redacted]" {
		t.Fatalf("InspectWorkload(healthy) logs = %#v, want one redacted record", first.Logs)
	}

	failed, err := observer.InspectWorkload(ctx, root.ID, created.Resource.ID, 1)
	if err != nil {
		t.Fatalf("InspectWorkload(failed) error = %v", err)
	}
	if failed.Status.Health != models.WorkloadHealthUnhealthy || failed.Status.Readiness != models.WorkloadReadinessNotReady || failed.Status.Reason != "[redacted]" {
		t.Fatalf("InspectWorkload(failed) status = %#v, want redacted unhealthy status", failed.Status)
	}

	recovered, err := observer.InspectWorkload(ctx, root.ID, created.Resource.ID, 2)
	if err != nil {
		t.Fatalf("InspectWorkload(recovered) error = %v", err)
	}
	if recovered.Status.Health != models.WorkloadHealthHealthy || recovered.Status.Readiness != models.WorkloadReadinessReady || len(recovered.Logs) != 2 {
		t.Fatalf("InspectWorkload(recovered) = %#v, want recovered health and two bounded logs", recovered)
	}
	if recovered.Metrics.Cardinality != 1 || recovered.Metrics.DeliveryCount != 1 {
		t.Fatalf("InspectWorkload(recovered) metrics = %#v, want one constant-cardinality delivery", recovered.Metrics)
	}
	if _, err := observer.InspectWorkload(ctx, root.ID, created.Resource.ID, 0); !errors.Is(err, ErrInvalidObservabilityLogLimit) {
		t.Fatalf("InspectWorkload(zero limit) error = %v, want ErrInvalidObservabilityLogLimit", err)
	}
}

func TestObservabilityManagerRejectsInvalidDependencies(t *testing.T) {
	resources, err := NewResourceManager(newObservabilityResourceStore(t))
	if err != nil {
		t.Fatalf("NewResourceManager() error = %v", err)
	}
	registry, err := NewWorkloadProviderRegistry()
	if err != nil {
		t.Fatalf("NewWorkloadProviderRegistry() error = %v", err)
	}
	workloads, err := NewWorkloadManager(resources, registry)
	if err != nil {
		t.Fatalf("NewWorkloadManager() error = %v", err)
	}
	if _, err := NewObservabilityManager(nil, events.NewMetrics()); !errors.Is(err, ErrInvalidObservabilityManager) {
		t.Fatalf("NewObservabilityManager(nil workloads) error = %v, want ErrInvalidObservabilityManager", err)
	}
	if _, err := NewObservabilityManager(workloads, nil); !errors.Is(err, ErrInvalidObservabilityManager) {
		t.Fatalf("NewObservabilityManager(nil metrics) error = %v, want ErrInvalidObservabilityManager", err)
	}
}

type observabilityWorkloadProvider struct {
	statuses []models.WorkloadStatus
	logs     []models.WorkloadLog
	getCall  int
}

func (provider *observabilityWorkloadProvider) Create(context.Context, models.Resource) (models.WorkloadStatus, error) {
	return provider.statuses[0], nil
}

func (provider *observabilityWorkloadProvider) Get(context.Context, models.Resource) (models.WorkloadStatus, error) {
	provider.getCall++
	return provider.statuses[provider.getCall], nil
}

func (provider *observabilityWorkloadProvider) Update(context.Context, models.Resource) (models.WorkloadStatus, error) {
	return provider.statuses[len(provider.statuses)-1], nil
}

func (provider *observabilityWorkloadProvider) Delete(context.Context, models.Resource) error {
	return nil
}

func (provider *observabilityWorkloadProvider) Logs(context.Context, models.Resource, int) ([]models.WorkloadLog, error) {
	return provider.logs, nil
}

func newObservabilityResourceStore(t *testing.T) persistence.ResourceStore {
	t.Helper()
	store, err := persistence.NewFileResourceStore(filepath.Join(t.TempDir(), "resources.json"))
	if err != nil {
		t.Fatalf("NewFileResourceStore() error = %v", err)
	}
	return store
}
