package ember

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/deployment"
	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

func TestFileOperatorWiresDeploymentInspectionAndIdempotentRollback(t *testing.T) {
	ctx := context.Background()
	operator, err := NewFileOperator(filepath.Join(t.TempDir(), "state"), 64)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	resource, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "platform"})
	if err != nil {
		t.Fatalf("CreateResource() error = %v", err)
	}
	mutation, err := operator.UpdateResourceTags(ctx, OperatorPrincipal{}, resource.ID, map[string]string{"tier": "test"}, "operation-progress", "correlation-progress")
	if err != nil {
		t.Fatalf("UpdateResourceTags() error = %v", err)
	}
	now := time.Unix(1, 0).UTC()
	progress := models.ApplyProgressRecord{
		ID: "apply-recovery-1", RequestID: "apply-request-recovery-1", CorrelationID: "apply-correlation-recovery-1",
		Status: models.ApplyProgressFailed, OperationIDs: []string{mutation.Operation.ID},
		Entries:   []models.ApplyProgressEntry{{LogicalID: "/resources/group/platform", ResourceID: resource.ID, Action: models.ApplyProgressActionCreate, Status: models.ApplyProgressSucceeded, OperationID: mutation.Operation.ID, StartedAt: now, CompletedAt: now.Add(time.Second)}},
		CreatedAt: now, UpdatedAt: now.Add(time.Second),
	}
	if _, err := operator.applyProgress.Create(ctx, progress); err != nil {
		t.Fatalf("ApplyProgress.Create() error = %v", err)
	}

	var output []byte
	response, err := runDeploymentCLI(operator, &output, "deployment", "apply-progress", "get", "--id", progress.ID)
	if err != nil || response.ApplyProgress == nil || response.ApplyProgress.ID != progress.ID {
		t.Fatalf("apply progress inspection = (%#v, %v), want persisted record", response, err)
	}
	response, err = runDeploymentCLI(operator, &output, "deployment", "recovery", "run", "--request-id", "recover-actual-1", "--apply-progress-id", progress.ID, "--action", "rollback")
	if err != nil || response.Recovery == nil || response.Recovery.Outcome != models.RecoveryOutcomeRecovered {
		t.Fatalf("recovery run = (%#v, %v), want recovered outcome", response, err)
	}
	if _, err := operator.GetResource(ctx, OperatorPrincipal{}, resource.ID); !errors.Is(err, persistence.ErrResourceNotFound) {
		t.Fatalf("rolled-back resource lookup error = %v, want resource removed", err)
	}
	response, err = runDeploymentCLI(operator, &output, "deployment", "recovery", "run", "--request-id", "recover-actual-1", "--apply-progress-id", progress.ID, "--action", "rollback")
	if err != nil || !response.Replayed {
		t.Fatalf("replayed recovery = (%#v, %v), want durable idempotent replay", response, err)
	}
	if err := operator.Reset(ctx, OperatorPrincipal{}); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if records, err := operator.ListRecoveries(ctx, OperatorPrincipal{}, 10); err != nil || len(records) != 0 {
		t.Fatalf("recoveries after reset = (%#v, %v), want empty clean state", records, err)
	}
}

func TestCLIExposesBoundedApplyProgressInspection(t *testing.T) {
	operator := newOperatorWithDeploymentStub(t, &deploymentInspectionStub{
		progress: &models.ApplyProgressRecord{
			ID: "apply-1", RequestID: "request-1", CorrelationID: "correlation-1",
			Status:    models.ApplyProgressFailed,
			Entries:   []models.ApplyProgressEntry{{LogicalID: "group/platform", Action: models.ApplyProgressActionCreate, Status: models.ApplyProgressFailed, Failure: models.ApplyProgressFailureAuthority, StartedAt: time.Unix(1, 0).UTC(), CompletedAt: time.Unix(2, 0).UTC()}},
			CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC(),
		},
	})

	var output []byte
	response, err := runDeploymentCLI(operator, &output, "deployment", "apply-progress", "get", "--id", "apply-1")
	if err != nil {
		t.Fatalf("RunCLI() error = %v", err)
	}
	if response.ApplyProgress == nil || response.ApplyProgress.ID != "apply-1" || response.ApplyProgress.Entries[0].Failure != models.ApplyProgressFailureAuthority {
		t.Fatalf("CLI response = %#v, want bounded apply progress", response)
	}
	if string(output) == "" {
		t.Fatal("CLI output is empty")
	}
}

func TestCLIAndHTTPExposeExplicitRecoveryAndBoundedLists(t *testing.T) {
	recovery := &models.RecoveryRecord{ID: "recovery-1", RecoveryRequestID: "recover-1", ApplyProgressID: "apply-1", ApplyRequestID: "request-1", ApplyCorrelationID: "correlation-1", Action: models.RecoveryActionRollback, Status: models.RecoveryStatusSucceeded, Outcome: models.RecoveryOutcomeRecovered, EntryLogicalIDs: []string{"group/platform"}, CompletedEntries: []string{"group/platform"}, CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC()}
	stub := &deploymentInspectionStub{
		progress: &models.ApplyProgressRecord{ID: "apply-1", RequestID: "request-1", CorrelationID: "correlation-1", Status: models.ApplyProgressFailed, Entries: []models.ApplyProgressEntry{{LogicalID: "group/platform", Action: models.ApplyProgressActionCreate, Status: models.ApplyProgressFailed, Failure: models.ApplyProgressFailureAuthority, StartedAt: time.Unix(1, 0).UTC(), CompletedAt: time.Unix(2, 0).UTC()}}, CreatedAt: time.Unix(1, 0).UTC(), UpdatedAt: time.Unix(2, 0).UTC()},
		recovery: recovery, recoveryResult: &deployment.RecoveryResult{Record: *recovery},
	}
	operator := newOperatorWithDeploymentStub(t, stub)

	var output []byte
	response, err := runDeploymentCLI(operator, &output, "deployment", "recovery", "run", "--request-id", "recover-1", "--apply-progress-id", "apply-1", "--action", "rollback")
	if err != nil || response.Recovery == nil || response.Recovery.ID != recovery.ID {
		t.Fatalf("recovery CLI response = (%#v, %v), want bounded recovery", response, err)
	}

	handler := NewHTTPHandler(operator)
	list := httptest.NewRecorder()
	handler.ServeHTTP(list, httptest.NewRequest(http.MethodGet, "/v1/apply-progress?limit=1", nil))
	if list.Code != http.StatusOK || !bytes.Contains(list.Body.Bytes(), []byte(`"applyProgresses"`)) {
		t.Fatalf("apply progress list = %d %s, want bounded list", list.Code, list.Body.String())
	}
	body := bytes.NewBufferString(`{"requestId":"recover-1","applyProgressId":"apply-1","action":"rollback"}`)
	run := httptest.NewRecorder()
	handler.ServeHTTP(run, httptest.NewRequest(http.MethodPost, "/v1/recoveries", body))
	if run.Code != http.StatusOK || !bytes.Contains(run.Body.Bytes(), []byte(`"recovery"`)) {
		t.Fatalf("recovery POST = %d %s, want recovery response", run.Code, run.Body.String())
	}
}

func TestStoreDeploymentControlPlanePreservesScopedRecoveryAcrossRestart(t *testing.T) {
	operator := newTestOperator(t)
	ctx := context.Background()
	resource, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "platform"})
	if err != nil {
		t.Fatalf("CreateResource() error = %v", err)
	}
	mutation, err := operator.UpdateResourceTags(ctx, OperatorPrincipal{}, resource.ID, map[string]string{"tier": "test"}, "request-progress", "correlation-progress")
	if err != nil {
		t.Fatalf("UpdateResourceTags() error = %v", err)
	}
	now := time.Unix(10, 0).UTC()
	progress := models.ApplyProgressRecord{
		ID: "apply-1", RequestID: "apply-request-1", CorrelationID: "apply-correlation-1", Status: models.ApplyProgressFailed,
		OperationIDs: []string{mutation.Operation.ID},
		Entries: []models.ApplyProgressEntry{
			{LogicalID: "group/platform", ResourceID: resource.ID, Action: models.ApplyProgressActionCreate, Status: models.ApplyProgressSucceeded, OperationID: mutation.Operation.ID, StartedAt: now, CompletedAt: now.Add(time.Second)},
			{LogicalID: "bucket/assets", Action: models.ApplyProgressActionCreate, Status: models.ApplyProgressFailed, Failure: models.ApplyProgressFailureAuthority, StartedAt: now, CompletedAt: now.Add(time.Second)},
		},
		CreatedAt: now, UpdatedAt: now.Add(time.Second),
	}
	progressPath := filepath.Join(t.TempDir(), "apply-progress.json")
	progressStore, err := persistence.NewFileApplyProgressStore(progressPath)
	if err != nil {
		t.Fatalf("NewFileApplyProgressStore() error = %v", err)
	}
	if _, err := progressStore.Create(ctx, progress); err != nil {
		t.Fatalf("progress Create() error = %v", err)
	}
	recoveryPath := filepath.Join(t.TempDir(), "recovery.json")
	recoveryStore, err := persistence.NewFileRecoveryStore(recoveryPath)
	if err != nil {
		t.Fatalf("NewFileRecoveryStore() error = %v", err)
	}
	executor := &operatorRecoveryExecutor{}
	authority, err := deployment.NewRecoveryAuthority(progressStore, recoveryStore, executor)
	if err != nil {
		t.Fatalf("NewRecoveryAuthority() error = %v", err)
	}
	control, err := NewStoreDeploymentControlPlane(progressStore, recoveryStore, authority, func(ctx context.Context, principal OperatorPrincipal, resourceID string) error {
		_, _, err := operator.authorizeResource(ctx, principal, resourceID)
		return err
	}, operator.operations)
	if err != nil {
		t.Fatalf("NewStoreDeploymentControlPlane() error = %v", err)
	}
	composed, err := NewOperatorWithDeployment(operator.resources, operator.blobs, operator.operations, operator.reset, control)
	if err != nil {
		t.Fatalf("NewOperatorWithDeployment() error = %v", err)
	}
	if got, err := composed.GetApplyProgress(ctx, OperatorPrincipal{ScopeID: resource.ID}, progress.ID); err != nil || got.Status != models.ApplyProgressFailed {
		t.Fatalf("scoped progress inspection = (%#v, %v), want failed record", got, err)
	}
	if got, err := composed.GetApplyProgressByOperation(ctx, OperatorPrincipal{}, mutation.Operation.ID); err != nil || got.ID != progress.ID {
		t.Fatalf("operation-linked progress inspection = (%#v, %v), want apply record", got, err)
	}
	other, err := composed.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "other"})
	if err != nil {
		t.Fatalf("CreateResource(other) error = %v", err)
	}
	if _, err := composed.GetApplyProgress(ctx, OperatorPrincipal{ScopeID: other.ID}, progress.ID); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("cross-scope progress inspection error = %v, want scope denial", err)
	}
	request := deployment.RecoveryRequest{RequestID: "recover-1", ApplyProgressID: progress.ID, Action: models.RecoveryActionRollback}
	result, err := composed.Recover(ctx, OperatorPrincipal{ScopeID: resource.ID}, request)
	if err != nil || result.Recovery == nil || result.Recovery.Outcome != models.RecoveryOutcomeRecovered || len(executor.calls) != 1 {
		t.Fatalf("scoped recovery = (%#v, %v), want one recovered action", result, err)
	}

	reopenedProgress, err := persistence.NewFileApplyProgressStore(progressPath)
	if err != nil {
		t.Fatalf("reopen progress store error = %v", err)
	}
	reopenedRecovery, err := persistence.NewFileRecoveryStore(recoveryPath)
	if err != nil {
		t.Fatalf("reopen recovery store error = %v", err)
	}
	reopenedAuthority, err := deployment.NewRecoveryAuthority(reopenedProgress, reopenedRecovery, &operatorRecoveryExecutor{})
	if err != nil {
		t.Fatalf("NewRecoveryAuthority(reopen) error = %v", err)
	}
	reopenedControl, err := NewStoreDeploymentControlPlane(reopenedProgress, reopenedRecovery, reopenedAuthority, func(ctx context.Context, principal OperatorPrincipal, resourceID string) error {
		_, _, err := operator.authorizeResource(ctx, principal, resourceID)
		return err
	}, operator.operations)
	if err != nil {
		t.Fatalf("NewStoreDeploymentControlPlane(reopen) error = %v", err)
	}
	reopenedOperator, err := NewOperatorWithDeployment(operator.resources, operator.blobs, operator.operations, operator.reset, reopenedControl)
	if err != nil {
		t.Fatalf("NewOperatorWithDeployment(reopen) error = %v", err)
	}
	if got, err := reopenedOperator.GetRecovery(ctx, OperatorPrincipal{ScopeID: resource.ID}, result.Recovery.ID); err != nil || got.Outcome != models.RecoveryOutcomeRecovered {
		t.Fatalf("reopened recovery inspection = (%#v, %v), want recovered record", got, err)
	}
	replayed, err := reopenedOperator.Recover(ctx, OperatorPrincipal{ScopeID: resource.ID}, request)
	if err != nil || !replayed.Replayed {
		t.Fatalf("reopened recovery replay = (%#v, %v), want replay", replayed, err)
	}
}

type operatorRecoveryExecutor struct{ calls []string }

func (executor *operatorRecoveryExecutor) Rollback(_ context.Context, entry models.ApplyProgressEntry) error {
	executor.calls = append(executor.calls, "rollback:"+entry.LogicalID)
	return nil
}

func (executor *operatorRecoveryExecutor) ForwardRecover(_ context.Context, entry models.ApplyProgressEntry) error {
	executor.calls = append(executor.calls, "forward:"+entry.LogicalID)
	return nil
}

func runDeploymentCLI(operator *Operator, output *[]byte, args ...string) (OperatorResponse, error) {
	var rawOutput deploymentOutputBuffer
	err := RunCLI(context.Background(), operator, args, &rawOutput)
	*output = rawOutput.data
	if err != nil {
		return OperatorResponse{}, err
	}
	var response OperatorResponse
	if err := json.Unmarshal(rawOutput.data, &response); err != nil {
		return OperatorResponse{}, err
	}
	return response, nil
}

type deploymentOutputBuffer struct{ data []byte }

func (buffer *deploymentOutputBuffer) Write(data []byte) (int, error) {
	buffer.data = append(buffer.data, data...)
	return len(data), nil
}

type deploymentInspectionStub struct {
	progress       *models.ApplyProgressRecord
	recovery       *models.RecoveryRecord
	recoveryResult *deployment.RecoveryResult
}

func (stub *deploymentInspectionStub) GetApplyProgress(context.Context, OperatorPrincipal, string) (*models.ApplyProgressRecord, error) {
	return stub.progress, nil
}
func (stub *deploymentInspectionStub) ListApplyProgress(context.Context, OperatorPrincipal, int) ([]models.ApplyProgressRecord, error) {
	return []models.ApplyProgressRecord{*stub.progress}, nil
}
func (stub *deploymentInspectionStub) GetApplyProgressByOperation(context.Context, OperatorPrincipal, string) (*models.ApplyProgressRecord, error) {
	return stub.progress, nil
}
func (stub *deploymentInspectionStub) GetRecovery(context.Context, OperatorPrincipal, string) (*models.RecoveryRecord, error) {
	return stub.recovery, nil
}
func (stub *deploymentInspectionStub) ListRecoveries(context.Context, OperatorPrincipal, int) ([]models.RecoveryRecord, error) {
	return []models.RecoveryRecord{*stub.recovery}, nil
}
func (stub *deploymentInspectionStub) Recover(context.Context, OperatorPrincipal, deployment.RecoveryRequest) (*deployment.RecoveryResult, error) {
	return stub.recoveryResult, nil
}

func newOperatorWithDeploymentStub(t *testing.T, control DeploymentControlPlane) *Operator {
	t.Helper()
	resourceStore, err := persistence.NewFileResourceStore(filepath.Join(t.TempDir(), "resources.json"))
	if err != nil {
		t.Fatalf("NewFileResourceStore() error = %v", err)
	}
	resourceManager, err := NewResourceManager(resourceStore)
	if err != nil {
		t.Fatalf("NewResourceManager() error = %v", err)
	}
	blobStore, err := persistence.NewFileBlobStore(filepath.Join(t.TempDir(), "blobs"), 64)
	if err != nil {
		t.Fatalf("NewFileBlobStore() error = %v", err)
	}
	operationStore, err := persistence.NewFileOperationStore(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatalf("NewFileOperationStore() error = %v", err)
	}
	auditStore, err := persistence.NewFileAuditStore(filepath.Join(t.TempDir(), "audit.json"))
	if err != nil {
		t.Fatalf("NewFileAuditStore() error = %v", err)
	}
	coordinator, err := NewResourceOperationCoordinator(operationStore, auditStore)
	if err != nil {
		t.Fatalf("NewResourceOperationCoordinator() error = %v", err)
	}
	operator, err := NewOperatorWithDeployment(resourceManager, blobStore, coordinator, func(context.Context) error { return nil }, control)
	if err != nil {
		t.Fatalf("NewOperatorWithDeployment() error = %v", err)
	}
	return operator
}
