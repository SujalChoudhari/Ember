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
