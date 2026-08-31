package persistence

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

type memoryOperationStore struct {
	mu         sync.RWMutex
	operations map[string]models.Operation
	requestIDs map[string]string
}

func newMemoryOperationStore() *memoryOperationStore {
	return &memoryOperationStore{
		operations: make(map[string]models.Operation),
		requestIDs: make(map[string]string),
	}
}

func (store *memoryOperationStore) Create(ctx context.Context, operation models.Operation) (*models.Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := operation.Validate(); err != nil {
		return nil, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	if existingID, exists := store.requestIDs[operation.RequestID]; exists {
		existing := store.operations[existingID]
		if existing.ResourceID != operation.ResourceID {
			return nil, ErrOperationRequestConflict
		}
		copy := existing
		return &copy, nil
	}
	if _, exists := store.operations[operation.ID]; exists {
		return nil, ErrDuplicateOperation
	}
	store.operations[operation.ID] = operation
	store.requestIDs[operation.RequestID] = operation.ID
	copy := operation
	return &copy, nil
}

func (store *memoryOperationStore) Get(ctx context.Context, operationID string) (*models.Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	operation, exists := store.operations[operationID]
	if !exists {
		return nil, ErrOperationNotFound
	}
	copy := operation
	return &copy, nil
}

func (store *memoryOperationStore) GetByRequestID(ctx context.Context, requestID string) (*models.Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	operationID, exists := store.requestIDs[requestID]
	if !exists {
		return nil, ErrOperationNotFound
	}
	operation := store.operations[operationID]
	copy := operation
	return &copy, nil
}

func (store *memoryOperationStore) List(ctx context.Context, resourceID string, limit int) ([]models.Operation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxOperationListLimit {
		return nil, ErrInvalidOperationListLimit
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	operations := make([]models.Operation, 0, limit)
	for _, operation := range store.operations {
		if resourceID == "" || operation.ResourceID == resourceID {
			operations = append(operations, operation)
		}
	}
	sort.Slice(operations, func(i, j int) bool {
		if operations[i].CreatedAt.Equal(operations[j].CreatedAt) {
			return operations[i].ID < operations[j].ID
		}
		return operations[i].CreatedAt.Before(operations[j].CreatedAt)
	})
	if len(operations) > limit {
		operations = operations[:limit]
	}
	return operations, nil
}

func operationRecord(id, resourceID, requestID string, createdAt time.Time) models.Operation {
	return models.Operation{
		ID:            id,
		ResourceID:    resourceID,
		CorrelationID: "correlation-" + id,
		RequestID:     requestID,
		Status:        models.OperationStatusSucceeded,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt.Add(time.Second),
		Outcome:       "completed",
	}
}

func TestOperationStoreCreatesInspectsAndReplaysByRequestID(t *testing.T) {
	store := newMemoryOperationStore()
	createdAt := time.Unix(10, 0).UTC()
	original := operationRecord("operation-1", "resource-1", "request-1", createdAt)

	created, err := store.Create(context.Background(), original)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !reflect.DeepEqual(*created, original) {
		t.Fatalf("Create() = %#v, want %#v", *created, original)
	}

	inspected, err := store.Get(context.Background(), original.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !reflect.DeepEqual(*inspected, original) {
		t.Fatalf("Get() = %#v, want %#v", *inspected, original)
	}

	byRequest, err := store.GetByRequestID(context.Background(), original.RequestID)
	if err != nil {
		t.Fatalf("GetByRequestID() error = %v", err)
	}
	if !reflect.DeepEqual(*byRequest, original) {
		t.Fatalf("GetByRequestID() = %#v, want %#v", *byRequest, original)
	}

	retry := original
	retry.ID = "operation-retry"
	retry.CorrelationID = "correlation-retry"
	retry.UpdatedAt = createdAt.Add(2 * time.Second)
	retry.Outcome = "different attempt"
	replayed, err := store.Create(context.Background(), retry)
	if err != nil {
		t.Fatalf("idempotent retry Create() error = %v", err)
	}
	if !reflect.DeepEqual(*replayed, original) {
		t.Fatalf("idempotent retry Create() = %#v, want original %#v", *replayed, original)
	}
	if _, err := store.Get(context.Background(), retry.ID); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("retry operation Get() error = %v, want ErrOperationNotFound", err)
	}

	conflict := operationRecord("operation-2", "resource-2", original.RequestID, createdAt.Add(time.Minute))
	if _, err := store.Create(context.Background(), conflict); !errors.Is(err, ErrOperationRequestConflict) {
		t.Fatalf("request-key conflict Create() error = %v, want ErrOperationRequestConflict", err)
	}

	duplicateID := original
	duplicateID.RequestID = "request-2"
	if _, err := store.Create(context.Background(), duplicateID); !errors.Is(err, ErrDuplicateOperation) {
		t.Fatalf("duplicate operation ID Create() error = %v, want ErrDuplicateOperation", err)
	}
	if _, err := store.Get(context.Background(), "missing-operation"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("missing Get() error = %v, want ErrOperationNotFound", err)
	}
	if _, err := store.GetByRequestID(context.Background(), "missing-request"); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("missing GetByRequestID() error = %v, want ErrOperationNotFound", err)
	}
}

func TestOperationStoreListsBoundedDeterministicHistory(t *testing.T) {
	store := newMemoryOperationStore()
	createdAt := time.Unix(20, 0).UTC()
	operations := []models.Operation{
		operationRecord("operation-z", "resource-1", "request-z", createdAt),
		operationRecord("operation-a", "resource-1", "request-a", createdAt),
		operationRecord("operation-b", "resource-2", "request-b", createdAt.Add(time.Second)),
	}
	for _, operation := range operations {
		if _, err := store.Create(context.Background(), operation); err != nil {
			t.Fatalf("Create(%q) error = %v", operation.ID, err)
		}
	}

	first, err := store.List(context.Background(), "resource-1", 2)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	second, err := store.List(context.Background(), "resource-1", 2)
	if err != nil {
		t.Fatalf("repeat List() error = %v", err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("repeated List() = %#v and %#v, want deterministic history", first, second)
	}
	want := []models.Operation{operations[1], operations[0]}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("List() = %#v, want %#v", first, want)
	}
	if _, err := store.List(context.Background(), "resource-1", 0); !errors.Is(err, ErrInvalidOperationListLimit) {
		t.Fatalf("zero-limit List() error = %v, want ErrInvalidOperationListLimit", err)
	}
	if _, err := store.List(context.Background(), "resource-1", MaxOperationListLimit+1); !errors.Is(err, ErrInvalidOperationListLimit) {
		t.Fatalf("over-limit List() error = %v, want ErrInvalidOperationListLimit", err)
	}
}

var _ OperationStore = (*memoryOperationStore)(nil)
