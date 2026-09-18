package ember

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/SujalChoudhari/Ember/internal/ember/deployment"
	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

var (
	ErrInvalidCLIParameters       = errors.New("invalid CLI parameters")
	ErrOperatorDeploymentPlanning = errors.New("operator deployment planning unavailable")
	ErrOperatorDeploymentApply    = errors.New("operator deployment apply unavailable")
)

func (operator *Operator) PlanDeployment(ctx context.Context, principal OperatorPrincipal, data []byte, supplied map[string]string) (deployment.Resolution, deployment.DeploymentPlan, error) {
	return operator.planDeployment(ctx, principal, data, supplied)
}

func (operator *Operator) ApplyDeployment(ctx context.Context, principal OperatorPrincipal, data []byte, supplied map[string]string, options deployment.ApplyOptions) (*deployment.ApplyResult, deployment.Resolution, error) {
	return operator.applyDeployment(ctx, principal, data, supplied, options)
}
func (operator *Operator) planDeployment(ctx context.Context, principal OperatorPrincipal, data []byte, supplied map[string]string) (deployment.Resolution, deployment.DeploymentPlan, error) {
	if operator == nil || ctx == nil {
		return deployment.Resolution{}, deployment.DeploymentPlan{}, ErrOperatorDeploymentPlanning
	}
	document, err := deployment.ParseAndValidate(data)
	if err != nil {
		return deployment.Resolution{}, deployment.DeploymentPlan{}, err
	}
	resolution, err := deployment.Resolve(document, supplied)
	if err != nil {
		return deployment.Resolution{}, deployment.DeploymentPlan{}, err
	}
	observed, err := operator.ListResources(ctx, principal, deployment.MaxPlanResources)
	if err != nil {
		return deployment.Resolution{}, deployment.DeploymentPlan{}, err
	}
	plan, err := deployment.BuildPlan(resolution.Document(), observed)
	if err != nil {
		return deployment.Resolution{}, deployment.DeploymentPlan{}, err
	}
	return resolution, plan, nil
}

func (operator *Operator) applyDeployment(ctx context.Context, principal OperatorPrincipal, data []byte, supplied map[string]string, options deployment.ApplyOptions) (*deployment.ApplyResult, deployment.Resolution, error) {
	resolution, plan, err := operator.planDeployment(ctx, principal, data, supplied)
	if err != nil {
		return nil, deployment.Resolution{}, err
	}
	if options.ProgressRecorder == nil {
		options.ProgressRecorder = operator.applyProgress
	}
	observed, err := operator.ListResources(ctx, principal, deployment.MaxPlanResources)
	if err != nil {
		return nil, deployment.Resolution{}, err
	}
	result, err := deployment.ApplyPlan(ctx, plan, resolution.Document(), observed, &operatorApplyAuthority{operator: operator, principal: principal}, options)
	return result, resolution, err
}

type operatorApplyAuthority struct {
	operator  *Operator
	principal OperatorPrincipal
}

func (authority *operatorApplyAuthority) ApplyResource(ctx context.Context, action deployment.PlanAction, logicalID, resourceID string, spec *models.ResourceSpec, parentID, requestID, correlationID string) (*models.Resource, *models.Operation, error) {
	if authority == nil || authority.operator == nil || (spec == nil && action != deployment.ActionDelete) {
		return nil, nil, ErrOperatorDeploymentApply
	}
	requestResourceID := resourceID
	if requestResourceID == "" {
		requestResourceID = logicalID
	}
	var applied *models.Resource
	request := ResourceOperationRequest{
		ResourceID:    requestResourceID,
		ScopeID:       authority.principal.ScopeID,
		Action:        "deployment." + string(action),
		RequestID:     requestID,
		CorrelationID: correlationID,
	}
	result, err := authority.operator.operations.Execute(ctx, request, func(effectContext context.Context) error {
		switch action {
		case deployment.ActionCreate:
			if parentID == "" && authority.principal.ScopeID != "" {
				return ErrOperatorScopeDenied
			}
			if parentID != "" {
				if _, _, err := authority.operator.authorizeResource(effectContext, authority.principal, parentID); err != nil {
					return err
				}
			}
			copy := *spec
			copy.ParentID = parentID
			var createErr error
			applied, createErr = authority.operator.resources.CreateResource(effectContext, copy)
			return createErr
		case deployment.ActionUpdate:
			_, scopeID, authorizeErr := authority.operator.authorizeResource(effectContext, authority.principal, resourceID)
			if authorizeErr != nil {
				return authorizeErr
			}
			var updateErr error
			applied, updateErr = authority.operator.resources.UpdateResourceTags(effectContext, scopeID, resourceID, cloneTags(spec.Tags))
			return updateErr
		case deployment.ActionDelete:
			_, scopeID, authorizeErr := authority.operator.authorizeResource(effectContext, authority.principal, resourceID)
			if authorizeErr != nil {
				return authorizeErr
			}
			return authority.operator.resources.DeleteResource(effectContext, scopeID, resourceID)
		default:
			return ErrOperatorDeploymentApply
		}
	})
	if err != nil && result == nil {
		return applied, nil, err
	}
	if result == nil {
		return applied, nil, ErrOperatorDeploymentApply
	}
	if applied == nil && action == deployment.ActionCreate && result.Replayed {
		applied = authority.findCreatedResource(ctx, *spec, parentID)
	}
	return applied, &result.Operation, err
}

func (authority *operatorApplyAuthority) findCreatedResource(ctx context.Context, spec models.ResourceSpec, parentID string) *models.Resource {
	resources, err := authority.operator.resources.ListResources(ctx, parentID, persistence.MaxResourceListLimit)
	if err != nil {
		return nil
	}
	for _, resource := range resources {
		if resource.Spec.Type == spec.Type && resource.Spec.Name == spec.Name {
			copy := resource
			return &copy
		}
	}
	return nil
}

func cloneTags(tags map[string]string) map[string]string {
	if tags == nil {
		return nil
	}
	copy := make(map[string]string, len(tags))
	for key, value := range tags {
		copy[key] = value
	}
	return copy
}

func readCLIDeploymentDocument(inline, path string) ([]byte, error) {
	if (inline == "") == (path == "") {
		return nil, ErrInvalidCLIRequest
	}
	if inline != "" {
		if len(inline) > deployment.MaxDocumentBytes {
			return nil, deployment.ErrDocumentTooLarge
		}
		return []byte(inline), nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, ErrInvalidCLIRequest
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, deployment.MaxDocumentBytes+1))
	if err != nil {
		return nil, ErrInvalidCLIRequest
	}
	if len(data) > deployment.MaxDocumentBytes {
		return nil, deployment.ErrDocumentTooLarge
	}
	return data, nil
}

func parseCLIParameters(values []string) (map[string]string, error) {
	if len(values) > deployment.MaxParameterDeclarations {
		return nil, ErrInvalidCLIParameters
	}
	parameters := make(map[string]string, len(values))
	for _, value := range values {
		name, parameterValue, ok := strings.Cut(value, "=")
		name = strings.TrimSpace(name)
		if !ok || name == "" || len(name) > deployment.MaxParameterNameLength || len(parameterValue) > deployment.MaxParameterValueLength {
			return nil, ErrInvalidCLIParameters
		}
		if _, exists := parameters[name]; exists {
			return nil, ErrInvalidCLIParameters
		}
		parameters[name] = parameterValue
	}
	return parameters, nil
}
