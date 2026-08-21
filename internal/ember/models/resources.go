package models

import (
	"errors"
	"strings"
)

// ResourceType identifies the domain kind of a resource.
type ResourceType string

const (
	ResourceTypeGroup  ResourceType = "group"
	ResourceTypeBucket ResourceType = "bucket"
)

func (resourceType ResourceType) valid() bool {
	switch resourceType {
	case ResourceTypeGroup, ResourceTypeBucket:
		return true
	default:
		return false
	}
}

// OperationStatus describes the lifecycle state of an operation.
type OperationStatus string

const (
	OperationStatusPending   OperationStatus = "pending"
	OperationStatusRunning   OperationStatus = "running"
	OperationStatusSucceeded OperationStatus = "succeeded"
	OperationStatusFailed    OperationStatus = "failed"
)

func (status OperationStatus) valid() bool {
	switch status {
	case OperationStatusPending, OperationStatusRunning, OperationStatusSucceeded, OperationStatusFailed:
		return true
	default:
		return false
	}
}

// ResourceSpec contains the desired, user-provided identity and placement of a resource.
type ResourceSpec struct {
	Type     ResourceType
	Name     string
	ParentID string
}

// Resource is the control-plane representation of a resource.
type Resource struct {
	ID   string
	Spec ResourceSpec
}

// Operation records the control-plane work associated with a resource.
type Operation struct {
	ID         string
	ResourceID string
	Status     OperationStatus
}

var (
	ErrInvalidResourceSpec = errors.New("invalid resource spec")
	ErrInvalidResource     = errors.New("invalid resource")
	ErrInvalidOperation    = errors.New("invalid operation")
)

// Validate checks the invariants required for a resource specification.
func (spec ResourceSpec) Validate() error {
	if !spec.Type.valid() || strings.TrimSpace(spec.Name) == "" {
		return ErrInvalidResourceSpec
	}
	if spec.ParentID != "" && strings.TrimSpace(spec.ParentID) == "" {
		return ErrInvalidResourceSpec
	}
	return nil
}

// Validate checks the invariants required for a resource.
func (resource Resource) Validate() error {
	if strings.TrimSpace(resource.ID) == "" || resource.Spec.Validate() != nil {
		return ErrInvalidResource
	}
	return nil
}

// Validate checks the invariants required for an operation.
func (operation Operation) Validate() error {
	if strings.TrimSpace(operation.ID) == "" ||
		strings.TrimSpace(operation.ResourceID) == "" ||
		!operation.Status.valid() {
		return ErrInvalidOperation
	}
	return nil
}
