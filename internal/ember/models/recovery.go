package models

import (
	"errors"
	"strings"
	"time"
)

type RecoveryAction string

const (
	RecoveryActionRollback RecoveryAction = "rollback"
	RecoveryActionForward  RecoveryAction = "forward_recovery"
)

type RecoveryStatus string

const (
	RecoveryStatusPending    RecoveryStatus = "pending"
	RecoveryStatusInProgress RecoveryStatus = "in_progress"
	RecoveryStatusSucceeded  RecoveryStatus = "succeeded"
	RecoveryStatusFailed     RecoveryStatus = "failed"
)

const (
	RecoveryOutcomeRecovered       = "recovered"
	RecoveryOutcomeAlreadyComplete = "already_complete"
	RecoveryOutcomeFailed          = "failed"

	RecoveryFailureAuthority   = "recovery authority failure"
	RecoveryFailureCancelled   = "recovery cancelled"
	RecoveryFailurePersistence = "recovery persistence failure"

	MaxRecoveryIDLength              = 128
	MaxRecoveryRequestIDLength       = 96
	MaxRecoveryApplyProgressIDLength = 128
	MaxRecoveryApplyRequestIDLength  = 128
	MaxRecoveryCorrelationIDLength   = 128
	MaxRecoveryEntries               = 100
	MaxRecoveryOperationIDs          = 100
	MaxRecoveryFailureLength         = 64
	MaxRecoveryOutcomeLength         = 32
)

// RecoveryRecord is the durable, redacted outcome of one explicit recovery
// request. It contains only stable identifiers, action/status values, and
// bounded entry progress; provider payloads and error text are never stored.
type RecoveryRecord struct {
	ID                 string         `json:"id"`
	RecoveryRequestID  string         `json:"recovery_request_id"`
	ApplyProgressID    string         `json:"apply_progress_id"`
	ApplyRequestID     string         `json:"apply_request_id"`
	ApplyCorrelationID string         `json:"apply_correlation_id"`
	Action             RecoveryAction `json:"action"`
	Status             RecoveryStatus `json:"status"`
	Outcome            string         `json:"outcome,omitempty"`
	Failure            string         `json:"failure,omitempty"`
	OperationIDs       []string       `json:"operation_ids,omitempty"`
	EntryLogicalIDs    []string       `json:"entry_logical_ids"`
	CompletedEntries   []string       `json:"completed_entries,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
}

var ErrInvalidRecoveryRecord = errors.New("invalid recovery record")

func (action RecoveryAction) valid() bool {
	return action == RecoveryActionRollback || action == RecoveryActionForward
}

func (status RecoveryStatus) valid() bool {
	switch status {
	case RecoveryStatusPending, RecoveryStatusInProgress, RecoveryStatusSucceeded, RecoveryStatusFailed:
		return true
	default:
		return false
	}
}

func validRecoveryText(value string, maxLength int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maxLength
}

func validRecoveryList(values []string, maxLength, maxItems int, required bool) bool {
	if len(values) > maxItems || (required && len(values) == 0) {
		return false
	}
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validRecoveryText(value, maxLength) {
			return false
		}
		if _, exists := seen[value]; exists {
			return false
		}
		seen[value] = struct{}{}
	}
	return true
}

func (record RecoveryRecord) Validate() error {
	if !validRecoveryText(record.ID, MaxRecoveryIDLength) ||
		!validRecoveryText(record.RecoveryRequestID, MaxRecoveryRequestIDLength) ||
		!validRecoveryText(record.ApplyProgressID, MaxRecoveryApplyProgressIDLength) ||
		!validRecoveryText(record.ApplyRequestID, MaxRecoveryApplyRequestIDLength) ||
		!validRecoveryText(record.ApplyCorrelationID, MaxRecoveryCorrelationIDLength) ||
		!record.Action.valid() || !record.Status.valid() ||
		!validRecoveryList(record.OperationIDs, MaxRecoveryIDLength, MaxRecoveryOperationIDs, false) ||
		!validRecoveryList(record.EntryLogicalIDs, MaxRecoveryIDLength, MaxRecoveryEntries, true) ||
		!validRecoveryList(record.CompletedEntries, MaxRecoveryIDLength, MaxRecoveryEntries, false) ||
		record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) ||
		len(record.Outcome) > MaxRecoveryOutcomeLength || len(record.Failure) > MaxRecoveryFailureLength {
		return ErrInvalidRecoveryRecord
	}

	entries := make(map[string]struct{}, len(record.EntryLogicalIDs))
	for _, entry := range record.EntryLogicalIDs {
		entries[entry] = struct{}{}
	}
	for _, entry := range record.CompletedEntries {
		if _, exists := entries[entry]; !exists {
			return ErrInvalidRecoveryRecord
		}
	}

	switch record.Status {
	case RecoveryStatusPending, RecoveryStatusInProgress:
		if record.Outcome != "" || record.Failure != "" {
			return ErrInvalidRecoveryRecord
		}
	case RecoveryStatusSucceeded:
		if record.Outcome != RecoveryOutcomeRecovered && record.Outcome != RecoveryOutcomeAlreadyComplete || record.Failure != "" {
			return ErrInvalidRecoveryRecord
		}
	case RecoveryStatusFailed:
		if record.Outcome != RecoveryOutcomeFailed ||
			(record.Failure != RecoveryFailureAuthority && record.Failure != RecoveryFailureCancelled && record.Failure != RecoveryFailurePersistence) {
			return ErrInvalidRecoveryRecord
		}
	}
	return nil
}
