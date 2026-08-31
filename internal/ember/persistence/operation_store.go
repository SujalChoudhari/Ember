package persistence

import (
	"context"
	"errors"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const MaxOperationListLimit = 100

var (
	ErrOperationNotFound         = errors.New("operation not found")
	ErrDuplicateOperation        = errors.New("duplicate operation")
	ErrOperationRequestConflict  = errors.New("operation request conflict")
	ErrInvalidOperationListLimit = errors.New("invalid operation list limit")
)

// OperationStore is the bounded persistence boundary for operation inspection
// and request-key idempotency. Create returns the original record when a
// request key is retried for the same resource, and returns
// ErrOperationRequestConflict when that key is reused for another resource.
type OperationStore interface {
	Create(ctx context.Context, operation models.Operation) (*models.Operation, error)
	Get(ctx context.Context, operationID string) (*models.Operation, error)
	GetByRequestID(ctx context.Context, requestID string) (*models.Operation, error)
	List(ctx context.Context, resourceID string, limit int) ([]models.Operation, error)
}
