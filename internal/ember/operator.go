package ember

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/SujalChoudhari/Ember/internal/ember/deployment"
	"github.com/SujalChoudhari/Ember/internal/ember/events"
	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
	"github.com/SujalChoudhari/Ember/internal/ember/queue"
)

var (
	ErrInvalidOperator                 = errors.New("invalid operator")
	ErrInvalidOperatorPrincipal        = errors.New("invalid operator principal")
	ErrOperatorScopeDenied             = errors.New("operator scope denied")
	ErrOperatorResetUnavailable        = errors.New("operator reset unavailable")
	ErrOperatorBucketRequired          = errors.New("operator resource is not a bucket")
	ErrOperatorBlobRecoveryUnavailable = errors.New("operator blob recovery unavailable")
	ErrOperatorDeploymentUnavailable   = errors.New("operator deployment inspection unavailable")
	ErrOperatorWorkloadUnavailable     = errors.New("operator workload inspection unavailable")
	ErrOperatorNetworkUnavailable      = errors.New("operator network inspection unavailable")
	ErrDestructiveConfirmationRequired = errors.New("explicit confirmation is required for destructive actions")
	ErrInvalidOperatorListLimit        = errors.New("invalid operator list limit")
)

const MaxOperatorListLimit = 100

func validateOperatorListLimit(limit int) error {
	if limit <= 0 || limit > MaxOperatorListLimit {
		return ErrInvalidOperatorListLimit
	}
	return nil
}

type OperatorPrincipal struct {
	ScopeID  string
	TenantID string
}

func (principal OperatorPrincipal) validate() error {
	if principal.ScopeID != "" && (strings.TrimSpace(principal.ScopeID) == "" || len(principal.ScopeID) > models.MaxResourceIDLength) {
		return ErrInvalidOperatorPrincipal
	}
	if principal.TenantID != "" {
		if err := models.ValidateTenantID(principal.TenantID); err != nil {
			return ErrInvalidOperatorPrincipal
		}
	}
	return nil
}

type Operator struct {
	resources         ResourceControlPlane
	blobs             persistence.BlobStore
	operations        ResourceOperationControlPlane
	reset             func(context.Context) error
	deployment        DeploymentControlPlane
	workloads         WorkloadControlPlane
	networks          NetworkControlPlane
	observability     RuntimeObservability
	eventBroker       *events.TopicBroker
	eventDeadLetters  queue.DeadLetterStore
	eventMetrics      *events.Metrics
	eventTopologyPath string
	eventPersistMu    sync.Mutex
	workQueue         *queue.FileQueue
	applyProgress     persistence.ApplyProgressStore
	tenants           *persistence.TenantStore

	tenantResourcesMu sync.Mutex
	tenantResources   map[string]*ResourceManager
}

type OperatorResponse struct {
	Tenant          *models.Tenant               `json:"tenant,omitempty"`
	Tenants         []models.Tenant              `json:"tenants,omitempty"`
	Resource        *models.Resource             `json:"resource,omitempty"`
	Resources       []models.Resource            `json:"resources,omitempty"`
	Lock            *models.ResourceLock         `json:"lock,omitempty"`
	Object          *models.BlobObject           `json:"object,omitempty"`
	Integrity       *models.BlobIntegrityReport  `json:"integrity,omitempty"`
	Objects         []models.BlobObject          `json:"objects,omitempty"`
	Content         []byte                       `json:"content,omitempty"`
	Operation       *models.Operation            `json:"operation,omitempty"`
	Operations      []models.Operation           `json:"operations,omitempty"`
	Audit           []models.AuditEntry          `json:"audit,omitempty"`
	ApplyProgress   *models.ApplyProgressRecord  `json:"applyProgress,omitempty"`
	ApplyProgresses []models.ApplyProgressRecord `json:"applyProgresses,omitempty"`
	Recovery        *models.RecoveryRecord       `json:"recovery,omitempty"`
	Recoveries      []models.RecoveryRecord      `json:"recoveries,omitempty"`
	Workload        *WorkloadView                `json:"workload,omitempty"`
	Network         *models.Network              `json:"network,omitempty"`
	Networks        []models.Network             `json:"networks,omitempty"`
	Port            *models.NetworkPort          `json:"port,omitempty"`
	Ports           []models.NetworkPort         `json:"ports,omitempty"`
	Endpoint        *models.NetworkEndpoint      `json:"endpoint,omitempty"`
	Endpoints       []models.NetworkEndpoint     `json:"endpoints,omitempty"`
	Observability   *RuntimeObservabilityReport  `json:"observability,omitempty"`
	Metrics         *events.MetricsSnapshot      `json:"metrics,omitempty"`
	Volumes         []models.WorkloadVolume      `json:"volumes,omitempty"`
	Plan            *deployment.DeploymentPlan   `json:"plan,omitempty"`
	Resolution      *deployment.ResolvedDocument `json:"resolution,omitempty"`
	Apply           *deployment.ApplyResult      `json:"apply,omitempty"`
	Replayed        bool                         `json:"replayed,omitempty"`
}

func NewOperator(resources ResourceControlPlane, blobs persistence.BlobStore, operations ResourceOperationControlPlane, reset func(context.Context) error) (*Operator, error) {
	return newOperator(resources, blobs, operations, reset, nil)
}

func NewOperatorWithNetwork(resources ResourceControlPlane, blobs persistence.BlobStore, operations ResourceOperationControlPlane, reset func(context.Context) error, networks NetworkControlPlane) (*Operator, error) {
	return newOperatorWithRuntimeAndNetwork(resources, blobs, operations, reset, nil, nil, nil, networks)
}

// NewOperatorWithDeployment composes the operator with the existing bounded
// deployment inspection and recovery control plane.
func NewOperatorWithDeployment(resources ResourceControlPlane, blobs persistence.BlobStore, operations ResourceOperationControlPlane, reset func(context.Context) error, deployment DeploymentControlPlane) (*Operator, error) {
	return newOperator(resources, blobs, operations, reset, deployment)
}

func newOperator(resources ResourceControlPlane, blobs persistence.BlobStore, operations ResourceOperationControlPlane, reset func(context.Context) error, deployment DeploymentControlPlane) (*Operator, error) {
	return newOperatorWithRuntimeAndNetwork(resources, blobs, operations, reset, deployment, nil, nil, nil)
}

func newOperatorWithRuntime(resources ResourceControlPlane, blobs persistence.BlobStore, operations ResourceOperationControlPlane, reset func(context.Context) error, deployment DeploymentControlPlane, workloads WorkloadControlPlane, observability RuntimeObservability) (*Operator, error) {
	return newOperatorWithRuntimeAndNetwork(resources, blobs, operations, reset, deployment, workloads, observability, nil)
}

func newOperatorWithRuntimeAndNetwork(resources ResourceControlPlane, blobs persistence.BlobStore, operations ResourceOperationControlPlane, reset func(context.Context) error, deployment DeploymentControlPlane, workloads WorkloadControlPlane, observability RuntimeObservability, networks NetworkControlPlane) (*Operator, error) {
	if resources == nil || blobs == nil || operations == nil || reset == nil {
		return nil, ErrInvalidOperator
	}
	return &Operator{
		resources:       resources,
		blobs:           blobs,
		operations:      operations,
		reset:           reset,
		deployment:      deployment,
		workloads:       workloads,
		networks:        networks,
		observability:   observability,
		tenantResources: make(map[string]*ResourceManager),
	}, nil
}

func NewFileOperator(root string, quota int64) (*Operator, error) {
	if strings.TrimSpace(root) == "" {
		return nil, ErrInvalidOperator
	}
	root = filepath.Clean(root)
	if info, err := os.Stat(root); err == nil && !info.IsDir() {
		return nil, ErrInvalidOperator
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, ErrInvalidOperator
	}

	resourceStore, err := persistence.NewSQLiteResourceStore(filepath.Join(root, "platform.db"))
	if err != nil {
		return nil, err
	}
	resourceManager, err := NewResourceManager(resourceStore)
	if err != nil {
		return nil, err
	}
	blobStore, err := persistence.NewFileBlobStore(filepath.Join(root, "blobs"), quota)
	if err != nil {
		return nil, err
	}
	operationStore, err := persistence.NewFileOperationStore(filepath.Join(root, "operations.json"))
	if err != nil {
		return nil, err
	}
	auditStore, err := persistence.NewFileAuditStore(filepath.Join(root, "audit.json"))
	if err != nil {
		return nil, err
	}
	progressStore, err := persistence.NewFileApplyProgressStore(filepath.Join(root, "apply-progress.json"))
	if err != nil {
		return nil, err
	}
	recoveryStore, err := persistence.NewFileRecoveryStore(filepath.Join(root, "recoveries.json"))
	if err != nil {
		return nil, err
	}
	networkStore, err := persistence.NewFileNetworkStore(filepath.Join(root, "networks.json"))
	if err != nil {
		return nil, err
	}
	eventBroker, err := events.NewTopicBroker(events.TopicBrokerOptions{MaxTopics: events.MaxTopicCount, MaxSubscriptions: events.MaxSubscriptionCount})
	if err != nil {
		return nil, err
	}
	eventDeadLetters, err := queue.NewFileDeadLetterStore(filepath.Join(root, "event-dead-letters.json"), queue.DeadLetterStoreOptions{})
	if err != nil {
		return nil, err
	}
	eventMetrics := events.NewMetrics()
	workQueue, err := queue.NewFileQueue(filepath.Join(root, "work-queue.json"), queue.QueueOptions{})
	if err != nil {
		return nil, err
	}
	networkManager, err := NewNetworkManager(networkStore)
	if err != nil {
		return nil, err
	}
	coordinator, err := NewResourceOperationCoordinator(operationStore, auditStore)
	if err != nil {
		return nil, err
	}
	var provider *MemoryWorkloadProvider
	reset := func(ctx context.Context) error {
		if err := networkStore.Reset(ctx); err != nil {
			return err
		}
		if err := blobStore.Reset(ctx); err != nil {
			return err
		}
		if err := resourceStore.Reset(ctx); err != nil {
			return err
		}
		if err := operationStore.Reset(ctx); err != nil {
			return err
		}
		if err := progressStore.Reset(ctx); err != nil {
			return err
		}
		if err := recoveryStore.Reset(ctx); err != nil {
			return err
		}
		if err := provider.Reset(ctx); err != nil {
			return err
		}
		return auditStore.Reset(ctx)
	}
	registry, err := NewWorkloadProviderRegistry()
	if err != nil {
		return nil, err
	}
	provider, err = NewMemoryWorkloadProviderWithState(filepath.Join(root, "workloads.json"))
	if err != nil {
		return nil, err
	}
	if err := registry.Register(models.ProviderMetadata{Namespace: "Ember.Compute", Type: "workloads", Version: "v1"}, provider); err != nil {
		return nil, err
	}
	workloads, err := NewWorkloadManagerWithNetwork(resourceManager, registry, networkManager)
	if err != nil {
		return nil, err
	}
	observability, err := NewObservabilityManager(workloads, eventMetrics)
	if err != nil {
		return nil, err
	}
	operator, err := newOperatorWithRuntimeAndNetwork(resourceManager, blobStore, coordinator, reset, nil, workloads, observability, networkManager)
	if err != nil {
		return nil, err
	}
	operator.eventBroker = eventBroker
	operator.eventDeadLetters = eventDeadLetters
	operator.eventMetrics = eventMetrics
	operator.workQueue = workQueue
	operator.eventTopologyPath = filepath.Join(root, "event-topology.json")
	if err := operator.loadEventTopology(); err != nil {
		return nil, err
	}
	operator.applyProgress = progressStore
	recoveryAuthority, err := deployment.NewRecoveryAuthority(progressStore, recoveryStore, &fileRecoveryExecutor{operator: operator})
	if err != nil {
		return nil, err
	}
	deploymentControl, err := NewStoreDeploymentControlPlane(progressStore, recoveryStore, recoveryAuthority, func(ctx context.Context, principal OperatorPrincipal, resourceID string) error {
		_, _, err := operator.authorizeResource(ctx, principal, resourceID)
		return err
	}, coordinator)
	if err != nil {
		return nil, err
	}
	tenantStore, err := persistence.NewTenantStore(root)
	if err != nil {
		return nil, err
	}
	previousReset := operator.reset
	operator.reset = func(ctx context.Context) error {
		if err := operator.closeTenantResourceStores(); err != nil {
			return err
		}
		if err := previousReset(ctx); err != nil {
			return err
		}
		if err := resetRuntimeState(ctx, operator, root); err != nil {
			return err
		}
		return tenantStore.Reset(ctx)
	}
	operator.tenants = tenantStore
	operator.deployment = deploymentControl
	return operator, nil
}

func resetRuntimeState(ctx context.Context, operator *Operator, root string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, name := range []string{"work-queue.json", "event-dead-letters.json", "event-topology.json"} {
		if err := os.Remove(filepath.Join(root, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return ErrOperatorResetUnavailable
		}
	}
	broker, err := events.NewTopicBroker(events.TopicBrokerOptions{MaxTopics: events.MaxTopicCount, MaxSubscriptions: events.MaxSubscriptionCount})
	if err != nil {
		return ErrOperatorResetUnavailable
	}
	deadLetters, err := queue.NewFileDeadLetterStore(filepath.Join(root, "event-dead-letters.json"), queue.DeadLetterStoreOptions{})
	if err != nil {
		return ErrOperatorResetUnavailable
	}
	workQueue, err := queue.NewFileQueue(filepath.Join(root, "work-queue.json"), queue.QueueOptions{})
	if err != nil {
		return ErrOperatorResetUnavailable
	}
	operator.eventBroker = broker
	operator.eventDeadLetters = deadLetters
	operator.eventMetrics = events.NewMetrics()
	operator.workQueue = workQueue
	operator.eventTopologyPath = filepath.Join(root, "event-topology.json")
	return nil
}

func (operator *Operator) tenantResourceManager(ctx context.Context, tenantID string) (*ResourceManager, error) {
	if operator == nil || operator.tenants == nil {
		return nil, ErrOperatorTenantUnavailable
	}
	operator.tenantResourcesMu.Lock()
	defer operator.tenantResourcesMu.Unlock()
	if manager, ok := operator.tenantResources[tenantID]; ok {
		return manager, nil
	}
	store, err := operator.tenants.OpenResourceStore(ctx, tenantID)
	if errors.Is(err, persistence.ErrTenantNotFound) {
		return nil, ErrOperatorScopeDenied
	}
	if err != nil {
		return nil, err
	}
	manager, err := NewResourceManager(store)
	if err != nil {
		_ = store.Close()
		return nil, err
	}
	if operator.tenantResources == nil {
		operator.tenantResources = make(map[string]*ResourceManager)
	}
	operator.tenantResources[tenantID] = manager
	return manager, nil
}

func (operator *Operator) resourcePlane(ctx context.Context, principal OperatorPrincipal) (ResourceControlPlane, error) {
	if err := principal.validate(); err != nil {
		return nil, err
	}
	if principal.TenantID == "" {
		return operator.resources, nil
	}
	return operator.tenantResourceManager(ctx, principal.TenantID)
}

func (operator *Operator) closeTenantResourceStore(tenantID string) error {
	operator.tenantResourcesMu.Lock()
	defer operator.tenantResourcesMu.Unlock()
	manager, ok := operator.tenantResources[tenantID]
	if !ok {
		return nil
	}
	if err := manager.Close(); err != nil {
		return err
	}
	delete(operator.tenantResources, tenantID)
	return nil
}

func (operator *Operator) closeTenantResourceStores() error {
	operator.tenantResourcesMu.Lock()
	defer operator.tenantResourcesMu.Unlock()
	for tenantID, manager := range operator.tenantResources {
		if err := manager.Close(); err != nil {
			return err
		}
		delete(operator.tenantResources, tenantID)
	}
	return nil
}

func (operator *Operator) Close() error {
	if operator == nil {
		return nil
	}
	if err := operator.closeTenantResourceStores(); err != nil {
		return err
	}
	if closer, ok := operator.resources.(interface{ Close() error }); ok {
		if err := closer.Close(); err != nil {
			return err
		}
	}
	if operator.tenants != nil {
		return operator.tenants.Close()
	}
	return nil
}

func (operator *Operator) CreateResource(ctx context.Context, principal OperatorPrincipal, spec models.ResourceSpec) (*models.Resource, error) {
	if err := principal.validate(); err != nil {
		return nil, err
	}
	resources, err := operator.resourcePlane(ctx, principal)
	if err != nil {
		return nil, err
	}
	if spec.ParentID != principal.ScopeID {
		return nil, ErrOperatorScopeDenied
	}
	return resources.CreateResource(ctx, spec)
}

func (operator *Operator) CreateWorkload(ctx context.Context, principal OperatorPrincipal, spec models.ResourceSpec) (*WorkloadView, error) {
	if operator.workloads == nil {
		return nil, ErrOperatorWorkloadUnavailable
	}
	if err := principal.validate(); err != nil {
		return nil, err
	}
	if principal.ScopeID == "" || spec.ParentID != principal.ScopeID {
		return nil, ErrOperatorScopeDenied
	}
	return operator.workloads.CreateWorkload(ctx, principal.ScopeID, spec)
}

func (operator *Operator) GetWorkload(ctx context.Context, principal OperatorPrincipal, resourceID string) (*WorkloadView, error) {
	if operator.workloads == nil {
		return nil, ErrOperatorWorkloadUnavailable
	}
	if err := principal.validate(); err != nil {
		return nil, err
	}
	return operator.workloads.GetWorkload(ctx, principal.ScopeID, resourceID)
}

func (operator *Operator) RestartWorkload(ctx context.Context, principal OperatorPrincipal, resourceID string) (*WorkloadView, error) {
	if operator.workloads == nil {
		return nil, ErrOperatorWorkloadUnavailable
	}
	if err := principal.validate(); err != nil {
		return nil, err
	}
	return operator.workloads.RestartWorkload(ctx, principal.ScopeID, resourceID)
}

func (operator *Operator) DeleteWorkload(ctx context.Context, principal OperatorPrincipal, resourceID string) error {
	if operator.workloads == nil {
		return ErrOperatorWorkloadUnavailable
	}
	if err := principal.validate(); err != nil {
		return err
	}
	if principal.ScopeID == "" {
		return ErrOperatorScopeDenied
	}
	return operator.workloads.DeleteWorkload(ctx, principal.ScopeID, resourceID)
}

func (operator *Operator) InspectWorkload(ctx context.Context, principal OperatorPrincipal, resourceID string, logLimit int) (*RuntimeObservabilityReport, error) {
	if operator.observability == nil {
		return nil, ErrOperatorWorkloadUnavailable
	}
	if err := principal.validate(); err != nil {
		return nil, err
	}
	return operator.observability.InspectWorkload(ctx, principal.ScopeID, resourceID, logLimit)
}

func (operator *Operator) SnapshotMetrics() (events.MetricsSnapshot, error) {
	if operator.observability == nil {
		return events.MetricsSnapshot{}, ErrOperatorWorkloadUnavailable
	}
	return operator.observability.SnapshotMetrics(), nil
}

func (operator *Operator) AttachWorkloadVolume(ctx context.Context, principal OperatorPrincipal, resourceID, name string, maxBytes int64) (*models.WorkloadVolume, error) {
	if operator.workloads == nil {
		return nil, ErrOperatorWorkloadUnavailable
	}
	if err := principal.validate(); err != nil {
		return nil, err
	}
	if principal.ScopeID == "" {
		return nil, ErrOperatorScopeDenied
	}
	return operator.workloads.AttachWorkloadVolume(ctx, principal.ScopeID, resourceID, name, maxBytes)
}

func (operator *Operator) ListWorkloadVolumes(ctx context.Context, principal OperatorPrincipal, resourceID string, limit int) ([]models.WorkloadVolume, error) {
	if operator.workloads == nil {
		return nil, ErrOperatorWorkloadUnavailable
	}
	if err := principal.validate(); err != nil {
		return nil, err
	}
	if principal.ScopeID == "" {
		return nil, ErrOperatorScopeDenied
	}
	return operator.workloads.ListWorkloadVolumes(ctx, principal.ScopeID, resourceID, limit)
}

func (operator *Operator) CleanupWorkloadVolumes(ctx context.Context, principal OperatorPrincipal, resourceID string) error {
	if operator.workloads == nil {
		return ErrOperatorWorkloadUnavailable
	}
	if err := principal.validate(); err != nil {
		return err
	}
	if principal.ScopeID == "" {
		return ErrOperatorScopeDenied
	}
	return operator.workloads.CleanupWorkloadVolumes(ctx, principal.ScopeID, resourceID)
}

func (operator *Operator) networkScope(principal OperatorPrincipal) (string, error) {
	if operator.networks == nil {
		return "", ErrOperatorNetworkUnavailable
	}
	if err := principal.validate(); err != nil {
		return "", err
	}
	if principal.ScopeID == "" {
		return "", ErrOperatorScopeDenied
	}
	return principal.ScopeID, nil
}

func (operator *Operator) CreateNetwork(ctx context.Context, principal OperatorPrincipal, name string) (*models.Network, error) {
	scopeID, err := operator.networkScope(principal)
	if err != nil {
		return nil, err
	}
	return operator.networks.CreateNetwork(ctx, scopeID, name)
}

func (operator *Operator) GetNetwork(ctx context.Context, principal OperatorPrincipal, networkID string) (*models.Network, error) {
	scopeID, err := operator.networkScope(principal)
	if err != nil {
		return nil, err
	}
	return operator.networks.GetNetwork(ctx, scopeID, networkID)
}

func (operator *Operator) ListNetworks(ctx context.Context, principal OperatorPrincipal, limit int) ([]models.Network, error) {
	scopeID, err := operator.networkScope(principal)
	if err != nil {
		return nil, err
	}
	return operator.networks.ListNetworks(ctx, scopeID, limit)
}

func (operator *Operator) DeleteNetwork(ctx context.Context, principal OperatorPrincipal, networkID string) error {
	scopeID, err := operator.networkScope(principal)
	if err != nil {
		return err
	}
	return operator.networks.DeleteNetwork(ctx, scopeID, networkID)
}

func (operator *Operator) AllocateNetworkPort(ctx context.Context, principal OperatorPrincipal, networkID, workloadID string, number uint16, protocol models.NetworkProtocol) (*models.NetworkPort, error) {
	scopeID, err := operator.networkScope(principal)
	if err != nil {
		return nil, err
	}
	if operator.workloads != nil {
		if _, err := operator.workloads.GetWorkload(ctx, scopeID, workloadID); err != nil {
			return nil, err
		}
	}
	return operator.networks.AllocatePort(ctx, scopeID, networkID, workloadID, number, protocol)
}

func (operator *Operator) GetNetworkPort(ctx context.Context, principal OperatorPrincipal, portID string) (*models.NetworkPort, error) {
	scopeID, err := operator.networkScope(principal)
	if err != nil {
		return nil, err
	}
	return operator.networks.GetPort(ctx, scopeID, portID)
}

func (operator *Operator) ListNetworkPorts(ctx context.Context, principal OperatorPrincipal, networkID string, limit int) ([]models.NetworkPort, error) {
	scopeID, err := operator.networkScope(principal)
	if err != nil {
		return nil, err
	}
	return operator.networks.ListPorts(ctx, scopeID, networkID, limit)
}

func (operator *Operator) DeleteNetworkPort(ctx context.Context, principal OperatorPrincipal, portID string) error {
	scopeID, err := operator.networkScope(principal)
	if err != nil {
		return err
	}
	return operator.networks.DeletePort(ctx, scopeID, portID)
}

func (operator *Operator) PublishNetworkEndpoint(ctx context.Context, principal OperatorPrincipal, networkID, portID, name string) (*models.NetworkEndpoint, error) {
	scopeID, err := operator.networkScope(principal)
	if err != nil {
		return nil, err
	}
	return operator.networks.PublishEndpoint(ctx, scopeID, networkID, portID, name)
}

func (operator *Operator) GetNetworkEndpoint(ctx context.Context, principal OperatorPrincipal, endpointID string) (*models.NetworkEndpoint, error) {
	scopeID, err := operator.networkScope(principal)
	if err != nil {
		return nil, err
	}
	return operator.networks.ResolveEndpoint(ctx, scopeID, endpointID)
}

func (operator *Operator) ListNetworkEndpoints(ctx context.Context, principal OperatorPrincipal, networkID string, limit int) ([]models.NetworkEndpoint, error) {
	scopeID, err := operator.networkScope(principal)
	if err != nil {
		return nil, err
	}
	return operator.networks.ListEndpoints(ctx, scopeID, networkID, limit)
}

func (operator *Operator) DeleteNetworkEndpoint(ctx context.Context, principal OperatorPrincipal, endpointID string) error {
	scopeID, err := operator.networkScope(principal)
	if err != nil {
		return err
	}
	return operator.networks.DeleteEndpoint(ctx, scopeID, endpointID)
}

func (operator *Operator) resourceLookupScope(principal OperatorPrincipal, resourceID string) string {
	if principal.ScopeID != "" && resourceID == principal.ScopeID {
		return ""
	}
	return principal.ScopeID
}

func (operator *Operator) GetResource(ctx context.Context, principal OperatorPrincipal, resourceID string) (*models.Resource, error) {
	resource, _, err := operator.authorizeResource(ctx, principal, resourceID)
	return resource, err
}

func (operator *Operator) ListResources(ctx context.Context, principal OperatorPrincipal, limit int) ([]models.Resource, error) {
	if err := principal.validate(); err != nil {
		return nil, err
	}
	resources, err := operator.resourcePlane(ctx, principal)
	if err != nil {
		return nil, err
	}
	listed, err := resources.ListResources(ctx, principal.ScopeID, limit)
	if errors.Is(err, persistence.ErrResourceNotFound) && (principal.ScopeID != "" || principal.TenantID != "") {
		return nil, ErrOperatorScopeDenied
	}
	return listed, err
}

func (operator *Operator) authorizeResource(ctx context.Context, principal OperatorPrincipal, resourceID string) (*models.Resource, string, error) {
	if err := principal.validate(); err != nil {
		return nil, "", err
	}
	resources, err := operator.resourcePlane(ctx, principal)
	if err != nil {
		return nil, "", err
	}
	scopeID := operator.resourceLookupScope(principal, resourceID)
	resource, err := resources.GetResource(ctx, scopeID, resourceID)
	if errors.Is(err, persistence.ErrResourceNotFound) && (principal.ScopeID != "" || principal.TenantID != "") {
		return nil, "", ErrOperatorScopeDenied
	}
	return resource, scopeID, err
}

func (operator *Operator) ensureResourceUnlocked(ctx context.Context, principal OperatorPrincipal, resource *models.Resource, scopeID string) error {
	resources, err := operator.resourcePlane(ctx, principal)
	if err != nil {
		return err
	}
	visited := make(map[string]struct{})
	for resource != nil {
		if _, seen := visited[resource.ID]; seen {
			return persistence.ErrResourceLocked
		}
		visited[resource.ID] = struct{}{}

		lock, err := resources.InspectResourceLock(ctx, scopeID, resource.ID)
		if err != nil {
			return err
		}
		if lock != nil {
			return persistence.ErrResourceLocked
		}
		if resource.Spec.ParentID == "" {
			return nil
		}

		parentID := resource.Spec.ParentID
		parentScope := operator.resourceLookupScope(principal, parentID)
		resource, err = resources.GetResource(ctx, parentScope, parentID)
		if errors.Is(err, persistence.ErrResourceNotFound) && (principal.ScopeID != "" || principal.TenantID != "") {
			return ErrOperatorScopeDenied
		}
		if err != nil {
			return err
		}
		scopeID = parentScope
	}
	return nil
}

func (operator *Operator) UpdateResourceTags(ctx context.Context, principal OperatorPrincipal, resourceID string, tags map[string]string, requestID, correlationID string) (*OperatorResponse, error) {
	resource, scopeID, err := operator.authorizeResource(ctx, principal, resourceID)
	if err != nil {
		return nil, err
	}
	resources, err := operator.resourcePlane(ctx, principal)
	if err != nil {
		return nil, err
	}
	if err := operator.ensureResourceUnlocked(ctx, principal, resource, scopeID); err != nil {
		return nil, err
	}

	request := ResourceOperationRequest{
		ResourceID:    resourceID,
		ScopeID:       principal.ScopeID,
		Action:        "resource.update.tags",
		RequestID:     requestID,
		CorrelationID: correlationID,
	}
	var updated *models.Resource
	result, err := operator.operations.Execute(ctx, request, func(effectContext context.Context) error {
		updated, err = resources.UpdateResourceTags(effectContext, scopeID, resourceID, tags)
		return err
	})
	if err != nil && result == nil {
		return nil, err
	}
	if err != nil {
		response := &OperatorResponse{
			Operation: &result.Operation,
			Replayed:  result.Replayed,
		}
		return response, err
	}
	if result == nil {
		return nil, ErrOperatorResetUnavailable
	}
	if updated == nil {
		updated, err = resources.GetResource(ctx, scopeID, resourceID)
		if err != nil {
			return nil, err
		}
	}
	response := &OperatorResponse{
		Resource:  updated,
		Operation: &result.Operation,
		Replayed:  result.Replayed,
	}
	return response, err
}

func (operator *Operator) DeleteResource(ctx context.Context, principal OperatorPrincipal, resourceID string) error {
	_, scopeID, err := operator.authorizeResource(ctx, principal, resourceID)
	if err != nil {
		return err
	}
	resources, err := operator.resourcePlane(ctx, principal)
	if err != nil {
		return err
	}
	return resources.DeleteResource(ctx, scopeID, resourceID)
}

func (operator *Operator) AcquireResourceLock(ctx context.Context, principal OperatorPrincipal, resourceID string, lock models.ResourceLock) error {
	_, scopeID, err := operator.authorizeResource(ctx, principal, resourceID)
	if err != nil {
		return err
	}
	resources, err := operator.resourcePlane(ctx, principal)
	if err != nil {
		return err
	}
	return resources.AcquireResourceLock(ctx, scopeID, resourceID, lock)
}

func (operator *Operator) ReleaseResourceLock(ctx context.Context, principal OperatorPrincipal, resourceID string, lock models.ResourceLock) error {
	_, scopeID, err := operator.authorizeResource(ctx, principal, resourceID)
	if err != nil {
		return err
	}
	resources, err := operator.resourcePlane(ctx, principal)
	if err != nil {
		return err
	}
	return resources.ReleaseResourceLock(ctx, scopeID, resourceID, lock)
}

func (operator *Operator) InspectResourceLock(ctx context.Context, principal OperatorPrincipal, resourceID string) (*models.ResourceLock, error) {
	_, scopeID, err := operator.authorizeResource(ctx, principal, resourceID)
	if err != nil {
		return nil, err
	}
	resources, err := operator.resourcePlane(ctx, principal)
	if err != nil {
		return nil, err
	}
	return resources.InspectResourceLock(ctx, scopeID, resourceID)
}

func (operator *Operator) GetOperation(ctx context.Context, principal OperatorPrincipal, operationID string) (*models.Operation, error) {
	operation, err := operator.operations.GetOperation(ctx, operationID)
	if err != nil {
		return nil, err
	}
	if _, _, err := operator.authorizeResource(ctx, principal, operation.ResourceID); err == nil {
		return operation, nil
	} else if !strings.HasPrefix(operation.ResourceID, "/resources/") {
		return nil, err
	}

	resource, resolveErr := operator.findLogicalResource(ctx, principal, operation.ResourceID)
	if resolveErr != nil {
		return nil, err
	}
	if _, _, err := operator.authorizeResource(ctx, principal, resource.ID); err != nil {
		return nil, err
	}
	return operation, nil
}

func (operator *Operator) findLogicalResource(ctx context.Context, principal OperatorPrincipal, logicalID string) (*models.Resource, error) {
	if err := principal.validate(); err != nil {
		return nil, err
	}
	resources, err := operator.resourcePlane(ctx, principal)
	if err != nil {
		return nil, err
	}
	pending, err := resources.ListResources(ctx, principal.ScopeID, persistence.MaxResourceListLimit)
	if err != nil {
		return nil, err
	}
	visited := make(map[string]struct{}, len(pending))
	for len(pending) > 0 {
		resource := pending[0]
		pending = pending[1:]
		if _, seen := visited[resource.ID]; seen {
			continue
		}
		visited[resource.ID] = struct{}{}
		candidate := "/resources/" + url.PathEscape(string(resource.Spec.Type)) + "/" + url.PathEscape(resource.Spec.Name)
		if candidate == logicalID {
			return &resource, nil
		}
		children, listErr := resources.ListResources(ctx, resource.ID, persistence.MaxResourceListLimit)
		if listErr != nil && !errors.Is(listErr, persistence.ErrResourceNotFound) {
			return nil, listErr
		}
		pending = append(pending, children...)
	}
	return nil, persistence.ErrResourceNotFound
}

func (operator *Operator) ListOperations(ctx context.Context, principal OperatorPrincipal, resourceID string, limit int) ([]models.Operation, error) {
	if err := principal.validate(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > persistence.MaxOperationListLimit {
		return nil, persistence.ErrInvalidOperationListLimit
	}
	if resourceID != "" {
		if _, _, err := operator.authorizeResource(ctx, principal, resourceID); err != nil {
			return nil, err
		}
		return operator.operations.ListOperations(ctx, resourceID, limit)
	}

	queryLimit := limit
	if principal.ScopeID != "" {
		queryLimit = persistence.MaxOperationListLimit
	}
	operations, err := operator.operations.ListOperations(ctx, "", queryLimit)
	if err != nil {
		return nil, err
	}
	if principal.ScopeID == "" {
		return operations, nil
	}

	scoped := make([]models.Operation, 0, limit)
	for _, operation := range operations {
		if _, _, err := operator.authorizeResource(ctx, principal, operation.ResourceID); err != nil {
			if errors.Is(err, ErrOperatorScopeDenied) || errors.Is(err, persistence.ErrResourceNotFound) {
				continue
			}
			return nil, err
		}
		scoped = append(scoped, operation)
		if len(scoped) == limit {
			break
		}
	}
	return scoped, nil
}

func (operator *Operator) ListAuditHistory(ctx context.Context, principal OperatorPrincipal, resourceID string, limit int) ([]models.AuditEntry, error) {
	if _, _, err := operator.authorizeResource(ctx, principal, resourceID); err != nil {
		return nil, err
	}
	return operator.operations.ListAuditHistory(ctx, resourceID, limit)
}

func (operator *Operator) Reset(ctx context.Context, principal OperatorPrincipal) error {
	if err := principal.validate(); err != nil {
		return err
	}
	if principal.ScopeID != "" {
		return ErrOperatorScopeDenied
	}
	return operator.reset(ctx)
}

func (operator *Operator) authorizeBucket(ctx context.Context, principal OperatorPrincipal, bucketID string) error {
	resource, _, err := operator.authorizeResource(ctx, principal, bucketID)
	if err != nil {
		return err
	}
	if resource.Spec.Type != models.ResourceTypeBucket {
		return ErrOperatorBucketRequired
	}
	return nil
}

func (operator *Operator) PutBlob(ctx context.Context, principal OperatorPrincipal, bucketID, objectKey string, content []byte) (*OperatorResponse, error) {
	if err := operator.authorizeBucket(ctx, principal, bucketID); err != nil {
		return nil, err
	}
	object, err := operator.blobs.Put(ctx, bucketID, objectKey, content)
	if err != nil {
		return nil, err
	}
	return &OperatorResponse{Object: object}, nil
}

func (operator *Operator) GetBlob(ctx context.Context, principal OperatorPrincipal, bucketID, objectKey string) (*OperatorResponse, error) {
	if err := operator.authorizeBucket(ctx, principal, bucketID); err != nil {
		return nil, err
	}
	object, content, err := operator.blobs.Get(ctx, bucketID, objectKey)
	if err != nil {
		return nil, err
	}
	return &OperatorResponse{Object: object, Content: content}, nil
}

func (operator *Operator) VerifyBlob(ctx context.Context, principal OperatorPrincipal, bucketID, objectKey string) (*OperatorResponse, error) {
	if err := operator.authorizeBucket(ctx, principal, bucketID); err != nil {
		return nil, err
	}
	report, err := operator.blobs.Verify(ctx, bucketID, objectKey)
	return &OperatorResponse{Integrity: report}, err
}

func (operator *Operator) RecoverBlob(ctx context.Context, principal OperatorPrincipal, bucketID, objectKey, expectedSHA256 string, content []byte, requestID, correlationID string) (*OperatorResponse, error) {
	if err := operator.authorizeBucket(ctx, principal, bucketID); err != nil {
		return nil, err
	}
	request := ResourceOperationRequest{
		ResourceID:    bucketID,
		ScopeID:       principal.ScopeID,
		Action:        "blob.recover",
		RequestID:     requestID,
		CorrelationID: correlationID,
	}
	var object *models.BlobObject
	var recoveryErr error
	result, err := operator.operations.Execute(ctx, request, func(effectContext context.Context) error {
		object, recoveryErr = operator.blobs.Recover(effectContext, bucketID, objectKey, expectedSHA256, content)
		return recoveryErr
	})
	if err != nil && result == nil {
		return nil, err
	}
	if result == nil {
		return nil, ErrOperatorBlobRecoveryUnavailable
	}
	if err != nil {
		return &OperatorResponse{Operation: &result.Operation, Replayed: result.Replayed}, err
	}
	if object == nil {
		object, _, err = operator.blobs.Get(ctx, bucketID, objectKey)
		if err != nil {
			return nil, err
		}
	}
	response := &OperatorResponse{Object: object, Operation: &result.Operation, Replayed: result.Replayed}
	return response, err
}

func (operator *Operator) ReadBlobRange(ctx context.Context, principal OperatorPrincipal, bucketID, objectKey string, start, end int64) (*OperatorResponse, error) {
	if err := operator.authorizeBucket(ctx, principal, bucketID); err != nil {
		return nil, err
	}
	content, err := operator.blobs.ReadRange(ctx, bucketID, objectKey, start, end)
	if err != nil {
		return nil, err
	}
	return &OperatorResponse{Content: content}, nil
}

func (operator *Operator) ListBlobs(ctx context.Context, principal OperatorPrincipal, bucketID string, limit int) (*OperatorResponse, error) {
	if err := operator.authorizeBucket(ctx, principal, bucketID); err != nil {
		return nil, err
	}
	objects, err := operator.blobs.List(ctx, bucketID, limit)
	if err != nil {
		return nil, err
	}
	return &OperatorResponse{Objects: objects}, nil
}

func (operator *Operator) DeleteBlob(ctx context.Context, principal OperatorPrincipal, bucketID, objectKey string) error {
	if err := operator.authorizeBucket(ctx, principal, bucketID); err != nil {
		return err
	}
	return operator.blobs.Delete(ctx, bucketID, objectKey)
}
