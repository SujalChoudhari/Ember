package ember

import (
	"context"

	"github.com/SujalChoudhari/Ember/internal/ember/deployment"
	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

// DeploymentControlPlane exposes the bounded deployment records and explicit
// recovery action without adding a second persistence or recovery contract.
type DeploymentControlPlane interface {
	GetApplyProgress(context.Context, OperatorPrincipal, string) (*models.ApplyProgressRecord, error)
	ListApplyProgress(context.Context, OperatorPrincipal, int) ([]models.ApplyProgressRecord, error)
	GetApplyProgressByOperation(context.Context, OperatorPrincipal, string) (*models.ApplyProgressRecord, error)
	GetRecovery(context.Context, OperatorPrincipal, string) (*models.RecoveryRecord, error)
	ListRecoveries(context.Context, OperatorPrincipal, int) ([]models.RecoveryRecord, error)
	Recover(context.Context, OperatorPrincipal, deployment.RecoveryRequest) (*deployment.RecoveryResult, error)
}

type storeDeploymentControlPlane struct {
	progress   persistence.ApplyProgressStore
	recoveries persistence.RecoveryStore
	authority  *deployment.RecoveryAuthority
	authorize  func(context.Context, OperatorPrincipal, string) error
	operations ResourceOperationControlPlane
}

// NewStoreDeploymentControlPlane adapts the existing bounded stores and
// recovery authority to the operator inspection contract.
func NewStoreDeploymentControlPlane(progress persistence.ApplyProgressStore, recoveries persistence.RecoveryStore, authority *deployment.RecoveryAuthority, authorize func(context.Context, OperatorPrincipal, string) error, operations ResourceOperationControlPlane) (DeploymentControlPlane, error) {
	if progress == nil || recoveries == nil || authority == nil || authorize == nil || operations == nil {
		return nil, ErrOperatorDeploymentUnavailable
	}
	return &storeDeploymentControlPlane{
		progress: progress, recoveries: recoveries, authority: authority,
		authorize: authorize, operations: operations,
	}, nil
}

func (control *storeDeploymentControlPlane) authorizeProgress(ctx context.Context, principal OperatorPrincipal, record models.ApplyProgressRecord) error {
	if err := principal.validate(); err != nil {
		return err
	}
	if principal.ScopeID == "" {
		return nil
	}
	for _, operationID := range record.OperationIDs {
		operation, err := control.operations.GetOperation(ctx, operationID)
		if err != nil {
			return err
		}
		if err := control.authorize(ctx, principal, operation.ResourceID); err == nil {
			return nil
		}
	}
	for _, entry := range record.Entries {
		if entry.ResourceID != "" {
			if err := control.authorize(ctx, principal, entry.ResourceID); err == nil {
				return nil
			}
		}
	}
	return ErrOperatorScopeDenied
}

func (control *storeDeploymentControlPlane) GetApplyProgress(ctx context.Context, principal OperatorPrincipal, recordID string) (*models.ApplyProgressRecord, error) {
	record, err := control.progress.Get(ctx, recordID)
	if err != nil {
		return nil, err
	}
	if err := control.authorizeProgress(ctx, principal, *record); err != nil {
		return nil, err
	}
	return record, nil
}

func (control *storeDeploymentControlPlane) ListApplyProgress(ctx context.Context, principal OperatorPrincipal, limit int) ([]models.ApplyProgressRecord, error) {
	if err := principal.validate(); err != nil {
		return nil, err
	}
	records, err := control.progress.List(ctx, limit)
	if err != nil || principal.ScopeID == "" {
		return records, err
	}
	filtered := make([]models.ApplyProgressRecord, 0, len(records))
	for _, record := range records {
		if err := control.authorizeProgress(ctx, principal, record); err == nil {
			filtered = append(filtered, record)
		}
	}
	return filtered, nil
}

func (control *storeDeploymentControlPlane) GetApplyProgressByOperation(ctx context.Context, principal OperatorPrincipal, operationID string) (*models.ApplyProgressRecord, error) {
	if _, err := control.operations.GetOperation(ctx, operationID); err != nil {
		return nil, err
	}
	records, err := control.progress.List(ctx, persistence.MaxApplyProgressListLimit)
	if err != nil {
		return nil, err
	}
	for _, record := range records {
		for _, candidate := range record.OperationIDs {
			if candidate == operationID {
				if err := control.authorizeProgress(ctx, principal, record); err != nil {
					return nil, err
				}
				copy := record
				return &copy, nil
			}
		}
	}
	return nil, persistence.ErrApplyProgressNotFound
}

func (control *storeDeploymentControlPlane) GetRecovery(ctx context.Context, principal OperatorPrincipal, recordID string) (*models.RecoveryRecord, error) {
	record, err := control.recoveries.Get(ctx, recordID)
	if err != nil {
		return nil, err
	}
	if err := control.authorizeRecovery(ctx, principal, *record); err != nil {
		return nil, err
	}
	return record, nil
}

func (control *storeDeploymentControlPlane) ListRecoveries(ctx context.Context, principal OperatorPrincipal, limit int) ([]models.RecoveryRecord, error) {
	if err := principal.validate(); err != nil {
		return nil, err
	}
	records, err := control.recoveries.List(ctx, limit)
	if err != nil || principal.ScopeID == "" {
		return records, err
	}
	filtered := make([]models.RecoveryRecord, 0, len(records))
	for _, record := range records {
		if err := control.authorizeRecovery(ctx, principal, record); err == nil {
			filtered = append(filtered, record)
		}
	}
	return filtered, nil
}

func (control *storeDeploymentControlPlane) authorizeRecovery(ctx context.Context, principal OperatorPrincipal, record models.RecoveryRecord) error {
	if err := principal.validate(); err != nil {
		return err
	}
	if principal.ScopeID == "" {
		return nil
	}
	for _, operationID := range record.OperationIDs {
		operation, err := control.operations.GetOperation(ctx, operationID)
		if err != nil {
			return err
		}
		if err := control.authorize(ctx, principal, operation.ResourceID); err == nil {
			return nil
		}
	}
	return ErrOperatorScopeDenied
}

func (control *storeDeploymentControlPlane) Recover(ctx context.Context, principal OperatorPrincipal, request deployment.RecoveryRequest) (*deployment.RecoveryResult, error) {
	progress, err := control.progress.Get(ctx, request.ApplyProgressID)
	if err != nil {
		return nil, err
	}
	if err := control.authorizeProgress(ctx, principal, *progress); err != nil {
		return nil, err
	}
	return control.authority.Recover(ctx, request)
}

var _ DeploymentControlPlane = (*storeDeploymentControlPlane)(nil)

func (operator *Operator) GetApplyProgress(ctx context.Context, principal OperatorPrincipal, recordID string) (*models.ApplyProgressRecord, error) {
	if operator.deployment == nil {
		return nil, ErrOperatorDeploymentUnavailable
	}
	return operator.deployment.GetApplyProgress(ctx, principal, recordID)
}

func (operator *Operator) ListApplyProgress(ctx context.Context, principal OperatorPrincipal, limit int) ([]models.ApplyProgressRecord, error) {
	if operator.deployment == nil {
		return nil, ErrOperatorDeploymentUnavailable
	}
	return operator.deployment.ListApplyProgress(ctx, principal, limit)
}

func (operator *Operator) GetApplyProgressByOperation(ctx context.Context, principal OperatorPrincipal, operationID string) (*models.ApplyProgressRecord, error) {
	if operator.deployment == nil {
		return nil, ErrOperatorDeploymentUnavailable
	}
	return operator.deployment.GetApplyProgressByOperation(ctx, principal, operationID)
}

func (operator *Operator) GetRecovery(ctx context.Context, principal OperatorPrincipal, recordID string) (*models.RecoveryRecord, error) {
	if operator.deployment == nil {
		return nil, ErrOperatorDeploymentUnavailable
	}
	return operator.deployment.GetRecovery(ctx, principal, recordID)
}

func (operator *Operator) ListRecoveries(ctx context.Context, principal OperatorPrincipal, limit int) ([]models.RecoveryRecord, error) {
	if operator.deployment == nil {
		return nil, ErrOperatorDeploymentUnavailable
	}
	return operator.deployment.ListRecoveries(ctx, principal, limit)
}

func (operator *Operator) Recover(ctx context.Context, principal OperatorPrincipal, request deployment.RecoveryRequest) (*OperatorResponse, error) {
	if operator.deployment == nil {
		return nil, ErrOperatorDeploymentUnavailable
	}
	result, err := operator.deployment.Recover(ctx, principal, request)
	if result == nil {
		return nil, err
	}
	return &OperatorResponse{Recovery: &result.Record, Replayed: result.Replayed}, err
}
