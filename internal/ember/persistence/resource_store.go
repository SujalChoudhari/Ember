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
	ErrResourceHasDependents    = errors.New("resource has dependents")
	ErrInvalidScope             = errors.New("invalid resource scope")
	ErrInvalidResourceListLimit = errors.New("invalid resource list limit")
)

type ResourceStore interface {
	Create(ctx context.Context, spec models.ResourceSpec) (*models.Resource, error)
	Get(ctx context.Context, scopeID, resourceID string) (*models.Resource, error)
	List(ctx context.Context, scopeID string, limit int) ([]models.Resource, error)
	UpdateTags(ctx context.Context, scopeID, resourceID string, tags map[string]string) (*models.Resource, error)
	Delete(ctx context.Context, scopeID, resourceID string) error
}
