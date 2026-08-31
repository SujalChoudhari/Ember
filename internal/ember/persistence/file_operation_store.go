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
	fileOperationStoreVersion  = 1
	MaxOperationStoreFileBytes = 1 << 20
)

var (
	ErrInvalidOperationStorePath = errors.New("invalid operation store path")
	ErrOperationStoreCorrupt     = errors.New("corrupt operation store")
	ErrOperationStoreTooLarge    = errors.New("operation store exceeds size limit")
	ErrOperationStoreIO          = errors.New("operation store I/O failure")
)

type operationStoreDiskState struct {
	Version    int                `json:"version"`
	Operations []models.Operation `json:"operations"`
}

// FileOperationStore persists bounded operation records in one private JSON snapshot.
// Request-key idempotency is rebuilt from the persisted records on reopen.
type FileOperationStore struct {
	mu         sync.RWMutex
	path       string
	operations map[string]models.Operation
	requestIDs map[string]string
}

func NewFileOperationStore(path string) (*FileOperationStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrInvalidOperationStorePath
	}

	path = filepath.Clean(path)
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return nil, ErrInvalidOperationStorePath
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, ErrOperationStoreIO
	}

	store := &FileOperationStore{
		path:       path,
		operations: make(map[string]models.Operation),
		requestIDs: make(map[string]string),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *FileOperationStore) load() error {
	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrOperationStoreIO
	}
	if len(data) > MaxOperationStoreFileBytes {
		return ErrOperationStoreTooLarge
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state operationStoreDiskState
	if err := decoder.Decode(&state); err != nil {
		return ErrOperationStoreCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrOperationStoreCorrupt
	}
	if state.Version != fileOperationStoreVersion {
		return ErrOperationStoreCorrupt
	}

	operations := make(map[string]models.Operation, len(state.Operations))
	requestIDs := make(map[string]string, len(state.Operations))
	for _, operation := range state.Operations {
		if err := operation.Validate(); err != nil {
			return ErrOperationStoreCorrupt
		}
		if _, exists := operations[operation.ID]; exists {
			return ErrOperationStoreCorrupt
		}
		if _, exists := requestIDs[operation.RequestID]; exists {
			return ErrOperationStoreCorrupt
		}
		operations[operation.ID] = operation
		requestIDs[operation.RequestID] = operation.ID
	}

	store.operations = operations
	store.requestIDs = requestIDs
	return nil
}

func (store *FileOperationStore) saveLocked() error {
	operations := make([]models.Operation, 0, len(store.operations))
	for _, operation := range store.operations {
		operations = append(operations, operation)
	}
	sort.Slice(operations, func(i, j int) bool {
		return operations[i].ID < operations[j].ID
	})

	data, err := json.MarshalIndent(operationStoreDiskState{
		Version:    fileOperationStoreVersion,
		Operations: operations,
	}, "", "  ")
	if err != nil {
		return ErrOperationStoreIO
	}
	data = append(data, '\n')
	if len(data) > MaxOperationStoreFileBytes {
		return ErrOperationStoreTooLarge
	}

	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return ErrOperationStoreIO
	}
	temporary, err := os.CreateTemp(directory, ".ember-operations-*.tmp")
	if err != nil {
		return ErrOperationStoreIO
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return ErrOperationStoreIO
	}
	if written, err := temporary.Write(data); err != nil || written != len(data) {
		_ = temporary.Close()
		return ErrOperationStoreIO
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return ErrOperationStoreIO
	}
	if err := temporary.Close(); err != nil {
		return ErrOperationStoreIO
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return ErrOperationStoreIO
	}

	// The rename is the commit point. Directory sync is best effort after the
	// atomically replaced snapshot has become the current state.
	if directoryFile, err := os.Open(directory); err == nil {
		_ = directoryFile.Sync()
		_ = directoryFile.Close()
	}
	return nil
}

func (store *FileOperationStore) Create(ctx context.Context, operation models.Operation) (*models.Operation, error) {
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
	if err := store.saveLocked(); err != nil {
		delete(store.operations, operation.ID)
		delete(store.requestIDs, operation.RequestID)
		return nil, err
	}

	copy := operation
	return &copy, nil
}

func (store *FileOperationStore) Get(ctx context.Context, operationID string) (*models.Operation, error) {
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

func (store *FileOperationStore) GetByRequestID(ctx context.Context, requestID string) (*models.Operation, error) {
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

func (store *FileOperationStore) List(ctx context.Context, resourceID string, limit int) ([]models.Operation, error) {
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

var _ OperationStore = (*FileOperationStore)(nil)
