package ember

import (
	"context"
	"errors"
)

var (
	ErrResourceNotFound  = errors.New("resource not found")
	ErrOperationNotFound = errors.New("operation not found")
)

// ControlPlane is the domain boundary implemented by resource providers.
type ControlPlane interface {
	CreateResource(ctx context.Context, spec ResourceSpec) (*Resource, *Operation, error)
	GetResource(ctx context.Context, resourceID string) (*Resource, error)
	GetOperation(ctx context.Context, operationID string) (*Operation, error)
}
