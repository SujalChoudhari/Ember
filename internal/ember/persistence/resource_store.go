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
	ErrResourceLockConflict     = errors.New("resource lock conflict")
	ErrResourceLockNotHeld      = errors.New("resource lock not held")
	ErrResourceLockNotOwner     = errors.New("resource lock not owned")
	ErrInvalidScope             = errors.New("invalid resource scope")
	ErrInvalidResourceListLimit = errors.New("invalid resource list limit")
)

type ResourceStore interface {
	Create(ctx context.Context, spec models.ResourceSpec) (*models.Resource, error)
	Get(ctx context.Context, scopeID, resourceID string) (*models.Resource, error)
	List(ctx context.Context, scopeID string, limit int) ([]models.Resource, error)
	UpdateTags(ctx context.Context, scopeID, resourceID string, tags map[string]string) (*models.Resource, error)
	Delete(ctx context.Context, scopeID, resourceID string) error
	AcquireLock(ctx context.Context, scopeID, resourceID string, lock models.ResourceLock) error
	ReleaseLock(ctx context.Context, scopeID, resourceID string, lock models.ResourceLock) error
	InspectLock(ctx context.Context, scopeID, resourceID string) (*models.ResourceLock, error)
}
