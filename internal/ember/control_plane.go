package ember

import (
	"context"
	"errors"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

var (
	ErrResourceNotFound  = errors.New("resource not found")
	ErrOperationNotFound = errors.New("operation not found")
)

// ControlPlane is the domain boundary implemented by resource providers.
type ControlPlane interface {
	CreateResource(ctx context.Context, spec models.ResourceSpec) (*models.Resource, *models.Operation, error)
	GetResource(ctx context.Context, resourceID string) (*models.Resource, error)
	GetOperation(ctx context.Context, operationID string) (*models.Operation, error)
}
