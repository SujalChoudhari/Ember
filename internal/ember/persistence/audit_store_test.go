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

type memoryAuditStore struct {
	mu      sync.RWMutex
	entries map[string]models.AuditEntry
}

func newMemoryAuditStore() *memoryAuditStore {
	return &memoryAuditStore{entries: make(map[string]models.AuditEntry)}
}

func (store *memoryAuditStore) Append(ctx context.Context, entry models.AuditEntry) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := entry.Validate(); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.entries[entry.ID]; exists {
		return ErrDuplicateAuditEntry
	}
	store.entries[entry.ID] = entry
	return nil
}

func (store *memoryAuditStore) List(ctx context.Context, resourceID string, limit int) ([]models.AuditEntry, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxAuditListLimit {
		return nil, ErrInvalidAuditListLimit
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	entries := make([]models.AuditEntry, 0, limit)
	for _, entry := range store.entries {
		if resourceID == "" || entry.ResourceID == resourceID {
			entries = append(entries, entry)
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].CreatedAt.Equal(entries[j].CreatedAt) {
			return entries[i].ID < entries[j].ID
		}
		return entries[i].CreatedAt.Before(entries[j].CreatedAt)
	})
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries, nil
}

func auditEntry(id, resourceID string, createdAt time.Time) models.AuditEntry {
	return models.AuditEntry{
		ID:            id,
		OperationID:   "operation-" + id,
		ResourceID:    resourceID,
		CorrelationID: "correlation-" + id,
		RequestID:     "request-" + id,
		Action:        "resource.update",
		Outcome:       "succeeded",
		CreatedAt:     createdAt,
	}
}

func TestAuditStoreAppendsAndListsBoundedDeterministicHistory(t *testing.T) {
	var store AuditStore = newMemoryAuditStore()
	createdAt := time.Unix(60, 0).UTC()
	entries := []models.AuditEntry{
		auditEntry("audit-z", "resource-1", createdAt),
		auditEntry("audit-a", "resource-1", createdAt),
		auditEntry("audit-b", "resource-2", createdAt.Add(time.Second)),
	}

	for _, entry := range entries {
		if err := store.Append(context.Background(), entry); err != nil {
			t.Fatalf("Append(%q) error = %v", entry.ID, err)
		}
	}

	resourceHistory, err := store.List(context.Background(), "resource-1", 2)
	if err != nil {
		t.Fatalf("List(resource) error = %v", err)
	}
	wantResourceHistory := []models.AuditEntry{entries[1], entries[0]}
	if !reflect.DeepEqual(resourceHistory, wantResourceHistory) {
		t.Fatalf("List(resource) = %#v, want %#v", resourceHistory, wantResourceHistory)
	}

	allHistory, err := store.List(context.Background(), "", MaxAuditListLimit)
	if err != nil {
		t.Fatalf("List(all) error = %v", err)
	}
	if want := []models.AuditEntry{entries[1], entries[0], entries[2]}; !reflect.DeepEqual(allHistory, want) {
		t.Fatalf("List(all) = %#v, want %#v", allHistory, want)
	}

	duplicate := entries[0]
	duplicate.OperationID = "operation-duplicate"
	if err := store.Append(context.Background(), duplicate); !errors.Is(err, ErrDuplicateAuditEntry) {
		t.Fatalf("duplicate Append() error = %v, want ErrDuplicateAuditEntry", err)
	}
}

func TestAuditStoreRejectsInvalidEntriesLimitsAndCancelledContexts(t *testing.T) {
	store := newMemoryAuditStore()
	valid := auditEntry("audit-1", "resource-1", time.Unix(70, 0).UTC())
	invalid := valid
	invalid.Action = ""
	if err := store.Append(context.Background(), invalid); !errors.Is(err, models.ErrInvalidAuditEntry) {
		t.Fatalf("invalid Append() error = %v, want ErrInvalidAuditEntry", err)
	}
	if _, err := store.List(context.Background(), "resource-1", 0); !errors.Is(err, ErrInvalidAuditListLimit) {
		t.Fatalf("zero-limit List() error = %v, want ErrInvalidAuditListLimit", err)
	}
	if _, err := store.List(context.Background(), "resource-1", MaxAuditListLimit+1); !errors.Is(err, ErrInvalidAuditListLimit) {
		t.Fatalf("over-limit List() error = %v, want ErrInvalidAuditListLimit", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Append(cancelled, valid); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Append() error = %v, want context.Canceled", err)
	}
	if _, err := store.List(cancelled, "resource-1", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled List() error = %v, want context.Canceled", err)
	}
	if got, err := store.List(context.Background(), "resource-1", 1); err != nil {
		t.Fatalf("List(after cancellation) error = %v", err)
	} else if len(got) != 0 {
		t.Fatalf("List(after cancelled Append) = %#v, want no mutation", got)
	}
}

var _ AuditStore = (*memoryAuditStore)(nil)
