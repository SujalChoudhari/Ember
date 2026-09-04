package deployment

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

var (
	ErrInvalidRecoveryAuthority   = errors.New("invalid recovery authority")
	ErrInvalidRecoveryRequest     = errors.New("invalid recovery request")
	ErrUnsupportedRecoveryFailure = errors.New("recovery does not support this failure class")
	ErrRecoveryApplyNotReady      = errors.New("apply record is not ready for recovery")
	ErrRecoveryExecution          = errors.New("recovery execution failed")
)

// RecoveryRequest is the explicit, idempotent action contract for one apply
// progress record. RequestID is never reused for a different action or apply.
type RecoveryRequest struct {
	RequestID       string
	ApplyProgressID string
	Action          models.RecoveryAction
}

// RecoveryExecutor owns the provider/resource-specific effect. The recovery
// authority controls ordering, failure classification, persistence, and
// idempotency but never embeds provider behavior.
type RecoveryExecutor interface {
	Rollback(context.Context, models.ApplyProgressEntry) error
	ForwardRecover(context.Context, models.ApplyProgressEntry) error
}

type RecoveryResult struct {
	Record   models.RecoveryRecord
	Replayed bool
}

type RecoveryAuthority struct {
	progress   persistence.ApplyProgressStore
	recoveries persistence.RecoveryStore
	executor   RecoveryExecutor
	mu         sync.Mutex
}

func NewRecoveryAuthority(progress persistence.ApplyProgressStore, recoveries persistence.RecoveryStore, executor RecoveryExecutor) (*RecoveryAuthority, error) {
	if progress == nil || recoveries == nil || executor == nil {
		return nil, ErrInvalidRecoveryAuthority
	}
	return &RecoveryAuthority{progress: progress, recoveries: recoveries, executor: executor}, nil
}

func (request RecoveryRequest) validate() error {
	if strings.TrimSpace(request.RequestID) == "" || len(request.RequestID) > models.MaxRecoveryRequestIDLength ||
		strings.TrimSpace(request.ApplyProgressID) == "" || len(request.ApplyProgressID) > models.MaxRecoveryApplyProgressIDLength ||
		(request.Action != models.RecoveryActionRollback && request.Action != models.RecoveryActionForward) {
		return ErrInvalidRecoveryRequest
	}
	return nil
}

// Recover executes one supported recovery path. Completed entries are rolled
// back in reverse apply order; failed and pending entries are forward-recovered
// in their original order. Every decision is represented by one durable record.
func (authority *RecoveryAuthority) Recover(ctx context.Context, request RecoveryRequest) (*RecoveryResult, error) {
	if ctx == nil {
		return nil, ErrInvalidRecoveryRequest
	}
	if err := request.validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	authority.mu.Lock()
	defer authority.mu.Unlock()

	existing, err := authority.recoveries.GetByRequestID(ctx, request.RequestID)
	if err == nil {
		if existing.ApplyProgressID != request.ApplyProgressID || existing.Action != request.Action {
			return nil, persistence.ErrRecoveryRequestConflict
		}
		return &RecoveryResult{Record: *existing, Replayed: true}, nil
	}
	if !errors.Is(err, persistence.ErrRecoveryNotFound) {
		return nil, err
	}

	progress, err := authority.progress.Get(ctx, request.ApplyProgressID)
	if err != nil {
		return nil, err
	}
	entries, outcome, err := recoveryEntries(*progress, request.Action)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	record := models.RecoveryRecord{
		ID:                 "recovery-" + request.RequestID,
		RecoveryRequestID:  request.RequestID,
		ApplyProgressID:    progress.ID,
		ApplyRequestID:     progress.RequestID,
		ApplyCorrelationID: progress.CorrelationID,
		Action:             request.Action,
		Status:             models.RecoveryStatusInProgress,
		OperationIDs:       append([]string(nil), progress.OperationIDs...),
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	for _, entry := range entries {
		record.EntryLogicalIDs = append(record.EntryLogicalIDs, entry.LogicalID)
	}
	if outcome == models.RecoveryOutcomeAlreadyComplete {
		record.EntryLogicalIDs = make([]string, 0, len(progress.Entries))
		for _, entry := range progress.Entries {
			record.EntryLogicalIDs = append(record.EntryLogicalIDs, entry.LogicalID)
			record.CompletedEntries = append(record.CompletedEntries, entry.LogicalID)
		}
	}
	stored, err := authority.recoveries.Create(ctx, record)
	if err != nil {
		return nil, err
	}
	if stored.ID != record.ID {
		return &RecoveryResult{Record: *stored, Replayed: true}, nil
	}
	record = *stored
	if outcome == models.RecoveryOutcomeAlreadyComplete {
		record.Status = models.RecoveryStatusSucceeded
		record.Outcome = models.RecoveryOutcomeAlreadyComplete
		if err := authority.persistRecovery(ctx, &record); err != nil {
			return nil, err
		}
		return &RecoveryResult{Record: record}, nil
	}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			if persistErr := authority.failRecovery(ctx, &record, models.RecoveryFailureCancelled); persistErr != nil {
				return nil, persistErr
			}
			return &RecoveryResult{Record: record}, err
		}
		var executeErr error
		if request.Action == models.RecoveryActionRollback {
			executeErr = authority.executor.Rollback(ctx, entry)
		} else {
			executeErr = authority.executor.ForwardRecover(ctx, entry)
		}
		if executeErr != nil {
			if persistErr := authority.failRecovery(ctx, &record, models.RecoveryFailureAuthority); persistErr != nil {
				return nil, persistErr
			}
			return &RecoveryResult{Record: record}, ErrRecoveryExecution
		}
		record.CompletedEntries = append(record.CompletedEntries, entry.LogicalID)
		if err := authority.persistRecovery(ctx, &record); err != nil {
			return nil, err
		}
	}
	record.Status = models.RecoveryStatusSucceeded
	record.Outcome = models.RecoveryOutcomeRecovered
	if err := authority.persistRecovery(ctx, &record); err != nil {
		return nil, err
	}
	return &RecoveryResult{Record: record}, nil
}

func recoveryEntries(progress models.ApplyProgressRecord, action models.RecoveryAction) ([]models.ApplyProgressEntry, string, error) {
	if progress.Status == models.ApplyProgressSucceeded {
		return nil, models.RecoveryOutcomeAlreadyComplete, nil
	}
	if progress.Status != models.ApplyProgressFailed {
		return nil, "", ErrRecoveryApplyNotReady
	}
	for _, entry := range progress.Entries {
		if entry.Status == models.ApplyProgressFailed && entry.Failure == models.ApplyProgressFailurePersistence {
			return nil, "", ErrUnsupportedRecoveryFailure
		}
	}
	entries := make([]models.ApplyProgressEntry, 0, len(progress.Entries))
	if action == models.RecoveryActionRollback {
		for index := len(progress.Entries) - 1; index >= 0; index-- {
			if progress.Entries[index].Status == models.ApplyProgressSucceeded {
				entries = append(entries, progress.Entries[index])
			}
		}
	} else {
		for _, entry := range progress.Entries {
			if entry.Status == models.ApplyProgressFailed || entry.Status == models.ApplyProgressPending {
				entries = append(entries, entry)
			}
		}
	}
	if len(entries) == 0 {
		return nil, models.RecoveryOutcomeAlreadyComplete, nil
	}
	return entries, "", nil
}

func (authority *RecoveryAuthority) persistRecovery(ctx context.Context, record *models.RecoveryRecord) error {
	record.UpdatedAt = time.Now().UTC()
	return authority.recoveries.Update(ctx, *record)
}

func (authority *RecoveryAuthority) failRecovery(ctx context.Context, record *models.RecoveryRecord, failure string) error {
	record.Status = models.RecoveryStatusFailed
	record.Outcome = models.RecoveryOutcomeFailed
	record.Failure = failure
	return authority.persistRecovery(ctx, record)
}
