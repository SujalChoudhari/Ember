package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const (
	fileAuditStoreVersion  = 1
	MaxAuditStoreFileBytes = 1 << 20
)

var (
	ErrInvalidAuditStorePath = errors.New("invalid audit store path")
	ErrAuditStoreCorrupt     = errors.New("corrupt audit store")
	ErrAuditStoreTooLarge    = errors.New("audit store exceeds size limit")
	ErrAuditStoreIO          = errors.New("audit store I/O failure")
)

type auditStoreDiskState struct {
	Version int                 `json:"version"`
	Entries []models.AuditEntry `json:"entries"`
}

// FileAuditStore persists bounded, attribution-only audit entries in one
// private JSON snapshot. Entries are immutable after append and are rebuilt
// into the in-memory index when the store is reopened.
type FileAuditStore struct {
	mu      sync.RWMutex
	path    string
	entries map[string]models.AuditEntry
}

func NewFileAuditStore(path string) (*FileAuditStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrInvalidAuditStorePath
	}

	path = filepath.Clean(path)
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return nil, ErrInvalidAuditStorePath
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, ErrAuditStoreIO
	}

	store := &FileAuditStore{
		path:    path,
		entries: make(map[string]models.AuditEntry),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *FileAuditStore) load() error {
	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrAuditStoreIO
	}
	if len(data) > MaxAuditStoreFileBytes {
		return ErrAuditStoreTooLarge
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state auditStoreDiskState
	if err := decoder.Decode(&state); err != nil {
		return ErrAuditStoreCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrAuditStoreCorrupt
	}
	if state.Version != fileAuditStoreVersion {
		return ErrAuditStoreCorrupt
	}

	entries := make(map[string]models.AuditEntry, len(state.Entries))
	for _, entry := range state.Entries {
		if err := entry.Validate(); err != nil {
			return ErrAuditStoreCorrupt
		}
		if _, exists := entries[entry.ID]; exists {
			return ErrAuditStoreCorrupt
		}
		entries[entry.ID] = entry
	}

	store.entries = entries
	return nil
}

func (store *FileAuditStore) saveLocked() error {
	entries := make([]models.AuditEntry, 0, len(store.entries))
	for _, entry := range store.entries {
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ID < entries[j].ID
	})

	data, err := json.MarshalIndent(auditStoreDiskState{
		Version: fileAuditStoreVersion,
		Entries: entries,
	}, "", "  ")
	if err != nil {
		return ErrAuditStoreIO
	}
	data = append(data, '\n')
	if len(data) > MaxAuditStoreFileBytes {
		return ErrAuditStoreTooLarge
	}

	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return ErrAuditStoreIO
	}
	temporary, err := os.CreateTemp(directory, ".ember-audit-*.tmp")
	if err != nil {
		return ErrAuditStoreIO
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return ErrAuditStoreIO
	}
	if written, err := temporary.Write(data); err != nil || written != len(data) {
		_ = temporary.Close()
		return ErrAuditStoreIO
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return ErrAuditStoreIO
	}
	if err := temporary.Close(); err != nil {
		return ErrAuditStoreIO
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return ErrAuditStoreIO
	}

	// The rename is the commit point. Directory sync is best effort after the
	// atomically replaced snapshot has become the current state.
	if directoryFile, err := os.Open(directory); err == nil {
		_ = directoryFile.Sync()
		_ = directoryFile.Close()
	}
	return nil
}

func (store *FileAuditStore) Append(ctx context.Context, entry models.AuditEntry) error {
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
	if err := store.saveLocked(); err != nil {
		delete(store.entries, entry.ID)
		return err
	}
	return nil
}

func (store *FileAuditStore) List(ctx context.Context, resourceID string, limit int) ([]models.AuditEntry, error) {
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

var _ AuditStore = (*FileAuditStore)(nil)
