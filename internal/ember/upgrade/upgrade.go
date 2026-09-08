package upgrade

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const (
	VersionV1            = "v1"
	CurrentVersion       = "v2"
	MaxSnapshotBytes     = 1 << 20
	MaxSnapshotResources = 100
	MaxEvidenceBytes     = 512
)

const (
	StepValidateLegacyState = "validate-legacy-state"
	StepMigrateV1ToV2       = "migrate-v1-to-v2"
)

const (
	EvidenceSucceeded = "succeeded"
	EvidenceFailed    = "failed"

	FailureSnapshotTooLarge   = "snapshot-too-large"
	FailureMalformedSnapshot  = "malformed-snapshot"
	FailureUnsupportedVersion = "unsupported-version"
	FailureIncompatibleState  = "incompatible-state"
	FailureCancelled          = "cancelled"
)

var (
	ErrSnapshotTooLarge     = errors.New("upgrade snapshot exceeds size limit")
	ErrMalformedSnapshot    = errors.New("malformed upgrade snapshot")
	ErrUnsupportedVersion   = errors.New("unsupported upgrade version")
	ErrIncompatibleState    = errors.New("incompatible upgrade state")
	ErrUpgradeCancelled     = errors.New("upgrade cancelled")
	ErrInvalidTargetVersion = errors.New("invalid upgrade target version")
)

// Snapshot is the safe, bounded Ember control-plane state transferred between
// supported schema versions. It deliberately contains resource metadata only;
// provider payloads and secret-bearing state are not part of the upgrade input.
type Snapshot struct {
	Version   string            `json:"version"`
	Resources []models.Resource `json:"resources"`
}

// Plan is the deterministic, non-mutating compatibility result for one
// snapshot. A blank Step means the snapshot is already at the target version.
type Plan struct {
	SourceVersion string
	TargetVersion string
	Step          string
}

// Evidence is the bounded, redacted outcome of preflight or migration. It
// contains stable classes and identifiers only, never raw input or error text.
type Evidence struct {
	Status        string
	Code          string
	Step          string
	SourceVersion string
	TargetVersion string
}

func (e Evidence) String() string {
	value := fmt.Sprintf("status=%s code=%s step=%s source=%s target=%s", safeEvidenceText(e.Status), safeEvidenceText(e.Code), safeEvidenceText(e.Step), safeEvidenceText(e.SourceVersion), safeEvidenceText(e.TargetVersion))
	if len(value) > MaxEvidenceBytes {
		return value[:MaxEvidenceBytes]
	}
	return value
}

// Result contains the migrated state and its redacted evidence. On failure the
// snapshot is empty and Evidence remains available to an operator or caller.
type Result struct {
	Snapshot Snapshot
	Evidence Evidence
}

func EncodeSnapshot(snapshot Snapshot) ([]byte, error) {
	if err := validateSnapshot(snapshot); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return nil, ErrIncompatibleState
	}
	data = append(data, '\n')
	if len(data) > MaxSnapshotBytes {
		return nil, ErrSnapshotTooLarge
	}
	return data, nil
}

func DecodeSnapshot(data []byte) (Snapshot, error) {
	if len(data) > MaxSnapshotBytes {
		return Snapshot{}, ErrSnapshotTooLarge
	}
	return decodeSnapshot(data)
}

// Preflight validates compatibility without changing state or executing a
// migration. Only v1-to-v2 and already-current v2 state are supported.
func Preflight(data []byte, targetVersion string) (Plan, error) {
	if targetVersion != CurrentVersion {
		return Plan{}, ErrInvalidTargetVersion
	}
	snapshot, err := decodeSnapshot(data)
	if err != nil {
		return Plan{}, err
	}
	switch snapshot.Version {
	case VersionV1:
		return Plan{SourceVersion: VersionV1, TargetVersion: CurrentVersion, Step: StepMigrateV1ToV2}, nil
	case CurrentVersion:
		return Plan{SourceVersion: CurrentVersion, TargetVersion: CurrentVersion}, nil
	default:
		return Plan{}, ErrUnsupportedVersion
	}
}

// Upgrade performs a bounded v1-to-v2 migration after a successful preflight.
// It never writes files or emits input data; persistence remains the caller's
// responsibility, allowing the caller to commit only the validated result.
func Upgrade(ctx context.Context, data []byte, targetVersion string) (Result, error) {
	result := Result{Evidence: Evidence{TargetVersion: boundedVersion(targetVersion)}}
	if ctx == nil || ctx.Err() != nil {
		result.Evidence = failureEvidence("", targetVersion, "", FailureCancelled)
		return result, ErrUpgradeCancelled
	}
	if len(data) > MaxSnapshotBytes {
		result.Evidence = failureEvidence("", targetVersion, "", FailureSnapshotTooLarge)
		return result, ErrSnapshotTooLarge
	}

	plan, err := Preflight(data, targetVersion)
	if err != nil {
		step := ""
		if errors.Is(err, ErrIncompatibleState) {
			step = StepValidateLegacyState
		}
		result.Evidence = failureEvidence("", targetVersion, step, failureCode(err))
		if snapshot, decodeErr := decodeSnapshot(data); decodeErr == nil {
			result.Evidence.SourceVersion = boundedVersion(snapshot.Version)
		}
		return result, err
	}
	result.Evidence.SourceVersion = plan.SourceVersion
	result.Evidence.TargetVersion = plan.TargetVersion
	result.Evidence.Step = plan.Step

	snapshot, err := decodeSnapshot(data)
	if err != nil {
		result.Evidence.Status = EvidenceFailed
		result.Evidence.Code = failureCode(err)
		return result, err
	}
	if err := ctx.Err(); err != nil {
		result.Evidence.Status = EvidenceFailed
		result.Evidence.Code = FailureCancelled
		return result, ErrUpgradeCancelled
	}
	if plan.Step == StepMigrateV1ToV2 {
		snapshot.Version = CurrentVersion
	}
	if err := validateSnapshot(snapshot); err != nil {
		result.Evidence.Status = EvidenceFailed
		result.Evidence.Code = FailureIncompatibleState
		result.Evidence.Step = StepValidateLegacyState
		return result, err
	}
	result.Snapshot = snapshot
	result.Evidence.Status = EvidenceSucceeded
	result.Evidence.Code = "none"
	return result, nil
}

func decodeSnapshot(data []byte) (Snapshot, error) {
	if len(data) > MaxSnapshotBytes {
		return Snapshot{}, ErrSnapshotTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var snapshot Snapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, ErrMalformedSnapshot
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Snapshot{}, ErrMalformedSnapshot
	}
	if snapshot.Version != VersionV1 && snapshot.Version != CurrentVersion {
		return Snapshot{}, ErrUnsupportedVersion
	}
	if snapshot.Version == VersionV1 {
		for index := range snapshot.Resources {
			if snapshot.Resources[index].ObservedState == "" {
				snapshot.Resources[index].ObservedState = models.ResourceStateUnknown
			}
		}
	}
	if err := validateSnapshot(snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func validateSnapshot(snapshot Snapshot) error {
	if snapshot.Version != VersionV1 && snapshot.Version != CurrentVersion {
		return ErrUnsupportedVersion
	}
	if len(snapshot.Resources) > MaxSnapshotResources {
		return ErrIncompatibleState
	}
	resources := make(map[string]models.Resource, len(snapshot.Resources))
	for _, resource := range snapshot.Resources {
		if snapshot.Version == CurrentVersion && resource.ObservedState == "" {
			return ErrIncompatibleState
		}
		if err := resource.Validate(); err != nil {
			return ErrIncompatibleState
		}
		if _, exists := resources[resource.ID]; exists {
			return ErrIncompatibleState
		}
		resources[resource.ID] = resource
	}
	for _, resource := range snapshot.Resources {
		if resource.Spec.ParentID != "" {
			if _, exists := resources[resource.Spec.ParentID]; !exists || hasParentCycle(resources, resource.ID) {
				return ErrIncompatibleState
			}
		}
	}
	return nil
}

func hasParentCycle(resources map[string]models.Resource, resourceID string) bool {
	visited := make(map[string]struct{})
	for resourceID != "" {
		if _, exists := visited[resourceID]; exists {
			return true
		}
		visited[resourceID] = struct{}{}
		resource, exists := resources[resourceID]
		if !exists {
			return false
		}
		resourceID = resource.Spec.ParentID
	}
	return false
}

func failureEvidence(sourceVersion, targetVersion, step, code string) Evidence {
	return Evidence{
		Status:        EvidenceFailed,
		Code:          safeEvidenceText(code),
		Step:          safeEvidenceText(step),
		SourceVersion: boundedVersion(sourceVersion),
		TargetVersion: boundedVersion(targetVersion),
	}
}

func failureCode(err error) string {
	switch {
	case errors.Is(err, ErrSnapshotTooLarge):
		return FailureSnapshotTooLarge
	case errors.Is(err, ErrUnsupportedVersion):
		return FailureUnsupportedVersion
	case errors.Is(err, ErrIncompatibleState):
		return FailureIncompatibleState
	case errors.Is(err, ErrUpgradeCancelled):
		return FailureCancelled
	case errors.Is(err, ErrInvalidTargetVersion):
		return FailureUnsupportedVersion
	default:
		return FailureMalformedSnapshot
	}
}

func boundedVersion(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 32 {
		return "invalid"
	}
	return value
}

func safeEvidenceText(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "none"
	}
	if len(value) > 64 {
		return "invalid"
	}
	return value
}
