package upgrade

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func TestUpgradeMigratesGoldenV1StateToCurrentVersion(t *testing.T) {
	legacy := []byte(`{
  "version": "v1",
  "resources": [
    {
      "ID": "resource-1",
      "Spec": {
        "Type": "group",
        "Name": "platform",
        "Tags": {"environment": "test"},
        "Provider": {"Namespace": "Ember.Resource", "Type": "groups", "Version": "v1"},
        "DesiredState": "ready"
      }
    }
  ]
}`)

	result, err := Upgrade(context.Background(), legacy, CurrentVersion)
	if err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}
	if result.Evidence.Status != EvidenceSucceeded || result.Evidence.SourceVersion != VersionV1 || result.Evidence.TargetVersion != CurrentVersion {
		t.Fatalf("upgrade evidence = %#v, want successful v1-to-current evidence", result.Evidence)
	}
	if result.Snapshot.Version != CurrentVersion {
		t.Fatalf("migrated snapshot version = %q, want %q", result.Snapshot.Version, CurrentVersion)
	}
	if len(result.Snapshot.Resources) != 1 || result.Snapshot.Resources[0].ObservedState != models.ResourceStateUnknown {
		t.Fatalf("migrated resources = %#v, want one resource with unknown observed state", result.Snapshot.Resources)
	}

	encoded, err := EncodeSnapshot(result.Snapshot)
	if err != nil {
		t.Fatalf("EncodeSnapshot() error = %v", err)
	}
	reopened, err := DecodeSnapshot(encoded)
	if err != nil {
		t.Fatalf("DecodeSnapshot() after migration error = %v", err)
	}
	if reopened.Version != CurrentVersion || reopened.Resources[0].Spec.Tags["environment"] != "test" {
		t.Fatalf("reopened snapshot = %#v, want migrated golden state", reopened)
	}
}

func TestPreflightRejectsUnsupportedVersionsDeterministically(t *testing.T) {
	input := []byte(`{"version":"v9","resources":[]}`)

	first, err := Preflight(input, CurrentVersion)
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("first Preflight() error = %v, want ErrUnsupportedVersion", err)
	}
	second, err := Preflight(input, CurrentVersion)
	if !errors.Is(err, ErrUnsupportedVersion) {
		t.Fatalf("second Preflight() error = %v, want ErrUnsupportedVersion", err)
	}
	if first != second {
		t.Fatalf("Preflight() plans = %#v and %#v, want deterministic zero plans", first, second)
	}
}

func TestUpgradeReportsBoundedRedactedFailureEvidence(t *testing.T) {
	secret := "super-secret-value"
	input := []byte(`{"version":"v1","resources":[{"ID":"resource-1","Spec":{"Type":"unsupported","Name":"bad","Tags":{"credential":"` + secret + `"}}}]}`)

	result, err := Upgrade(context.Background(), input, CurrentVersion)
	if !errors.Is(err, ErrIncompatibleState) {
		t.Fatalf("Upgrade() error = %v, want ErrIncompatibleState", err)
	}
	if result.Evidence.Status != EvidenceFailed || result.Evidence.Code != FailureIncompatibleState || result.Evidence.Step != StepValidateLegacyState {
		t.Fatalf("failure evidence = %#v, want bounded incompatible-state evidence", result.Evidence)
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(result.Evidence.String(), secret) {
		t.Fatalf("failure output leaked secret value: error=%q evidence=%q", err, result.Evidence.String())
	}
	if len(result.Evidence.String()) > MaxEvidenceBytes {
		t.Fatalf("failure evidence length = %d, want at most %d", len(result.Evidence.String()), MaxEvidenceBytes)
	}
}

func TestUpgradeRejectsOversizedSnapshotsWithEvidence(t *testing.T) {
	input := []byte(strings.Repeat("x", MaxSnapshotBytes+1))
	result, err := Upgrade(context.Background(), input, CurrentVersion)
	if !errors.Is(err, ErrSnapshotTooLarge) {
		t.Fatalf("Upgrade() error = %v, want ErrSnapshotTooLarge", err)
	}
	if result.Evidence.Status != EvidenceFailed || result.Evidence.Code != FailureSnapshotTooLarge {
		t.Fatalf("oversized evidence = %#v, want bounded size failure", result.Evidence)
	}
}
