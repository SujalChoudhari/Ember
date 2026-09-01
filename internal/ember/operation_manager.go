package ember

import (
	"context"
	"errors"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

var ErrInvalidOperationStore = errors.New("invalid operation store")

// OperationControlPlane exposes operation inspection and request-key
// idempotency through the approved persistence boundary.
type OperationControlPlane interface {
	CreateOperation(ctx context.Context, operation models.Operation) (*models.Operation, error)
	GetOperation(ctx context.Context, operationID string) (*models.Operation, error)
	GetOperationByRequestID(ctx context.Context, requestID string) (*models.Operation, error)
	ListOperations(ctx context.Context, resourceID string, limit int) ([]models.Operation, error)
}

// OperationManager delegates operation calls to the approved store boundary
// without adding a second persistence implementation.
type OperationManager struct {
	store persistence.OperationStore
}

func NewOperationManager(store persistence.OperationStore) (*OperationManager, error) {
	if store == nil {
		return nil, ErrInvalidOperationStore
	}
	return &OperationManager{store: store}, nil
}

func (manager *OperationManager) CreateOperation(ctx context.Context, operation models.Operation) (*models.Operation, error) {
	return manager.store.Create(ctx, operation)
}

func (manager *OperationManager) GetOperation(ctx context.Context, operationID string) (*models.Operation, error) {
	return manager.store.Get(ctx, operationID)
}

func (manager *OperationManager) GetOperationByRequestID(ctx context.Context, requestID string) (*models.Operation, error) {
	return manager.store.GetByRequestID(ctx, requestID)
}

func (manager *OperationManager) ListOperations(ctx context.Context, resourceID string, limit int) ([]models.Operation, error) {
	return manager.store.List(ctx, resourceID, limit)
}

var _ OperationControlPlane = (*OperationManager)(nil)
