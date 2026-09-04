package deployment

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sort"
	"strings"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

var (
	// ErrInvalidApplyRequest identifies a malformed apply request or plan.
	ErrInvalidApplyRequest = errors.New("invalid deployment apply request")
	// ErrDestructiveApprovalRequired is returned before mutation when the plan
	// contains a destructive change without explicit approval.
	ErrDestructiveApprovalRequired = errors.New("explicit approval is required for destructive changes")
	// ErrApplyDependency identifies a missing persisted parent identity while
	// applying a plan.
	ErrApplyDependency = errors.New("deployment apply dependency is unavailable")
	// ErrIncompleteApplyResult identifies an authority response that cannot
	// provide the operation evidence required for an applied change.
	ErrIncompleteApplyResult = errors.New("deployment apply returned an incomplete result")
	// ErrApplyIdentifier identifies failure to generate a bounded request ID.
	ErrApplyIdentifier = errors.New("deployment apply identifier generation failed")
)

// ApplyAuthority is the narrow mutation boundary used by Apply. An authority
// owns persistence, provider execution, and operation/audit recording. The
// deployment package only supplies the validated action and safe resolved spec.
type ApplyAuthority interface {
	ApplyResource(ctx context.Context, action PlanAction, logicalID, resourceID string, spec *models.ResourceSpec, parentID, requestID, correlationID string) (*models.Resource, *models.Operation, error)
}

// ApplyOptions controls one bounded plan application. Destructive changes are
// never inferred from the caller's intent; ApproveDestructive must be true.
type ApplyOptions struct {
	RequestID          string
	CorrelationID      string
	ApproveDestructive bool
}

// ApplyResult contains the preview that was applied and operation evidence for
// each successful mutation. No resource payload is copied into the result.
type ApplyResult struct {
	Plan       DeploymentPlan      `json:"plan"`
	Operations []*models.Operation `json:"operations,omitempty"`
}

// Apply builds a fresh plan and applies it through the supplied authority.
// Planning and destructive approval are completed before the first mutation.
func Apply(ctx context.Context, document ResolvedDocument, observed []models.Resource, authority ApplyAuthority, options ApplyOptions) (*ApplyResult, error) {
	if ctx == nil || authority == nil {
		return nil, ErrInvalidApplyRequest
	}
	plan, err := BuildPlan(document, observed)
	if err != nil {
		return nil, err
	}
	return ApplyPlan(ctx, plan, document, observed, authority, options)
}

// ApplyDeploymentPlan is an explicit alias for ApplyPlan.
func ApplyDeploymentPlan(ctx context.Context, plan DeploymentPlan, document ResolvedDocument, observed []models.Resource, authority ApplyAuthority, options ApplyOptions) (*ApplyResult, error) {
	return ApplyPlan(ctx, plan, document, observed, authority, options)
}

// ApplyPlan applies an already-built plan. The document and observed resources
// supply the dependency and stale-resource metadata intentionally omitted from
// the value-safe plan representation.
func ApplyPlan(ctx context.Context, plan DeploymentPlan, document ResolvedDocument, observed []models.Resource, authority ApplyAuthority, options ApplyOptions) (*ApplyResult, error) {
	if ctx == nil || authority == nil {
		return nil, ErrInvalidApplyRequest
	}
	if err := validateApplyPlan(plan, document, observed); err != nil {
		return nil, err
	}
	result := &ApplyResult{Plan: plan}
	if !options.ApproveDestructive && hasDestructiveChange(plan) {
		return result, ErrDestructiveApprovalRequired
	}
	requestID, correlationID, err := applyIdentifiers(options)
	if err != nil {
		return result, err
	}

	observedByID := make(map[string]models.Resource, len(observed))
	actualByLogicalID := make(map[string]string, len(observed)+len(document.Resources))
	for _, resource := range observed {
		observedByID[resource.ID] = resource
		actualByLogicalID[logicalResourceID(resource.Spec.Type, resource.Spec.Name)] = resource.ID
	}
	for _, change := range plan.Changes {
		if change.ResourceID != "" {
			actualByLogicalID[change.LogicalID] = change.ResourceID
		}
	}

	graph, err := BuildDependencyGraph(document)
	if err != nil {
		return result, err
	}
	ordered := orderedApplyChanges(plan, graph, observedByID)
	for _, change := range ordered {
		if change.Action == ActionNoOp {
			continue
		}
		if err := ctx.Err(); err != nil {
			return result, err
		}

		spec, parentID, err := applySpec(change, observedByID, actualByLogicalID)
		if err != nil {
			return result, err
		}
		resource, operation, applyErr := authority.ApplyResource(ctx, change.Action, change.LogicalID, change.ResourceID, spec, parentID, requestID, correlationID)
		if applyErr != nil {
			return result, applyErr
		}
		if operation == nil {
			return result, ErrIncompleteApplyResult
		}
		result.Operations = append(result.Operations, operation)
		if resource != nil {
			actualByLogicalID[change.LogicalID] = resource.ID
		}
		if change.Action == ActionCreate && actualByLogicalID[change.LogicalID] == "" {
			return result, ErrIncompleteApplyResult
		}
	}
	return result, nil
}

// IsDestructiveChange classifies changes whose application may delete or
// replace persisted state. Tag-only and observed-state reconciliation updates
// use the existing safe update boundary and are not destructive.
func IsDestructiveChange(change PlanChange) bool {
	if change.Action == ActionDelete {
		return true
	}
	if change.Action != ActionUpdate {
		return false
	}
	if len(change.Drift) == 0 {
		return true
	}
	for _, drift := range change.Drift {
		if drift.Field != "spec.tags" && drift.Field != "observedState" {
			return true
		}
	}
	return false
}

func hasDestructiveChange(plan DeploymentPlan) bool {
	for _, change := range plan.Changes {
		if IsDestructiveChange(change) {
			return true
		}
	}
	return false
}

func validateApplyPlan(plan DeploymentPlan, document ResolvedDocument, observed []models.Resource) error {
	if plan.Version != CurrentVersion || len(plan.Changes) > MaxPlanResources*2 || len(observed) > MaxPlanResources {
		return ErrInvalidApplyRequest
	}
	if _, err := BuildDependencyGraph(document); err != nil {
		return err
	}
	if _, err := indexObservedResources(observed); err != nil {
		return err
	}
	seen := make(map[string]struct{}, len(plan.Changes))
	for _, change := range plan.Changes {
		if strings.TrimSpace(change.LogicalID) == "" || len(change.LogicalID) > models.MaxResourceIDLength {
			return ErrInvalidApplyRequest
		}
		if _, exists := seen[change.LogicalID]; exists {
			return ErrInvalidApplyRequest
		}
		seen[change.LogicalID] = struct{}{}
		switch change.Action {
		case ActionCreate, ActionUpdate:
			if change.Desired == nil {
				return ErrInvalidApplyRequest
			}
		case ActionDelete:
			if strings.TrimSpace(change.ResourceID) == "" {
				return ErrInvalidApplyRequest
			}
		case ActionNoOp:
		default:
			return ErrInvalidApplyRequest
		}
	}
	return nil
}

func applyIdentifiers(options ApplyOptions) (string, string, error) {
	requestID := strings.TrimSpace(options.RequestID)
	if requestID == "" {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return "", "", ErrApplyIdentifier
		}
		requestID = "apply-" + hex.EncodeToString(raw[:])
	}
	correlationID := strings.TrimSpace(options.CorrelationID)
	if correlationID == "" {
		correlationID = requestID
	}
	if len(requestID) > models.MaxOperationRequestIDLength || len(correlationID) > models.MaxOperationCorrelationIDLength {
		return "", "", ErrInvalidApplyRequest
	}
	return requestID, correlationID, nil
}

func orderedApplyChanges(plan DeploymentPlan, graph DependencyGraph, observedByID map[string]models.Resource) []PlanChange {
	byLogicalID := make(map[string]PlanChange, len(plan.Changes))
	deletes := make([]PlanChange, 0)
	for _, change := range plan.Changes {
		byLogicalID[change.LogicalID] = change
		if change.Action == ActionDelete {
			deletes = append(deletes, change)
		}
	}
	ordered := make([]PlanChange, 0, len(plan.Changes))
	for _, logicalID := range graph.Order {
		if change, exists := byLogicalID[logicalID]; exists && change.Action != ActionDelete {
			ordered = append(ordered, change)
		}
	}
	for _, change := range plan.Changes {
		if change.Action != ActionDelete {
			found := false
			for _, candidate := range ordered {
				if candidate.LogicalID == change.LogicalID {
					found = true
					break
				}
			}
			if !found {
				ordered = append(ordered, change)
			}
		}
	}
	sort.SliceStable(deletes, func(left, right int) bool {
		leftDepth := deleteDepth(deletes[left].ResourceID, observedByID, make(map[string]struct{}))
		rightDepth := deleteDepth(deletes[right].ResourceID, observedByID, make(map[string]struct{}))
		if leftDepth == rightDepth {
			return deletes[left].LogicalID < deletes[right].LogicalID
		}
		return leftDepth > rightDepth
	})
	return append(ordered, deletes...)
}

func deleteDepth(resourceID string, observedByID map[string]models.Resource, visited map[string]struct{}) int {
	resource, exists := observedByID[resourceID]
	if !exists || resource.Spec.ParentID == "" {
		return 0
	}
	if _, seen := visited[resourceID]; seen {
		return 0
	}
	visited[resourceID] = struct{}{}
	return 1 + deleteDepth(resource.Spec.ParentID, observedByID, visited)
}

func applySpec(change PlanChange, observedByID map[string]models.Resource, actualByLogicalID map[string]string) (*models.ResourceSpec, string, error) {
	if change.Action == ActionDelete {
		resource, exists := observedByID[change.ResourceID]
		if !exists {
			return nil, "", ErrInvalidApplyRequest
		}
		spec := cloneApplySpec(resource.Spec)
		return &spec, resource.Spec.ParentID, nil
	}
	if change.Desired == nil {
		return nil, "", ErrInvalidApplyRequest
	}
	spec := cloneApplySpec(*change.Desired)
	parentID := ""
	if spec.ParentID != "" {
		var exists bool
		parentID, exists = actualByLogicalID[spec.ParentID]
		if !exists || parentID == "" {
			return nil, "", ErrApplyDependency
		}
		spec.ParentID = parentID
	}
	return &spec, parentID, nil
}

func cloneApplySpec(spec models.ResourceSpec) models.ResourceSpec {
	if spec.Tags != nil {
		tags := make(map[string]string, len(spec.Tags))
		for key, value := range spec.Tags {
			tags[key] = value
		}
		spec.Tags = tags
	}
	return spec
}
