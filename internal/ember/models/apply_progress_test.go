package models

import (
	"errors"
	"testing"
	"time"
)

func TestApplyProgressRecordValidatesBoundedPartialFailureState(t *testing.T) {
	now := time.Unix(100, 0).UTC()
	record := ApplyProgressRecord{
		ID:            "apply-request-1",
		RequestID:     "request-1",
		CorrelationID: "correlation-1",
		Status:        ApplyProgressFailed,
		OperationIDs:  []string{"operation-1"},
		Entries: []ApplyProgressEntry{
			{LogicalID: "group-platform", Action: "create", Status: ApplyProgressSucceeded, OperationID: "operation-1", StartedAt: now, CompletedAt: now},
			{LogicalID: "bucket-assets", Action: "create", Status: ApplyProgressFailed, Failure: "apply authority failure", StartedAt: now, CompletedAt: now},
			{LogicalID: "bucket-extra", Action: "create", Status: ApplyProgressPending},
		},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := record.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	record.Entries[1].Failure = "provider secret value"
	if !errors.Is(record.Validate(), ErrInvalidApplyProgressRecord) {
		t.Fatalf("secret-bearing failure Validate() = %v, want ErrInvalidApplyProgressRecord", record.Validate())
	}
}
