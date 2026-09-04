package deployment

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func TestBuildPlanClassifiesStableChangesAndPreservesResourceIdentity(t *testing.T) {
	platform := resolvedPlanResource(models.ResourceTypeGroup, "platform", "", map[string]string{"environment": "test"}, models.ResourceStateReady)
	assets := resolvedPlanResource(models.ResourceTypeBucket, "assets", platform.ID, map[string]string{"environment": "production"}, models.ResourceStateReady)
	fresh := resolvedPlanResource(models.ResourceTypeBucket, "fresh", platform.ID, map[string]string{"environment": "test"}, models.ResourceStateReady)
	document := ResolvedDocument{
		Version:   CurrentVersion,
		Resources: []ResolvedResource{fresh, assets, platform},
	}

	secretValue := "runtime-secret-" + t.Name()
	observed := []models.Resource{
		observedPlanResource("resource-platform", platform.Spec, models.ResourceStateReady),
		observedPlanResource("resource-assets", models.ResourceSpec{
			Type:         assets.Spec.Type,
			Name:         assets.Spec.Name,
			ParentID:     "resource-platform",
			Tags:         map[string]string{"environment": "dev"},
			Provider:     assets.Spec.Provider,
			DesiredState: assets.Spec.DesiredState,
		}, models.ResourceStateReady),
		observedPlanResource("resource-orphan", models.ResourceSpec{
			Type:         models.ResourceTypeBucket,
			Name:         "orphan",
			ParentID:     "resource-platform",
			Tags:         map[string]string{"sensitive": secretValue},
			DesiredState: models.ResourceStateReady,
		}, models.ResourceStateReady),
	}
	plan, err := BuildPlan(document, observed)
	if err != nil {
		t.Fatalf("BuildPlan() error = %v", err)
	}
	if got, want := plan.Summary, (PlanSummary{Create: 1, Update: 1, Delete: 1, NoOp: 1}); !reflect.DeepEqual(got, want) {
		t.Fatalf("plan summary = %#v, want %#v", got, want)
	}
	if len(plan.Changes) != 4 {
		t.Fatalf("plan changes = %d, want four bounded changes", len(plan.Changes))
	}

	changes := make(map[string]PlanChange, len(plan.Changes))
	for _, change := range plan.Changes {
		changes[change.LogicalID] = change
	}
	assertPlanAction(t, changes, platform.ID, ActionNoOp, "resource-platform")
	assetsChange := assertPlanAction(t, changes, assets.ID, ActionUpdate, "resource-assets")
	if !reflect.DeepEqual(assetsChange.Desired.Tags, assets.Spec.Tags) {
		t.Fatalf("assets desired tags = %#v, want %#v", assetsChange.Desired.Tags, assets.Spec.Tags)
	}
	if !reflect.DeepEqual(assetsChange.Drift, []PlanDrift{{Field: "spec.tags"}}) {
		t.Fatalf("assets drift = %#v, want tag drift", assetsChange.Drift)
	}
	assertPlanAction(t, changes, fresh.ID, ActionCreate, "")
	assertPlanAction(t, changes, logicalResourceID(models.ResourceTypeBucket, "orphan"), ActionDelete, "resource-orphan")

	shuffled := document
	shuffled.Resources = []ResolvedResource{platform, fresh, assets}
	shuffledObserved := []models.Resource{observed[2], observed[1], observed[0]}
	shuffledPlan, err := BuildDeploymentPlan(shuffled, shuffledObserved)
	if err != nil {
		t.Fatalf("BuildDeploymentPlan(shuffled) error = %v", err)
	}
	if !reflect.DeepEqual(plan, shuffledPlan) {
		t.Fatalf("plan changed with declaration order: original = %#v, shuffled = %#v", plan, shuffledPlan)
	}

	encoded, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("json.Marshal(plan) error = %v", err)
	}
	if strings.Contains(string(encoded), secretValue) {
		t.Fatalf("plan JSON exposes observed value: %s", encoded)
	}
}

func TestBuildPlanReturnsNoOpForConvergedStateAndShowsObservedDrift(t *testing.T) {
	platform := resolvedPlanResource(models.ResourceTypeGroup, "platform", "", nil, models.ResourceStateReady)
	assets := resolvedPlanResource(models.ResourceTypeBucket, "assets", platform.ID, nil, models.ResourceStateReady)
	document := ResolvedDocument{Version: CurrentVersion, Resources: []ResolvedResource{platform, assets}}
	converged := []models.Resource{
		observedPlanResource("opaque-platform", platform.Spec, models.ResourceStateReady),
		observedPlanResource("opaque-assets", models.ResourceSpec{
			Type:         assets.Spec.Type,
			Name:         assets.Spec.Name,
			ParentID:     "opaque-platform",
			DesiredState: assets.Spec.DesiredState,
		}, models.ResourceStateReady),
	}

	first, err := BuildPlan(document, converged)
	if err != nil {
		t.Fatalf("BuildPlan(converged) error = %v", err)
	}
	second, err := BuildPlan(document, converged)
	if err != nil {
		t.Fatalf("BuildPlan(repeated) error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated converged plans differ: first = %#v, second = %#v", first, second)
	}
	if first.Summary != (PlanSummary{NoOp: 2}) {
		t.Fatalf("converged summary = %#v, want two no-op changes", first.Summary)
	}
	for _, change := range first.Changes {
		if change.Action != ActionNoOp || change.Reason != ReasonConverged || len(change.Drift) != 0 {
			t.Fatalf("converged change = %#v, want no-op with no drift", change)
		}
	}

	drifted := append([]models.Resource(nil), converged...)
	drifted[1].ObservedState = models.ResourceStateFailed
	driftPlan, err := BuildPlan(document, drifted)
	if err != nil {
		t.Fatalf("BuildPlan(drifted) error = %v", err)
	}
	var driftChange PlanChange
	for _, change := range driftPlan.Changes {
		if change.LogicalID == assets.ID {
			driftChange = change
			break
		}
	}
	if got := driftChange.Action; got != ActionUpdate {
		t.Fatalf("drift action = %q, want update", got)
	}
	if got := driftChange.Drift; !reflect.DeepEqual(got, []PlanDrift{{Field: "observedState"}}) {
		t.Fatalf("drift fields = %#v, want observedState", got)
	}
}

func TestBuildPlanRejectsUnboundedOrInvalidObservedStateWithoutEchoingValues(t *testing.T) {
	secretValue := "runtime-secret-" + t.Name()
	tooMany := make([]models.Resource, MaxPlanResources+1)
	for index := range tooMany {
		tooMany[index] = observedPlanResource("resource-"+string(rune('a'+index%26))+string(rune('0'+index/26)), models.ResourceSpec{
			Type: models.ResourceTypeBucket,
			Name: "object-" + string(rune('a'+index%26)) + string(rune('0'+index/26)),
		}, models.ResourceStateReady)
	}
	_, err := BuildPlan(ResolvedDocument{Version: CurrentVersion}, tooMany)
	if err == nil || !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("BuildPlan(too many observed) error = %v, want ErrInvalidPlan", err)
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Fatalf("BuildPlan() error echoed secret value: %v", err)
	}

	invalid := observedPlanResource("resource-invalid", models.ResourceSpec{Type: models.ResourceTypeBucket, Name: "valid"}, models.ResourceStateReady)
	invalid.Spec.Tags = map[string]string{"secret": secretValue}
	invalid.Spec.Name = " "
	_, err = BuildPlan(ResolvedDocument{Version: CurrentVersion}, []models.Resource{invalid})
	if err == nil || !errors.Is(err, ErrInvalidPlan) {
		t.Fatalf("BuildPlan(invalid observed) error = %v, want ErrInvalidPlan", err)
	}
	if strings.Contains(err.Error(), secretValue) {
		t.Fatalf("BuildPlan() invalid-state error echoed secret value: %v", err)
	}
}

func resolvedPlanResource(resourceType models.ResourceType, name, parentID string, tags map[string]string, state models.ResourceState) ResolvedResource {
	return ResolvedResource{
		ID: logicalResourceID(resourceType, name),
		Spec: models.ResourceSpec{
			Type:         resourceType,
			Name:         name,
			ParentID:     parentID,
			Tags:         tags,
			DesiredState: state,
		},
	}
}

func observedPlanResource(id string, spec models.ResourceSpec, observedState models.ResourceState) models.Resource {
	return models.Resource{ID: id, Spec: spec, ObservedState: observedState}
}

func assertPlanAction(t *testing.T, changes map[string]PlanChange, logicalID string, wantAction PlanAction, wantResourceID string) PlanChange {
	t.Helper()
	change, ok := changes[logicalID]
	if !ok {
		t.Fatalf("plan has no change for %q", logicalID)
	}
	if change.Action != wantAction {
		t.Fatalf("change %q action = %q, want %q", logicalID, change.Action, wantAction)
	}
	if change.ResourceID != wantResourceID {
		t.Fatalf("change %q resource ID = %q, want %q", logicalID, change.ResourceID, wantResourceID)
	}
	return change
}
