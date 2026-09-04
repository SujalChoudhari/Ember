package persistence

import (
	"context"
	"errors"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const MaxRecoveryListLimit = 100

var (
	ErrRecoveryNotFound         = errors.New("recovery record not found")
	ErrDuplicateRecovery        = errors.New("duplicate recovery record")
	ErrRecoveryRequestConflict  = errors.New("recovery request conflict")
	ErrInvalidRecoveryListLimit = errors.New("invalid recovery list limit")
	ErrRecoveryStoreFull        = errors.New("recovery store is full")
)

// RecoveryStore is the bounded persistence boundary for explicit recovery
// actions. RecoveryRequestID is the idempotency key for a recovery attempt.
type RecoveryStore interface {
	Create(ctx context.Context, record models.RecoveryRecord) (*models.RecoveryRecord, error)
	Update(ctx context.Context, record models.RecoveryRecord) error
	Get(ctx context.Context, recordID string) (*models.RecoveryRecord, error)
	GetByRequestID(ctx context.Context, requestID string) (*models.RecoveryRecord, error)
	List(ctx context.Context, limit int) ([]models.RecoveryRecord, error)
}
