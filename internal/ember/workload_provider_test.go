package ember

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

func workloadProviderMetadata(version string) models.ProviderMetadata {
	return models.ProviderMetadata{Namespace: "Ember.Compute", Type: "workloads", Version: version}
}

func workloadResourceSpec(scopeID, name string, provider models.ProviderMetadata) models.ResourceSpec {
	return models.ResourceSpec{
		Type:         models.ResourceTypeWorkload,
		Name:         name,
		ParentID:     scopeID,
		Provider:     provider,
		DesiredState: models.ResourceStateReady,
	}
}

type unsupportedWorkloadUpdateProvider struct {
	*MemoryWorkloadProvider
}

func (provider *unsupportedWorkloadUpdateProvider) Update(context.Context, models.Resource) (models.WorkloadStatus, error) {
	return models.WorkloadStatus{}, errProviderOperationUnsupported
}

func TestWorkloadProviderBoundaryScopesLifecycleAndStatus(t *testing.T) {
	ctx := context.Background()
	resourceStore, err := persistence.NewFileResourceStore(filepath.Join(t.TempDir(), "resources.json"))
	if err != nil {
		t.Fatalf("NewFileResourceStore() error = %v", err)
	}
	resources, err := NewResourceManager(resourceStore)
	if err != nil {
		t.Fatalf("NewResourceManager() error = %v", err)
	}
	registry, err := NewWorkloadProviderRegistry()
	if err != nil {
		t.Fatalf("NewWorkloadProviderRegistry() error = %v", err)
	}
	provider := NewMemoryWorkloadProvider()
	metadata := workloadProviderMetadata("v1")
	if err := registry.Register(metadata, provider); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	manager, err := NewWorkloadManager(resources, registry)
	if err != nil {
		t.Fatalf("NewWorkloadManager() error = %v", err)
	}

	root, err := resources.CreateResource(ctx, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "compute"})
	if err != nil {
		t.Fatalf("CreateResource(root) error = %v", err)
	}
	other, err := resources.CreateResource(ctx, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "other"})
	if err != nil {
		t.Fatalf("CreateResource(other) error = %v", err)
	}

	created, err := manager.CreateWorkload(ctx, root.ID, workloadResourceSpec(root.ID, "api", metadata))
	if err != nil {
		t.Fatalf("CreateWorkload() error = %v", err)
	}
	if created.Resource.ID == "" || created.Resource.Spec.Type != models.ResourceTypeWorkload || created.Resource.Spec.ParentID != root.ID {
		t.Fatalf("CreateWorkload() resource = %#v, want scoped workload identity", created.Resource)
	}
	if created.Resource.ObservedState != models.ResourceStateReady || created.Status.ObservedState != models.ResourceStateReady {
		t.Fatalf("CreateWorkload() states = resource %q/status %q, want ready", created.Resource.ObservedState, created.Status.ObservedState)
	}
	if created.Status.ExecutionID == "" || len(created.Status.ExecutionID) > models.MaxWorkloadExecutionIDLength {
		t.Fatalf("CreateWorkload() execution metadata = %#v, want bounded ID", created.Status)
	}
	if strings.Contains(created.Status.Reason, "secret") {
		t.Fatalf("CreateWorkload() status reason leaked secret marker: %q", created.Status.Reason)
	}

	got, err := manager.GetWorkload(ctx, root.ID, created.Resource.ID)
	if err != nil {
		t.Fatalf("GetWorkload() error = %v", err)
	}
	if got.Resource.ID != created.Resource.ID || got.Resource.Spec.Name != "api" || got.Status.ObservedState != models.ResourceStateReady {
		t.Fatalf("GetWorkload() = %#v, want stable resource and status", got)
	}
	if _, err := manager.GetWorkload(ctx, other.ID, created.Resource.ID); !errors.Is(err, ErrWorkloadNotFound) {
		t.Fatalf("GetWorkload(cross scope) error = %v, want ErrWorkloadNotFound", err)
	}

	updated, err := manager.UpdateWorkload(ctx, root.ID, created.Resource.ID)
	if err != nil {
		t.Fatalf("UpdateWorkload() error = %v", err)
	}
	if updated.Resource.ID != created.Resource.ID || updated.Status.ObservedState != models.ResourceStateReady {
		t.Fatalf("UpdateWorkload() = %#v, want stable identity and status", updated)
	}

	if err := manager.DeleteWorkload(ctx, root.ID, created.Resource.ID); err != nil {
		t.Fatalf("DeleteWorkload() error = %v", err)
	}
	if _, err := manager.GetWorkload(ctx, root.ID, created.Resource.ID); !errors.Is(err, ErrWorkloadNotFound) {
		t.Fatalf("GetWorkload(deleted) error = %v, want ErrWorkloadNotFound", err)
	}
}

func TestWorkloadProviderBoundaryReportsHealthAndReadinessTransitions(t *testing.T) {
	ctx := context.Background()
	resources, err := NewResourceManager(newWorkloadFileResourceStore(t))
	if err != nil {
		t.Fatalf("NewResourceManager() error = %v", err)
	}
	registry, err := NewWorkloadProviderRegistry()
	if err != nil {
		t.Fatalf("NewWorkloadProviderRegistry() error = %v", err)
	}
	metadata := workloadProviderMetadata("v1")
	provider := &transitioningWorkloadProvider{statuses: []models.WorkloadStatus{
		{
			ObservedState: models.ResourceStatePending,
			Health:        models.WorkloadHealthUnknown,
			Readiness:     models.WorkloadReadinessNotReady,
			Reason:        "starting",
			ExecutionID:   "execution-1",
		},
		{
			ObservedState: models.ResourceStateReady,
			Health:        models.WorkloadHealthHealthy,
			Readiness:     models.WorkloadReadinessReady,
			Reason:        "health check passed",
			ExecutionID:   "execution-2",
		},
		{
			ObservedState: models.ResourceStateFailed,
			Health:        models.WorkloadHealthUnhealthy,
			Readiness:     models.WorkloadReadinessNotReady,
			Reason:        "api-key=super-secret",
			ExecutionID:   "token=super-secret",
		},
	}}
	if err := registry.Register(metadata, provider); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	manager, err := NewWorkloadManager(resources, registry)
	if err != nil {
		t.Fatalf("NewWorkloadManager() error = %v", err)
	}
	root, err := resources.CreateResource(ctx, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "compute"})
	if err != nil {
		t.Fatalf("CreateResource(root) error = %v", err)
	}
	created, err := manager.CreateWorkload(ctx, root.ID, workloadResourceSpec(root.ID, "api", metadata))
	if err != nil {
		t.Fatalf("CreateWorkload() error = %v", err)
	}
	if created.Status.Health != models.WorkloadHealthUnknown || created.Status.Readiness != models.WorkloadReadinessNotReady {
		t.Fatalf("initial workload status = %#v, want unknown and not-ready", created.Status)
	}

	healthy, err := manager.GetWorkload(ctx, root.ID, created.Resource.ID)
	if err != nil {
		t.Fatalf("GetWorkload(healthy) error = %v", err)
	}
	if healthy.Status.Health != models.WorkloadHealthHealthy || healthy.Status.Readiness != models.WorkloadReadinessReady {
		t.Fatalf("healthy workload status = %#v, want healthy and ready", healthy.Status)
	}

	unhealthy, err := manager.GetWorkload(ctx, root.ID, created.Resource.ID)
	if err != nil {
		t.Fatalf("GetWorkload(unhealthy) error = %v", err)
	}
	if unhealthy.Status.Health != models.WorkloadHealthUnhealthy || unhealthy.Status.Readiness != models.WorkloadReadinessNotReady {
		t.Fatalf("unhealthy workload status = %#v, want unhealthy and not-ready", unhealthy.Status)
	}
	if unhealthy.Status.Reason != "[redacted]" || unhealthy.Status.ExecutionID != "[redacted]" {
		t.Fatalf("unhealthy workload status = %#v, want redacted sensitive fields", unhealthy.Status)
	}
	if unhealthy.Resource.Spec.ParentID != root.ID || unhealthy.Resource.ID != created.Resource.ID {
		t.Fatalf("unhealthy workload resource = %#v, want original scoped identity", unhealthy.Resource)
	}
}

type transitioningWorkloadProvider struct {
	mu       sync.Mutex
	statuses []models.WorkloadStatus
	getCall  int
}

func (provider *transitioningWorkloadProvider) Create(context.Context, models.Resource) (models.WorkloadStatus, error) {
	return provider.statuses[0], nil
}

func (provider *transitioningWorkloadProvider) Get(context.Context, models.Resource) (models.WorkloadStatus, error) {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	status := provider.statuses[provider.getCall+1]
	provider.getCall++
	return status, nil
}

func (provider *transitioningWorkloadProvider) Update(context.Context, models.Resource) (models.WorkloadStatus, error) {
	return provider.statuses[len(provider.statuses)-1], nil
}

func (provider *transitioningWorkloadProvider) Delete(context.Context, models.Resource) error {
	return nil
}

func TestWorkloadProviderBoundaryMapsUnsupportedAndInvalidProviderResults(t *testing.T) {
	ctx := context.Background()
	resources, err := NewResourceManager(newWorkloadFileResourceStore(t))
	if err != nil {
		t.Fatalf("NewResourceManager() error = %v", err)
	}
	registry, err := NewWorkloadProviderRegistry()
	if err != nil {
		t.Fatalf("NewWorkloadProviderRegistry() error = %v", err)
	}
	provider := &unsupportedWorkloadUpdateProvider{MemoryWorkloadProvider: NewMemoryWorkloadProvider()}
	metadata := workloadProviderMetadata("v2")
	if err := registry.Register(metadata, provider); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	manager, err := NewWorkloadManager(resources, registry)
	if err != nil {
		t.Fatalf("NewWorkloadManager() error = %v", err)
	}
	root, err := resources.CreateResource(ctx, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "compute"})
	if err != nil {
		t.Fatalf("CreateResource(root) error = %v", err)
	}
	created, err := manager.CreateWorkload(ctx, root.ID, workloadResourceSpec(root.ID, "worker", metadata))
	if err != nil {
		t.Fatalf("CreateWorkload() error = %v", err)
	}
	updateErr := error(nil)
	if _, updateErr = manager.UpdateWorkload(ctx, root.ID, created.Resource.ID); !errors.Is(updateErr, ErrWorkloadProviderUnsupported) {
		t.Fatalf("UpdateWorkload(unsupported) error = %v, want ErrWorkloadProviderUnsupported", updateErr)
	}
	if strings.Contains(updateErr.Error(), "secret") {
		t.Fatalf("UpdateWorkload(unsupported) leaked provider details: %v", updateErr)
	}

	invalid := &invalidWorkloadStatusProvider{MemoryWorkloadProvider: NewMemoryWorkloadProvider()}
	invalidMetadata := workloadProviderMetadata("v3")
	if err := registry.Register(invalidMetadata, invalid); err != nil {
		t.Fatalf("Register(invalid) error = %v", err)
	}
	invalidCreated, err := manager.CreateWorkload(ctx, root.ID, workloadResourceSpec(root.ID, "invalid", invalidMetadata))
	if err == nil {
		t.Fatalf("CreateWorkload(invalid status) = %#v, want error", invalidCreated)
	}
	if !errors.Is(err, ErrInvalidWorkloadProviderStatus) {
		t.Fatalf("CreateWorkload(invalid status) error = %v, want ErrInvalidWorkloadProviderStatus", err)
	}
	if strings.Contains(err.Error(), "provider-secret") {
		t.Fatalf("CreateWorkload(invalid status) leaked provider details: %v", err)
	}
}

type invalidWorkloadStatusProvider struct {
	*MemoryWorkloadProvider
}

func (provider *invalidWorkloadStatusProvider) Create(context.Context, models.Resource) (models.WorkloadStatus, error) {
	return models.WorkloadStatus{ObservedState: models.ResourceState("invalid"), Reason: "provider-secret"}, nil
}

func TestWorkloadProviderRegistryRejectsDuplicateAndUnboundedRegistrations(t *testing.T) {
	registry, err := NewWorkloadProviderRegistry()
	if err != nil {
		t.Fatalf("NewWorkloadProviderRegistry() error = %v", err)
	}
	metadata := workloadProviderMetadata("v1")
	provider := NewMemoryWorkloadProvider()
	if err := registry.Register(metadata, provider); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	if err := registry.Register(metadata, provider); !errors.Is(err, ErrDuplicateWorkloadProvider) {
		t.Fatalf("duplicate Register() error = %v, want ErrDuplicateWorkloadProvider", err)
	}
	if _, err := registry.Resolve(workloadProviderMetadata("missing")); !errors.Is(err, ErrWorkloadProviderNotFound) {
		t.Fatalf("Resolve(missing) error = %v, want ErrWorkloadProviderNotFound", err)
	}
	if _, err := NewWorkloadManager(nil, registry); !errors.Is(err, ErrInvalidWorkloadControlPlane) {
		t.Fatalf("NewWorkloadManager(nil resources) error = %v, want ErrInvalidWorkloadControlPlane", err)
	}
	if _, err := NewWorkloadManager(resourcesForWorkloadTest(t), nil); !errors.Is(err, ErrInvalidWorkloadControlPlane) {
		t.Fatalf("NewWorkloadManager(nil registry) error = %v, want ErrInvalidWorkloadControlPlane", err)
	}
}

func resourcesForWorkloadTest(t *testing.T) ResourceControlPlane {
	t.Helper()
	resources, err := NewResourceManager(newWorkloadFileResourceStore(t))
	if err != nil {
		t.Fatalf("NewResourceManager() error = %v", err)
	}
	return resources
}

func newWorkloadFileResourceStore(t *testing.T) persistence.ResourceStore {
	t.Helper()
	store, err := persistence.NewFileResourceStore(filepath.Join(t.TempDir(), "resources.json"))
	if err != nil {
		t.Fatalf("NewFileResourceStore() error = %v", err)
	}
	return store
}
