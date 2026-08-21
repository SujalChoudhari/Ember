package ember

import (
	"context"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

type controlPlaneContract struct{}

func (controlPlaneContract) CreateResource(context.Context, models.ResourceSpec) (*models.Resource, *models.Operation, error) {
	return nil, nil, nil
}

func (controlPlaneContract) GetResource(context.Context, string) (*models.Resource, error) {
	return nil, nil
}

func (controlPlaneContract) GetOperation(context.Context, string) (*models.Operation, error) {
	return nil, nil
}

var _ ControlPlane = controlPlaneContract{}
