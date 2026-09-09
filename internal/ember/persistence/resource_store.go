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
	ErrResourceLocked           = errors.New("resource is read-only")
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
	UpdateObservedState(ctx context.Context, scopeID, resourceID string, state models.ResourceState) (*models.Resource, error)
	Delete(ctx context.Context, scopeID, resourceID string) error
	AcquireLock(ctx context.Context, scopeID, resourceID string, lock models.ResourceLock) error
	ReleaseLock(ctx context.Context, scopeID, resourceID string, lock models.ResourceLock) error
	InspectLock(ctx context.Context, scopeID, resourceID string) (*models.ResourceLock, error)
}

// resourceHasReadOnlyLock expects the caller to hold the adapter mutex.
func resourceHasReadOnlyLock(resources map[string]models.Resource, locks map[string]models.ResourceLock, resourceID string) bool {
	visited := make(map[string]struct{})
	for resourceID != "" {
		if _, seen := visited[resourceID]; seen {
			return true
		}
		visited[resourceID] = struct{}{}
		if _, locked := locks[resourceID]; locked {
			return true
		}
		resource, exists := resources[resourceID]
		if !exists {
			return false
		}
		resourceID = resource.Spec.ParentID
	}
	return false
}
