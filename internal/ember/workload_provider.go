package ember

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

const MaxWorkloadProviderRecords = 100

var (
	ErrInvalidWorkloadControlPlane   = errors.New("invalid workload control plane")
	ErrInvalidWorkloadSpec           = errors.New("invalid workload spec")
	ErrWorkloadNotFound              = errors.New("workload not found")
	ErrWorkloadScopeDenied           = errors.New("workload scope denied")
	ErrDuplicateWorkloadProvider     = errors.New("duplicate workload provider")
	ErrWorkloadProviderNotFound      = errors.New("workload provider not found")
	ErrInvalidWorkloadProvider       = errors.New("invalid workload provider")
	ErrWorkloadProviderUnsupported   = errors.New("workload provider operation unsupported")
	ErrWorkloadProviderOperation     = errors.New("workload provider operation failed")
	ErrInvalidWorkloadProviderStatus = errors.New("invalid workload provider status")

	errProviderOperationUnsupported = errors.New("provider operation unsupported")
	errProviderNotFound             = errors.New("provider workload not found")
	errProviderConflict             = errors.New("provider workload conflict")
)

// WorkloadProvider is the execution boundary for a workload resource. The
// provider receives only the bounded control-plane resource and returns the
// stable status contract; provider-specific errors never cross the boundary.
type WorkloadProvider interface {
	Create(context.Context, models.Resource) (models.WorkloadStatus, error)
	Get(context.Context, models.Resource) (models.WorkloadStatus, error)
	Update(context.Context, models.Resource) (models.WorkloadStatus, error)
	Delete(context.Context, models.Resource) error
}

// WorkloadProviderRegistry holds the bounded provider registrations used by
// the workload control plane.
type WorkloadProviderRegistry struct {
	mu        sync.RWMutex
	providers map[string]WorkloadProvider
}

func NewWorkloadProviderRegistry() (*WorkloadProviderRegistry, error) {
	return &WorkloadProviderRegistry{providers: make(map[string]WorkloadProvider)}, nil
}

func (registry *WorkloadProviderRegistry) Register(metadata models.ProviderMetadata, provider WorkloadProvider) error {
	if registry == nil || provider == nil || !validWorkloadProviderMetadata(metadata) {
		return ErrInvalidWorkloadProvider
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	if len(registry.providers) >= MaxWorkloadProviderRecords {
		return ErrInvalidWorkloadProvider
	}
	key := workloadProviderKey(metadata)
	if _, exists := registry.providers[key]; exists {
		return ErrDuplicateWorkloadProvider
	}
	registry.providers[key] = provider
	return nil
}

func (registry *WorkloadProviderRegistry) Resolve(metadata models.ProviderMetadata) (WorkloadProvider, error) {
	if registry == nil || !validWorkloadProviderMetadata(metadata) {
		return nil, ErrWorkloadProviderNotFound
	}
	registry.mu.RLock()
	defer registry.mu.RUnlock()
	provider, exists := registry.providers[workloadProviderKey(metadata)]
	if !exists {
		return nil, ErrWorkloadProviderNotFound
	}
	return provider, nil
}

func validWorkloadProviderMetadata(metadata models.ProviderMetadata) bool {
	return strings.TrimSpace(metadata.Namespace) != "" && len(metadata.Namespace) <= models.MaxProviderNamespaceLength &&
		strings.TrimSpace(metadata.Type) != "" && len(metadata.Type) <= models.MaxProviderTypeLength &&
		strings.TrimSpace(metadata.Version) != "" && len(metadata.Version) <= models.MaxProviderVersionLength
}

func workloadProviderKey(metadata models.ProviderMetadata) string {
	return metadata.Namespace + "\x00" + metadata.Type + "\x00" + metadata.Version
}

// WorkloadView combines the stable resource identity with the provider's
// bounded observed status.
type WorkloadView struct {
	Resource models.Resource
	Status   models.WorkloadStatus
}

type WorkloadControlPlane interface {
	CreateWorkload(context.Context, string, models.ResourceSpec) (*WorkloadView, error)
	GetWorkload(context.Context, string, string) (*WorkloadView, error)
	UpdateWorkload(context.Context, string, string) (*WorkloadView, error)
	DeleteWorkload(context.Context, string, string) error
}

type WorkloadManager struct {
	resources ResourceControlPlane
	registry  *WorkloadProviderRegistry
}

func NewWorkloadManager(resources ResourceControlPlane, registry *WorkloadProviderRegistry) (*WorkloadManager, error) {
	if resources == nil || registry == nil {
		return nil, ErrInvalidWorkloadControlPlane
	}
	return &WorkloadManager{resources: resources, registry: registry}, nil
}

func validateWorkloadRequest(ctx context.Context, scopeID string, spec models.ResourceSpec) error {
	if ctx == nil || strings.TrimSpace(scopeID) == "" || len(scopeID) > models.MaxResourceIDLength ||
		spec.Type != models.ResourceTypeWorkload || spec.ParentID != scopeID {
		return ErrInvalidWorkloadSpec
	}
	if err := spec.Validate(); err != nil {
		return ErrInvalidWorkloadSpec
	}
	return nil
}

func (manager *WorkloadManager) CreateWorkload(ctx context.Context, scopeID string, spec models.ResourceSpec) (*WorkloadView, error) {
	if err := validateWorkloadRequest(ctx, scopeID, spec); err != nil {
		return nil, err
	}
	provider, err := manager.registry.Resolve(spec.Provider)
	if err != nil {
		return nil, err
	}
	resource, err := manager.resources.CreateResource(ctx, spec)
	if errors.Is(err, persistence.ErrResourceNotFound) {
		return nil, ErrWorkloadScopeDenied
	}
	if err != nil {
		return nil, err
	}
	status, providerErr := provider.Create(ctx, *resource)
	if providerErr != nil {
		_ = manager.resources.DeleteResource(ctx, scopeID, resource.ID)
		return nil, mapWorkloadProviderError(providerErr)
	}
	view, statusErr := newWorkloadView(*resource, status)
	if statusErr != nil {
		_ = manager.resources.DeleteResource(ctx, scopeID, resource.ID)
		return nil, statusErr
	}
	return view, nil
}

func (manager *WorkloadManager) GetWorkload(ctx context.Context, scopeID, resourceID string) (*WorkloadView, error) {
	if err := validateWorkloadLookup(ctx, scopeID, resourceID); err != nil {
		return nil, err
	}
	resource, err := manager.resources.GetResource(ctx, scopeID, resourceID)
	if errors.Is(err, persistence.ErrResourceNotFound) {
		return nil, ErrWorkloadNotFound
	}
	if err != nil {
		return nil, err
	}
	if resource.Spec.Type != models.ResourceTypeWorkload {
		return nil, ErrWorkloadNotFound
	}
	provider, err := manager.registry.Resolve(resource.Spec.Provider)
	if err != nil {
		return nil, err
	}
	status, providerErr := provider.Get(ctx, *resource)
	if providerErr != nil {
		return nil, mapWorkloadProviderError(providerErr)
	}
	return newWorkloadView(*resource, status)
}

func validateWorkloadLookup(ctx context.Context, scopeID, resourceID string) error {
	if ctx == nil || strings.TrimSpace(scopeID) == "" || len(scopeID) > models.MaxResourceIDLength ||
		strings.TrimSpace(resourceID) == "" || len(resourceID) > models.MaxResourceIDLength {
		return ErrInvalidWorkloadSpec
	}
	return nil
}

func (manager *WorkloadManager) UpdateWorkload(ctx context.Context, scopeID, resourceID string) (*WorkloadView, error) {
	if err := validateWorkloadLookup(ctx, scopeID, resourceID); err != nil {
		return nil, err
	}
	resource, err := manager.resources.GetResource(ctx, scopeID, resourceID)
	if errors.Is(err, persistence.ErrResourceNotFound) {
		return nil, ErrWorkloadNotFound
	}
	if err != nil {
		return nil, err
	}
	if resource.Spec.Type != models.ResourceTypeWorkload {
		return nil, ErrWorkloadNotFound
	}
	provider, err := manager.registry.Resolve(resource.Spec.Provider)
	if err != nil {
		return nil, err
	}
	status, providerErr := provider.Update(ctx, *resource)
	if providerErr != nil {
		return nil, mapWorkloadProviderError(providerErr)
	}
	return newWorkloadView(*resource, status)
}

func (manager *WorkloadManager) DeleteWorkload(ctx context.Context, scopeID, resourceID string) error {
	if err := validateWorkloadLookup(ctx, scopeID, resourceID); err != nil {
		return err
	}
	resource, err := manager.resources.GetResource(ctx, scopeID, resourceID)
	if errors.Is(err, persistence.ErrResourceNotFound) {
		return ErrWorkloadNotFound
	}
	if err != nil {
		return err
	}
	if resource.Spec.Type != models.ResourceTypeWorkload {
		return ErrWorkloadNotFound
	}
	provider, err := manager.registry.Resolve(resource.Spec.Provider)
	if err != nil {
		return err
	}
	if providerErr := provider.Delete(ctx, *resource); providerErr != nil {
		return mapWorkloadProviderError(providerErr)
	}
	if err := manager.resources.DeleteResource(ctx, scopeID, resourceID); errors.Is(err, persistence.ErrResourceNotFound) {
		return ErrWorkloadNotFound
	} else {
		return err
	}
}

func newWorkloadView(resource models.Resource, status models.WorkloadStatus) (*WorkloadView, error) {
	if err := status.Validate(); err != nil {
		return nil, ErrInvalidWorkloadProviderStatus
	}
	if status.Health == "" {
		status.Health = models.WorkloadHealthUnknown
	}
	if status.Readiness == "" {
		status.Readiness = models.WorkloadReadinessUnknown
	}
	status.Reason = redactWorkloadStatusValue(status.Reason)
	status.ExecutionID = redactWorkloadStatusValue(status.ExecutionID)
	resource.ObservedState = status.ObservedState
	return &WorkloadView{Resource: resource, Status: status}, nil
}

func redactWorkloadStatusValue(value string) string {
	lowerValue := strings.ToLower(value)
	for _, marker := range []string{"secret", "password", "token", "credential", "authorization", "bearer", "api-key", "apikey"} {
		if strings.Contains(lowerValue, marker) {
			return "[redacted]"
		}
	}
	return value
}

func mapWorkloadProviderError(err error) error {
	switch {
	case errors.Is(err, errProviderOperationUnsupported):
		return ErrWorkloadProviderUnsupported
	case errors.Is(err, errProviderNotFound):
		return ErrWorkloadNotFound
	case errors.Is(err, errProviderConflict):
		return ErrWorkloadProviderOperation
	default:
		return ErrWorkloadProviderOperation
	}
}

// MemoryWorkloadProvider is a bounded provider fixture and local execution
// implementation. It stores only safe status metadata, never payloads.
type MemoryWorkloadProvider struct {
	mu        sync.RWMutex
	workloads map[string]models.WorkloadStatus
}

func NewMemoryWorkloadProvider() *MemoryWorkloadProvider {
	return &MemoryWorkloadProvider{workloads: make(map[string]models.WorkloadStatus)}
}

func validateProviderResource(resource models.Resource) error {
	if resource.Spec.Type != models.ResourceTypeWorkload || resource.Validate() != nil {
		return ErrInvalidWorkloadSpec
	}
	return nil
}

func memoryWorkloadStatus(resource models.Resource) models.WorkloadStatus {
	state := resource.Spec.DesiredState
	if state == "" {
		state = models.ResourceStatePending
	}
	health := models.WorkloadHealthUnknown
	readiness := models.WorkloadReadinessNotReady
	switch state {
	case models.ResourceStateReady:
		health = models.WorkloadHealthHealthy
		readiness = models.WorkloadReadinessReady
	case models.ResourceStateFailed, models.ResourceStateDeleting:
		health = models.WorkloadHealthUnhealthy
	}
	return models.WorkloadStatus{
		ObservedState: state,
		Health:        health,
		Readiness:     readiness,
		Reason:        "provider accepted desired state",
		ExecutionID:   "memory:" + resource.ID,
	}
}

func (provider *MemoryWorkloadProvider) Create(ctx context.Context, resource models.Resource) (models.WorkloadStatus, error) {
	if err := ctx.Err(); err != nil {
		return models.WorkloadStatus{}, err
	}
	if err := validateProviderResource(resource); err != nil {
		return models.WorkloadStatus{}, errProviderOperationUnsupported
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if _, exists := provider.workloads[resource.ID]; exists {
		return models.WorkloadStatus{}, errProviderConflict
	}
	if len(provider.workloads) >= MaxWorkloadProviderRecords {
		return models.WorkloadStatus{}, errProviderConflict
	}
	status := memoryWorkloadStatus(resource)
	provider.workloads[resource.ID] = status
	return status, nil
}

func (provider *MemoryWorkloadProvider) Get(ctx context.Context, resource models.Resource) (models.WorkloadStatus, error) {
	if err := ctx.Err(); err != nil {
		return models.WorkloadStatus{}, err
	}
	if err := validateProviderResource(resource); err != nil {
		return models.WorkloadStatus{}, errProviderOperationUnsupported
	}
	provider.mu.RLock()
	defer provider.mu.RUnlock()
	status, exists := provider.workloads[resource.ID]
	if !exists {
		return models.WorkloadStatus{}, errProviderNotFound
	}
	return status, nil
}

func (provider *MemoryWorkloadProvider) Update(ctx context.Context, resource models.Resource) (models.WorkloadStatus, error) {
	if err := ctx.Err(); err != nil {
		return models.WorkloadStatus{}, err
	}
	if err := validateProviderResource(resource); err != nil {
		return models.WorkloadStatus{}, errProviderOperationUnsupported
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if _, exists := provider.workloads[resource.ID]; !exists {
		return models.WorkloadStatus{}, errProviderNotFound
	}
	status := memoryWorkloadStatus(resource)
	provider.workloads[resource.ID] = status
	return status, nil
}

func (provider *MemoryWorkloadProvider) Delete(ctx context.Context, resource models.Resource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateProviderResource(resource); err != nil {
		return errProviderOperationUnsupported
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	if _, exists := provider.workloads[resource.ID]; !exists {
		return errProviderNotFound
	}
	delete(provider.workloads, resource.ID)
	return nil
}

var _ WorkloadProvider = (*MemoryWorkloadProvider)(nil)
var _ WorkloadControlPlane = (*WorkloadManager)(nil)
