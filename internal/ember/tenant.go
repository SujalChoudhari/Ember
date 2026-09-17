package ember

import (
	"context"
	"errors"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

var ErrOperatorTenantUnavailable = errors.New("operator tenant registry unavailable")

func (operator *Operator) CreateTenant(ctx context.Context, principal OperatorPrincipal, tenant models.Tenant) (*models.Tenant, error) {
	if err := principal.validate(); err != nil {
		return nil, err
	}
	if principal.ScopeID != "" || principal.TenantID != "" {
		return nil, ErrOperatorScopeDenied
	}
	if operator == nil || operator.tenants == nil {
		return nil, ErrOperatorTenantUnavailable
	}
	return operator.tenants.CreateTenant(ctx, tenant)
}

func (operator *Operator) ListTenants(ctx context.Context, principal OperatorPrincipal) ([]models.Tenant, error) {
	if err := principal.validate(); err != nil {
		return nil, err
	}
	if principal.ScopeID != "" || principal.TenantID != "" {
		return nil, ErrOperatorScopeDenied
	}
	if operator == nil || operator.tenants == nil {
		return nil, ErrOperatorTenantUnavailable
	}
	return operator.tenants.ListTenants(ctx, persistence.MaxTenantListLimit)
}

func (operator *Operator) GetTenant(ctx context.Context, principal OperatorPrincipal, id string) (*models.Tenant, error) {
	if err := operator.authorizeTenant(ctx, principal, id); err != nil {
		return nil, err
	}
	return operator.tenants.GetTenant(ctx, id)
}

func (operator *Operator) OpenTenant(ctx context.Context, principal OperatorPrincipal, id string) (*persistence.TenantDatabase, error) {
	if err := operator.authorizeTenant(ctx, principal, id); err != nil {
		return nil, err
	}
	return operator.tenants.OpenTenant(ctx, id)
}

func (operator *Operator) DeleteTenant(ctx context.Context, principal OperatorPrincipal, id string, confirm bool) error {
	if err := principal.validate(); err != nil {
		return err
	}
	if principal.ScopeID != "" || principal.TenantID != "" {
		return ErrOperatorScopeDenied
	}
	if operator == nil || operator.tenants == nil {
		return ErrOperatorTenantUnavailable
	}
	if !confirm {
		return ErrDestructiveConfirmationRequired
	}
	if err := operator.closeTenantResourceStore(id); err != nil {
		return err
	}
	return operator.tenants.DeleteTenant(ctx, id)
}

func (operator *Operator) authorizeTenant(ctx context.Context, principal OperatorPrincipal, id string) error {
	if err := principal.validate(); err != nil {
		return err
	}
	if operator == nil || operator.tenants == nil {
		return ErrOperatorTenantUnavailable
	}
	if (principal.ScopeID != "" && principal.ScopeID != id) || principal.TenantID != "" {
		return ErrOperatorScopeDenied
	}
	if _, err := operator.tenants.GetTenant(ctx, id); err != nil {
		return err
	}
	return nil
}
