package ember

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

func (provider *unsupportedWorkloadUpdateProvider) Restart(context.Context, models.Resource) (models.WorkloadStatus, error) {
	return models.WorkloadStatus{}, errProviderOperationUnsupported
}

type failingReconcileProvider struct {
	*MemoryWorkloadProvider
	failUpdates bool
}

func (provider *failingReconcileProvider) Get(_ context.Context, resource models.Resource) (models.WorkloadStatus, error) {
	if provider.failUpdates {
		return workloadResourceLimitStatus(resource), nil
	}
	return provider.MemoryWorkloadProvider.Get(context.Background(), resource)
}

func (provider *failingReconcileProvider) Update(ctx context.Context, resource models.Resource) (models.WorkloadStatus, error) {
	if provider.failUpdates {
		status := workloadResourceLimitStatus(resource)
		provider.MemoryWorkloadProvider.mu.Lock()
		provider.MemoryWorkloadProvider.workloads[resource.ID] = status
		provider.MemoryWorkloadProvider.mu.Unlock()
		return status, errProviderResourceLimit
	}
	return provider.MemoryWorkloadProvider.Update(ctx, resource)
}

type workloadProviderWithoutVolumeCleanup struct {
	*MemoryWorkloadProvider
}

func (provider *workloadProviderWithoutVolumeCleanup) Delete(context.Context, models.Resource) error {
	return nil
}

func TestWorkloadProviderPersistsObservedStateAfterReconciliation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "resources.json")
	resourceStore, err := persistence.NewFileResourceStore(path)
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
	metadata := workloadProviderMetadata("reconcile-v1")
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
	stored, err := resourceStore.Get(ctx, root.ID, created.Resource.ID)
	if err != nil {
		t.Fatalf("resource store Get() error = %v", err)
	}
	if stored.ObservedState != models.ResourceStateReady {
		t.Fatalf("persisted observed state = %q, want %q", stored.ObservedState, models.ResourceStateReady)
	}
	reopened, err := persistence.NewFileResourceStore(path)
	if err != nil {
		t.Fatalf("NewFileResourceStore(reopen) error = %v", err)
	}
	reopenedResource, err := reopened.Get(ctx, root.ID, created.Resource.ID)
	if err != nil {
		t.Fatalf("reopened resource Get() error = %v", err)
	}
	if reopenedResource.ObservedState != models.ResourceStateReady {
		t.Fatalf("reopened observed state = %q, want %q", reopenedResource.ObservedState, models.ResourceStateReady)
	}
}

func TestWorkloadProviderPersistsStableFailureFromReconciliation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "resources.json")
	resourceStore, err := persistence.NewFileResourceStore(path)
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
	provider := &failingReconcileProvider{MemoryWorkloadProvider: NewMemoryWorkloadProvider()}
	metadata := workloadProviderMetadata("reconcile-failure-v1")
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
	provider.failUpdates = true
	reconciled, err := manager.ReconcileWorkload(ctx, root.ID, created.Resource.ID)
	if !errors.Is(err, ErrWorkloadResourceLimit) {
		t.Fatalf("ReconcileWorkload(failure) error = %v, want ErrWorkloadResourceLimit", err)
	}
	if reconciled == nil || reconciled.Status.ObservedState != models.ResourceStateFailed {
		t.Fatalf("ReconcileWorkload(failure) = %#v, want stable failed status", reconciled)
	}
	stored, err := resourceStore.Get(ctx, root.ID, created.Resource.ID)
	if err != nil {
		t.Fatalf("resource store Get() error = %v", err)
	}
	if stored.ObservedState != models.ResourceStateFailed {
		t.Fatalf("persisted failure state = %q, want %q", stored.ObservedState, models.ResourceStateFailed)
	}
}

func TestWorkloadProviderReconcilesMissingProviderWithoutDuplicateResource(t *testing.T) {
	ctx := context.Background()
	resources, err := NewResourceManager(newWorkloadFileResourceStore(t))
	if err != nil {
		t.Fatalf("NewResourceManager() error = %v", err)
	}
	registry, err := NewWorkloadProviderRegistry()
	if err != nil {
		t.Fatalf("NewWorkloadProviderRegistry() error = %v", err)
	}
	provider := NewMemoryWorkloadProvider()
	metadata := workloadProviderMetadata("reconcile-v2")
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
	if err := provider.Delete(ctx, created.Resource); err != nil {
		t.Fatalf("provider Delete() error = %v", err)
	}

	reconciled, err := manager.ReconcileWorkload(ctx, root.ID, created.Resource.ID)
	if err != nil {
		t.Fatalf("ReconcileWorkload(recreate) error = %v", err)
	}
	if reconciled == nil || reconciled.Resource.ID != created.Resource.ID || reconciled.Status.ObservedState != models.ResourceStateReady {
		t.Fatalf("ReconcileWorkload(recreate) = %#v, want original ready workload", reconciled)
	}
	if _, err := manager.ReconcileWorkload(ctx, root.ID, created.Resource.ID); err != nil {
		t.Fatalf("ReconcileWorkload(repeat) error = %v", err)
	}
	workloads, err := resources.ListResources(ctx, root.ID, persistence.MaxResourceListLimit)
	if err != nil {
		t.Fatalf("ListResources() error = %v", err)
	}
	if len(workloads) != 1 || workloads[0].ID != created.Resource.ID {
		t.Fatalf("reconciled resources = %#v, want one original resource", workloads)
	}
}

func TestWorkloadProviderReconciliationDeletesDesiredDeletingWorkload(t *testing.T) {
	ctx := context.Background()
	resources, err := NewResourceManager(newWorkloadFileResourceStore(t))
	if err != nil {
		t.Fatalf("NewResourceManager() error = %v", err)
	}
	registry, err := NewWorkloadProviderRegistry()
	if err != nil {
		t.Fatalf("NewWorkloadProviderRegistry() error = %v", err)
	}
	provider := NewMemoryWorkloadProvider()
	metadata := workloadProviderMetadata("reconcile-v3")
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
	spec := workloadResourceSpec(root.ID, "api", metadata)
	spec.DesiredState = models.ResourceStateDeleting
	created, err := manager.CreateWorkload(ctx, root.ID, spec)
	if err != nil {
		t.Fatalf("CreateWorkload() error = %v", err)
	}
	if _, err := manager.ReconcileWorkload(ctx, root.ID, created.Resource.ID); err != nil {
		t.Fatalf("ReconcileWorkload(delete) error = %v", err)
	}
	if _, err := resources.GetResource(ctx, root.ID, created.Resource.ID); !errors.Is(err, persistence.ErrResourceNotFound) {
		t.Fatalf("GetResource(deleted workload) error = %v, want ErrResourceNotFound", err)
	}
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

func TestWorkloadProviderEnforcesResourceBounds(t *testing.T) {
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
	provider, err := NewMemoryWorkloadProviderWithLimits(WorkloadResourceLimits{
		MaxCPUMillis:   2000,
		MaxMemoryBytes: 512 << 20,
		MaxDiskBytes:   2 << 30,
	})
	if err != nil {
		t.Fatalf("NewMemoryWorkloadProviderWithLimits() error = %v", err)
	}
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

	withinBounds := workloadResourceSpec(root.ID, "bounded", metadata)
	withinBounds.WorkloadResources = models.WorkloadResources{CPUMillis: 1000, MemoryBytes: 256 << 20}
	created, err := manager.CreateWorkload(ctx, root.ID, withinBounds)
	if err != nil {
		t.Fatalf("CreateWorkload(within bounds) error = %v", err)
	}
	if created.Status.ObservedState != models.ResourceStateReady || created.Status.Health != models.WorkloadHealthHealthy || created.Status.Readiness != models.WorkloadReadinessReady {
		t.Fatalf("within-bounds status = %#v, want ready/healthy/ready", created.Status)
	}

	overLimit := workloadResourceSpec(root.ID, "over-limit", metadata)
	overLimit.WorkloadResources = models.WorkloadResources{CPUMillis: 4000, MemoryBytes: 256 << 20}
	rejected, err := manager.CreateWorkload(ctx, root.ID, overLimit)
	if !errors.Is(err, ErrWorkloadResourceLimit) {
		t.Fatalf("CreateWorkload(over CPU limit) error = %v, want ErrWorkloadResourceLimit", err)
	}
	if rejected == nil || rejected.Status.ObservedState != models.ResourceStateFailed || rejected.Status.Health != models.WorkloadHealthUnhealthy || rejected.Status.Readiness != models.WorkloadReadinessNotReady {
		t.Fatalf("over-limit response = %#v, want bounded failed status", rejected)
	}
	if rejected.Status.Reason != "workload resource limit exceeded" || strings.Contains(rejected.Status.Reason, "4000") {
		t.Fatalf("over-limit reason = %q, want stable redacted reason", rejected.Status.Reason)
	}
	inspected, err := manager.GetWorkload(ctx, root.ID, rejected.Resource.ID)
	if err != nil {
		t.Fatalf("GetWorkload(over CPU limit) error = %v", err)
	}
	if inspected.Status.ObservedState != models.ResourceStateFailed || inspected.Status.Reason != rejected.Status.Reason {
		t.Fatalf("inspected over-limit status = %#v, want persisted bounded failure", inspected.Status)
	}

	overMemory := workloadResourceSpec(root.ID, "over-memory", metadata)
	overMemory.WorkloadResources = models.WorkloadResources{CPUMillis: 1000, MemoryBytes: 1024 << 20}
	if _, err := manager.CreateWorkload(ctx, root.ID, overMemory); !errors.Is(err, ErrWorkloadResourceLimit) {
		t.Fatalf("CreateWorkload(over memory limit) error = %v, want ErrWorkloadResourceLimit", err)
	}
}

func TestWorkloadProviderEnforcesLeastPrivilegeDefaults(t *testing.T) {
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
	provider := NewMemoryWorkloadProvider()
	if err := registry.Register(metadata, provider); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	manager, err := NewWorkloadManager(resources, registry)
	if err != nil {
		t.Fatalf("NewWorkloadManager() error = %v", err)
	}
	root, err := resources.CreateResource(ctx, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "compute"})
	if err != nil {
		t.Fatalf("CreateResource() error = %v", err)
	}

	defaultSpec := workloadResourceSpec(root.ID, "default", metadata)
	created, err := manager.CreateWorkload(ctx, root.ID, defaultSpec)
	if err != nil {
		t.Fatalf("CreateWorkload(default) error = %v", err)
	}
	if created.Resource.Spec.SecurityContext.Privileged || created.Resource.Spec.SecurityContext.AllowPrivilegeEscalation {
		t.Fatalf("default security context = %#v, want least privilege", created.Resource.Spec.SecurityContext)
	}

	for name, context := range map[string]models.WorkloadSecurityContext{
		"privileged":           {Privileged: true},
		"privilege-escalation": {AllowPrivilegeEscalation: true},
	} {
		spec := workloadResourceSpec(root.ID, name, metadata)
		spec.SecurityContext = context
		if _, err := manager.CreateWorkload(ctx, root.ID, spec); !errors.Is(err, ErrWorkloadPrivilegeDenied) {
			t.Fatalf("CreateWorkload(%s) error = %v, want ErrWorkloadPrivilegeDenied", name, err)
		}
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
	if _, err := manager.RestartWorkload(ctx, root.ID, created.Resource.ID); !errors.Is(err, ErrWorkloadProviderUnsupported) {
		t.Fatalf("RestartWorkload(unsupported) error = %v, want ErrWorkloadProviderUnsupported", err)
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

func TestWorkloadProviderBoundaryRetrievesRedactedLogsAndRestarts(t *testing.T) {
	ctx := context.Background()
	resources, err := NewResourceManager(newWorkloadFileResourceStore(t))
	if err != nil {
		t.Fatalf("NewResourceManager() error = %v", err)
	}
	registry, err := NewWorkloadProviderRegistry()
	if err != nil {
		t.Fatalf("NewWorkloadProviderRegistry() error = %v", err)
	}
	metadata := workloadProviderMetadata("v4")
	provider := NewMemoryWorkloadProvider()
	if err := registry.Register(metadata, provider); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	manager, err := NewWorkloadManager(resources, registry)
	if err != nil {
		t.Fatalf("NewWorkloadManager() error = %v", err)
	}
	root, err := resources.CreateResource(ctx, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "compute"})
	if err != nil {
		t.Fatalf("CreateResource() error = %v", err)
	}
	created, err := manager.CreateWorkload(ctx, root.ID, workloadResourceSpec(root.ID, "api", metadata))
	if err != nil {
		t.Fatalf("CreateWorkload() error = %v", err)
	}

	if _, err := manager.GetWorkloadLogs(ctx, root.ID, created.Resource.ID, 0); !errors.Is(err, ErrInvalidWorkloadLogLimit) {
		t.Fatalf("GetWorkloadLogs(invalid limit) error = %v, want ErrInvalidWorkloadLogLimit", err)
	}
	restarted, err := manager.RestartWorkload(ctx, root.ID, created.Resource.ID)
	if err != nil {
		t.Fatalf("RestartWorkload() error = %v", err)
	}
	if restarted.Resource.ID != created.Resource.ID || restarted.Status.ObservedState != models.ResourceStateReady || restarted.Status.ExecutionID == created.Status.ExecutionID {
		t.Fatalf("RestartWorkload() = %#v, want stable resource and new ready execution", restarted)
	}
	for i := 1; i < MaxWorkloadLogRecords+5; i++ {
		restarted, err = manager.RestartWorkload(ctx, root.ID, created.Resource.ID)
		if err != nil {
			t.Fatalf("RestartWorkload(%d) error = %v", i, err)
		}
	}
	logs, err := manager.GetWorkloadLogs(ctx, root.ID, created.Resource.ID, MaxWorkloadLogRecords)
	if err != nil {
		t.Fatalf("GetWorkloadLogs() error = %v", err)
	}
	if len(logs) != MaxWorkloadLogRecords {
		t.Fatalf("GetWorkloadLogs() returned %d records, want bounded %d", len(logs), MaxWorkloadLogRecords)
	}
	foundCorrelation := false
	for _, log := range logs {
		if log.ExecutionID == restarted.Status.ExecutionID {
			foundCorrelation = true
		}
		if strings.Contains(strings.ToLower(log.Message), "secret") || strings.Contains(strings.ToLower(log.Message), "token") {
			t.Fatalf("GetWorkloadLogs() leaked sensitive marker: %#v", log)
		}
	}
	if !foundCorrelation {
		t.Fatalf("GetWorkloadLogs() = %#v, want restart execution correlation %q", logs, restarted.Status.ExecutionID)
	}
	if _, err := manager.GetWorkloadLogs(ctx, "wrong-scope", created.Resource.ID, MaxWorkloadLogRecords); !errors.Is(err, ErrWorkloadNotFound) {
		t.Fatalf("GetWorkloadLogs(cross scope) error = %v, want ErrWorkloadNotFound", err)
	}
}

type secretLogWorkloadProvider struct {
	*MemoryWorkloadProvider
}

func (provider *secretLogWorkloadProvider) Logs(context.Context, models.Resource, int) ([]models.WorkloadLog, error) {
	return []models.WorkloadLog{{
		Timestamp:   time.Unix(1, 0).UTC(),
		Stream:      "stderr",
		Message:     "api-key=super-secret",
		ExecutionID: "execution-safe",
	}}, nil
}

func TestWorkloadProviderBoundaryScopesOwnedVolumeLifecycle(t *testing.T) {
	ctx := context.Background()
	resources, err := NewResourceManager(newWorkloadFileResourceStore(t))
	if err != nil {
		t.Fatalf("NewResourceManager() error = %v", err)
	}
	registry, err := NewWorkloadProviderRegistry()
	if err != nil {
		t.Fatalf("NewWorkloadProviderRegistry() error = %v", err)
	}
	metadata := workloadProviderMetadata("v6")
	provider := NewMemoryWorkloadProvider()
	if err := registry.Register(metadata, provider); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	manager, err := NewWorkloadManager(resources, registry)
	if err != nil {
		t.Fatalf("NewWorkloadManager() error = %v", err)
	}
	rootA, err := resources.CreateResource(ctx, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "compute-a"})
	if err != nil {
		t.Fatalf("CreateResource(rootA) error = %v", err)
	}
	rootB, err := resources.CreateResource(ctx, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "compute-b"})
	if err != nil {
		t.Fatalf("CreateResource(rootB) error = %v", err)
	}
	workloadA, err := manager.CreateWorkload(ctx, rootA.ID, workloadResourceSpec(rootA.ID, "api-a", metadata))
	if err != nil {
		t.Fatalf("CreateWorkload(workloadA) error = %v", err)
	}
	workloadB, err := manager.CreateWorkload(ctx, rootB.ID, workloadResourceSpec(rootB.ID, "api-b", metadata))
	if err != nil {
		t.Fatalf("CreateWorkload(workloadB) error = %v", err)
	}

	volumeA, err := manager.AttachWorkloadVolume(ctx, rootA.ID, workloadA.Resource.ID, "cache", 1024)
	if err != nil {
		t.Fatalf("AttachWorkloadVolume() error = %v", err)
	}
	if volumeA.ID == "" || volumeA.WorkloadID != workloadA.Resource.ID || volumeA.Name != "cache" || volumeA.MaxBytes != 1024 || volumeA.UsedBytes != 0 {
		t.Fatalf("AttachWorkloadVolume() = %#v, want bounded owned volume metadata", volumeA)
	}
	if _, err := manager.AttachWorkloadVolume(ctx, rootA.ID, workloadA.Resource.ID, "invalid", 0); !errors.Is(err, ErrInvalidWorkloadVolume) {
		t.Fatalf("AttachWorkloadVolume(invalid bound) error = %v, want ErrInvalidWorkloadVolume", err)
	}
	volumeB, err := manager.AttachWorkloadVolume(ctx, rootB.ID, workloadB.Resource.ID, "cache", 1024)
	if err != nil {
		t.Fatalf("AttachWorkloadVolume(unrelated workload) error = %v", err)
	}

	volumesA, err := manager.ListWorkloadVolumes(ctx, rootA.ID, workloadA.Resource.ID, MaxWorkloadVolumeRecords)
	if err != nil {
		t.Fatalf("ListWorkloadVolumes() error = %v", err)
	}
	if len(volumesA) != 1 || volumesA[0].ID != volumeA.ID {
		t.Fatalf("ListWorkloadVolumes() = %#v, want workload A volume only", volumesA)
	}
	if err := manager.CleanupWorkloadVolumes(ctx, rootA.ID, workloadA.Resource.ID); err != nil {
		t.Fatalf("CleanupWorkloadVolumes() error = %v", err)
	}
	if err := manager.CleanupWorkloadVolumes(ctx, rootA.ID, workloadA.Resource.ID); err != nil {
		t.Fatalf("CleanupWorkloadVolumes(repeat) error = %v, want idempotent cleanup", err)
	}
	volumesA, err = manager.ListWorkloadVolumes(ctx, rootA.ID, workloadA.Resource.ID, MaxWorkloadVolumeRecords)
	if err != nil {
		t.Fatalf("ListWorkloadVolumes(after cleanup) error = %v", err)
	}
	if len(volumesA) != 0 {
		t.Fatalf("ListWorkloadVolumes(after cleanup) = %#v, want no owned volumes", volumesA)
	}
	volumesB, err := manager.ListWorkloadVolumes(ctx, rootB.ID, workloadB.Resource.ID, MaxWorkloadVolumeRecords)
	if err != nil {
		t.Fatalf("ListWorkloadVolumes(unrelated workload) error = %v", err)
	}
	if len(volumesB) != 1 || volumesB[0].ID != volumeB.ID {
		t.Fatalf("ListWorkloadVolumes(unrelated workload) = %#v, want unrelated volume preserved", volumesB)
	}
}

func TestWorkloadProviderDeleteCleansOwnedVolumesBeforeProviderDelete(t *testing.T) {
	ctx := context.Background()
	provider := &workloadProviderWithoutVolumeCleanup{MemoryWorkloadProvider: NewMemoryWorkloadProvider()}
	metadata := workloadProviderMetadata("delete-cleanup-v1")
	registry, err := NewWorkloadProviderRegistry()
	if err != nil {
		t.Fatalf("NewWorkloadProviderRegistry() error = %v", err)
	}
	if err := registry.Register(metadata, provider); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	resources, err := NewResourceManager(newWorkloadFileResourceStore(t))
	if err != nil {
		t.Fatalf("NewResourceManager() error = %v", err)
	}
	manager, err := NewWorkloadManager(resources, registry)
	if err != nil {
		t.Fatalf("NewWorkloadManager() error = %v", err)
	}
	root, err := resources.CreateResource(ctx, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "compute"})
	if err != nil {
		t.Fatalf("CreateResource() error = %v", err)
	}
	workload, err := manager.CreateWorkload(ctx, root.ID, workloadResourceSpec(root.ID, "api", metadata))
	if err != nil {
		t.Fatalf("CreateWorkload() error = %v", err)
	}
	if _, err := manager.AttachWorkloadVolume(ctx, root.ID, workload.Resource.ID, "cache", 1024); err != nil {
		t.Fatalf("AttachWorkloadVolume() error = %v", err)
	}
	if err := manager.DeleteWorkload(ctx, root.ID, workload.Resource.ID); err != nil {
		t.Fatalf("DeleteWorkload() error = %v", err)
	}
	volumes, err := provider.ListVolumes(ctx, workload.Resource, MaxWorkloadVolumeRecords)
	if err != nil {
		t.Fatalf("provider ListVolumes(after delete) error = %v", err)
	}
	if len(volumes) != 0 {
		t.Fatalf("provider volumes after workload delete = %#v, want no residue", volumes)
	}
}

func TestWorkloadProviderBoundaryRedactsProviderLogs(t *testing.T) {
	ctx := context.Background()
	resources, err := NewResourceManager(newWorkloadFileResourceStore(t))
	if err != nil {
		t.Fatalf("NewResourceManager() error = %v", err)
	}
	registry, err := NewWorkloadProviderRegistry()
	if err != nil {
		t.Fatalf("NewWorkloadProviderRegistry() error = %v", err)
	}
	metadata := workloadProviderMetadata("v5")
	provider := &secretLogWorkloadProvider{MemoryWorkloadProvider: NewMemoryWorkloadProvider()}
	if err := registry.Register(metadata, provider); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	manager, err := NewWorkloadManager(resources, registry)
	if err != nil {
		t.Fatalf("NewWorkloadManager() error = %v", err)
	}
	root, err := resources.CreateResource(ctx, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "compute"})
	if err != nil {
		t.Fatalf("CreateResource() error = %v", err)
	}
	created, err := manager.CreateWorkload(ctx, root.ID, workloadResourceSpec(root.ID, "api", metadata))
	if err != nil {
		t.Fatalf("CreateWorkload() error = %v", err)
	}
	logs, err := manager.GetWorkloadLogs(ctx, root.ID, created.Resource.ID, 1)
	if err != nil {
		t.Fatalf("GetWorkloadLogs() error = %v", err)
	}
	if len(logs) != 1 || logs[0].Message != "[redacted]" || logs[0].ExecutionID != "execution-safe" {
		t.Fatalf("GetWorkloadLogs() = %#v, want redacted bounded log", logs)
	}
}

func TestWorkloadProviderEnforcesRuntimeAndVolumeDiskBounds(t *testing.T) {
	ctx := context.Background()
	resources, err := NewResourceManager(newWorkloadFileResourceStore(t))
	if err != nil {
		t.Fatalf("NewResourceManager() error = %v", err)
	}
	registry, err := NewWorkloadProviderRegistry()
	if err != nil {
		t.Fatalf("NewWorkloadProviderRegistry() error = %v", err)
	}
	metadata := workloadProviderMetadata("v7")
	provider, err := NewMemoryWorkloadProviderWithLimits(WorkloadResourceLimits{
		MaxCPUMillis:   2000,
		MaxMemoryBytes: 512 << 20,
		MaxDiskBytes:   2048,
	})
	if err != nil {
		t.Fatalf("NewMemoryWorkloadProviderWithLimits() error = %v", err)
	}
	if err := registry.Register(metadata, provider); err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	manager, err := NewWorkloadManager(resources, registry)
	if err != nil {
		t.Fatalf("NewWorkloadManager() error = %v", err)
	}
	root, err := resources.CreateResource(ctx, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "compute"})
	if err != nil {
		t.Fatalf("CreateResource() error = %v", err)
	}

	workloadSpec := workloadResourceSpec(root.ID, "disk-bounded", metadata)
	workloadSpec.WorkloadResources.DiskBytes = 1024
	created, err := manager.CreateWorkload(ctx, root.ID, workloadSpec)
	if err != nil {
		t.Fatalf("CreateWorkload(within disk limit) error = %v", err)
	}
	if created.Status.ObservedState != models.ResourceStateReady {
		t.Fatalf("within-disk status = %#v, want ready", created.Status)
	}

	tooLarge := workloadResourceSpec(root.ID, "runtime-too-large", metadata)
	tooLarge.WorkloadResources.DiskBytes = 4096
	rejected, err := manager.CreateWorkload(ctx, root.ID, tooLarge)
	if !errors.Is(err, ErrWorkloadResourceLimit) {
		t.Fatalf("CreateWorkload(over runtime disk limit) error = %v, want ErrWorkloadResourceLimit", err)
	}
	if rejected == nil || rejected.Status.ObservedState != models.ResourceStateFailed {
		t.Fatalf("over-runtime response = %#v, want inspectable failed status", rejected)
	}
	inspected, err := manager.GetWorkload(ctx, root.ID, rejected.Resource.ID)
	if err != nil {
		t.Fatalf("GetWorkload(over runtime disk limit) error = %v", err)
	}
	if inspected.Status.Reason != "workload resource limit exceeded" {
		t.Fatalf("inspected runtime failure reason = %q, want stable reason", inspected.Status.Reason)
	}

	if _, err := manager.AttachWorkloadVolume(ctx, root.ID, created.Resource.ID, "cache", 1024); err != nil {
		t.Fatalf("AttachWorkloadVolume(within total disk limit) error = %v", err)
	}
	if _, err := manager.AttachWorkloadVolume(ctx, root.ID, created.Resource.ID, "logs", 1024); !errors.Is(err, ErrWorkloadResourceLimit) {
		t.Fatalf("AttachWorkloadVolume(over total disk limit) error = %v, want ErrWorkloadResourceLimit", err)
	}
	volumeFailure, err := manager.GetWorkload(ctx, root.ID, created.Resource.ID)
	if err != nil {
		t.Fatalf("GetWorkload(after volume limit) error = %v", err)
	}
	if volumeFailure.Status.ObservedState != models.ResourceStateFailed || volumeFailure.Status.Reason != "workload resource limit exceeded" {
		t.Fatalf("workload after volume limit = %#v, want inspectable stable failure", volumeFailure.Status)
	}
	volumes, err := manager.ListWorkloadVolumes(ctx, root.ID, created.Resource.ID, MaxWorkloadVolumeRecords)
	if err != nil {
		t.Fatalf("ListWorkloadVolumes(after rejected attach) error = %v", err)
	}
	if len(volumes) != 1 || volumes[0].Name != "cache" {
		t.Fatalf("volumes after rejected attach = %#v, want only cache", volumes)
	}
	if err := manager.CleanupWorkloadVolumes(ctx, root.ID, created.Resource.ID); err != nil {
		t.Fatalf("CleanupWorkloadVolumes() error = %v", err)
	}
	if err := manager.CleanupWorkloadVolumes(ctx, root.ID, created.Resource.ID); err != nil {
		t.Fatalf("CleanupWorkloadVolumes(repeat) error = %v, want idempotent cleanup", err)
	}
	if _, err := manager.AttachWorkloadVolume(ctx, root.ID, created.Resource.ID, "logs", 1024); err != nil {
		t.Fatalf("AttachWorkloadVolume(after cleanup) error = %v, want released bound", err)
	}
}
