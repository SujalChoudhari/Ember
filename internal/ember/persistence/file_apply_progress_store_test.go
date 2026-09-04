package persistence

import (
	"context"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func TestFileApplyProgressStorePersistsPartialFailureAcrossReopen(t *testing.T) {
	path := t.TempDir() + "/apply-progress.json"
	now := time.Unix(100, 0).UTC()
	record := models.ApplyProgressRecord{
		ID:            "apply-request-1",
		RequestID:     "request-1",
		CorrelationID: "correlation-1",
		Status:        models.ApplyProgressFailed,
		Entries: []models.ApplyProgressEntry{
			{LogicalID: "resource-1", Action: "create", Status: models.ApplyProgressFailed, Failure: "apply authority failure", StartedAt: now, CompletedAt: now},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	store, err := NewFileApplyProgressStore(path)
	if err != nil {
		t.Fatalf("NewFileApplyProgressStore() error = %v", err)
	}
	if _, err := store.Create(context.Background(), record); err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	reopened, err := NewFileApplyProgressStore(path)
	if err != nil {
		t.Fatalf("NewFileApplyProgressStore(reopen) error = %v", err)
	}
	got, err := reopened.Get(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Get(reopen) error = %v", err)
	}
	if !reflect.DeepEqual(*got, record) {
		t.Fatalf("Get(reopen) = %#v, want %#v", *got, record)
	}
	updated := record
	updated.UpdatedAt = now.Add(time.Second)
	if err := reopened.Update(context.Background(), updated); err != nil {
		t.Fatalf("Update() error = %v", err)
	}
	reopenedAgain, err := NewFileApplyProgressStore(path)
	if err != nil {
		t.Fatalf("NewFileApplyProgressStore(second reopen) error = %v", err)
	}
	updatedGot, err := reopenedAgain.Get(context.Background(), record.ID)
	if err != nil {
		t.Fatalf("Get(updated) error = %v", err)
	}
	if !reflect.DeepEqual(*updatedGot, updated) {
		t.Fatalf("Get(updated) = %#v, want %#v", *updatedGot, updated)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(before inspection) error = %v", err)
	}
	if _, err := reopenedAgain.Get(context.Background(), record.ID); err != nil {
		t.Fatalf("repeat Get() error = %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(after inspection) error = %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("snapshot changed after inspection")
	}
}
