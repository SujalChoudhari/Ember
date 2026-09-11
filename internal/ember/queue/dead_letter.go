package queue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	deadLetterStoreVersion       = 1
	DefaultMaxDeadLetterRecords  = 256
	DefaultMaxDeadLetterFileSize = 1 << 20
	MaxDeadLetterRecords         = 4096
	MaxDeadLetterFileSize        = 8 << 20
	MaxDeadLetterReasonLength    = 128
	MaxDeadLetterListLimit       = 100
)

var (
	ErrInvalidDeadLetterStore  = errors.New("invalid dead-letter store")
	ErrInvalidDeadLetterRecord = errors.New("invalid dead-letter record")
	ErrDeadLetterConflict      = errors.New("dead-letter identity conflict")
	ErrDeadLetterFull          = errors.New("dead-letter store is full")
	ErrDeadLetterListLimit     = errors.New("invalid dead-letter list limit")
	ErrDeadLetterStoreCorrupt  = errors.New("corrupt dead-letter store")
	ErrDeadLetterStoreTooLarge = errors.New("dead-letter store exceeds size limit")
	ErrDeadLetterStoreIO       = errors.New("dead-letter store I/O failure")
)

// DeadLetterRecord is bounded failure metadata for an exhausted delivery.
// Payload bytes are represented only by size and digest so inspection and
// persisted state cannot expose the delivery payload or consumer error text.
type DeadLetterRecord struct {
	DeliveryID    string    `json:"delivery_id"`
	CorrelationID string    `json:"correlation_id"`
	Attempts      int       `json:"attempts"`
	Reason        string    `json:"reason"`
	PayloadBytes  int       `json:"payload_bytes"`
	PayloadSHA256 string    `json:"payload_sha256"`
	RecordedAt    time.Time `json:"recorded_at"`
}

func (record DeadLetterRecord) Validate() error {
	if strings.TrimSpace(record.DeliveryID) == "" || len(record.DeliveryID) > MaxDeliveryIDLength ||
		strings.TrimSpace(record.CorrelationID) == "" || len(record.CorrelationID) > MaxCorrelationIDLength ||
		record.Attempts < 1 || record.Attempts > MaxRetryCount+1 ||
		strings.TrimSpace(record.Reason) == "" || len(record.Reason) > MaxDeadLetterReasonLength ||
		record.PayloadBytes < 0 || record.PayloadBytes > MaxDeliveryPayloadBytes ||
		len(record.PayloadSHA256) != sha256.Size*2 || !isLowerHex(record.PayloadSHA256) ||
		record.RecordedAt.IsZero() {
		return ErrInvalidDeadLetterRecord
	}
	return nil
}

func isLowerHex(value string) bool {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return false
	}
	return strings.ToLower(value) == value
}

// DeadLetterStoreOptions bounds both record count and the on-disk snapshot.
// Zero values select conservative defaults.
type DeadLetterStoreOptions struct {
	MaxRecords  int
	MaxFileSize int
}

func (options DeadLetterStoreOptions) normalized() (DeadLetterStoreOptions, error) {
	if options.MaxRecords == 0 {
		options.MaxRecords = DefaultMaxDeadLetterRecords
	}
	if options.MaxFileSize == 0 {
		options.MaxFileSize = DefaultMaxDeadLetterFileSize
	}
	if options.MaxRecords < 1 || options.MaxRecords > MaxDeadLetterRecords ||
		options.MaxFileSize < 1 || options.MaxFileSize > MaxDeadLetterFileSize {
		return DeadLetterStoreOptions{}, ErrInvalidDeadLetterStore
	}
	return options, nil
}

type DeadLetterStore interface {
	Record(ctx context.Context, delivery Delivery, outcome DeliveryOutcome) error
	List(ctx context.Context, limit int) ([]DeadLetterRecord, error)
}

type deadLetterDiskState struct {
	Version  int                 `json:"version"`
	Records  []DeadLetterRecord  `json:"records"`
	Redrives []redriveDiskRecord `json:"redrives,omitempty"`
}

// FileDeadLetterStore persists bounded, payload-redacted dead-letter metadata
// in one private JSON snapshot.
type FileDeadLetterStore struct {
	mu                 sync.RWMutex
	path               string
	options            DeadLetterStoreOptions
	records            []DeadLetterRecord
	redrives           []redriveDiskRecord
	inflight           map[string]struct{}
	inflightDeliveries map[string]struct{}
}

func NewFileDeadLetterStore(path string, options DeadLetterStoreOptions) (*FileDeadLetterStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrInvalidDeadLetterStore
	}
	normalized, err := options.normalized()
	if err != nil {
		return nil, err
	}
	path = filepath.Clean(path)
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return nil, ErrInvalidDeadLetterStore
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, ErrDeadLetterStoreIO
	}

	store := &FileDeadLetterStore{path: path, options: normalized, inflight: make(map[string]struct{}), inflightDeliveries: make(map[string]struct{})}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *FileDeadLetterStore) load() error {
	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrDeadLetterStoreIO
	}
	if len(data) > store.options.MaxFileSize {
		return ErrDeadLetterStoreTooLarge
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state deadLetterDiskState
	if err := decoder.Decode(&state); err != nil {
		return ErrDeadLetterStoreCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrDeadLetterStoreCorrupt
	}
	if state.Version != deadLetterStoreVersion || len(state.Records) > store.options.MaxRecords {
		return ErrDeadLetterStoreCorrupt
	}

	seen := make(map[string]struct{}, len(state.Records))
	for _, record := range state.Records {
		if err := record.Validate(); err != nil {
			return ErrDeadLetterStoreCorrupt
		}
		if _, exists := seen[record.DeliveryID]; exists {
			return ErrDeadLetterStoreCorrupt
		}
		seen[record.DeliveryID] = struct{}{}
	}
	redriveIDs := make(map[string]struct{}, len(state.Redrives))
	if len(state.Redrives) > store.options.MaxRecords {
		return ErrDeadLetterStoreCorrupt
	}
	for _, redrive := range state.Redrives {
		if err := redrive.validate(); err != nil {
			return ErrDeadLetterStoreCorrupt
		}
		if _, exists := redriveIDs[redrive.RequestID]; exists {
			return ErrDeadLetterStoreCorrupt
		}
		redriveIDs[redrive.RequestID] = struct{}{}
	}
	store.records = append([]DeadLetterRecord(nil), state.Records...)
	store.redrives = append([]redriveDiskRecord(nil), state.Redrives...)
	return nil
}

func (store *FileDeadLetterStore) saveLocked() error {
	data, err := json.MarshalIndent(deadLetterDiskState{
		Version:  deadLetterStoreVersion,
		Records:  store.records,
		Redrives: store.redrives,
	}, "", "  ")
	if err != nil {
		return ErrDeadLetterStoreIO
	}
	data = append(data, '\n')
	if len(data) > store.options.MaxFileSize {
		return ErrDeadLetterStoreTooLarge
	}

	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return ErrDeadLetterStoreIO
	}
	temporary, err := os.CreateTemp(directory, ".ember-dead-letters-*.tmp")
	if err != nil {
		return ErrDeadLetterStoreIO
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if written, err := temporary.Write(data); err != nil || written != len(data) {
		_ = temporary.Close()
		return ErrDeadLetterStoreIO
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return ErrDeadLetterStoreIO
	}
	if err := temporary.Close(); err != nil {
		return ErrDeadLetterStoreIO
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return ErrDeadLetterStoreIO
	}
	if directoryFile, err := os.Open(directory); err == nil {
		_ = directoryFile.Sync()
		_ = directoryFile.Close()
	}
	return nil
}

func deadLetterRecord(delivery Delivery, outcome DeliveryOutcome) DeadLetterRecord {
	digest := sha256.Sum256(delivery.Payload)
	return DeadLetterRecord{
		DeliveryID:    delivery.ID,
		CorrelationID: delivery.CorrelationID,
		Attempts:      outcome.Attempts,
		Reason:        outcome.Reason,
		PayloadBytes:  len(delivery.Payload),
		PayloadSHA256: hex.EncodeToString(digest[:]),
		RecordedAt:    time.Now().UTC(),
	}
}

func (store *FileDeadLetterStore) Record(ctx context.Context, delivery Delivery, outcome DeliveryOutcome) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := delivery.Validate(); err != nil {
		return err
	}
	if outcome.Status != DeliveryStatusFailed || outcome.DeliveryID != delivery.ID ||
		outcome.CorrelationID != delivery.CorrelationID || outcome.Reason == "" {
		return ErrInvalidDeadLetterRecord
	}
	record := deadLetterRecord(delivery, outcome)
	if err := record.Validate(); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	for _, existing := range store.records {
		if existing.DeliveryID != record.DeliveryID {
			continue
		}
		if existing.CorrelationID == record.CorrelationID && existing.Attempts == record.Attempts &&
			existing.Reason == record.Reason && existing.PayloadBytes == record.PayloadBytes &&
			existing.PayloadSHA256 == record.PayloadSHA256 {
			return nil
		}
		return ErrDeadLetterConflict
	}
	if len(store.records) >= store.options.MaxRecords {
		return ErrDeadLetterFull
	}
	store.records = append(store.records, record)
	if err := store.saveLocked(); err != nil {
		store.records = store.records[:len(store.records)-1]
		return err
	}
	return nil
}

func (store *FileDeadLetterStore) List(ctx context.Context, limit int) ([]DeadLetterRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxDeadLetterListLimit || limit > store.options.MaxRecords {
		return nil, ErrDeadLetterListLimit
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	if len(store.records) > limit {
		return append([]DeadLetterRecord(nil), store.records[:limit]...), nil
	}
	return append([]DeadLetterRecord(nil), store.records...), nil
}

// DeliverWithDeadLetter records an exhausted delivery after Deliver returns its
// stable terminal failure. Other delivery outcomes and context errors pass
// through unchanged.
func DeliverWithDeadLetter(ctx context.Context, delivery Delivery, policy RetryPolicy, consumer Consumer, wait Waiter, store DeadLetterStore) (DeliveryOutcome, error) {
	if store == nil {
		return DeliveryOutcome{}, ErrInvalidDeadLetterStore
	}
	outcome, err := Deliver(ctx, delivery, policy, consumer, wait)
	if !errors.Is(err, ErrDeliveryFailed) {
		return outcome, err
	}
	if recordErr := store.Record(ctx, delivery, outcome); recordErr != nil {
		return outcome, recordErr
	}
	return outcome, err
}

var _ DeadLetterStore = (*FileDeadLetterStore)(nil)
