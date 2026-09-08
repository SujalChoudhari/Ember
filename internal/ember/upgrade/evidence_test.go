package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestRunEvidenceCoversLifecycleAndArtifactProvenance(t *testing.T) {
	content := []byte("ember-release-artifact")
	digest := sha256.Sum256(content)

	report, err := RunEvidence(context.Background(), Artifact{
		Name:           "ember",
		Version:        CurrentVersion,
		Content:        content,
		ExpectedSHA256: hex.EncodeToString(digest[:]),
	})
	if err != nil {
		t.Fatalf("RunEvidence() error = %v", err)
	}
	if report.Status != EvidenceRunSucceeded || !report.Verified || !report.Bounded || !report.SecretFree {
		t.Fatalf("evidence report = %#v, want verified bounded secret-free success", report)
	}
	if report.Artifact.SHA256 != hex.EncodeToString(digest[:]) || report.Artifact.Size != int64(len(content)) {
		t.Fatalf("artifact evidence = %#v, want computed provenance", report.Artifact)
	}
	if len(report.Stages) != 4 || len(report.Checks) != 3 {
		t.Fatalf("evidence counts = stages %d checks %d, want 4 stages and 3 checks", len(report.Stages), len(report.Checks))
	}
	for _, stage := range report.Stages {
		if stage.Status != EvidenceSucceeded {
			t.Fatalf("stage = %#v, want success", stage)
		}
	}
	for _, check := range report.Checks {
		if check.Status != EvidenceSucceeded {
			t.Fatalf("check = %#v, want success", check)
		}
	}
	if strings.Contains(report.String(), "release-artifact") || strings.Contains(report.String(), "evidence-payload") {
		t.Fatalf("evidence string retained payload text: %q", report.String())
	}
}

func TestRunEvidenceRejectsUnverifiedArtifactWithoutLeakingInput(t *testing.T) {
	secret := "artifact-secret-value"
	report, err := RunEvidence(context.Background(), Artifact{
		Name:           "ember",
		Version:        CurrentVersion,
		Content:        []byte(secret),
		ExpectedSHA256: strings.Repeat("0", sha256.Size*2),
	})
	if !errors.Is(err, ErrArtifactProvenanceMismatch) {
		t.Fatalf("RunEvidence() error = %v, want ErrArtifactProvenanceMismatch", err)
	}
	if report.Status != EvidenceRunFailed || report.Verified || strings.Contains(report.String(), secret) {
		t.Fatalf("failure report = %#v, want redacted failed report", report)
	}
}
