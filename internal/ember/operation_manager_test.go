package ember

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

func TestNewOperationManagerRejectsNilStore(t *testing.T) {
	if _, err := NewOperationManager(nil); !errors.Is(err, ErrInvalidOperationStore) {
		t.Fatalf("NewOperationManager(nil) error = %v, want ErrInvalidOperationStore", err)
	}
}

func TestOperationManagerExposesInspectionReplayAndBoundedHistory(t *testing.T) {
	store, err := persistence.NewFileOperationStore(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatalf("NewFileOperationStore() error = %v", err)
	}
	manager, err := NewOperationManager(store)
	if err != nil {
		t.Fatalf("NewOperationManager() error = %v", err)
	}
	var plane OperationControlPlane = manager
	ctx := context.Background()
	createdAt := time.Unix(100, 0).UTC()
	original := models.Operation{
		ID:            "operation-1",
		ResourceID:    "resource-1",
		CorrelationID: "correlation-1",
		RequestID:     "request-1",
		Status:        models.OperationStatusSucceeded,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt.Add(time.Second),
		Outcome:       "completed",
	}
	second := original
	second.ID = "operation-2"
	second.CorrelationID = "correlation-2"
	second.RequestID = "request-2"
	second.CreatedAt = createdAt.Add(time.Minute)
	second.UpdatedAt = second.CreatedAt.Add(time.Second)
	third := second
	third.ID = "operation-3"
	third.CorrelationID = "correlation-3"
	third.RequestID = "request-3"
	third.ResourceID = "resource-2"

	created, err := plane.CreateOperation(ctx, original)
	if err != nil {
		t.Fatalf("CreateOperation() error = %v", err)
	}
	if !reflect.DeepEqual(*created, original) {
		t.Fatalf("CreateOperation() = %#v, want %#v", *created, original)
	}
	if _, err := plane.CreateOperation(ctx, second); err != nil {
		t.Fatalf("CreateOperation(second) error = %v", err)
	}
	if _, err := plane.CreateOperation(ctx, third); err != nil {
		t.Fatalf("CreateOperation(third) error = %v", err)
	}

	byID, err := plane.GetOperation(ctx, original.ID)
	if err != nil {
		t.Fatalf("GetOperation() error = %v", err)
	}
	if !reflect.DeepEqual(*byID, original) {
		t.Fatalf("GetOperation() = %#v, want %#v", *byID, original)
	}
	byRequest, err := plane.GetOperationByRequestID(ctx, original.RequestID)
	if err != nil {
		t.Fatalf("GetOperationByRequestID() error = %v", err)
	}
	if !reflect.DeepEqual(*byRequest, original) {
		t.Fatalf("GetOperationByRequestID() = %#v, want %#v", *byRequest, original)
	}

	retry := original
	retry.ID = "operation-retry"
	retry.CorrelationID = "correlation-retry"
	retry.Outcome = "retry outcome must not replace original"
	replayed, err := plane.CreateOperation(ctx, retry)
	if err != nil {
		t.Fatalf("idempotent CreateOperation() error = %v", err)
	}
	if !reflect.DeepEqual(*replayed, original) {
		t.Fatalf("idempotent CreateOperation() = %#v, want original %#v", *replayed, original)
	}

	conflict := third
	conflict.ID = "operation-conflict"
	conflict.RequestID = original.RequestID
	if _, err := plane.CreateOperation(ctx, conflict); !errors.Is(err, persistence.ErrOperationRequestConflict) {
		t.Fatalf("conflicting CreateOperation() error = %v, want ErrOperationRequestConflict", err)
	}

	history, err := plane.ListOperations(ctx, original.ResourceID, 2)
	if err != nil {
		t.Fatalf("ListOperations() error = %v", err)
	}
	if want := []models.Operation{original, second}; !reflect.DeepEqual(history, want) {
		t.Fatalf("ListOperations() = %#v, want %#v", history, want)
	}
	if _, err := plane.GetOperation(ctx, "missing-operation"); !errors.Is(err, persistence.ErrOperationNotFound) {
		t.Fatalf("missing GetOperation() error = %v, want ErrOperationNotFound", err)
	}
}
