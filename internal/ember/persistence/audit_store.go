package persistence

import (
	"context"
	"errors"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const MaxAuditListLimit = 100

var (
	ErrDuplicateAuditEntry   = errors.New("duplicate audit entry")
	ErrInvalidAuditListLimit = errors.New("invalid audit list limit")
)

// AuditStore is the bounded persistence boundary for immutable audit history.
// Append validates and records one attribution-only entry. List returns
// deterministic history filtered by resource, or across resources when the
// resource filter is empty.
type AuditStore interface {
	Append(ctx context.Context, entry models.AuditEntry) error
	List(ctx context.Context, resourceID string, limit int) ([]models.AuditEntry, error)
}
