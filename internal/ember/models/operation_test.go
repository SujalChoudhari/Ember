package models

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOperationValidate(t *testing.T) {
	statuses := []OperationStatus{
		OperationStatusPending,
		OperationStatusRunning,
		OperationStatusSucceeded,
		OperationStatusFailed,
	}
	for _, status := range statuses {
		t.Run(string(status), func(t *testing.T) {
			createdAt := time.Unix(1, 0).UTC()
			op := Operation{
				ID:            "operation-1",
				ResourceID:    "resource-1",
				CorrelationID: "correlation-1",
				RequestID:     "request-1",
				Status:        status,
				CreatedAt:     createdAt,
				UpdatedAt:     createdAt,
			}
			if status == OperationStatusSucceeded || status == OperationStatusFailed {
				op.UpdatedAt = createdAt.Add(time.Second)
				op.Outcome = "completed"
			}
			if err := op.Validate(); err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}

	tests := []struct {
		name      string
		operation Operation
	}{
		{
			name:      "missing id",
			operation: Operation{ResourceID: "resource-1", Status: OperationStatusPending},
		},
		{
			name:      "missing resource id",
			operation: Operation{ID: "operation-1", Status: OperationStatusPending},
		},
		{
			name:      "invalid status",
			operation: Operation{ID: "operation-1", ResourceID: "resource-1", Status: OperationStatus("unknown")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.operation.Validate(); !errors.Is(err, ErrInvalidOperation) {
				t.Fatalf("Validate() error = %v, want %v", err, ErrInvalidOperation)
			}
		})
	}
}

func TestOperationRequiresBoundedMetadata(t *testing.T) {
	operation := Operation{
		ID:         "operation-1",
		ResourceID: "resource-1",
		Status:     OperationStatusPending,
	}

	if err := operation.Validate(); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("Validate() error = %v, want %v for missing operation metadata", err, ErrInvalidOperation)
	}
}

func TestInProgressOperationCannotHaveOutcome(t *testing.T) {
	createdAt := time.Unix(10, 0).UTC()
	operation := Operation{
		ID:            "operation-1",
		ResourceID:    "resource-1",
		CorrelationID: "correlation-1",
		RequestID:     "request-1",
		Status:        OperationStatusRunning,
		CreatedAt:     createdAt,
		Outcome:       "still running",
	}

	if err := operation.Validate(); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("Validate() error = %v, want %v for in-progress operation with outcome", err, ErrInvalidOperation)
	}
}

func TestTerminalOperationRequiresOutcome(t *testing.T) {
	createdAt := time.Unix(10, 0).UTC()
	operation := Operation{
		ID:            "operation-1",
		ResourceID:    "resource-1",
		CorrelationID: "correlation-1",
		RequestID:     "request-1",
		Status:        OperationStatusSucceeded,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt.Add(time.Second),
	}

	if err := operation.Validate(); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("Validate() error = %v, want %v for terminal operation without outcome", err, ErrInvalidOperation)
	}
}

func TestOperationRejectsInvertedTimestamp(t *testing.T) {
	createdAt := time.Unix(10, 0).UTC()
	operation := Operation{
		ID:            "operation-1",
		ResourceID:    "resource-1",
		CorrelationID: "correlation-1",
		RequestID:     "request-1",
		Status:        OperationStatusSucceeded,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt.Add(-time.Second),
		Outcome:       "completed",
	}

	if err := operation.Validate(); !errors.Is(err, ErrInvalidOperation) {
		t.Fatalf("Validate() error = %v, want %v for inverted timestamp", err, ErrInvalidOperation)
	}
}

func TestOperationRejectsUnboundedMetadata(t *testing.T) {
	base := Operation{
		ID:            "operation-1",
		ResourceID:    "resource-1",
		CorrelationID: "correlation-1",
		RequestID:     "request-1",
		Status:        OperationStatusPending,
		CreatedAt:     time.Unix(1, 0).UTC(),
		UpdatedAt:     time.Unix(1, 0).UTC(),
	}
	tests := []struct {
		name      string
		operation Operation
	}{
		{
			name: "overlong operation id",
			operation: func() Operation {
				operation := base
				operation.ID = strings.Repeat("o", MaxOperationIDLength+1)
				return operation
			}(),
		},
		{
			name: "overlong correlation id",
			operation: func() Operation {
				operation := base
				operation.CorrelationID = strings.Repeat("c", MaxOperationCorrelationIDLength+1)
				return operation
			}(),
		},
		{
			name: "overlong request id",
			operation: func() Operation {
				operation := base
				operation.RequestID = strings.Repeat("r", MaxOperationRequestIDLength+1)
				return operation
			}(),
		},
		{
			name: "overlong outcome",
			operation: func() Operation {
				operation := base
				operation.Outcome = strings.Repeat("x", MaxOperationOutcomeLength+1)
				return operation
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.operation.Validate(); !errors.Is(err, ErrInvalidOperation) {
				t.Fatalf("Validate() error = %v, want %v", err, ErrInvalidOperation)
			}
		})
	}
}
