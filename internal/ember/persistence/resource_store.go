// Package persistence defines the resource persistence port used by Ember's
// control plane. Concrete durable adapters are intentionally deferred from this
// contract slice.
package persistence

import (
	"context"
	"errors"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const MaxResourceListLimit = 100

var (
	ErrResourceNotFound         = errors.New("resource not found")
	ErrDuplicateResource        = errors.New("duplicate resource")
	ErrInvalidScope             = errors.New("invalid resource scope")
	ErrInvalidResourceListLimit = errors.New("invalid resource list limit")
)

// ResourceStore is the bounded persistence port for scoped resources.
//
// ResourceSpec.ParentID is the resource's parent scope; an empty ParentID is the
// root scope. Get and List accept that same scope ID and must return
// ErrResourceNotFound for an ID that exists outside the requested scope. IDs are
// opaque and immutable, and List must return a deterministic order with at most
// limit resources. Implementations must not place arbitrary serialized state or
// secrets in the resource model.
type ResourceStore interface {
	Create(ctx context.Context, spec models.ResourceSpec) (*models.Resource, error)
	Get(ctx context.Context, scopeID, resourceID string) (*models.Resource, error)
	List(ctx context.Context, scopeID string, limit int) ([]models.Resource, error)
}
