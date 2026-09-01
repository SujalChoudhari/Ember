package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func TestFileAuditStorePersistsAndListsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.json")
	ctx := context.Background()
	createdAt := time.Unix(80, 0).UTC()
	entries := []models.AuditEntry{
		auditEntry("audit-z", "resource-1", createdAt),
		auditEntry("audit-a", "resource-1", createdAt),
		auditEntry("audit-b", "resource-2", createdAt.Add(time.Second)),
	}

	store, err := NewFileAuditStore(path)
	if err != nil {
		t.Fatalf("NewFileAuditStore() error = %v", err)
	}
	for _, entry := range entries {
		if err := store.Append(ctx, entry); err != nil {
			t.Fatalf("Append(%q) error = %v", entry.ID, err)
		}
	}

	reopened, err := NewFileAuditStore(path)
	if err != nil {
		t.Fatalf("NewFileAuditStore(reopen) error = %v", err)
	}
	resourceHistory, err := reopened.List(ctx, "resource-1", 2)
	if err != nil {
		t.Fatalf("List(resource) error = %v", err)
	}
	wantResourceHistory := []models.AuditEntry{entries[1], entries[0]}
	if !reflect.DeepEqual(resourceHistory, wantResourceHistory) {
		t.Fatalf("List(resource) = %#v, want %#v", resourceHistory, wantResourceHistory)
	}

	allHistory, err := reopened.List(ctx, "", MaxAuditListLimit)
	if err != nil {
		t.Fatalf("List(all) error = %v", err)
	}
	wantAllHistory := []models.AuditEntry{entries[1], entries[0], entries[2]}
	if !reflect.DeepEqual(allHistory, wantAllHistory) {
		t.Fatalf("List(all) = %#v, want %#v", allHistory, wantAllHistory)
	}
}

func TestFileAuditStoreEnforcesAuditContractAndCancellation(t *testing.T) {
	store, err := NewFileAuditStore(filepath.Join(t.TempDir(), "audit.json"))
	if err != nil {
		t.Fatalf("NewFileAuditStore() error = %v", err)
	}
	valid := auditEntry("audit-1", "resource-1", time.Unix(90, 0).UTC())
	if err := store.Append(context.Background(), valid); err != nil {
		t.Fatalf("Append(valid) error = %v", err)
	}

	duplicate := valid
	duplicate.OperationID = "operation-duplicate"
	if err := store.Append(context.Background(), duplicate); !errors.Is(err, ErrDuplicateAuditEntry) {
		t.Fatalf("duplicate Append() error = %v, want ErrDuplicateAuditEntry", err)
	}

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
	if err := store.Append(cancelled, auditEntry("audit-cancelled", "resource-1", time.Unix(91, 0).UTC())); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled Append() error = %v, want context.Canceled", err)
	}
	if _, err := store.List(cancelled, "resource-1", 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled List() error = %v, want context.Canceled", err)
	}
}

func TestFileAuditStoreRejectsInvalidPathsAndCorruptSnapshots(t *testing.T) {
	if _, err := NewFileAuditStore(""); !errors.Is(err, ErrInvalidAuditStorePath) {
		t.Fatalf("blank path error = %v, want ErrInvalidAuditStorePath", err)
	}
	directory := t.TempDir()
	if _, err := NewFileAuditStore(directory); !errors.Is(err, ErrInvalidAuditStorePath) {
		t.Fatalf("directory path error = %v, want ErrInvalidAuditStorePath", err)
	}

	valid := auditEntry("audit-1", "resource-1", time.Unix(100, 0).UTC())
	invalid := valid
	invalid.Action = ""
	tests := []struct {
		name  string
		data  []byte
		state *auditStoreDiskState
		want  error
	}{
		{name: "malformed json", data: []byte("{not-json"), want: ErrAuditStoreCorrupt},
		{name: "unsupported version", state: &auditStoreDiskState{Version: 2}, want: ErrAuditStoreCorrupt},
		{name: "unknown field", data: []byte(`{"version":1,"entries":[],"unexpected":"value"}`), want: ErrAuditStoreCorrupt},
		{name: "trailing value", data: []byte(`{"version":1,"entries":[]} {"extra":true}`), want: ErrAuditStoreCorrupt},
		{name: "invalid entry", state: &auditStoreDiskState{Version: 1, Entries: []models.AuditEntry{invalid}}, want: ErrAuditStoreCorrupt},
		{name: "duplicate entry", state: &auditStoreDiskState{Version: 1, Entries: []models.AuditEntry{valid, valid}}, want: ErrAuditStoreCorrupt},
		{name: "oversized snapshot", data: bytes.Repeat([]byte("x"), MaxAuditStoreFileBytes+1), want: ErrAuditStoreTooLarge},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "audit.json")
			data := tt.data
			if tt.state != nil {
				var err error
				data, err = json.Marshal(tt.state)
				if err != nil {
					t.Fatalf("json.Marshal() error = %v", err)
				}
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			if _, err := NewFileAuditStore(path); !errors.Is(err, tt.want) {
				t.Fatalf("NewFileAuditStore() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestFileAuditStoreUsesPrivateBoundedSnapshots(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "audit.json")
	store, err := NewFileAuditStore(path)
	if err != nil {
		t.Fatalf("NewFileAuditStore() error = %v", err)
	}
	if err := store.Append(context.Background(), auditEntry("audit-1", "resource-1", time.Unix(110, 0).UTC())); err != nil {
		t.Fatalf("Append() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(snapshot) error = %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("snapshot mode = %o, want %o", got, want)
	}
	if info.Size() > MaxAuditStoreFileBytes {
		t.Fatalf("snapshot size = %d, want at most %d", info.Size(), MaxAuditStoreFileBytes)
	}
}
