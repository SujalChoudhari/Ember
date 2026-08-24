package models

import (
	"errors"
	"strings"
)

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

// ProviderMetadata identifies the bounded provider registration for a resource.
// It intentionally contains no arbitrary properties or secret-bearing payload.
type ProviderMetadata struct {
	Namespace string
	Type      string
	Version   string
}

type ResourceState string

const (
	ResourceStateUnknown  ResourceState = "unknown"
	ResourceStatePending  ResourceState = "pending"
	ResourceStateReady    ResourceState = "ready"
	ResourceStateFailed   ResourceState = "failed"
	ResourceStateDeleting ResourceState = "deleting"
)

const (
	MaxResourceIDLength        = 128
	MaxResourceNameLength      = 128
	MaxParentIDLength          = 128
	MaxProviderNamespaceLength = 64
	MaxProviderTypeLength      = 128
	MaxProviderVersionLength   = 32
	MaxResourceStateLength     = 32
)

func (metadata ProviderMetadata) valid() bool {
	return validOptionalBoundedText(metadata.Namespace, MaxProviderNamespaceLength) &&
		validOptionalBoundedText(metadata.Type, MaxProviderTypeLength) &&
		validOptionalBoundedText(metadata.Version, MaxProviderVersionLength)
}

func validOptionalBoundedText(value string, maxLength int) bool {
	return value == "" || (strings.TrimSpace(value) != "" && len(value) <= maxLength)
}

func (state ResourceState) valid() bool {
	switch state {
	case "", ResourceStateUnknown, ResourceStatePending, ResourceStateReady, ResourceStateFailed, ResourceStateDeleting:
		return true
	default:
		return false
	}
}

type ResourceSpec struct {
	Type         ResourceType
	Name         string
	ParentID     string
	Provider     ProviderMetadata
	DesiredState ResourceState
}

type Resource struct {
	ID            string
	Spec          ResourceSpec
	ObservedState ResourceState
}

var (
	ErrInvalidResourceSpec = errors.New("invalid resource spec")
	ErrInvalidResource     = errors.New("invalid resource")
)

func (spec ResourceSpec) Validate() error {
	if !spec.Type.valid() || strings.TrimSpace(spec.Name) == "" || len(spec.Name) > MaxResourceNameLength {
		return ErrInvalidResourceSpec
	}
	if spec.ParentID != "" && (strings.TrimSpace(spec.ParentID) == "" || len(spec.ParentID) > MaxParentIDLength) {
		return ErrInvalidResourceSpec
	}
	if !spec.Provider.valid() || len(string(spec.DesiredState)) > MaxResourceStateLength || !spec.DesiredState.valid() {
		return ErrInvalidResourceSpec
	}
	return nil
}

func (resource Resource) Validate() error {
	if strings.TrimSpace(resource.ID) == "" || len(resource.ID) > MaxResourceIDLength ||
		resource.Spec.Validate() != nil || len(string(resource.ObservedState)) > MaxResourceStateLength ||
		!resource.ObservedState.valid() {
		return ErrInvalidResource
	}
	return nil
}
