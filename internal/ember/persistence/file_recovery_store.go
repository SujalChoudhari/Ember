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
	fileRecoveryStoreVersion  = 1
	MaxRecoveryStoreFileBytes = 1 << 20
	MaxRecoveryStoreRecords   = 256
)

var (
	ErrInvalidRecoveryStorePath = errors.New("invalid recovery store path")
	ErrRecoveryStoreCorrupt     = errors.New("corrupt recovery store")
	ErrRecoveryStoreTooLarge    = errors.New("recovery store exceeds size limit")
	ErrRecoveryStoreIO          = errors.New("recovery store I/O failure")
)

type recoveryStoreDiskState struct {
	Version int                     `json:"version"`
	Records []models.RecoveryRecord `json:"records"`
}

// FileRecoveryStore persists bounded redacted recovery records in one private
// JSON snapshot. Request-key idempotency is rebuilt after reopening.
type FileRecoveryStore struct {
	mu       sync.RWMutex
	path     string
	records  map[string]models.RecoveryRecord
	requests map[string]string
}

func NewFileRecoveryStore(path string) (*FileRecoveryStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrInvalidRecoveryStorePath
	}
	path = filepath.Clean(path)
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return nil, ErrInvalidRecoveryStorePath
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, ErrRecoveryStoreIO
	}

	store := &FileRecoveryStore{
		path:     path,
		records:  make(map[string]models.RecoveryRecord),
		requests: make(map[string]string),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func cloneRecoveryRecord(record models.RecoveryRecord) models.RecoveryRecord {
	copy := record
	copy.OperationIDs = append([]string(nil), record.OperationIDs...)
	copy.EntryLogicalIDs = append([]string(nil), record.EntryLogicalIDs...)
	copy.CompletedEntries = append([]string(nil), record.CompletedEntries...)
	return copy
}

func (store *FileRecoveryStore) load() error {
	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrRecoveryStoreIO
	}
	if len(data) > MaxRecoveryStoreFileBytes {
		return ErrRecoveryStoreTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state recoveryStoreDiskState
	if err := decoder.Decode(&state); err != nil {
		return ErrRecoveryStoreCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrRecoveryStoreCorrupt
	}
	if state.Version != fileRecoveryStoreVersion || len(state.Records) > MaxRecoveryStoreRecords {
		return ErrRecoveryStoreCorrupt
	}
	for _, record := range state.Records {
		if err := record.Validate(); err != nil {
			return ErrRecoveryStoreCorrupt
		}
		if _, exists := store.records[record.ID]; exists {
			return ErrRecoveryStoreCorrupt
		}
		if _, exists := store.requests[record.RecoveryRequestID]; exists {
			return ErrRecoveryStoreCorrupt
		}
		store.records[record.ID] = cloneRecoveryRecord(record)
		store.requests[record.RecoveryRequestID] = record.ID
	}
	return nil
}

func (store *FileRecoveryStore) saveLocked() error {
	records := make([]models.RecoveryRecord, 0, len(store.records))
	for _, record := range store.records {
		records = append(records, cloneRecoveryRecord(record))
	}
	sort.Slice(records, func(left, right int) bool { return records[left].ID < records[right].ID })
	data, err := json.MarshalIndent(recoveryStoreDiskState{Version: fileRecoveryStoreVersion, Records: records}, "", "  ")
	if err != nil {
		return ErrRecoveryStoreIO
	}
	data = append(data, '\n')
	if len(data) > MaxRecoveryStoreFileBytes {
		return ErrRecoveryStoreTooLarge
	}
	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return ErrRecoveryStoreIO
	}
	temporary, err := os.CreateTemp(directory, ".ember-recovery-*.tmp")
	if err != nil {
		return ErrRecoveryStoreIO
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return ErrRecoveryStoreIO
	}
	if written, err := temporary.Write(data); err != nil || written != len(data) {
		_ = temporary.Close()
		return ErrRecoveryStoreIO
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return ErrRecoveryStoreIO
	}
	if err := temporary.Close(); err != nil {
		return ErrRecoveryStoreIO
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return ErrRecoveryStoreIO
	}
	if directoryFile, err := os.Open(directory); err == nil {
		_ = directoryFile.Sync()
		_ = directoryFile.Close()
	}
	return nil
}

func (store *FileRecoveryStore) Create(ctx context.Context, record models.RecoveryRecord) (*models.RecoveryRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := record.Validate(); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if existingID, exists := store.requests[record.RecoveryRequestID]; exists {
		existing := store.records[existingID]
		if existing.ApplyProgressID != record.ApplyProgressID || existing.Action != record.Action {
			return nil, ErrRecoveryRequestConflict
		}
		copy := cloneRecoveryRecord(existing)
		return &copy, nil
	}
	if _, exists := store.records[record.ID]; exists {
		return nil, ErrDuplicateRecovery
	}
	if len(store.records) >= MaxRecoveryStoreRecords {
		return nil, ErrRecoveryStoreFull
	}
	store.records[record.ID] = cloneRecoveryRecord(record)
	store.requests[record.RecoveryRequestID] = record.ID
	if err := store.saveLocked(); err != nil {
		delete(store.records, record.ID)
		delete(store.requests, record.RecoveryRequestID)
		return nil, err
	}
	copy := cloneRecoveryRecord(record)
	return &copy, nil
}

func (store *FileRecoveryStore) Update(ctx context.Context, record models.RecoveryRecord) error {
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
		return ErrRecoveryNotFound
	}
	if previous.RecoveryRequestID != record.RecoveryRequestID || previous.ApplyProgressID != record.ApplyProgressID || previous.Action != record.Action || previous.CreatedAt != record.CreatedAt {
		return ErrRecoveryRequestConflict
	}
	store.records[record.ID] = cloneRecoveryRecord(record)
	if err := store.saveLocked(); err != nil {
		store.records[record.ID] = previous
		return err
	}
	return nil
}

func (store *FileRecoveryStore) Get(ctx context.Context, recordID string) (*models.RecoveryRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	record, exists := store.records[recordID]
	if !exists {
		return nil, ErrRecoveryNotFound
	}
	copy := cloneRecoveryRecord(record)
	return &copy, nil
}

func (store *FileRecoveryStore) GetByRequestID(ctx context.Context, requestID string) (*models.RecoveryRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	recordID, exists := store.requests[requestID]
	if !exists {
		return nil, ErrRecoveryNotFound
	}
	record := cloneRecoveryRecord(store.records[recordID])
	return &record, nil
}

func (store *FileRecoveryStore) List(ctx context.Context, limit int) ([]models.RecoveryRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxRecoveryListLimit {
		return nil, ErrInvalidRecoveryListLimit
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	records := make([]models.RecoveryRecord, 0, limit)
	for _, record := range store.records {
		records = append(records, cloneRecoveryRecord(record))
	}
	sort.Slice(records, func(left, right int) bool {
		if records[left].CreatedAt.Equal(records[right].CreatedAt) {
			return records[left].ID < records[right].ID
		}
		return records[left].CreatedAt.Before(records[right].CreatedAt)
	})
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

var _ RecoveryStore = (*FileRecoveryStore)(nil)
