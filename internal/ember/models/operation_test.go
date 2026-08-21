package models

import (
	"errors"
	"testing"
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
			op := Operation{ID: "operation-1", ResourceID: "resource-1", Status: status}
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
