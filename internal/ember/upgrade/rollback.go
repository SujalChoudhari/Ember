package upgrade

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const (
	MaxRollbackRecords         = 16
	MaxRollbackListLimit       = 100
	MaxRollbackAttempts        = 3
	MaxRollbackRequestIDLength = 128

	StepRollback = "rollback"

	RollbackStatusPending   = "pending"
	RollbackStatusSucceeded = "succeeded"
	RollbackStatusFailed    = "failed"

	RollbackCodeSucceeded        = "rollback-succeeded"
	RollbackCodeExecutionFailed  = "rollback-execution-failed"
	RollbackCodeAttemptsExceeded = "rollback-attempts-exceeded"
	RollbackCodeCancelled        = "rollback-cancelled"
)

var (
	ErrInvalidRollbackRequest   = errors.New("invalid rollback request")
	ErrRollbackRequestConflict  = errors.New("rollback request conflict")
	ErrRollbackNotFound         = errors.New("rollback record not found")
	ErrRollbackStoreFull        = errors.New("rollback journal is full")
	ErrRollbackExecution        = errors.New("rollback execution failed")
	ErrRollbackAttemptsExceeded = errors.New("rollback attempts exceeded")
	ErrRollbackCancelled        = errors.New("rollback cancelled")
	ErrRollbackNotComplete      = errors.New("rollback is not complete")
)

// RestoreFunc applies the previously committed safe state. The journal owns
// ordering and idempotency; callers own the resource-specific state effect.
type RestoreFunc func(context.Context, Snapshot) error

// RollbackRecord is bounded operator evidence for one prepared rollback. The
// safe snapshot is retained privately by RollbackJournal and is never included
// in listings or status records.
type RollbackRecord struct {
	RequestID     string
	SourceVersion string
	TargetVersion string
	Status        string
	Attempts      int
}

type rollbackEntry struct {
	record   RollbackRecord
	snapshot Snapshot
}

// RollbackResult contains the safe state selected for restoration and bounded
// evidence. A replayed result never invokes RestoreFunc again.
type RollbackResult struct {
	Snapshot Snapshot
	Record   RollbackRecord
	Evidence Evidence
	Replayed bool
}

// RollbackJournal retains a bounded set of safe snapshots. It rejects new
// records when full and only removes terminal records through explicit Discard.
type RollbackJournal struct {
	mu      sync.Mutex
	entries map[string]rollbackEntry
}

func NewRollbackJournal() *RollbackJournal {
	return &RollbackJournal{entries: make(map[string]rollbackEntry)}
}

// Prepare records the safe pre-upgrade state before the caller commits an
// upgrade. Repeating the same request and state is idempotent; reusing a
// request ID for a different state or target is rejected.
func (journal *RollbackJournal) Prepare(ctx context.Context, requestID string, snapshot Snapshot, targetVersion string) (RollbackRecord, error) {
	if journal == nil || ctx == nil {
		return RollbackRecord{}, ErrInvalidRollbackRequest
	}
	if err := ctx.Err(); err != nil {
		return RollbackRecord{}, ErrRollbackCancelled
	}
	if err := validateRollbackRequestID(requestID); err != nil || targetVersion != CurrentVersion {
		return RollbackRecord{}, ErrInvalidRollbackRequest
	}
	copy, err := cloneRollbackSnapshot(snapshot)
	if err != nil {
		return RollbackRecord{}, err
	}

	journal.mu.Lock()
	defer journal.mu.Unlock()
	if existing, ok := journal.entries[requestID]; ok {
		if existing.record.TargetVersion != targetVersion || !reflect.DeepEqual(existing.snapshot, copy) {
			return RollbackRecord{}, ErrRollbackRequestConflict
		}
		return existing.record, nil
	}
	if len(journal.entries) >= MaxRollbackRecords {
		return RollbackRecord{}, ErrRollbackStoreFull
	}
	record := RollbackRecord{
		RequestID:     requestID,
		SourceVersion: snapshot.Version,
		TargetVersion: targetVersion,
		Status:        RollbackStatusPending,
	}
	journal.entries[requestID] = rollbackEntry{record: record, snapshot: copy}
	return record, nil
}

// Rollback restores a prepared safe state at most MaxRollbackAttempts times.
// A successful request is idempotent and returns the same result without
// calling restore again. Failed attempts remain retained for explicit retry
// until the bounded attempt limit is reached.
func (journal *RollbackJournal) Rollback(ctx context.Context, requestID string, restore RestoreFunc) (*RollbackResult, error) {
	if journal == nil || ctx == nil || restore == nil {
		return nil, ErrInvalidRollbackRequest
	}
	if err := ctx.Err(); err != nil {
		return nil, ErrRollbackCancelled
	}
	if err := validateRollbackRequestID(requestID); err != nil {
		return nil, err
	}

	journal.mu.Lock()
	defer journal.mu.Unlock()
	entry, ok := journal.entries[requestID]
	if !ok {
		return nil, ErrRollbackNotFound
	}
	if entry.record.Status == RollbackStatusSucceeded {
		return journal.rollbackResult(entry, true, RollbackCodeSucceeded), nil
	}
	if entry.record.Status == RollbackStatusFailed || entry.record.Attempts >= MaxRollbackAttempts {
		entry.record.Status = RollbackStatusFailed
		journal.entries[requestID] = entry
		return journal.rollbackResult(entry, false, RollbackCodeAttemptsExceeded), ErrRollbackAttemptsExceeded
	}
	if err := ctx.Err(); err != nil {
		return journal.rollbackResult(entry, false, RollbackCodeCancelled), ErrRollbackCancelled
	}

	entry.record.Attempts++
	snapshot := cloneRollbackSnapshotUnchecked(entry.snapshot)
	if err := restore(ctx, snapshot); err != nil {
		if entry.record.Attempts >= MaxRollbackAttempts {
			entry.record.Status = RollbackStatusFailed
		}
		journal.entries[requestID] = entry
		if entry.record.Status == RollbackStatusFailed {
			return journal.rollbackResult(entry, false, RollbackCodeAttemptsExceeded), ErrRollbackAttemptsExceeded
		}
		return journal.rollbackResult(entry, false, RollbackCodeExecutionFailed), ErrRollbackExecution
	}

	entry.record.Status = RollbackStatusSucceeded
	journal.entries[requestID] = entry
	return journal.rollbackResult(entry, false, RollbackCodeSucceeded), nil
}

func (journal *RollbackJournal) Get(ctx context.Context, requestID string) (RollbackRecord, error) {
	if journal == nil || ctx == nil {
		return RollbackRecord{}, ErrInvalidRollbackRequest
	}
	if err := ctx.Err(); err != nil {
		return RollbackRecord{}, ErrRollbackCancelled
	}
	if err := validateRollbackRequestID(requestID); err != nil {
		return RollbackRecord{}, err
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	entry, ok := journal.entries[requestID]
	if !ok {
		return RollbackRecord{}, ErrRollbackNotFound
	}
	return entry.record, nil
}

func (journal *RollbackJournal) List(ctx context.Context, limit int) ([]RollbackRecord, error) {
	if journal == nil || ctx == nil {
		return nil, ErrInvalidRollbackRequest
	}
	if err := ctx.Err(); err != nil {
		return nil, ErrRollbackCancelled
	}
	if limit <= 0 || limit > MaxRollbackListLimit {
		return nil, ErrInvalidRollbackRequest
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	records := make([]RollbackRecord, 0, len(journal.entries))
	for _, entry := range journal.entries {
		records = append(records, entry.record)
	}
	sort.Slice(records, func(left, right int) bool { return records[left].RequestID < records[right].RequestID })
	if len(records) > limit {
		records = records[:limit]
	}
	return records, nil
}

// Discard explicitly removes a terminal rollback record to make room for a
// later bounded operation. Pending records cannot be silently discarded.
func (journal *RollbackJournal) Discard(ctx context.Context, requestID string) error {
	if journal == nil || ctx == nil {
		return ErrInvalidRollbackRequest
	}
	if err := ctx.Err(); err != nil {
		return ErrRollbackCancelled
	}
	if err := validateRollbackRequestID(requestID); err != nil {
		return err
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	entry, ok := journal.entries[requestID]
	if !ok {
		return ErrRollbackNotFound
	}
	if entry.record.Status != RollbackStatusSucceeded && entry.record.Status != RollbackStatusFailed {
		return ErrRollbackNotComplete
	}
	delete(journal.entries, requestID)
	return nil
}

func (journal *RollbackJournal) rollbackResult(entry rollbackEntry, replayed bool, code string) *RollbackResult {
	status := EvidenceSucceeded
	if code != RollbackCodeSucceeded {
		status = EvidenceFailed
	}
	return &RollbackResult{
		Snapshot: cloneRollbackSnapshotUnchecked(entry.snapshot),
		Record:   entry.record,
		Evidence: Evidence{
			Status:        status,
			Code:          code,
			Step:          StepRollback,
			SourceVersion: boundedVersion(entry.record.TargetVersion),
			TargetVersion: boundedVersion(entry.record.SourceVersion),
		},
		Replayed: replayed,
	}
}

func validateRollbackRequestID(requestID string) error {
	if strings.TrimSpace(requestID) == "" || len(requestID) > MaxRollbackRequestIDLength {
		return ErrInvalidRollbackRequest
	}
	return nil
}

func cloneRollbackSnapshot(snapshot Snapshot) (Snapshot, error) {
	if err := validateSnapshot(snapshot); err != nil {
		return Snapshot{}, err
	}
	return cloneRollbackSnapshotUnchecked(snapshot), nil
}

func cloneRollbackSnapshotUnchecked(snapshot Snapshot) Snapshot {
	copy := snapshot
	copy.Resources = append([]models.Resource(nil), snapshot.Resources...)
	for index := range copy.Resources {
		if snapshot.Resources[index].Spec.Tags != nil {
			copy.Resources[index].Spec.Tags = make(map[string]string, len(snapshot.Resources[index].Spec.Tags))
			for key, value := range snapshot.Resources[index].Spec.Tags {
				copy.Resources[index].Spec.Tags[key] = value
			}
		}
	}
	return copy
}
