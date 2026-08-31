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

var _ ResourceControlPlane = (*ResourceManager)(nil)
