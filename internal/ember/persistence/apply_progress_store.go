package persistence

import (
	"context"
	"errors"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const MaxApplyProgressListLimit = 100

var (
	ErrApplyProgressNotFound         = errors.New("apply progress not found")
	ErrDuplicateApplyProgress        = errors.New("duplicate apply progress")
	ErrApplyProgressRequestConflict  = errors.New("apply progress request conflict")
	ErrInvalidApplyProgressListLimit = errors.New("invalid apply progress list limit")
	ErrApplyProgressStoreFull        = errors.New("apply progress store is full")
)

// ApplyProgressStore is the bounded persistence boundary for apply progress.
type ApplyProgressStore interface {
	Create(ctx context.Context, record models.ApplyProgressRecord) (*models.ApplyProgressRecord, error)
	Update(ctx context.Context, record models.ApplyProgressRecord) error
	Get(ctx context.Context, recordID string) (*models.ApplyProgressRecord, error)
	List(ctx context.Context, limit int) ([]models.ApplyProgressRecord, error)
}
