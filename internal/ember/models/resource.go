package models

import (
	"errors"
	"strings"
	"time"
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

type WorkloadHealth string

const (
	WorkloadHealthUnknown   WorkloadHealth = "unknown"
	WorkloadHealthHealthy   WorkloadHealth = "healthy"
	WorkloadHealthUnhealthy WorkloadHealth = "unhealthy"
)

type WorkloadReadiness string

const (
	WorkloadReadinessUnknown  WorkloadReadiness = "unknown"
	WorkloadReadinessReady    WorkloadReadiness = "ready"
	WorkloadReadinessNotReady WorkloadReadiness = "not-ready"
)

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

func (health WorkloadHealth) valid() bool {
	switch health {
	case "", WorkloadHealthUnknown, WorkloadHealthHealthy, WorkloadHealthUnhealthy:
		return true
	default:
		return false
	}
}

func (readiness WorkloadReadiness) valid() bool {
	switch readiness {
	case "", WorkloadReadinessUnknown, WorkloadReadinessReady, WorkloadReadinessNotReady:
		return true
	default:
		return false
	}
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
	Health        WorkloadHealth
	Readiness     WorkloadReadiness
	Reason        string
	ExecutionID   string
}

type WorkloadLog struct {
	Timestamp   time.Time
	Stream      string
	Message     string
	ExecutionID string
}

// WorkloadVolume describes bounded, provider-owned volume metadata. The path is
// an opaque ownership path; volume payload bytes are intentionally outside the
// workload control-plane contract.
type WorkloadVolume struct {
	ID         string
	WorkloadID string
	Name       string
	Path       string
	MaxBytes   int64
	UsedBytes  int64
}

const (
	MaxWorkloadVolumeIDLength   = 128
	MaxWorkloadVolumeNameLength = 128
	MaxWorkloadVolumePathLength = 512
	MaxWorkloadVolumeBytes      = 1 << 30
)

var (
	ErrInvalidResourceSpec   = errors.New("invalid resource spec")
	ErrInvalidResource       = errors.New("invalid resource")
	ErrInvalidResourceLock   = errors.New("invalid resource lock")
	ErrInvalidWorkloadStatus = errors.New("invalid workload status")
	ErrInvalidWorkloadLog    = errors.New("invalid workload log")
	ErrInvalidWorkloadVolume = errors.New("invalid workload volume")
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

func (volume WorkloadVolume) Validate() error {
	if strings.TrimSpace(volume.ID) == "" || len(volume.ID) > MaxWorkloadVolumeIDLength ||
		strings.TrimSpace(volume.WorkloadID) == "" || len(volume.WorkloadID) > MaxResourceIDLength ||
		strings.TrimSpace(volume.Name) == "" || len(volume.Name) > MaxWorkloadVolumeNameLength ||
		strings.TrimSpace(volume.Path) == "" || len(volume.Path) > MaxWorkloadVolumePathLength ||
		volume.MaxBytes <= 0 || volume.MaxBytes > MaxWorkloadVolumeBytes ||
		volume.UsedBytes < 0 || volume.UsedBytes > volume.MaxBytes {
		return ErrInvalidWorkloadVolume
	}
	return nil
}

func (status WorkloadStatus) Validate() error {
	if !status.ObservedState.valid() || !status.Health.valid() || !status.Readiness.valid() ||
		!validOptionalBoundedText(status.Reason, MaxWorkloadReasonLength) ||
		!validOptionalBoundedText(status.ExecutionID, MaxWorkloadExecutionIDLength) {
		return ErrInvalidWorkloadStatus
	}
	return nil
}

const (
	MaxWorkloadLogStreamLength  = 16
	MaxWorkloadLogMessageLength = 512
)

func (log WorkloadLog) Validate() error {
	if log.Timestamp.IsZero() || strings.TrimSpace(log.Stream) == "" || len(log.Stream) > MaxWorkloadLogStreamLength ||
		strings.TrimSpace(log.Message) == "" || len(log.Message) > MaxWorkloadLogMessageLength ||
		!validOptionalBoundedText(log.ExecutionID, MaxWorkloadExecutionIDLength) {
		return ErrInvalidWorkloadLog
	}
	switch log.Stream {
	case "stdout", "stderr", "system":
		return nil
	default:
		return ErrInvalidWorkloadLog
	}
}
