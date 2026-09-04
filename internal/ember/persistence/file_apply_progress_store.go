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
	fileApplyProgressStoreVersion  = 1
	MaxApplyProgressStoreFileBytes = 1 << 20
	MaxApplyProgressStoreRecords   = 256
)

var (
	ErrInvalidApplyProgressStorePath = errors.New("invalid apply progress store path")
	ErrApplyProgressStoreCorrupt     = errors.New("corrupt apply progress store")
	ErrApplyProgressStoreTooLarge    = errors.New("apply progress store exceeds size limit")
	ErrApplyProgressStoreIO          = errors.New("apply progress store I/O failure")
)

type applyProgressStoreDiskState struct {
	Version int                          `json:"version"`
	Records []models.ApplyProgressRecord `json:"records"`
}

// FileApplyProgressStore persists bounded redacted apply records in one private JSON snapshot.
type FileApplyProgressStore struct {
	mu       sync.RWMutex
	path     string
	records  map[string]models.ApplyProgressRecord
	requests map[string]string
}

func NewFileApplyProgressStore(path string) (*FileApplyProgressStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrInvalidApplyProgressStorePath
	}
	path = filepath.Clean(path)
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return nil, ErrInvalidApplyProgressStorePath
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, ErrApplyProgressStoreIO
	}

	store := &FileApplyProgressStore{
		path:     path,
		records:  make(map[string]models.ApplyProgressRecord),
		requests: make(map[string]string),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func cloneApplyProgressRecord(record models.ApplyProgressRecord) models.ApplyProgressRecord {
	copy := record
	copy.OperationIDs = append([]string(nil), record.OperationIDs...)
	copy.Entries = append([]models.ApplyProgressEntry(nil), record.Entries...)
	return copy
}

func (store *FileApplyProgressStore) load() error {
	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrApplyProgressStoreIO
	}
	if len(data) > MaxApplyProgressStoreFileBytes {
		return ErrApplyProgressStoreTooLarge
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state applyProgressStoreDiskState
	if err := decoder.Decode(&state); err != nil {
		return ErrApplyProgressStoreCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrApplyProgressStoreCorrupt
	}
	if state.Version != fileApplyProgressStoreVersion || len(state.Records) > MaxApplyProgressStoreRecords {
		return ErrApplyProgressStoreCorrupt
	}

	for _, record := range state.Records {
		if record.Validate() != nil {
			return ErrApplyProgressStoreCorrupt
		}
		if _, exists := store.records[record.ID]; exists {
			return ErrApplyProgressStoreCorrupt
		}
		if _, exists := store.requests[record.RequestID]; exists {
			return ErrApplyProgressStoreCorrupt
		}
		store.records[record.ID] = cloneApplyProgressRecord(record)
		store.requests[record.RequestID] = record.ID
	}
	return nil
}

func (store *FileApplyProgressStore) saveLocked() error {
	records := make([]models.ApplyProgressRecord, 0, len(store.records))
	for _, record := range store.records {
		records = append(records, cloneApplyProgressRecord(record))
	}
	sort.Slice(records, func(left, right int) bool { return records[left].ID < records[right].ID })
	data, err := json.MarshalIndent(applyProgressStoreDiskState{Version: fileApplyProgressStoreVersion, Records: records}, "", "  ")
	if err != nil {
		return ErrApplyProgressStoreIO
	}
	data = append(data, '\n')
	if len(data) > MaxApplyProgressStoreFileBytes {
		return ErrApplyProgressStoreTooLarge
	}

	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return ErrApplyProgressStoreIO
	}
	temporary, err := os.CreateTemp(directory, ".ember-apply-progress-*.tmp")
	if err != nil {
		return ErrApplyProgressStoreIO
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return ErrApplyProgressStoreIO
	}
	if written, err := temporary.Write(data); err != nil || written != len(data) {
		_ = temporary.Close()
		return ErrApplyProgressStoreIO
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return ErrApplyProgressStoreIO
	}
	if err := temporary.Close(); err != nil {
		return ErrApplyProgressStoreIO
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return ErrApplyProgressStoreIO
	}
	if directoryFile, err := os.Open(directory); err == nil {
		_ = directoryFile.Sync()
		_ = directoryFile.Close()
	}
	return nil
}

func (store *FileApplyProgressStore) Create(ctx context.Context, record models.ApplyProgressRecord) (*models.ApplyProgressRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := record.Validate(); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()

	if existing, exists := store.records[record.ID]; exists {
		copy := cloneApplyProgressRecord(existing)
		return &copy, nil
	}
	if existingID, exists := store.requests[record.RequestID]; exists {
		if existingID != record.ID {
			return nil, ErrApplyProgressRequestConflict
		}
	}
	if len(store.records) >= MaxApplyProgressStoreRecords {
		return nil, ErrApplyProgressStoreFull
	}
	store.records[record.ID] = cloneApplyProgressRecord(record)
	store.requests[record.RequestID] = record.ID
	if err := store.saveLocked(); err != nil {
		delete(store.records, record.ID)
		delete(store.requests, record.RequestID)
		return nil, err
	}
	copy := cloneApplyProgressRecord(record)
	return &copy, nil
}

func (store *FileApplyProgressStore) Update(ctx context.Context, record models.ApplyProgressRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := record.Validate(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	previous, exists := store.records[record.ID]
	if !exists {
		return ErrApplyProgressNotFound
	}
	if previous.RequestID != record.RequestID || previous.CreatedAt != record.CreatedAt {
		return ErrApplyProgressRequestConflict
	}
	store.records[record.ID] = cloneApplyProgressRecord(record)
	if err := store.saveLocked(); err != nil {
		store.records[record.ID] = previous
		return err
	}
	return nil
}

func (store *FileApplyProgressStore) Get(ctx context.Context, recordID string) (*models.ApplyProgressRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	record, exists := store.records[recordID]
	if !exists {
		return nil, ErrApplyProgressNotFound
	}
	copy := cloneApplyProgressRecord(record)
	return &copy, nil
}

func (store *FileApplyProgressStore) List(ctx context.Context, limit int) ([]models.ApplyProgressRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxApplyProgressListLimit {
		return nil, ErrInvalidApplyProgressListLimit
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	records := make([]models.ApplyProgressRecord, 0, limit)
	for _, record := range store.records {
		records = append(records, cloneApplyProgressRecord(record))
	}
	sort.Slice(records, func(left, right int) bool {
		if records[left].UpdatedAt.Equal(records[right].UpdatedAt) {
			return records[left].ID < records[right].ID
		}
		return records[left].UpdatedAt.After(records[right].UpdatedAt)
	})
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}
