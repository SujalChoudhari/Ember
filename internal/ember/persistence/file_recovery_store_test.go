package persistence

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func TestFileRecoveryStorePersistsReplaysAndRejectsConflicts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.json")
	store, err := NewFileRecoveryStore(path)
	if err != nil {
		t.Fatalf("NewFileRecoveryStore() error = %v", err)
	}
	record := recoveryStoreRecord("recovery-1", "recover-request-1", models.RecoveryActionRollback)
	created, err := store.Create(context.Background(), record)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if created.ID != record.ID {
		t.Fatalf("created ID = %q, want %q", created.ID, record.ID)
	}
	if replay, err := store.Create(context.Background(), record); err != nil || replay.ID != record.ID {
		t.Fatalf("Create(replay) = (%#v, %v), want original record", replay, err)
	}
	conflict := record
	conflict.Action = models.RecoveryActionForward
	if _, err := store.Create(context.Background(), conflict); !errors.Is(err, ErrRecoveryRequestConflict) {
		t.Fatalf("Create(conflict) error = %v, want ErrRecoveryRequestConflict", err)
	}
	reopened, err := NewFileRecoveryStore(path)
	if err != nil {
		t.Fatalf("NewFileRecoveryStore(reopen) error = %v", err)
	}
	got, err := reopened.GetByRequestID(context.Background(), record.RecoveryRequestID)
	if err != nil {
		t.Fatalf("GetByRequestID(reopen) error = %v", err)
	}
	if got.ApplyProgressID != record.ApplyProgressID || got.ApplyCorrelationID != record.ApplyCorrelationID {
		t.Fatalf("reopened linkage = %#v, want apply linkage", got)
	}
	listed, err := reopened.List(context.Background(), 1)
	if err != nil || len(listed) != 1 {
		t.Fatalf("List() = (%#v, %v), want one bounded record", listed, err)
	}
}

func recoveryStoreRecord(id, requestID string, action models.RecoveryAction) models.RecoveryRecord {
	now := time.Unix(600, 0).UTC()
	return models.RecoveryRecord{
		ID:                 id,
		RecoveryRequestID:  requestID,
		ApplyProgressID:    "apply-1",
		ApplyRequestID:     "apply-request-1",
		ApplyCorrelationID: "apply-correlation-1",
		Action:             action,
		Status:             models.RecoveryStatusInProgress,
		OperationIDs:       []string{"operation-1"},
		EntryLogicalIDs:    []string{"group/platform"},
		CreatedAt:          now,
		UpdatedAt:          now,
	}
}
