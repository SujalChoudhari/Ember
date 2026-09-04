package deployment

import (
	"errors"
	"sort"
	"strings"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const (
	// MaxPlanResources bounds both sides of a comparison. A plan therefore has
	// at most twice the document resource bound worth of changes.
	MaxPlanResources = MaxResourceDeclarations
)

var (
	// ErrInvalidPlan identifies an invalid desired or observed planning input.
	ErrInvalidPlan = errors.New("invalid deployment plan")
	// ErrPlanTooLarge identifies input above the planner's bounded collection
	// limit before any change list is allocated.
	ErrPlanTooLarge = errors.New("deployment plan input exceeds size limit")
)

// PlanAction identifies the operation a later apply boundary would perform.
type PlanAction string

const (
	ActionCreate PlanAction = "create"
	ActionUpdate PlanAction = "update"
	ActionDelete PlanAction = "delete"
	ActionNoOp   PlanAction = "no-op"

	// Compatibility spellings keep the action vocabulary unambiguous for
	// callers while retaining one stable wire value.
	ActionNoop       = ActionNoOp
	PlanActionCreate = ActionCreate
	PlanActionUpdate = ActionUpdate
	PlanActionDelete = ActionDelete
	PlanActionNoOp   = ActionNoOp
	PlanActionNoop   = ActionNoOp
)

// ChangeAction is an alternate name for the plan action type.
type ChangeAction = PlanAction

// PlanReason explains why a change is present without carrying resource values.
type PlanReason string

const (
	ReasonMissing       PlanReason = "missing"
	ReasonSpecDrift     PlanReason = "spec_drift"
	ReasonObservedDrift PlanReason = "observed_state_drift"
	ReasonOrphaned      PlanReason = "orphaned"
	ReasonConverged     PlanReason = "converged"
)

// PlanDrift identifies a changed field without exposing either side's value.
type PlanDrift struct {
	Field string `json:"field"`
}

// PlanChange is one deterministic desired-versus-observed outcome. LogicalID
// is stable across applies; ResourceID is the opaque persisted identity and is
// retained for existing resources so a normal update does not replace them.
// Desired is always a defensive copy for create, update, and no-op changes.
type PlanChange struct {
	Action        PlanAction           `json:"action"`
	LogicalID     string               `json:"logicalId"`
	ResourceID    string               `json:"resourceId,omitempty"`
	Desired       *models.ResourceSpec `json:"desired,omitempty"`
	ObservedState models.ResourceState `json:"observedState,omitempty"`
	Drift         []PlanDrift          `json:"drift,omitempty"`
	Reason        PlanReason           `json:"reason"`
	Reasons       []PlanReason         `json:"reasons,omitempty"`
}

// Change is a concise compatibility name for PlanChange.
type Change = PlanChange

// PlanSummary counts every change, including no-op entries. Keeping no-op
// entries in the plan makes convergence and repeated planning inspectable.
type PlanSummary struct {
	Create int `json:"create"`
	Update int `json:"update"`
	Delete int `json:"delete"`
	NoOp   int `json:"noOp"`
}

// DeploymentPlan is a bounded, deterministic diff for one resolved document.
// It contains no private Resolution secret map and no observed resource payload;
// only safe desired specs and observed states are carried to the next boundary.
type DeploymentPlan struct {
	Version string       `json:"version"`
	Changes []PlanChange `json:"changes"`
	Summary PlanSummary  `json:"summary"`
}

// Plan is the pure planning entry point.
func Plan(document ResolvedDocument, observed []models.Resource) (DeploymentPlan, error) {
	return BuildPlan(document, observed)
}

// BuildPlan compares a resolved desired document with observed resources. It
// validates the dependency graph first, matches existing resources by stable
// type/name identity, normalizes opaque parent IDs for comparison, and never
// mutates Ember state.
func BuildPlan(document ResolvedDocument, observed []models.Resource) (DeploymentPlan, error) {
	if len(document.Resources) > MaxPlanResources {
		return DeploymentPlan{}, newPlanError(
			[]Diagnostic{{
				Path:    "$.resources",
				Code:    "too_many_items",
				Message: "planning input exceeds the configured limit",
			}},
			ErrPlanTooLarge,
		)
	}
	if len(observed) > MaxPlanResources {
		return DeploymentPlan{}, newPlanError(
			[]Diagnostic{{
				Path:    "$.observed",
				Code:    "too_many_items",
				Message: "planning input exceeds the configured limit",
			}},
			ErrPlanTooLarge,
		)
	}

	normalizedDocument := clonePlanDocument(document)
	if _, err := BuildDependencyGraph(normalizedDocument); err != nil {
		return DeploymentPlan{}, planErrorFromDependency(err)
	}

	observedIndex, err := indexObservedResources(observed)
	if err != nil {
		return DeploymentPlan{}, err
	}

	desiredByID := make(map[string]ResolvedResource, len(normalizedDocument.Resources))
	for _, resource := range normalizedDocument.Resources {
		desiredByID[resource.ID] = resource
	}
	desiredIDs := make([]string, 0, len(desiredByID))
	for id := range desiredByID {
		desiredIDs = append(desiredIDs, id)
	}
	sort.Strings(desiredIDs)

	changes := make([]PlanChange, 0, len(normalizedDocument.Resources)+len(observed))
	matchedObserved := make(map[string]struct{}, len(observed))
	for _, logicalID := range desiredIDs {
		desired := desiredByID[logicalID]
		candidate, found, selectionErr := selectObservedResource(desired, observedIndex)
		if selectionErr != nil {
			return DeploymentPlan{}, selectionErr
		}
		if !found {
			changes = append(changes, newPlanChange(
				ActionCreate,
				logicalID,
				"",
				&desired.Spec,
				"",
				nil,
				ReasonMissing,
			))
			continue
		}

		matchedObserved[candidate.resource.ID] = struct{}{}
		drift := comparePlanResource(desired.Spec, candidate.resource, observedIndex)
		if len(drift) == 0 {
			changes = append(changes, newPlanChange(
				ActionNoOp,
				logicalID,
				candidate.resource.ID,
				&desired.Spec,
				candidate.resource.ObservedState,
				nil,
				ReasonConverged,
			))
			continue
		}

		reason, reasons := classifyPlanDrift(drift)
		changes = append(changes, newPlanChangeWithReasons(
			ActionUpdate,
			logicalID,
			candidate.resource.ID,
			&desired.Spec,
			candidate.resource.ObservedState,
			drift,
			reason,
			reasons,
		))
	}

	observedIDs := make([]string, 0, len(observedIndex.byID))
	for resourceID := range observedIndex.byID {
		observedIDs = append(observedIDs, resourceID)
	}
	sort.Strings(observedIDs)
	for _, resourceID := range observedIDs {
		candidate := observedIndex.byID[resourceID]
		if _, matched := matchedObserved[resourceID]; matched {
			continue
		}
		changes = append(changes, newPlanChange(
			ActionDelete,
			candidate.logicalID,
			candidate.resource.ID,
			nil,
			candidate.resource.ObservedState,
			nil,
			ReasonOrphaned,
		))
	}

	sort.Slice(changes, func(left, right int) bool {
		if changes[left].LogicalID == changes[right].LogicalID {
			if changes[left].ResourceID == changes[right].ResourceID {
				return changes[left].Action < changes[right].Action
			}
			return changes[left].ResourceID < changes[right].ResourceID
		}
		return changes[left].LogicalID < changes[right].LogicalID
	})

	plan := DeploymentPlan{
		Version: normalizedDocument.Version,
		Changes: changes,
	}
	for _, change := range changes {
		switch change.Action {
		case ActionCreate:
			plan.Summary.Create++
		case ActionUpdate:
			plan.Summary.Update++
		case ActionDelete:
			plan.Summary.Delete++
		case ActionNoOp:
			plan.Summary.NoOp++
		}
	}
	return plan, nil
}

// BuildDeploymentPlan is an explicit long-form alias for BuildPlan.
func BuildDeploymentPlan(document ResolvedDocument, observed []models.Resource) (DeploymentPlan, error) {
	return BuildPlan(document, observed)
}

// ComputePlan is a descriptive alias for callers that treat planning as a
// pure computation.
func ComputePlan(document ResolvedDocument, observed []models.Resource) (DeploymentPlan, error) {
	return BuildPlan(document, observed)
}

// Diff is a concise alias for the desired-versus-observed comparison.
func Diff(document ResolvedDocument, observed []models.Resource) (DeploymentPlan, error) {
	return BuildPlan(document, observed)
}

// PlanResources is retained as a resource-oriented alias for the same bounded
// comparison; it does not perform any resource mutation.
func PlanResources(document ResolvedDocument, observed []models.Resource) (DeploymentPlan, error) {
	return BuildPlan(document, observed)
}

// PlanError contains bounded, value-free planning diagnostics.
type PlanError struct {
	Diagnostics []Diagnostic
	cause       error
}

func (err *PlanError) Error() string {
	if err == nil || len(err.Diagnostics) == 0 {
		return ErrInvalidPlan.Error()
	}
	parts := make([]string, 0, len(err.Diagnostics))
	for _, diagnostic := range err.Diagnostics {
		parts = append(parts, diagnostic.Path+" ["+diagnostic.Code+"]: "+diagnostic.Message)
	}
	return ErrInvalidPlan.Error() + ": " + strings.Join(parts, "; ")
}

func (err *PlanError) Unwrap() error {
	if err == nil {
		return nil
	}
	if err.cause != nil {
		return err.cause
	}
	return ErrInvalidPlan
}

func (err *PlanError) Is(target error) bool {
	if err == nil {
		return false
	}
	if target == ErrInvalidPlan {
		return true
	}
	return err.cause != nil && errors.Is(err.cause, target)
}

type indexedPlanResource struct {
	resource  models.Resource
	logicalID string
}

type observedPlanIndex struct {
	byID        map[string]indexedPlanResource
	byLogicalID map[string][]indexedPlanResource
}

func indexObservedResources(observed []models.Resource) (observedPlanIndex, error) {
	index := observedPlanIndex{
		byID:        make(map[string]indexedPlanResource, len(observed)),
		byLogicalID: make(map[string][]indexedPlanResource, len(observed)),
	}
	diagnostics := make([]Diagnostic, 0)
	for resourceIndex, resource := range observed {
		if err := resource.Validate(); err != nil {
			diagnostics = append(diagnostics, Diagnostic{
				Path:    observedResourcePath(resourceIndex),
				Code:    "invalid_observed_resource",
				Message: "observed resource is not valid",
			})
			continue
		}
		if _, exists := index.byID[resource.ID]; exists {
			diagnostics = append(diagnostics, Diagnostic{
				Path:    observedResourcePath(resourceIndex) + ".id",
				Code:    "duplicate_observed_id",
				Message: "observed resource ID is declared more than once",
			})
			continue
		}

		candidate := indexedPlanResource{
			resource:  clonePlanResource(resource),
			logicalID: logicalResourceID(resource.Spec.Type, resource.Spec.Name),
		}
		index.byID[resource.ID] = candidate
		index.byLogicalID[candidate.logicalID] = append(index.byLogicalID[candidate.logicalID], candidate)
	}
	if len(diagnostics) > 0 {
		return observedPlanIndex{}, newPlanError(diagnostics, nil)
	}
	for logicalID := range index.byLogicalID {
		sort.Slice(index.byLogicalID[logicalID], func(left, right int) bool {
			return index.byLogicalID[logicalID][left].resource.ID < index.byLogicalID[logicalID][right].resource.ID
		})
	}
	return index, nil
}

func selectObservedResource(desired ResolvedResource, index observedPlanIndex) (indexedPlanResource, bool, error) {
	candidates := index.byLogicalID[desired.ID]
	if len(candidates) == 0 {
		return indexedPlanResource{}, false, nil
	}
	if len(candidates) == 1 {
		return candidates[0], true, nil
	}

	matchingParents := make([]indexedPlanResource, 0, len(candidates))
	for _, candidate := range candidates {
		if normalizeObservedParentID(candidate.resource.Spec.ParentID, index) == desired.Spec.ParentID {
			matchingParents = append(matchingParents, candidate)
		}
	}
	if len(matchingParents) == 1 {
		return matchingParents[0], true, nil
	}
	return indexedPlanResource{}, false, newPlanError([]Diagnostic{{
		Path:    "$.resources",
		Code:    "ambiguous_observed_identity",
		Message: "multiple observed resources match the desired identity",
	}}, nil)
}

func comparePlanResource(desired models.ResourceSpec, observed models.Resource, index observedPlanIndex) []PlanDrift {
	drift := make([]PlanDrift, 0, 5)
	if desired.Type != observed.Spec.Type || desired.Name != observed.Spec.Name {
		drift = append(drift, PlanDrift{Field: "spec.identity"})
	}
	if desired.ParentID != normalizeObservedParentID(observed.Spec.ParentID, index) {
		drift = append(drift, PlanDrift{Field: "spec.parentId"})
	}
	if !equalPlanTags(desired.Tags, observed.Spec.Tags) {
		drift = append(drift, PlanDrift{Field: "spec.tags"})
	}
	if desired.Provider != observed.Spec.Provider {
		drift = append(drift, PlanDrift{Field: "spec.provider"})
	}
	if desired.DesiredState != observed.Spec.DesiredState {
		drift = append(drift, PlanDrift{Field: "spec.desiredState"})
	}
	if desired.DesiredState != "" && observed.ObservedState != desired.DesiredState {
		drift = append(drift, PlanDrift{Field: "observedState"})
	}
	sort.Slice(drift, func(left, right int) bool {
		return drift[left].Field < drift[right].Field
	})
	return drift
}

func classifyPlanDrift(drift []PlanDrift) (PlanReason, []PlanReason) {
	hasSpecDrift := false
	hasObservedDrift := false
	for _, field := range drift {
		if field.Field == "observedState" {
			hasObservedDrift = true
		} else {
			hasSpecDrift = true
		}
	}
	switch {
	case hasSpecDrift && hasObservedDrift:
		return ReasonSpecDrift, []PlanReason{ReasonSpecDrift, ReasonObservedDrift}
	case hasSpecDrift:
		return ReasonSpecDrift, []PlanReason{ReasonSpecDrift}
	default:
		return ReasonObservedDrift, []PlanReason{ReasonObservedDrift}
	}
}

func normalizeObservedParentID(parentID string, index observedPlanIndex) string {
	if parentID == "" {
		return ""
	}
	if parent, exists := index.byID[parentID]; exists {
		return parent.logicalID
	}
	return parentID
}

func equalPlanTags(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, leftValue := range left {
		if rightValue, exists := right[key]; !exists || rightValue != leftValue {
			return false
		}
	}
	return true
}

func newPlanChange(action PlanAction, logicalID, resourceID string, desired *models.ResourceSpec, observedState models.ResourceState, drift []PlanDrift, reason PlanReason) PlanChange {
	return newPlanChangeWithReasons(action, logicalID, resourceID, desired, observedState, drift, reason, []PlanReason{reason})
}

func newPlanChangeWithReasons(action PlanAction, logicalID, resourceID string, desired *models.ResourceSpec, observedState models.ResourceState, drift []PlanDrift, reason PlanReason, reasons []PlanReason) PlanChange {
	change := PlanChange{
		Action:        action,
		LogicalID:     logicalID,
		ResourceID:    resourceID,
		ObservedState: observedState,
		Reason:        reason,
	}
	if desired != nil {
		copy := clonePlanSpec(*desired)
		change.Desired = &copy
	}
	if len(drift) > 0 {
		change.Drift = append([]PlanDrift(nil), drift...)
	}
	if len(reasons) > 0 {
		change.Reasons = append([]PlanReason(nil), reasons...)
	}
	return change
}

func clonePlanDocument(document ResolvedDocument) ResolvedDocument {
	clone := document
	if document.Parameters != nil {
		clone.Parameters = make(map[string]ResolvedParameter, len(document.Parameters))
		for name, parameter := range document.Parameters {
			clone.Parameters[name] = parameter
		}
	}
	if document.Resources != nil {
		clone.Resources = make([]ResolvedResource, len(document.Resources))
		for index, resource := range document.Resources {
			clone.Resources[index] = cloneResolvedResource(resource)
			if clone.Resources[index].ID == "" {
				clone.Resources[index].ID = logicalResourceID(resource.Spec.Type, resource.Spec.Name)
			}
		}
	}
	return clone
}

func cloneResolvedResource(resource ResolvedResource) ResolvedResource {
	resource.Spec = clonePlanSpec(resource.Spec)
	return resource
}

func clonePlanResource(resource models.Resource) models.Resource {
	resource.Spec = clonePlanSpec(resource.Spec)
	return resource
}

func clonePlanSpec(spec models.ResourceSpec) models.ResourceSpec {
	if spec.Tags != nil {
		tags := spec.Tags
		spec.Tags = make(map[string]string, len(tags))
		for key, value := range tags {
			spec.Tags[key] = value
		}
	}
	return spec
}

func observedResourcePath(index int) string {
	return "$.observed[" + integerString(index) + "]"
}

func integerString(value int) string {
	if value == 0 {
		return "0"
	}
	var digits [20]byte
	position := len(digits)
	for value > 0 {
		position--
		digits[position] = byte('0' + value%10)
		value /= 10
	}
	return string(digits[position:])
}

func newPlanError(diagnostics []Diagnostic, cause error) *PlanError {
	return &PlanError{
		Diagnostics: append([]Diagnostic(nil), diagnostics...),
		cause:       cause,
	}
}

func planErrorFromDependency(err error) *PlanError {
	var graphErr *DependencyGraphError
	if errors.As(err, &graphErr) {
		return newPlanError(graphErr.Diagnostics, err)
	}
	return newPlanError([]Diagnostic{{
		Path:    "$.resources",
		Code:    "invalid_dependency_graph",
		Message: "dependency graph is not valid",
	}}, err)
}
