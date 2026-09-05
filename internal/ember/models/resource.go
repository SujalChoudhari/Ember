package models

import (
	"errors"
	"strings"
)

type ResourceType string

const (
	ResourceTypeGroup    ResourceType = "group"
	ResourceTypeBucket   ResourceType = "bucket"
	ResourceTypeWorkload ResourceType = "workload"
)

func (resourceType ResourceType) valid() bool {
	switch resourceType {
	case ResourceTypeGroup, ResourceTypeBucket, ResourceTypeWorkload:
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

type ResourceLock struct {
	Owner string
	Token string
}

const (
	MaxResourceIDLength          = 128
	MaxResourceNameLength        = 128
	MaxParentIDLength            = 128
	MaxResourceTagCount          = 32
	MaxResourceTagKeyLength      = 64
	MaxResourceTagValueLength    = 256
	MaxProviderNamespaceLength   = 64
	MaxProviderTypeLength        = 128
	MaxProviderVersionLength     = 32
	MaxResourceStateLength       = 32
	MaxResourceLockOwnerLength   = 128
	MaxResourceLockTokenLength   = 128
	MaxWorkloadExecutionIDLength = 128
	MaxWorkloadReasonLength      = 256
)

func (metadata ProviderMetadata) valid() bool {
	return validOptionalBoundedText(metadata.Namespace, MaxProviderNamespaceLength) &&
		validOptionalBoundedText(metadata.Type, MaxProviderTypeLength) &&
		validOptionalBoundedText(metadata.Version, MaxProviderVersionLength)
}

func validOptionalBoundedText(value string, maxLength int) bool {
	return value == "" || (strings.TrimSpace(value) != "" && len(value) <= maxLength)
}

func (lock ResourceLock) Validate() error {
	if strings.TrimSpace(lock.Owner) == "" || len(lock.Owner) > MaxResourceLockOwnerLength ||
		strings.TrimSpace(lock.Token) == "" || len(lock.Token) > MaxResourceLockTokenLength {
		return ErrInvalidResourceLock
	}
	return nil
}

func validTags(tags map[string]string) bool {
	if len(tags) > MaxResourceTagCount {
		return false
	}
	for key, value := range tags {
		if strings.TrimSpace(key) == "" || len(key) > MaxResourceTagKeyLength || len(value) > MaxResourceTagValueLength {
			return false
		}
	}
	return true
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
	Tags         map[string]string
	Provider     ProviderMetadata
	DesiredState ResourceState
}

type Resource struct {
	ID            string
	Spec          ResourceSpec
	ObservedState ResourceState
}

// WorkloadStatus is the stable, value-bounded status returned by a workload
// provider. It intentionally contains no provider-specific payload or error.
type WorkloadStatus struct {
	ObservedState ResourceState
	Reason        string
	ExecutionID   string
}

var (
	ErrInvalidResourceSpec   = errors.New("invalid resource spec")
	ErrInvalidResource       = errors.New("invalid resource")
	ErrInvalidResourceLock   = errors.New("invalid resource lock")
	ErrInvalidWorkloadStatus = errors.New("invalid workload status")
)

func (spec ResourceSpec) Validate() error {
	if !spec.Type.valid() || strings.TrimSpace(spec.Name) == "" || len(spec.Name) > MaxResourceNameLength {
		return ErrInvalidResourceSpec
	}
	if spec.ParentID != "" && (strings.TrimSpace(spec.ParentID) == "" || len(spec.ParentID) > MaxParentIDLength) {
		return ErrInvalidResourceSpec
	}
	if !validTags(spec.Tags) || !spec.Provider.valid() || len(string(spec.DesiredState)) > MaxResourceStateLength || !spec.DesiredState.valid() {
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

func (status WorkloadStatus) Validate() error {
	if !status.ObservedState.valid() || !validOptionalBoundedText(status.Reason, MaxWorkloadReasonLength) ||
		!validOptionalBoundedText(status.ExecutionID, MaxWorkloadExecutionIDLength) {
		return ErrInvalidWorkloadStatus
	}
	return nil
}
