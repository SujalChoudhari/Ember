package deployment

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

func TestApplyCreatesInDependencyOrderAndReturnsOperationEvidence(t *testing.T) {
	root := resolvedApplyResource(models.ResourceTypeGroup, "platform", "", nil)
	child := resolvedApplyResource(models.ResourceTypeBucket, "assets", root.ID, map[string]string{"tier": "test"})
	authority := &recordingApplyAuthority{}

	result, err := Apply(context.Background(), ResolvedDocument{
		Version:   CurrentVersion,
		Resources: []ResolvedResource{child, root},
	}, nil, authority, ApplyOptions{RequestID: "request-apply-1", CorrelationID: "correlation-apply-1"})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got, want := result.Plan.Summary, (PlanSummary{Create: 2}); got != want {
		t.Fatalf("plan summary = %#v, want %#v", got, want)
	}
	if got, want := authority.calls, []applyCall{
		{action: ActionCreate, logicalID: root.ID, parentID: "", requestID: "request-apply-1", correlationID: "correlation-apply-1"},
		{action: ActionCreate, logicalID: child.ID, parentID: "actual-" + root.ID, requestID: "request-apply-1", correlationID: "correlation-apply-1"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("authority calls = %#v, want %#v", got, want)
	}
	if len(result.Operations) != 2 || result.Operations[0].CorrelationID != "correlation-apply-1" || result.Operations[1].RequestID != "request-apply-1" {
		t.Fatalf("operations = %#v, want request/correlation evidence", result.Operations)
	}
}

func TestApplyRefusesUnapprovedDestructiveChangesBeforeMutation(t *testing.T) {
	root := resolvedApplyResource(models.ResourceTypeGroup, "platform", "", nil)
	orphan := resolvedApplyResource(models.ResourceTypeBucket, "orphan", root.ID, nil)
	observedRoot := observedApplyResource("actual-root", root.Spec, models.ResourceStateReady)
	observedOrphan := observedApplyResource("actual-orphan", models.ResourceSpec{
		Type:         orphan.Spec.Type,
		Name:         orphan.Spec.Name,
		ParentID:     observedRoot.ID,
		DesiredState: models.ResourceStateReady,
	}, models.ResourceStateReady)
	authority := &recordingApplyAuthority{}

	result, err := Apply(context.Background(), ResolvedDocument{Version: CurrentVersion, Resources: []ResolvedResource{root}}, []models.Resource{observedRoot, observedOrphan}, authority, ApplyOptions{})
	if !errors.Is(err, ErrDestructiveApprovalRequired) {
		t.Fatalf("Apply() error = %v, want ErrDestructiveApprovalRequired", err)
	}
	if len(authority.calls) != 0 {
		t.Fatalf("authority calls = %#v, want no mutation before approval", authority.calls)
	}
	if result == nil || result.Plan.Summary.Delete != 1 {
		t.Fatalf("result = %#v, want preview containing one delete", result)
	}
}

func TestApplyUpdatesTagsAndSkipsConvergedChanges(t *testing.T) {
	root := resolvedApplyResource(models.ResourceTypeGroup, "platform", "", nil)
	child := resolvedApplyResource(models.ResourceTypeBucket, "assets", root.ID, map[string]string{"tier": "production"})
	observedRoot := observedApplyResource("actual-root", root.Spec, models.ResourceStateReady)
	observedChild := observedApplyResource("actual-child", models.ResourceSpec{
		Type:         child.Spec.Type,
		Name:         child.Spec.Name,
		ParentID:     observedRoot.ID,
		Tags:         map[string]string{"tier": "test"},
		DesiredState: models.ResourceStateReady,
	}, models.ResourceStateReady)
	authority := &recordingApplyAuthority{}

	result, err := Apply(context.Background(), ResolvedDocument{Version: CurrentVersion, Resources: []ResolvedResource{child, root}}, []models.Resource{observedChild, observedRoot}, authority, ApplyOptions{
		RequestID:     "request-update-1",
		CorrelationID: "correlation-update-1",
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got, want := result.Plan.Summary, (PlanSummary{Update: 1, NoOp: 1}); got != want {
		t.Fatalf("plan summary = %#v, want %#v", got, want)
	}
	if got, want := authority.calls, []applyCall{{
		action: ActionUpdate, logicalID: child.ID, resourceID: observedChild.ID, parentID: observedRoot.ID,
		requestID: "request-update-1", correlationID: "correlation-update-1",
	}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("authority calls = %#v, want %#v", got, want)
	}
}

func TestApplyDeletesChildrenBeforeParentsWithExplicitApproval(t *testing.T) {
	root := resolvedApplyResource(models.ResourceTypeGroup, "platform", "", nil)
	child := resolvedApplyResource(models.ResourceTypeBucket, "assets", root.ID, nil)
	observedRoot := observedApplyResource("actual-root", root.Spec, models.ResourceStateReady)
	observedChild := observedApplyResource("actual-child", models.ResourceSpec{
		Type:         child.Spec.Type,
		Name:         child.Spec.Name,
		ParentID:     observedRoot.ID,
		DesiredState: models.ResourceStateReady,
	}, models.ResourceStateReady)
	authority := &recordingApplyAuthority{}

	result, err := Apply(context.Background(), ResolvedDocument{Version: CurrentVersion, Resources: nil}, []models.Resource{observedChild, observedRoot}, authority, ApplyOptions{
		RequestID:          "request-delete-1",
		CorrelationID:      "correlation-delete-1",
		ApproveDestructive: true,
	})
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if got, want := authority.calls, []applyCall{
		{action: ActionDelete, logicalID: child.ID, resourceID: observedChild.ID, parentID: observedRoot.ID, requestID: "request-delete-1", correlationID: "correlation-delete-1"},
		{action: ActionDelete, logicalID: root.ID, resourceID: observedRoot.ID, requestID: "request-delete-1", correlationID: "correlation-delete-1"},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("authority calls = %#v, want %#v", got, want)
	}
	if len(result.Operations) != 2 {
		t.Fatalf("operations = %#v, want two delete operations", result.Operations)
	}
}

func TestApplyPropagatesAuthorityFailureWithoutEchoingValues(t *testing.T) {
	secret := "apply-secret-" + t.Name()
	root := resolvedApplyResource(models.ResourceTypeGroup, "platform", "", map[string]string{"secret": secret})
	authority := &recordingApplyAuthority{err: errors.New("provider failed")}

	result, err := Apply(context.Background(), ResolvedDocument{Version: CurrentVersion, Resources: []ResolvedResource{root}}, nil, authority, ApplyOptions{RequestID: "request-failure", CorrelationID: "correlation-failure"})
	if err == nil || !errors.Is(err, authority.err) {
		t.Fatalf("Apply() error = %v, want authority failure", err)
	}
	if result == nil || len(result.Operations) != 0 {
		t.Fatalf("result = %#v, want no completed operations", result)
	}
	if stringsContains(err.Error(), secret) {
		t.Fatalf("Apply() error echoed secret value: %v", err)
	}
}

func TestApplyPersistsPartialFailureAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apply-progress.json")
	store, err := persistence.NewFileApplyProgressStore(path)
	if err != nil {
		t.Fatalf("NewFileApplyProgressStore() error = %v", err)
	}
	root := resolvedApplyResource(models.ResourceTypeGroup, "platform", "", nil)
	child := resolvedApplyResource(models.ResourceTypeBucket, "assets", root.ID, nil)

	_, applyErr := Apply(context.Background(), ResolvedDocument{
		Version:   CurrentVersion,
		Resources: []ResolvedResource{root, child},
	}, nil, &partialFailureApplyAuthority{}, ApplyOptions{
		RequestID:        "request-reopen-1",
		CorrelationID:    "correlation-reopen-1",
		ProgressRecorder: store,
	})
	if applyErr == nil {
		t.Fatal("Apply() error = nil, want injected failure")
	}
	reopened, err := persistence.NewFileApplyProgressStore(path)
	if err != nil {
		t.Fatalf("NewFileApplyProgressStore(reopen) error = %v", err)
	}
	record, err := reopened.Get(context.Background(), "apply-request-reopen-1")
	if err != nil {
		t.Fatalf("Get(reopen) error = %v", err)
	}
	if got, want := progressStatuses(*record), []models.ApplyProgressStatus{models.ApplyProgressSucceeded, models.ApplyProgressFailed}; !reflect.DeepEqual(got, want) {
		t.Fatalf("reopened progress statuses = %#v, want %#v", got, want)
	}
	if record.Entries[1].Failure != models.ApplyProgressFailureAuthority || stringsContains(record.Entries[1].Failure, "secret") {
		t.Fatalf("reopened failure = %q, want bounded redacted classification", record.Entries[1].Failure)
	}
}

type applyCall struct {
	action        PlanAction
	logicalID     string
	resourceID    string
	parentID      string
	requestID     string
	correlationID string
}

type recordingApplyAuthority struct {
	calls []applyCall
	err   error
}

func (authority *recordingApplyAuthority) ApplyResource(_ context.Context, action PlanAction, logicalID, resourceID string, spec *models.ResourceSpec, parentID, requestID, correlationID string) (*models.Resource, *models.Operation, error) {
	call := applyCall{action: action, logicalID: logicalID, resourceID: resourceID, parentID: parentID, requestID: requestID, correlationID: correlationID}
	authority.calls = append(authority.calls, call)
	if authority.err != nil {
		return nil, nil, authority.err
	}
	now := time.Unix(100, int64(len(authority.calls))).UTC()
	operation := &models.Operation{ID: "operation-" + logicalID, ResourceID: logicalID, CorrelationID: correlationID, RequestID: requestID, Status: models.OperationStatusSucceeded, CreatedAt: now, UpdatedAt: now, Outcome: "applied"}
	if action == ActionDelete {
		return nil, operation, nil
	}
	if spec == nil {
		return nil, nil, errors.New("missing spec")
	}
	resourceID = "actual-" + logicalID
	return &models.Resource{ID: resourceID, Spec: *spec, ObservedState: models.ResourceStateReady}, operation, nil
}

func resolvedApplyResource(resourceType models.ResourceType, name, parentID string, tags map[string]string) ResolvedResource {
	return ResolvedResource{ID: logicalResourceID(resourceType, name), Spec: models.ResourceSpec{Type: resourceType, Name: name, ParentID: parentID, Tags: tags, DesiredState: models.ResourceStateReady}}
}

func observedApplyResource(id string, spec models.ResourceSpec, state models.ResourceState) models.Resource {
	return models.Resource{ID: id, Spec: spec, ObservedState: state}
}

func stringsContains(value, fragment string) bool {
	for i := 0; i+len(fragment) <= len(value); i++ {
		if value[i:i+len(fragment)] == fragment {
			return true
		}
	}
	return false
}

func TestApplyRecordsBoundedPartialFailureProgress(t *testing.T) {
	root := resolvedApplyResource(models.ResourceTypeGroup, "platform", "", nil)
	child := resolvedApplyResource(models.ResourceTypeBucket, "assets", root.ID, nil)
	recorder := &recordingApplyProgressRecorder{}
	authority := &partialFailureApplyAuthority{}

	result, err := Apply(context.Background(), ResolvedDocument{
		Version:   CurrentVersion,
		Resources: []ResolvedResource{root, child},
	}, nil, authority, ApplyOptions{
		RequestID:        "request-partial-1",
		CorrelationID:    "correlation-partial-1",
		ProgressRecorder: recorder,
	})
	if err == nil || result == nil {
		t.Fatalf("Apply() = (%#v, %v), want partial failure", result, err)
	}
	if recorder.record.ID != "apply-request-partial-1" || recorder.record.RequestID != "request-partial-1" || recorder.record.CorrelationID != "correlation-partial-1" {
		t.Fatalf("progress identity = %#v, want request/correlation linkage", recorder.record)
	}
	if got, want := progressStatuses(recorder.record), []models.ApplyProgressStatus{
		models.ApplyProgressSucceeded,
		models.ApplyProgressFailed,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("progress statuses = %#v, want %#v", got, want)
	}
	if recorder.record.Entries[1].Failure != "apply authority failure" {
		t.Fatalf("failure evidence = %q, want redacted classification", recorder.record.Entries[1].Failure)
	}
	if len(recorder.record.OperationIDs) != 1 || recorder.record.OperationIDs[0] != "operation-"+root.ID {
		t.Fatalf("operation IDs = %#v, want completed operation only", recorder.record.OperationIDs)
	}
}

type recordingApplyProgressRecorder struct {
	record models.ApplyProgressRecord
}

func (recorder *recordingApplyProgressRecorder) Create(_ context.Context, record models.ApplyProgressRecord) (*models.ApplyProgressRecord, error) {
	recorder.record = record
	return &recorder.record, nil
}

func (recorder *recordingApplyProgressRecorder) Update(_ context.Context, record models.ApplyProgressRecord) error {
	recorder.record = record
	return nil
}

type partialFailureApplyAuthority struct {
	calls int
}

func (authority *partialFailureApplyAuthority) ApplyResource(_ context.Context, action PlanAction, logicalID, resourceID string, spec *models.ResourceSpec, parentID, requestID, correlationID string) (*models.Resource, *models.Operation, error) {
	authority.calls++
	if authority.calls == 2 {
		return nil, nil, errors.New("apply authority failure: secret-must-not-persist")
	}
	now := time.Unix(200, int64(authority.calls)).UTC()
	operation := &models.Operation{ID: "operation-" + logicalID, ResourceID: logicalID, CorrelationID: correlationID, RequestID: requestID, Status: models.OperationStatusSucceeded, CreatedAt: now, UpdatedAt: now, Outcome: "applied"}
	if action == ActionDelete {
		return nil, operation, nil
	}
	return &models.Resource{ID: "actual-" + logicalID, Spec: *spec, ObservedState: models.ResourceStateReady}, operation, nil
}

func progressStatuses(record models.ApplyProgressRecord) []models.ApplyProgressStatus {
	statuses := make([]models.ApplyProgressStatus, len(record.Entries))
	for index, entry := range record.Entries {
		statuses[index] = entry.Status
	}
	return statuses
}
