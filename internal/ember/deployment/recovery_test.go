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

func TestRecoveryAuthorityRollsBackCompletedPrefixAndReplaysAfterReopen(t *testing.T) {
	progressPath := filepath.Join(t.TempDir(), "apply-progress.json")
	recoveryPath := filepath.Join(t.TempDir(), "recovery.json")
	progressStore, err := persistence.NewFileApplyProgressStore(progressPath)
	if err != nil {
		t.Fatalf("NewFileApplyProgressStore() error = %v", err)
	}
	progress := recoveryApplyProgressRecord(models.ApplyProgressFailed)
	if _, err := progressStore.Create(context.Background(), progress); err != nil {
		t.Fatalf("progress Create() error = %v", err)
	}
	recoveryStore, err := persistence.NewFileRecoveryStore(recoveryPath)
	if err != nil {
		t.Fatalf("NewFileRecoveryStore() error = %v", err)
	}
	executor := &recordingRecoveryExecutor{active: map[string]bool{"group/platform": true}}
	authority, err := NewRecoveryAuthority(progressStore, recoveryStore, executor)
	if err != nil {
		t.Fatalf("NewRecoveryAuthority() error = %v", err)
	}
	request := RecoveryRequest{RequestID: "recover-request-1", ApplyProgressID: progress.ID, Action: models.RecoveryActionRollback}
	result, err := authority.Recover(context.Background(), request)
	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if got, want := executor.calls, []string{"rollback:group/platform"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("executor calls = %#v, want %#v", got, want)
	}
	if executor.active["group/platform"] {
		t.Fatal("rollback left the completed resource active")
	}
	if result.Record.Outcome != models.RecoveryOutcomeRecovered || result.Record.ApplyCorrelationID != progress.CorrelationID {
		t.Fatalf("recovery record = %#v, want recovered linked outcome", result.Record)
	}

	reopenedProgress, err := persistence.NewFileApplyProgressStore(progressPath)
	if err != nil {
		t.Fatalf("NewFileApplyProgressStore(reopen) error = %v", err)
	}
	reopenedRecovery, err := persistence.NewFileRecoveryStore(recoveryPath)
	if err != nil {
		t.Fatalf("NewFileRecoveryStore(reopen) error = %v", err)
	}
	reopenedExecutor := &recordingRecoveryExecutor{active: make(map[string]bool)}
	reopenedAuthority, err := NewRecoveryAuthority(reopenedProgress, reopenedRecovery, reopenedExecutor)
	if err != nil {
		t.Fatalf("NewRecoveryAuthority(reopen) error = %v", err)
	}
	replayed, err := reopenedAuthority.Recover(context.Background(), request)
	if err != nil {
		t.Fatalf("Recover(replay) error = %v", err)
	}
	if !replayed.Replayed || !reflect.DeepEqual(replayed.Record, result.Record) {
		t.Fatalf("replay = %#v, want original durable result", replayed)
	}
	if len(reopenedExecutor.calls) != 0 {
		t.Fatalf("replay executor calls = %#v, want no duplicate side effects", reopenedExecutor.calls)
	}
}

func TestRecoveryAuthorityForwardRecoversFailedAndPendingEntries(t *testing.T) {
	progressStore, err := persistence.NewFileApplyProgressStore(filepath.Join(t.TempDir(), "apply-progress.json"))
	if err != nil {
		t.Fatalf("NewFileApplyProgressStore() error = %v", err)
	}
	progress := recoveryApplyProgressRecord(models.ApplyProgressFailed)
	progress.Entries = append(progress.Entries, models.ApplyProgressEntry{LogicalID: "bucket/logs", Action: models.ApplyProgressActionCreate, Status: models.ApplyProgressPending})
	progress.OperationIDs = []string{"operation-platform"}
	if err := progress.Validate(); err != nil {
		t.Fatalf("progress Validate() error = %v", err)
	}
	if _, err := progressStore.Create(context.Background(), progress); err != nil {
		t.Fatalf("progress Create() error = %v", err)
	}
	recoveryStore, err := persistence.NewFileRecoveryStore(filepath.Join(t.TempDir(), "recovery.json"))
	if err != nil {
		t.Fatalf("NewFileRecoveryStore() error = %v", err)
	}
	executor := &recordingRecoveryExecutor{active: make(map[string]bool)}
	authority, err := NewRecoveryAuthority(progressStore, recoveryStore, executor)
	if err != nil {
		t.Fatalf("NewRecoveryAuthority() error = %v", err)
	}
	result, err := authority.Recover(context.Background(), RecoveryRequest{RequestID: "recover-forward-1", ApplyProgressID: progress.ID, Action: models.RecoveryActionForward})
	if err != nil {
		t.Fatalf("Recover(forward) error = %v", err)
	}
	if got, want := executor.calls, []string{"forward:bucket/assets", "forward:bucket/logs"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("executor calls = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(result.Record.CompletedEntries, []string{"bucket/assets", "bucket/logs"}) {
		t.Fatalf("completed entries = %#v, want deterministic forward order", result.Record.CompletedEntries)
	}
	if !executor.active["bucket/assets"] || !executor.active["bucket/logs"] {
		t.Fatalf("forward recovery state = %#v, want both resources active", executor.active)
	}
}

func TestRecoveryAuthorityRejectsUnsupportedFailureAndHandlesAlreadyComplete(t *testing.T) {
	progressStore, err := persistence.NewFileApplyProgressStore(filepath.Join(t.TempDir(), "apply-progress.json"))
	if err != nil {
		t.Fatalf("NewFileApplyProgressStore() error = %v", err)
	}
	unsupported := recoveryApplyProgressRecord(models.ApplyProgressFailed)
	unsupported.Entries[1].Failure = models.ApplyProgressFailurePersistence
	if err := unsupported.Validate(); err != nil {
		t.Fatalf("unsupported progress Validate() error = %v", err)
	}
	if _, err := progressStore.Create(context.Background(), unsupported); err != nil {
		t.Fatalf("progress Create() error = %v", err)
	}
	complete := recoveryApplyProgressRecord(models.ApplyProgressSucceeded)
	complete.ID = "apply-recovery-complete"
	complete.RequestID = "apply-request-recovery-complete"
	if _, err := progressStore.Create(context.Background(), complete); err != nil {
		t.Fatalf("complete progress Create() error = %v", err)
	}
	recoveryStore, err := persistence.NewFileRecoveryStore(filepath.Join(t.TempDir(), "recovery.json"))
	if err != nil {
		t.Fatalf("NewFileRecoveryStore() error = %v", err)
	}
	executor := &recordingRecoveryExecutor{active: make(map[string]bool)}
	authority, err := NewRecoveryAuthority(progressStore, recoveryStore, executor)
	if err != nil {
		t.Fatalf("NewRecoveryAuthority() error = %v", err)
	}
	if _, err := authority.Recover(context.Background(), RecoveryRequest{RequestID: "recover-unsupported", ApplyProgressID: unsupported.ID, Action: models.RecoveryActionRollback}); !errors.Is(err, ErrUnsupportedRecoveryFailure) {
		t.Fatalf("Recover(unsupported) error = %v, want ErrUnsupportedRecoveryFailure", err)
	}
	result, err := authority.Recover(context.Background(), RecoveryRequest{RequestID: "recover-complete", ApplyProgressID: complete.ID, Action: models.RecoveryActionForward})
	if err != nil || result.Record.Outcome != models.RecoveryOutcomeAlreadyComplete {
		t.Fatalf("Recover(complete) = (%#v, %v), want already-complete outcome", result, err)
	}
	if len(executor.calls) != 0 {
		t.Fatalf("complete executor calls = %#v, want none", executor.calls)
	}
}

type recordingRecoveryExecutor struct {
	calls  []string
	active map[string]bool
}

func (executor *recordingRecoveryExecutor) Rollback(_ context.Context, entry models.ApplyProgressEntry) error {
	executor.calls = append(executor.calls, "rollback:"+entry.LogicalID)
	executor.active[entry.LogicalID] = false
	return nil
}

func (executor *recordingRecoveryExecutor) ForwardRecover(_ context.Context, entry models.ApplyProgressEntry) error {
	executor.calls = append(executor.calls, "forward:"+entry.LogicalID)
	executor.active[entry.LogicalID] = true
	return nil
}

func recoveryApplyProgressRecord(status models.ApplyProgressStatus) models.ApplyProgressRecord {
	now := time.Unix(700, 0).UTC()
	return models.ApplyProgressRecord{
		ID:            "apply-recovery-1",
		RequestID:     "apply-request-recovery-1",
		CorrelationID: "apply-correlation-recovery-1",
		Status:        status,
		OperationIDs:  []string{"operation-platform"},
		Entries: []models.ApplyProgressEntry{
			{LogicalID: "group/platform", ResourceID: "resource-platform", Action: models.ApplyProgressActionCreate, Status: models.ApplyProgressSucceeded, OperationID: "operation-platform", StartedAt: now, CompletedAt: now.Add(time.Second)},
			{LogicalID: "bucket/assets", Action: models.ApplyProgressActionCreate, Status: models.ApplyProgressFailed, Failure: models.ApplyProgressFailureAuthority, StartedAt: now, CompletedAt: now.Add(time.Second)},
		},
		CreatedAt: now,
		UpdatedAt: now.Add(time.Second),
	}
}
