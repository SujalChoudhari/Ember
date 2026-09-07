package ember

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/operationcontext"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

type identityCapturingWorkloadProvider struct {
	observed operationcontext.Identity
	found    bool
}

func (provider *identityCapturingWorkloadProvider) Create(context.Context, models.Resource) (models.WorkloadStatus, error) {
	return models.WorkloadStatus{ObservedState: models.ResourceStateReady}, nil
}

func (provider *identityCapturingWorkloadProvider) Get(context.Context, models.Resource) (models.WorkloadStatus, error) {
	return models.WorkloadStatus{ObservedState: models.ResourceStateReady}, nil
}

func (provider *identityCapturingWorkloadProvider) Update(ctx context.Context, _ models.Resource) (models.WorkloadStatus, error) {
	provider.observed, provider.found = operationcontext.From(ctx)
	return models.WorkloadStatus{ObservedState: models.ResourceStateReady}, nil
}

func (provider *identityCapturingWorkloadProvider) Delete(context.Context, models.Resource) error {
	return nil
}

func TestResourceOperationIdentityReachesWorkloadProvider(t *testing.T) {
	ctx := context.Background()
	resources, err := NewResourceManager(mustIdentityResourceStore(t))
	if err != nil {
		t.Fatalf("NewResourceManager() error = %v", err)
	}
	registry, err := NewWorkloadProviderRegistry()
	if err != nil {
		t.Fatalf("NewWorkloadProviderRegistry() error = %v", err)
	}
	provider := &identityCapturingWorkloadProvider{}
	metadata := models.ProviderMetadata{Namespace: "Ember.Compute", Type: "workloads", Version: "v1"}
	if err := registry.Register(metadata, provider); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	workloads, err := NewWorkloadManager(resources, registry)
	if err != nil {
		t.Fatalf("NewWorkloadManager() error = %v", err)
	}

	group, err := resources.CreateResource(ctx, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "compute"})
	if err != nil {
		t.Fatalf("CreateResource(group) error = %v", err)
	}
	workload, err := workloads.CreateWorkload(ctx, group.ID, models.ResourceSpec{
		Type:         models.ResourceTypeWorkload,
		Name:         "api",
		ParentID:     group.ID,
		Provider:     metadata,
		DesiredState: models.ResourceStateReady,
	})
	if err != nil {
		t.Fatalf("CreateWorkload() error = %v", err)
	}

	operations, err := persistence.NewFileOperationStore(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatalf("NewFileOperationStore() error = %v", err)
	}
	audits, err := persistence.NewFileAuditStore(filepath.Join(t.TempDir(), "audit.json"))
	if err != nil {
		t.Fatalf("NewFileAuditStore() error = %v", err)
	}
	coordinator, err := NewResourceOperationCoordinator(operations, audits)
	if err != nil {
		t.Fatalf("NewResourceOperationCoordinator() error = %v", err)
	}

	request := ResourceOperationRequest{
		ResourceID:    workload.Resource.ID,
		ScopeID:       group.ID,
		Action:        "workload.update",
		RequestID:     "request-workload-update",
		CorrelationID: "correlation-workload-update",
	}
	result, err := coordinator.Execute(ctx, request, func(effectContext context.Context) error {
		_, err := workloads.UpdateWorkload(effectContext, group.ID, workload.Resource.ID)
		return err
	})
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result == nil || !provider.found {
		t.Fatalf("provider identity = %#v, found = %v; want operation identity", provider.observed, provider.found)
	}
	if provider.observed.OperationID != result.Operation.ID ||
		provider.observed.RequestID != request.RequestID ||
		provider.observed.CorrelationID != request.CorrelationID {
		t.Fatalf("provider identity = %#v, operation = %#v, request = %#v; want consistent identifiers", provider.observed, result.Operation, request)
	}
	history, err := coordinator.ListAuditHistory(ctx, workload.Resource.ID, 10)
	if err != nil {
		t.Fatalf("ListAuditHistory() error = %v", err)
	}
	if len(history) != 1 || history[0].OperationID != result.Operation.ID ||
		history[0].ResourceID != workload.Resource.ID || history[0].ScopeID != request.ScopeID ||
		history[0].RequestID != request.RequestID || history[0].CorrelationID != request.CorrelationID {
		t.Fatalf("audit history = %#v, operation = %#v, request = %#v; want scoped consistent identifiers", history, result.Operation, request)
	}
}

func mustIdentityResourceStore(t *testing.T) *persistence.FileResourceStore {
	t.Helper()
	store, err := persistence.NewFileResourceStore(filepath.Join(t.TempDir(), "resources.json"))
	if err != nil {
		t.Fatalf("NewFileResourceStore() error = %v", err)
	}
	return store
}
