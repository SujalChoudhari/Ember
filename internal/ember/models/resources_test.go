package models

import (
	"errors"
	"testing"
)

func TestResourceSpecValidate(t *testing.T) {
	tests := []struct {
		name string
		spec ResourceSpec
		want error
	}{
		{
			name: "group",
			spec: ResourceSpec{Type: ResourceTypeGroup, Name: "accounts"},
		},
		{
			name: "bucket with parent",
			spec: ResourceSpec{Type: ResourceTypeBucket, Name: "assets", ParentID: "group-1"},
		},
		{
			name: "missing type",
			spec: ResourceSpec{Name: "accounts"},
			want: ErrInvalidResourceSpec,
		},
		{
			name: "unsupported type",
			spec: ResourceSpec{Type: ResourceType("unsupported"), Name: "accounts"},
			want: ErrInvalidResourceSpec,
		},
		{
			name: "missing name",
			spec: ResourceSpec{Type: ResourceTypeGroup},
			want: ErrInvalidResourceSpec,
		},
		{
			name: "blank name",
			spec: ResourceSpec{Type: ResourceTypeGroup, Name: " \t"},
			want: ErrInvalidResourceSpec,
		},
		{
			name: "blank parent id",
			spec: ResourceSpec{Type: ResourceTypeBucket, Name: "assets", ParentID: " "},
			want: ErrInvalidResourceSpec,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.spec.Validate()
			if !errors.Is(err, tt.want) {
				t.Fatalf("Validate() error = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestResourceValidate(t *testing.T) {
	validSpec := ResourceSpec{Type: ResourceTypeGroup, Name: "accounts"}

	tests := []struct {
		name     string
		resource Resource
		want     error
	}{
		{
			name:     "valid resource",
			resource: Resource{ID: "resource-1", Spec: validSpec},
		},
		{
			name:     "missing id",
			resource: Resource{Spec: validSpec},
			want:     ErrInvalidResource,
		},
		{
			name:     "blank id",
			resource: Resource{ID: " ", Spec: validSpec},
			want:     ErrInvalidResource,
		},
		{
			name:     "invalid spec",
			resource: Resource{ID: "resource-1"},
			want:     ErrInvalidResource,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.resource.Validate()
			if !errors.Is(err, tt.want) {
				t.Fatalf("Validate() error = %v, want %v", err, tt.want)
			}
		})
	}
}

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
