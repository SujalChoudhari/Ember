package models

import (
	"testing"
	"time"
)

func TestRecoveryRecordValidateAcceptsBoundedLinkedOutcome(t *testing.T) {
	now := time.Unix(500, 0).UTC()
	record := RecoveryRecord{
		ID:                 "recovery-1",
		RecoveryRequestID:  "recover-request-1",
		ApplyProgressID:    "apply-1",
		ApplyRequestID:     "apply-request-1",
		ApplyCorrelationID: "apply-correlation-1",
		Action:             RecoveryActionRollback,
		Status:             RecoveryStatusSucceeded,
		Outcome:            RecoveryOutcomeRecovered,
		OperationIDs:       []string{"operation-1"},
		EntryLogicalIDs:    []string{"group/platform"},
		CompletedEntries:   []string{"group/platform"},
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

func TestRecoveryRecordValidateRejectsUnboundedFailure(t *testing.T) {
	now := time.Unix(501, 0).UTC()
	record := RecoveryRecord{
		ID:                 "recovery-1",
		RecoveryRequestID:  "recover-request-1",
		ApplyProgressID:    "apply-1",
		ApplyRequestID:     "apply-request-1",
		ApplyCorrelationID: "apply-correlation-1",
		Action:             RecoveryActionForward,
		Status:             RecoveryStatusFailed,
		Outcome:            RecoveryOutcomeFailed,
		Failure:            "secret-value-that-must-not-be-persisted",
		EntryLogicalIDs:    []string{"group/platform"},
		CreatedAt:          now,
		UpdatedAt:          now,
	}
	if err := record.Validate(); err == nil {
		t.Fatal("Validate() error = nil, want bounded failure classification rejection")
	}
}
