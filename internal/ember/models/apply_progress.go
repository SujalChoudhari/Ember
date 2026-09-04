package models

import (
	"errors"
	"strings"
	"time"
)

type ApplyProgressStatus string

const (
	ApplyProgressPending    ApplyProgressStatus = "pending"
	ApplyProgressInProgress ApplyProgressStatus = "in_progress"
	ApplyProgressSucceeded  ApplyProgressStatus = "succeeded"
	ApplyProgressFailed     ApplyProgressStatus = "failed"
)

const (
	ApplyProgressActionCreate = "create"
	ApplyProgressActionUpdate = "update"
	ApplyProgressActionDelete = "delete"
	ApplyProgressActionNoOp   = "no_op"

	ApplyProgressFailureAuthority        = "apply authority failure"
	ApplyProgressFailureIncompleteResult = "apply returned incomplete result"
	ApplyProgressFailureCancelled        = "apply cancelled"
	ApplyProgressFailurePersistence      = "apply progress persistence failure"

	MaxApplyProgressIDLength            = 128
	MaxApplyProgressRequestIDLength     = 128
	MaxApplyProgressCorrelationIDLength = 128
	MaxApplyProgressEntries             = 100
	MaxApplyProgressOperationIDs        = 100
	MaxApplyProgressFailureLength       = 64
)

// ApplyProgressEntry is the bounded, redacted state for one planned resource action.
type ApplyProgressEntry struct {
	LogicalID   string              `json:"logical_id"`
	ResourceID  string              `json:"resource_id,omitempty"`
	Action      string              `json:"action"`
	Status      ApplyProgressStatus `json:"status"`
	OperationID string              `json:"operation_id,omitempty"`
	Failure     string              `json:"failure,omitempty"`
	StartedAt   time.Time           `json:"started_at,omitempty"`
	CompletedAt time.Time           `json:"completed_at,omitempty"`
}

// ApplyProgressRecord is the durable, inspection-safe progress record for one apply request.
// It contains identifiers and stable failure classes only; it never stores provider payloads.
type ApplyProgressRecord struct {
	ID            string               `json:"id"`
	RequestID     string               `json:"request_id"`
	CorrelationID string               `json:"correlation_id"`
	Status        ApplyProgressStatus  `json:"status"`
	OperationIDs  []string             `json:"operation_ids,omitempty"`
	Entries       []ApplyProgressEntry `json:"entries"`
	CreatedAt     time.Time            `json:"created_at"`
	UpdatedAt     time.Time            `json:"updated_at"`
}

var ErrInvalidApplyProgressRecord = errors.New("invalid apply progress record")

func validApplyProgressText(value string, maxLength int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maxLength
}

func (status ApplyProgressStatus) valid() bool {
	switch status {
	case ApplyProgressPending, ApplyProgressInProgress, ApplyProgressSucceeded, ApplyProgressFailed:
		return true
	default:
		return false
	}
}

func (entry ApplyProgressEntry) Validate() error {
	if !validApplyProgressText(entry.LogicalID, MaxApplyProgressIDLength) ||
		(entry.ResourceID != "" && !validApplyProgressText(entry.ResourceID, MaxApplyProgressIDLength)) ||
		!validApplyProgressText(entry.Action, 16) ||
		!entry.Status.valid() ||
		(entry.OperationID != "" && !validApplyProgressText(entry.OperationID, MaxApplyProgressIDLength)) ||
		len(entry.Failure) > MaxApplyProgressFailureLength {
		return ErrInvalidApplyProgressRecord
	}
	switch entry.Action {
	case ApplyProgressActionCreate, ApplyProgressActionUpdate, ApplyProgressActionDelete, ApplyProgressActionNoOp:
	default:
		return ErrInvalidApplyProgressRecord
	}
	if entry.StartedAt.IsZero() != (entry.Status == ApplyProgressPending) ||
		entry.CompletedAt.IsZero() != (entry.Status == ApplyProgressPending || entry.Status == ApplyProgressInProgress) {
		return ErrInvalidApplyProgressRecord
	}
	if !entry.CompletedAt.IsZero() && entry.CompletedAt.Before(entry.StartedAt) {
		return ErrInvalidApplyProgressRecord
	}
	switch entry.Status {
	case ApplyProgressPending, ApplyProgressInProgress:
		if entry.OperationID != "" || entry.Failure != "" {
			return ErrInvalidApplyProgressRecord
		}
	case ApplyProgressSucceeded:
		if entry.Failure != "" || (entry.Action != ApplyProgressActionNoOp && entry.OperationID == "") {
			return ErrInvalidApplyProgressRecord
		}
	case ApplyProgressFailed:
		if entry.Failure != ApplyProgressFailureAuthority && entry.Failure != ApplyProgressFailureIncompleteResult && entry.Failure != ApplyProgressFailureCancelled && entry.Failure != ApplyProgressFailurePersistence {
			return ErrInvalidApplyProgressRecord
		}
	}
	return nil
}

func (record ApplyProgressRecord) Validate() error {
	if !validApplyProgressText(record.ID, MaxApplyProgressIDLength) ||
		!validApplyProgressText(record.RequestID, MaxApplyProgressRequestIDLength) ||
		!validApplyProgressText(record.CorrelationID, MaxApplyProgressCorrelationIDLength) ||
		!record.Status.valid() ||
		record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() || record.UpdatedAt.Before(record.CreatedAt) ||
		len(record.Entries) == 0 || len(record.Entries) > MaxApplyProgressEntries ||
		len(record.OperationIDs) > MaxApplyProgressOperationIDs {
		return ErrInvalidApplyProgressRecord
	}

	operations := make(map[string]struct{}, len(record.OperationIDs))
	for _, operationID := range record.OperationIDs {
		if !validApplyProgressText(operationID, MaxApplyProgressIDLength) {
			return ErrInvalidApplyProgressRecord
		}
		if _, exists := operations[operationID]; exists {
			return ErrInvalidApplyProgressRecord
		}
		operations[operationID] = struct{}{}
	}
	logicalIDs := make(map[string]struct{}, len(record.Entries))
	for _, entry := range record.Entries {
		if _, exists := logicalIDs[entry.LogicalID]; exists || entry.Validate() != nil {
			return ErrInvalidApplyProgressRecord
		}
		logicalIDs[entry.LogicalID] = struct{}{}
		if entry.OperationID != "" {
			if _, exists := operations[entry.OperationID]; !exists {
				return ErrInvalidApplyProgressRecord
			}
		}
	}
	return nil
}
