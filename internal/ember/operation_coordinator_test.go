package ember

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

func TestResourceOperationCoordinatorExecutesEffectOnceAndPersistsAudit(t *testing.T) {
	operationPath := filepath.Join(t.TempDir(), "operations.json")
	auditPath := filepath.Join(t.TempDir(), "audit.json")
	operations, err := persistence.NewFileOperationStore(operationPath)
	if err != nil {
		t.Fatalf("NewFileOperationStore() error = %v", err)
	}
	audits, err := persistence.NewFileAuditStore(auditPath)
	if err != nil {
		t.Fatalf("NewFileAuditStore() error = %v", err)
	}
	coordinator, err := NewResourceOperationCoordinator(operations, audits)
	if err != nil {
		t.Fatalf("NewResourceOperationCoordinator() error = %v", err)
	}

	request := ResourceOperationRequest{
		ResourceID:    "resource-1",
		ScopeID:       "scope-1",
		Action:        "resource.update",
		RequestID:     "request-1",
		CorrelationID: "correlation-1",
	}
	calls := 0
	effect := func(context.Context) error {
		calls++
		return nil
	}
	first, err := coordinator.Execute(context.Background(), request, effect)
	if err != nil {
		t.Fatalf("first Execute() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("effect calls after first Execute() = %d, want 1", calls)
	}
	if first.Operation.Status != models.OperationStatusSucceeded || first.Operation.ResourceID != request.ResourceID {
		t.Fatalf("first operation = %#v, want successful resource operation", first.Operation)
	}

	history, err := coordinator.ListAuditHistory(context.Background(), request.ResourceID, persistence.MaxAuditListLimit)
	if err != nil {
		t.Fatalf("ListAuditHistory() error = %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("audit history length = %d, want 1", len(history))
	}
	if history[0].OperationID != first.Operation.ID || history[0].RequestID != request.RequestID || history[0].CorrelationID != request.CorrelationID {
		t.Fatalf("audit entry = %#v, want operation/request/correlation linkage", history[0])
	}

	reopenedOperations, err := persistence.NewFileOperationStore(operationPath)
	if err != nil {
		t.Fatalf("NewFileOperationStore(reopen) error = %v", err)
	}
	reopenedAudits, err := persistence.NewFileAuditStore(auditPath)
	if err != nil {
		t.Fatalf("NewFileAuditStore(reopen) error = %v", err)
	}
	reopened, err := NewResourceOperationCoordinator(reopenedOperations, reopenedAudits)
	if err != nil {
		t.Fatalf("NewResourceOperationCoordinator(reopen) error = %v", err)
	}
	second, err := reopened.Execute(context.Background(), request, func(context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("replayed Execute() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("effect calls after replay = %d, want no duplicate side effect", calls)
	}
	if !second.Replayed || second.Operation.ID != first.Operation.ID {
		t.Fatalf("replayed result = %#v, want original operation", second)
	}
}

func TestResourceOperationCoordinatorExposesOperationInspectionAndBoundedHistory(t *testing.T) {
	operations, err := persistence.NewFileOperationStore(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatalf("NewFileOperationStore() error = %v", err)
	}
	audits, err := persistence.NewFileAuditStore(filepath.Join(t.TempDir(), "audit.json"))
	if err != nil {
		t.Fatalf("NewFileAuditStore() error = %v", err)
	}
	coordinator, err := NewResourceOperationCoordinator(operations, audits)
	if err != nil {
		t.Fatalf("NewResourceOperationCoordinator() error = %v", err)
	}
	var plane ResourceOperationControlPlane = coordinator

	request := ResourceOperationRequest{
		ResourceID:    "resource-1",
		ScopeID:       "scope-1",
		Action:        "resource.update",
		RequestID:     "request-1",
		CorrelationID: "correlation-1",
	}
	first, err := plane.Execute(context.Background(), request, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	secondRequest := request
	secondRequest.RequestID = "request-2"
	secondRequest.CorrelationID = "correlation-2"
	second, err := plane.Execute(context.Background(), secondRequest, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("second Execute() error = %v", err)
	}

	byID, err := plane.GetOperation(context.Background(), first.Operation.ID)
	if err != nil {
		t.Fatalf("GetOperation() error = %v", err)
	}
	if byID.ID != first.Operation.ID {
		t.Fatalf("GetOperation() ID = %q, want %q", byID.ID, first.Operation.ID)
	}
	byRequest, err := plane.GetOperationByRequestID(context.Background(), secondRequest.RequestID)
	if err != nil {
		t.Fatalf("GetOperationByRequestID() error = %v", err)
	}
	if byRequest.ID != second.Operation.ID {
		t.Fatalf("GetOperationByRequestID() ID = %q, want %q", byRequest.ID, second.Operation.ID)
	}
	listed, err := plane.ListOperations(context.Background(), request.ResourceID, 1)
	if err != nil {
		t.Fatalf("ListOperations() error = %v", err)
	}
	if len(listed) != 1 || listed[0].ID != first.Operation.ID {
		t.Fatalf("ListOperations() = %#v, want the bounded oldest operation", listed)
	}
}

func TestResourceOperationCoordinatorRecordsFailedEffectWithoutPersistingError(t *testing.T) {
	operationPath := filepath.Join(t.TempDir(), "operations.json")
	auditPath := filepath.Join(t.TempDir(), "audit.json")
	operations, err := persistence.NewFileOperationStore(operationPath)
	if err != nil {
		t.Fatalf("NewFileOperationStore() error = %v", err)
	}
	audits, err := persistence.NewFileAuditStore(auditPath)
	if err != nil {
		t.Fatalf("NewFileAuditStore() error = %v", err)
	}
	coordinator, err := NewResourceOperationCoordinator(operations, audits)
	if err != nil {
		t.Fatalf("NewResourceOperationCoordinator() error = %v", err)
	}
	request := ResourceOperationRequest{
		ResourceID:    "resource-1",
		ScopeID:       "scope-1",
		Action:        "resource.update",
		RequestID:     "request-failure",
		CorrelationID: "correlation-failure",
	}
	calls := 0
	result, err := coordinator.Execute(context.Background(), request, func(context.Context) error {
		calls++
		return errors.New("secret-value-must-not-be-persisted")
	})
	if !errors.Is(err, ErrResourceOperationFailed) {
		t.Fatalf("failed Execute() error = %v, want ErrResourceOperationFailed", err)
	}
	if result == nil || result.Operation.Status != models.OperationStatusFailed || result.Operation.Outcome != "failed" {
		t.Fatalf("failed result = %#v, want bounded failed operation", result)
	}
	if calls != 1 {
		t.Fatalf("effect calls after failed Execute() = %d, want 1", calls)
	}
	stored, err := coordinator.GetOperation(context.Background(), result.Operation.ID)
	if err != nil {
		t.Fatalf("GetOperation(failed) error = %v", err)
	}
	if stored.Outcome != "failed" {
		t.Fatalf("stored failed outcome = %q, want failed", stored.Outcome)
	}
	history, err := coordinator.ListAuditHistory(context.Background(), request.ResourceID, persistence.MaxAuditListLimit)
	if err != nil {
		t.Fatalf("ListAuditHistory(failed) error = %v", err)
	}
	if len(history) != 1 || history[0].Outcome != "failed" || history[0].OperationID != result.Operation.ID {
		t.Fatalf("failed audit history = %#v, want linked failed entry", history)
	}

	operationData, err := os.ReadFile(operationPath)
	if err != nil {
		t.Fatalf("ReadFile(operation snapshot) error = %v", err)
	}
	auditData, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("ReadFile(audit snapshot) error = %v", err)
	}
	for name, data := range map[string][]byte{"operation": operationData, "audit": auditData} {
		if len(data) == 0 || bytes.Contains(data, []byte("secret-value-must-not-be-persisted")) {
			t.Fatalf("%s snapshot contains the effect error", name)
		}
	}

	replayed, err := coordinator.Execute(context.Background(), request, func(context.Context) error {
		calls++
		return nil
	})
	if !errors.Is(err, ErrResourceOperationFailed) {
		t.Fatalf("replayed failed Execute() error = %v, want ErrResourceOperationFailed", err)
	}
	if replayed == nil || !replayed.Replayed || replayed.Operation.ID != result.Operation.ID {
		t.Fatalf("replayed failed result = %#v, want original operation", replayed)
	}
	if calls != 1 {
		t.Fatalf("effect calls after failed replay = %d, want 1", calls)
	}
}

func TestResourceOperationCoordinatorCorrelatesEffectWithResourceState(t *testing.T) {
	resourceStore, err := persistence.NewFileResourceStore(filepath.Join(t.TempDir(), "resources.json"))
	if err != nil {
		t.Fatalf("NewFileResourceStore() error = %v", err)
	}
	resource, err := resourceStore.Create(context.Background(), models.ResourceSpec{
		Type: models.ResourceTypeGroup,
		Name: "platform",
	})
	if err != nil {
		t.Fatalf("Create(resource) error = %v", err)
	}
	operations, err := persistence.NewFileOperationStore(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatalf("NewFileOperationStore() error = %v", err)
	}
	audits, err := persistence.NewFileAuditStore(filepath.Join(t.TempDir(), "audit.json"))
	if err != nil {
		t.Fatalf("NewFileAuditStore() error = %v", err)
	}
	coordinator, err := NewResourceOperationCoordinator(operations, audits)
	if err != nil {
		t.Fatalf("NewResourceOperationCoordinator() error = %v", err)
	}
	request := ResourceOperationRequest{
		ResourceID:    resource.ID,
		Action:        "resource.update",
		RequestID:     "request-resource-update",
		CorrelationID: "correlation-resource-update",
	}
	result, err := coordinator.Execute(context.Background(), request, func(ctx context.Context) error {
		_, err := resourceStore.UpdateTags(ctx, "", resource.ID, map[string]string{"environment": "test"})
		return err
	})
	if err != nil {
		t.Fatalf("Execute(resource update) error = %v", err)
	}
	updated, err := resourceStore.Get(context.Background(), "", resource.ID)
	if err != nil {
		t.Fatalf("Get(updated resource) error = %v", err)
	}
	if updated.Spec.Tags["environment"] != "test" {
		t.Fatalf("updated resource tags = %#v, want environment=test", updated.Spec.Tags)
	}
	if result.Operation.ResourceID != updated.ID {
		t.Fatalf("operation resource ID = %q, want %q", result.Operation.ResourceID, updated.ID)
	}
	history, err := coordinator.ListAuditHistory(context.Background(), updated.ID, persistence.MaxAuditListLimit)
	if err != nil {
		t.Fatalf("ListAuditHistory(resource update) error = %v", err)
	}
	if len(history) != 1 || history[0].ResourceID != updated.ID || history[0].Action != request.Action {
		t.Fatalf("resource update audit history = %#v, want linked action", history)
	}
}

func TestResourceOperationCoordinatorRepairsAuditAfterTransientWriteFailure(t *testing.T) {
	operations, err := persistence.NewFileOperationStore(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatalf("NewFileOperationStore() error = %v", err)
	}
	innerAudits, err := persistence.NewFileAuditStore(filepath.Join(t.TempDir(), "audit.json"))
	if err != nil {
		t.Fatalf("NewFileAuditStore() error = %v", err)
	}
	audits := &flakyAuditStore{inner: innerAudits, failNext: true}
	coordinator, err := NewResourceOperationCoordinator(operations, audits)
	if err != nil {
		t.Fatalf("NewResourceOperationCoordinator() error = %v", err)
	}
	request := ResourceOperationRequest{
		ResourceID:    "resource-1",
		ScopeID:       "scope-1",
		Action:        "resource.update",
		RequestID:     "request-repair",
		CorrelationID: "correlation-repair",
	}
	calls := 0
	first, err := coordinator.Execute(context.Background(), request, func(context.Context) error {
		calls++
		return nil
	})
	if !errors.Is(err, ErrOperationAuditWrite) {
		t.Fatalf("first Execute() error = %v, want ErrOperationAuditWrite", err)
	}
	if first != nil {
		t.Fatalf("first result = %#v, want no result until audit is durable", first)
	}

	replayed, err := coordinator.Execute(context.Background(), request, func(context.Context) error {
		calls++
		return nil
	})
	if err != nil {
		t.Fatalf("repaired replay Execute() error = %v", err)
	}
	if replayed == nil || !replayed.Replayed {
		t.Fatalf("repaired replay result = %#v, want replayed operation", replayed)
	}
	if calls != 1 {
		t.Fatalf("effect calls after audit repair = %d, want 1", calls)
	}
	history, err := coordinator.ListAuditHistory(context.Background(), request.ResourceID, persistence.MaxAuditListLimit)
	if err != nil {
		t.Fatalf("ListAuditHistory(after repair) error = %v", err)
	}
	if len(history) != 1 || history[0].OperationID != replayed.Operation.ID {
		t.Fatalf("repaired audit history = %#v, want one linked entry", history)
	}
}

type flakyAuditStore struct {
	inner    persistence.AuditStore
	failNext bool
}

func (store *flakyAuditStore) Append(ctx context.Context, entry models.AuditEntry) error {
	if store.failNext {
		store.failNext = false
		return errors.New("temporary audit failure")
	}
	return store.inner.Append(ctx, entry)
}

func (store *flakyAuditStore) List(ctx context.Context, resourceID string, limit int) ([]models.AuditEntry, error) {
	return store.inner.List(ctx, resourceID, limit)
}

var _ persistence.AuditStore = (*flakyAuditStore)(nil)

func TestResourceOperationCoordinatorRejectsInvalidRequestsBeforeEffect(t *testing.T) {
	operations, err := persistence.NewFileOperationStore(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatalf("NewFileOperationStore() error = %v", err)
	}
	audits, err := persistence.NewFileAuditStore(filepath.Join(t.TempDir(), "audit.json"))
	if err != nil {
		t.Fatalf("NewFileAuditStore() error = %v", err)
	}
	coordinator, err := NewResourceOperationCoordinator(operations, audits)
	if err != nil {
		t.Fatalf("NewResourceOperationCoordinator() error = %v", err)
	}
	valid := ResourceOperationRequest{
		ResourceID:    "resource-1",
		ScopeID:       "scope-1",
		Action:        "resource.update",
		RequestID:     "request-valid",
		CorrelationID: "correlation-valid",
	}
	invalidRequests := []ResourceOperationRequest{
		{ResourceID: valid.ResourceID, ScopeID: valid.ScopeID, Action: valid.Action, CorrelationID: valid.CorrelationID},
		{ResourceID: valid.ResourceID, ScopeID: valid.ScopeID, Action: " ", RequestID: valid.RequestID, CorrelationID: valid.CorrelationID},
		{ResourceID: valid.ResourceID, ScopeID: valid.ScopeID, Action: valid.Action, RequestID: valid.RequestID, CorrelationID: " "},
	}
	calls := 0
	for _, request := range invalidRequests {
		result, err := coordinator.Execute(context.Background(), request, func(context.Context) error {
			calls++
			return nil
		})
		if !errors.Is(err, ErrInvalidResourceOperationRequest) {
			t.Fatalf("invalid Execute(%#v) error = %v, want ErrInvalidResourceOperationRequest", request, err)
		}
		if result != nil {
			t.Fatalf("invalid Execute(%#v) result = %#v, want nil", request, result)
		}
	}
	if calls != 0 {
		t.Fatalf("effect calls after invalid requests = %d, want 0", calls)
	}
	if _, err := coordinator.Execute(context.Background(), valid, nil); !errors.Is(err, ErrInvalidResourceOperationEffect) {
		t.Fatalf("nil effect Execute() error = %v, want ErrInvalidResourceOperationEffect", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := coordinator.Execute(cancelled, valid, func(context.Context) error {
		calls++
		return nil
	}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Execute() error = %v, want context.Canceled", err)
	}
	if calls != 0 {
		t.Fatalf("effect calls after rejected requests = %d, want 0", calls)
	}
}
