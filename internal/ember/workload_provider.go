package ember

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

const MaxWorkloadProviderRecords = 100
const MaxWorkloadLogRecords = 100
const MaxWorkloadVolumeRecords = 100

var (
	ErrInvalidWorkloadControlPlane   = errors.New("invalid workload control plane")
	ErrInvalidWorkloadSpec           = errors.New("invalid workload spec")
	ErrWorkloadNotFound              = errors.New("workload not found")
	ErrWorkloadScopeDenied           = errors.New("workload scope denied")
	ErrDuplicateWorkloadProvider     = errors.New("duplicate workload provider")
	ErrWorkloadProviderNotFound      = errors.New("workload provider not found")
	ErrInvalidWorkloadProvider       = errors.New("invalid workload provider")
	ErrWorkloadResourceLimit         = errors.New("workload resource limit exceeded")
	ErrWorkloadProviderUnsupported   = errors.New("workload provider operation unsupported")
	ErrWorkloadProviderOperation     = errors.New("workload provider operation failed")
	ErrInvalidWorkloadProviderStatus = errors.New("invalid workload provider status")
	ErrInvalidWorkloadLogLimit       = errors.New("invalid workload log limit")
	ErrInvalidWorkloadProviderLog    = errors.New("invalid workload provider log")
	ErrInvalidWorkloadVolume         = errors.New("invalid workload volume")
	ErrInvalidWorkloadVolumeLimit    = errors.New("invalid workload volume limit")
	ErrDuplicateWorkloadVolume       = errors.New("duplicate workload volume")

	errProviderOperationUnsupported = errors.New("provider operation unsupported")
	errProviderNotFound             = errors.New("provider workload not found")
	errProviderConflict             = errors.New("provider workload conflict")
	errProviderInvalidVolume        = errors.New("provider volume invalid")
	errProviderDuplicateVolume      = errors.New("provider volume duplicate")
	errProviderResourceLimit        = errors.New("provider workload resource limit exceeded")
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

type WorkloadLogProvider interface {
	Logs(context.Context, models.Resource, int) ([]models.WorkloadLog, error)
}

type WorkloadRestartProvider interface {
	Restart(context.Context, models.Resource) (models.WorkloadStatus, error)
}

// WorkloadVolumeProvider owns only bounded volume metadata for a workload.
// Volume payload storage is deliberately outside this provider contract.
type WorkloadVolumeProvider interface {
	AttachVolume(context.Context, models.Resource, string, int64) (models.WorkloadVolume, error)
	ListVolumes(context.Context, models.Resource, int) ([]models.WorkloadVolume, error)
	CleanupVolumes(context.Context, models.Resource) error
}

// WorkloadProviderRegistry holds the bounded provider registrations used by
// the workload control plane.
type WorkloadProviderRegistry struct {
	mu        sync.RWMutex
	providers map[string]WorkloadProvider
}

type WorkloadResourceLimits struct {
	MaxCPUMillis   int64
	MaxMemoryBytes int64
}

func (limits WorkloadResourceLimits) Validate() error {
	if limits.MaxCPUMillis <= 0 || limits.MaxCPUMillis > models.MaxWorkloadCPUMillis ||
		limits.MaxMemoryBytes <= 0 || limits.MaxMemoryBytes > models.MaxWorkloadMemoryBytes {
		return ErrInvalidWorkloadProvider
	}
	return nil
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
	RestartWorkload(context.Context, string, string) (*WorkloadView, error)
	GetWorkloadLogs(context.Context, string, string, int) ([]models.WorkloadLog, error)
	AttachWorkloadVolume(context.Context, string, string, string, int64) (*models.WorkloadVolume, error)
	ListWorkloadVolumes(context.Context, string, string, int) ([]models.WorkloadVolume, error)
	CleanupWorkloadVolumes(context.Context, string, string) error
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
		if errors.Is(providerErr, errProviderResourceLimit) {
			view, statusErr := newWorkloadView(*resource, status)
			if statusErr != nil {
				_ = manager.resources.DeleteResource(ctx, scopeID, resource.ID)
				return nil, statusErr
			}
			return view, ErrWorkloadResourceLimit
		}
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

func (manager *WorkloadManager) RestartWorkload(ctx context.Context, scopeID, resourceID string) (*WorkloadView, error) {
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
	restarter, ok := provider.(WorkloadRestartProvider)
	if !ok {
		return nil, ErrWorkloadProviderUnsupported
	}
	status, providerErr := restarter.Restart(ctx, *resource)
	if providerErr != nil {
		return nil, mapWorkloadProviderError(providerErr)
	}
	return newWorkloadView(*resource, status)
}

func (manager *WorkloadManager) GetWorkloadLogs(ctx context.Context, scopeID, resourceID string, limit int) ([]models.WorkloadLog, error) {
	if err := validateWorkloadLookup(ctx, scopeID, resourceID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxWorkloadLogRecords {
		return nil, ErrInvalidWorkloadLogLimit
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
	logProvider, ok := provider.(WorkloadLogProvider)
	if !ok {
		return nil, ErrWorkloadProviderUnsupported
	}
	logs, providerErr := logProvider.Logs(ctx, *resource, limit)
	if providerErr != nil {
		return nil, mapWorkloadProviderError(providerErr)
	}
	if len(logs) > limit {
		logs = logs[:limit]
	}
	safeLogs := make([]models.WorkloadLog, 0, len(logs))
	for _, log := range logs {
		if err := log.Validate(); err != nil {
			return nil, ErrInvalidWorkloadProviderLog
		}
		log.Message = redactWorkloadStatusValue(log.Message)
		log.ExecutionID = redactWorkloadStatusValue(log.ExecutionID)
		safeLogs = append(safeLogs, log)
	}
	return safeLogs, nil
}

func validateWorkloadVolumeRequest(name string, maxBytes int64) error {
	if strings.TrimSpace(name) == "" || len(name) > models.MaxWorkloadVolumeNameLength ||
		maxBytes <= 0 || maxBytes > models.MaxWorkloadVolumeBytes {
		return ErrInvalidWorkloadVolume
	}
	return nil
}

func validateOwnedWorkloadVolume(volume models.WorkloadVolume, workloadID string) error {
	if volume.WorkloadID != workloadID || volume.Validate() != nil {
		return ErrInvalidWorkloadVolume
	}
	return nil
}

func (manager *WorkloadManager) resolveWorkloadVolumeProvider(ctx context.Context, scopeID, resourceID string) (*models.Resource, WorkloadVolumeProvider, error) {
	if err := validateWorkloadLookup(ctx, scopeID, resourceID); err != nil {
		return nil, nil, err
	}
	resource, err := manager.resources.GetResource(ctx, scopeID, resourceID)
	if errors.Is(err, persistence.ErrResourceNotFound) {
		return nil, nil, ErrWorkloadNotFound
	}
	if err != nil {
		return nil, nil, err
	}
	if resource.Spec.Type != models.ResourceTypeWorkload {
		return nil, nil, ErrWorkloadNotFound
	}
	provider, err := manager.registry.Resolve(resource.Spec.Provider)
	if err != nil {
		return nil, nil, err
	}
	volumeProvider, ok := provider.(WorkloadVolumeProvider)
	if !ok {
		return nil, nil, ErrWorkloadProviderUnsupported
	}
	return resource, volumeProvider, nil
}

func (manager *WorkloadManager) AttachWorkloadVolume(ctx context.Context, scopeID, resourceID, name string, maxBytes int64) (*models.WorkloadVolume, error) {
	if err := validateWorkloadVolumeRequest(name, maxBytes); err != nil {
		return nil, err
	}
	resource, provider, err := manager.resolveWorkloadVolumeProvider(ctx, scopeID, resourceID)
	if err != nil {
		return nil, err
	}
	volume, providerErr := provider.AttachVolume(ctx, *resource, name, maxBytes)
	if providerErr != nil {
		if errors.Is(providerErr, errProviderNotFound) {
			return nil, ErrWorkloadNotFound
		}
		if errors.Is(providerErr, errProviderInvalidVolume) {
			return nil, ErrInvalidWorkloadVolume
		}
		return nil, mapWorkloadProviderError(providerErr)
	}
	if err := validateOwnedWorkloadVolume(volume, resource.ID); err != nil {
		return nil, err
	}
	return &volume, nil
}

func (manager *WorkloadManager) ListWorkloadVolumes(ctx context.Context, scopeID, resourceID string, limit int) ([]models.WorkloadVolume, error) {
	if limit <= 0 || limit > MaxWorkloadVolumeRecords {
		return nil, ErrInvalidWorkloadVolumeLimit
	}
	resource, provider, err := manager.resolveWorkloadVolumeProvider(ctx, scopeID, resourceID)
	if err != nil {
		return nil, err
	}
	volumes, providerErr := provider.ListVolumes(ctx, *resource, limit)
	if providerErr != nil {
		if errors.Is(providerErr, errProviderNotFound) {
			return nil, ErrWorkloadNotFound
		}
		return nil, mapWorkloadProviderError(providerErr)
	}
	if len(volumes) > limit {
		volumes = volumes[:limit]
	}
	for _, volume := range volumes {
		if err := validateOwnedWorkloadVolume(volume, resource.ID); err != nil {
			return nil, err
		}
	}
	return volumes, nil
}

func (manager *WorkloadManager) CleanupWorkloadVolumes(ctx context.Context, scopeID, resourceID string) error {
	resource, provider, err := manager.resolveWorkloadVolumeProvider(ctx, scopeID, resourceID)
	if err != nil {
		return err
	}
	providerErr := provider.CleanupVolumes(ctx, *resource)
	if providerErr == nil {
		return nil
	}
	if errors.Is(providerErr, errProviderNotFound) {
		return ErrWorkloadNotFound
	}
	return mapWorkloadProviderError(providerErr)
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
	case errors.Is(err, errProviderDuplicateVolume):
		return ErrDuplicateWorkloadVolume
	case errors.Is(err, errProviderResourceLimit):
		return ErrWorkloadResourceLimit
	default:
		return ErrWorkloadProviderOperation
	}
}

// MemoryWorkloadProvider is a bounded provider fixture and local execution
// implementation. It stores only safe status metadata, never payloads.
type MemoryWorkloadProvider struct {
	mu           sync.RWMutex
	workloads    map[string]models.WorkloadStatus
	logs         map[string][]models.WorkloadLog
	restarts     map[string]uint64
	volumes      map[string][]models.WorkloadVolume
	nextVolumeID uint64
	limits       WorkloadResourceLimits
}

func NewMemoryWorkloadProvider() *MemoryWorkloadProvider {
	provider, _ := NewMemoryWorkloadProviderWithLimits(WorkloadResourceLimits{
		MaxCPUMillis:   models.MaxWorkloadCPUMillis,
		MaxMemoryBytes: models.MaxWorkloadMemoryBytes,
	})
	return provider
}

func NewMemoryWorkloadProviderWithLimits(limits WorkloadResourceLimits) (*MemoryWorkloadProvider, error) {
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	return &MemoryWorkloadProvider{
		workloads: make(map[string]models.WorkloadStatus),
		logs:      make(map[string][]models.WorkloadLog),
		restarts:  make(map[string]uint64),
		volumes:   make(map[string][]models.WorkloadVolume),
		limits:    limits,
	}, nil
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

func (provider *MemoryWorkloadProvider) withinResourceLimits(resources models.WorkloadResources) bool {
	return (resources.CPUMillis == 0 || resources.CPUMillis <= provider.limits.MaxCPUMillis) &&
		(resources.MemoryBytes == 0 || resources.MemoryBytes <= provider.limits.MaxMemoryBytes)
}

func workloadResourceLimitStatus(resource models.Resource) models.WorkloadStatus {
	return models.WorkloadStatus{
		ObservedState: models.ResourceStateFailed,
		Health:        models.WorkloadHealthUnhealthy,
		Readiness:     models.WorkloadReadinessNotReady,
		Reason:        "workload resource limit exceeded",
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
	if !provider.withinResourceLimits(resource.Spec.WorkloadResources) {
		status := workloadResourceLimitStatus(resource)
		provider.workloads[resource.ID] = status
		return status, errProviderResourceLimit
	}
	status := memoryWorkloadStatus(resource)
	provider.workloads[resource.ID] = status
	return status, nil
}

func (provider *MemoryWorkloadProvider) Restart(ctx context.Context, resource models.Resource) (models.WorkloadStatus, error) {
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
	provider.restarts[resource.ID]++
	status := memoryWorkloadStatus(resource)
	status.ExecutionID = fmt.Sprintf("restart:%s:%d", resource.ID, provider.restarts[resource.ID])
	provider.workloads[resource.ID] = status
	provider.appendLogLocked(resource.ID, models.WorkloadLog{
		Timestamp:   time.Now().UTC(),
		Stream:      "system",
		Message:     "workload restarted",
		ExecutionID: status.ExecutionID,
	})
	return status, nil
}

func (provider *MemoryWorkloadProvider) Logs(ctx context.Context, resource models.Resource, limit int) ([]models.WorkloadLog, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateProviderResource(resource); err != nil {
		return nil, errProviderOperationUnsupported
	}
	if limit <= 0 || limit > MaxWorkloadLogRecords {
		return nil, errProviderOperationUnsupported
	}
	provider.mu.RLock()
	defer provider.mu.RUnlock()
	if _, exists := provider.workloads[resource.ID]; !exists {
		return nil, errProviderNotFound
	}
	logs := provider.logs[resource.ID]
	if len(logs) > limit {
		logs = logs[len(logs)-limit:]
	}
	return append([]models.WorkloadLog(nil), logs...), nil
}

func (provider *MemoryWorkloadProvider) appendLogLocked(resourceID string, log models.WorkloadLog) {
	logs := append(provider.logs[resourceID], log)
	if len(logs) > MaxWorkloadLogRecords {
		logs = logs[len(logs)-MaxWorkloadLogRecords:]
	}
	provider.logs[resourceID] = logs
}

func (provider *MemoryWorkloadProvider) AttachVolume(ctx context.Context, resource models.Resource, name string, maxBytes int64) (models.WorkloadVolume, error) {
	if err := ctx.Err(); err != nil {
		return models.WorkloadVolume{}, err
	}
	if err := validateProviderResource(resource); err != nil {
		return models.WorkloadVolume{}, errProviderOperationUnsupported
	}
	if validateWorkloadVolumeRequest(name, maxBytes) != nil {
		return models.WorkloadVolume{}, errProviderInvalidVolume
	}

	provider.mu.Lock()
	defer provider.mu.Unlock()
	if _, exists := provider.workloads[resource.ID]; !exists {
		return models.WorkloadVolume{}, errProviderNotFound
	}
	volumes := provider.volumes[resource.ID]
	if len(volumes) >= MaxWorkloadVolumeRecords {
		return models.WorkloadVolume{}, errProviderConflict
	}
	for _, volume := range volumes {
		if volume.Name == name {
			return models.WorkloadVolume{}, errProviderDuplicateVolume
		}
	}
	if provider.nextVolumeID == ^uint64(0) {
		return models.WorkloadVolume{}, errProviderConflict
	}
	provider.nextVolumeID++
	volume := models.WorkloadVolume{
		ID:         fmt.Sprintf("volume-%08d", provider.nextVolumeID),
		WorkloadID: resource.ID,
		Name:       name,
		Path:       fmt.Sprintf("workloads/%s/volumes/volume-%08d", resource.ID, provider.nextVolumeID),
		MaxBytes:   maxBytes,
	}
	if err := volume.Validate(); err != nil {
		provider.nextVolumeID--
		return models.WorkloadVolume{}, errProviderInvalidVolume
	}
	provider.volumes[resource.ID] = append(volumes, volume)
	return volume, nil
}

func (provider *MemoryWorkloadProvider) ListVolumes(ctx context.Context, resource models.Resource, limit int) ([]models.WorkloadVolume, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateProviderResource(resource); err != nil {
		return nil, errProviderOperationUnsupported
	}
	if limit <= 0 || limit > MaxWorkloadVolumeRecords {
		return nil, errProviderOperationUnsupported
	}

	provider.mu.RLock()
	defer provider.mu.RUnlock()
	if _, exists := provider.workloads[resource.ID]; !exists {
		return nil, errProviderNotFound
	}
	volumes := provider.volumes[resource.ID]
	if len(volumes) > limit {
		volumes = volumes[:limit]
	}
	return append([]models.WorkloadVolume(nil), volumes...), nil
}

func (provider *MemoryWorkloadProvider) CleanupVolumes(ctx context.Context, resource models.Resource) error {
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
	delete(provider.volumes, resource.ID)
	return nil
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
	if !provider.withinResourceLimits(resource.Spec.WorkloadResources) {
		status := workloadResourceLimitStatus(resource)
		provider.workloads[resource.ID] = status
		return status, errProviderResourceLimit
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
	delete(provider.logs, resource.ID)
	delete(provider.restarts, resource.ID)
	delete(provider.volumes, resource.ID)
	return nil
}

var _ WorkloadProvider = (*MemoryWorkloadProvider)(nil)
var _ WorkloadLogProvider = (*MemoryWorkloadProvider)(nil)
var _ WorkloadRestartProvider = (*MemoryWorkloadProvider)(nil)
var _ WorkloadVolumeProvider = (*MemoryWorkloadProvider)(nil)
var _ WorkloadControlPlane = (*WorkloadManager)(nil)
