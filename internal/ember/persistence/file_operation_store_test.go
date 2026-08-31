package persistence

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func TestFileOperationStorePersistsAndReplaysAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	ctx := context.Background()
	createdAt := time.Unix(30, 0).UTC()
	original := operationRecord("operation-1", "resource-1", "request-1", createdAt)

	store, err := NewFileOperationStore(path)
	if err != nil {
		t.Fatalf("NewFileOperationStore() error = %v", err)
	}
	created, err := store.Create(ctx, original)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !reflect.DeepEqual(*created, original) {
		t.Fatalf("Create() = %#v, want %#v", *created, original)
	}

	reopened, err := NewFileOperationStore(path)
	if err != nil {
		t.Fatalf("NewFileOperationStore(reopen) error = %v", err)
	}
	inspected, err := reopened.Get(ctx, original.ID)
	if err != nil {
		t.Fatalf("Get(reopen) error = %v", err)
	}
	if !reflect.DeepEqual(*inspected, original) {
		t.Fatalf("Get(reopen) = %#v, want %#v", *inspected, original)
	}

	retry := original
	retry.ID = "operation-retry"
	retry.CorrelationID = "correlation-retry"
	replayed, err := reopened.Create(ctx, retry)
	if err != nil {
		t.Fatalf("idempotent retry Create() error = %v", err)
	}
	if !reflect.DeepEqual(*replayed, original) {
		t.Fatalf("idempotent retry Create() = %#v, want %#v", *replayed, original)
	}
	if _, err := reopened.Get(ctx, retry.ID); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("retry operation Get() error = %v, want ErrOperationNotFound", err)
	}
	conflict := retry
	conflict.ID = "operation-2"
	conflict.ResourceID = "resource-2"
	if _, err := reopened.Create(ctx, conflict); !errors.Is(err, ErrOperationRequestConflict) {
		t.Fatalf("request-key conflict Create() error = %v, want ErrOperationRequestConflict", err)
	}

	history, err := reopened.List(ctx, original.ResourceID, 1)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if !reflect.DeepEqual(history, []models.Operation{original}) {
		t.Fatalf("List() = %#v, want %#v", history, []models.Operation{original})
	}
}

func TestFileOperationStoreListsPersistedHistoryWithBounds(t *testing.T) {
	path := filepath.Join(t.TempDir(), "operations.json")
	ctx := context.Background()
	createdAt := time.Unix(40, 0).UTC()
	operations := []models.Operation{
		operationRecord("operation-z", "resource-1", "request-z", createdAt),
		operationRecord("operation-a", "resource-1", "request-a", createdAt),
		operationRecord("operation-b", "resource-2", "request-b", createdAt.Add(time.Second)),
	}

	store, err := NewFileOperationStore(path)
	if err != nil {
		t.Fatalf("NewFileOperationStore() error = %v", err)
	}
	for _, operation := range operations {
		if _, err := store.Create(ctx, operation); err != nil {
			t.Fatalf("Create(%q) error = %v", operation.ID, err)
		}
	}

	reopened, err := NewFileOperationStore(path)
	if err != nil {
		t.Fatalf("NewFileOperationStore(reopen) error = %v", err)
	}
	first, err := reopened.List(ctx, "resource-1", 2)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	want := []models.Operation{operations[1], operations[0]}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("List() = %#v, want %#v", first, want)
	}
	second, err := reopened.List(ctx, "resource-1", 2)
	if err != nil {
		t.Fatalf("repeat List() error = %v", err)
	}
	if !reflect.DeepEqual(second, want) {
		t.Fatalf("repeat List() = %#v, want %#v", second, want)
	}
	if _, err := reopened.List(ctx, "resource-1", 0); !errors.Is(err, ErrInvalidOperationListLimit) {
		t.Fatalf("zero-limit List() error = %v, want ErrInvalidOperationListLimit", err)
	}
	if _, err := reopened.List(ctx, "resource-1", MaxOperationListLimit+1); !errors.Is(err, ErrInvalidOperationListLimit) {
		t.Fatalf("over-limit List() error = %v, want ErrInvalidOperationListLimit", err)
	}
}

func TestFileOperationStoreRejectsCorruptAndOversizedSnapshots(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{name: "malformed json", data: []byte("{not-json"), want: ErrOperationStoreCorrupt},
		{name: "unsupported version", data: []byte(`{"version":2,"operations":[]}`), want: ErrOperationStoreCorrupt},
		{name: "trailing value", data: []byte(`{"version":1,"operations":[]} {"extra":true}`), want: ErrOperationStoreCorrupt},
		{name: "oversized snapshot", data: bytes.Repeat([]byte("x"), MaxOperationStoreFileBytes+1), want: ErrOperationStoreTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "operations.json")
			if err := os.WriteFile(path, tt.data, 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			if _, err := NewFileOperationStore(path); !errors.Is(err, tt.want) {
				t.Fatalf("NewFileOperationStore() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestFileOperationStoreValidatesPathAndCancellation(t *testing.T) {
	if _, err := NewFileOperationStore(""); !errors.Is(err, ErrInvalidOperationStorePath) {
		t.Fatalf("blank path error = %v, want ErrInvalidOperationStorePath", err)
	}
	directory := t.TempDir()
	if _, err := NewFileOperationStore(directory); !errors.Is(err, ErrInvalidOperationStorePath) {
		t.Fatalf("directory path error = %v, want ErrInvalidOperationStorePath", err)
	}

	store, err := NewFileOperationStore(filepath.Join(directory, "operations.json"))
	if err != nil {
		t.Fatalf("NewFileOperationStore() error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	operation := operationRecord("operation-1", "resource-1", "request-1", time.Unix(50, 0).UTC())
	if _, err := store.Create(cancelled, operation); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Create() error = %v, want context.Canceled", err)
	}
	if _, err := store.Get(cancelled, operation.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Get() error = %v, want context.Canceled", err)
	}
	if _, err := store.GetByRequestID(cancelled, operation.RequestID); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled GetByRequestID() error = %v, want context.Canceled", err)
	}
	if _, err := store.List(cancelled, operation.ResourceID, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled List() error = %v, want context.Canceled", err)
	}
}
