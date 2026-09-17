package ember

import (
	"context"
	"errors"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

var ErrInvalidResourceStore = errors.New("invalid resource store")

// ResourceControlPlane exposes the scoped resource portion of the control-plane
// contract. Operation tracking remains a separate follow-on boundary.
type ResourceControlPlane interface {
	CreateResource(ctx context.Context, spec models.ResourceSpec) (*models.Resource, error)
	GetResource(ctx context.Context, scopeID, resourceID string) (*models.Resource, error)
	ListResources(ctx context.Context, scopeID string, limit int) ([]models.Resource, error)
	UpdateResourceTags(ctx context.Context, scopeID, resourceID string, tags map[string]string) (*models.Resource, error)
	UpdateResourceObservedState(ctx context.Context, scopeID, resourceID string, state models.ResourceState) (*models.Resource, error)
	DeleteResource(ctx context.Context, scopeID, resourceID string) error
	AcquireResourceLock(ctx context.Context, scopeID, resourceID string, lock models.ResourceLock) error
	ReleaseResourceLock(ctx context.Context, scopeID, resourceID string, lock models.ResourceLock) error
	InspectResourceLock(ctx context.Context, scopeID, resourceID string) (*models.ResourceLock, error)
}

// ResourceManager delegates scoped resource operations to the approved store
// boundary without adding a second persistence implementation.
type ResourceManager struct {
	store persistence.ResourceStore
}

func NewResourceManager(store persistence.ResourceStore) (*ResourceManager, error) {
	if store == nil {
		return nil, ErrInvalidResourceStore
	}
	return &ResourceManager{store: store}, nil
}

func (manager *ResourceManager) CreateResource(ctx context.Context, spec models.ResourceSpec) (*models.Resource, error) {
	return manager.store.Create(ctx, spec)
}

func (manager *ResourceManager) GetResource(ctx context.Context, scopeID, resourceID string) (*models.Resource, error) {
	return manager.store.Get(ctx, scopeID, resourceID)
}

func (manager *ResourceManager) ListResources(ctx context.Context, scopeID string, limit int) ([]models.Resource, error) {
	return manager.store.List(ctx, scopeID, limit)
}

func (manager *ResourceManager) UpdateResourceTags(ctx context.Context, scopeID, resourceID string, tags map[string]string) (*models.Resource, error) {
	return manager.store.UpdateTags(ctx, scopeID, resourceID, tags)
}

func (manager *ResourceManager) UpdateResourceObservedState(ctx context.Context, scopeID, resourceID string, state models.ResourceState) (*models.Resource, error) {
	return manager.store.UpdateObservedState(ctx, scopeID, resourceID, state)
}

func (manager *ResourceManager) DeleteResource(ctx context.Context, scopeID, resourceID string) error {
	return manager.store.Delete(ctx, scopeID, resourceID)
}

func (manager *ResourceManager) AcquireResourceLock(ctx context.Context, scopeID, resourceID string, lock models.ResourceLock) error {
	return manager.store.AcquireLock(ctx, scopeID, resourceID, lock)
}

func (manager *ResourceManager) ReleaseResourceLock(ctx context.Context, scopeID, resourceID string, lock models.ResourceLock) error {
	return manager.store.ReleaseLock(ctx, scopeID, resourceID, lock)
}

func (manager *ResourceManager) InspectResourceLock(ctx context.Context, scopeID, resourceID string) (*models.ResourceLock, error) {
	return manager.store.InspectLock(ctx, scopeID, resourceID)
}

func (manager *ResourceManager) Close() error {
	if manager == nil || manager.store == nil {
		return nil
	}
	closer, ok := manager.store.(interface{ Close() error })
	if !ok {
		return nil
	}
	return closer.Close()
}

var _ ResourceControlPlane = (*ResourceManager)(nil)
