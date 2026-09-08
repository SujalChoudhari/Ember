package upgrade

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

const (
	EvidenceRunSucceeded = "succeeded"
	EvidenceRunFailed    = "failed"

	MaxEvidenceArtifactBytes = MaxSnapshotBytes
	MaxEvidenceStages        = 4
	MaxEvidenceChecks        = 3
	MaxEvidenceNameLength    = 64

	EvidenceStageCleanInstall = "clean-install"
	EvidenceStageUpgrade      = "upgrade"
	EvidenceStageRollback     = "rollback"
	EvidenceStageReset        = "reset"

	EvidenceCheckBounds     = "bounded-state"
	EvidenceCheckRedaction  = "secret-free"
	EvidenceCheckProvenance = "artifact-provenance"
)

var (
	ErrInvalidArtifact            = errors.New("invalid evidence artifact")
	ErrArtifactProvenanceMismatch = errors.New("artifact provenance mismatch")
	ErrEvidenceFailed             = errors.New("evidence run failed")
)

// Artifact is the release input inspected by RunEvidence. Content is consumed
// in memory only and never copied into the report or persisted by the runner.
type Artifact struct {
	Name           string
	Version        string
	Content        []byte
	ExpectedSHA256 string
}

type ArtifactEvidence struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	SHA256  string `json:"sha256"`
	Size    int64  `json:"size"`
}

type EvidenceStage struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Code   string `json:"code"`
}

type EvidenceCheck struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Code   string `json:"code"`
}

// EvidenceReport is a bounded, payload-free result of one local lifecycle run.
// It records only stable stage/check classes and artifact provenance.
type EvidenceReport struct {
	Status     string           `json:"status"`
	Verified   bool             `json:"verified"`
	Bounded    bool             `json:"bounded"`
	SecretFree bool             `json:"secret_free"`
	Artifact   ArtifactEvidence `json:"artifact"`
	Stages     []EvidenceStage  `json:"stages"`
	Checks     []EvidenceCheck  `json:"checks"`
}

func (report EvidenceReport) String() string {
	value := fmt.Sprintf(
		"status=%s verified=%t bounded=%t secret_free=%t artifact=%s@%s sha256=%s size=%d stages=%d checks=%d",
		safeEvidenceText(report.Status), report.Verified, report.Bounded, report.SecretFree,
		safeEvidenceText(report.Artifact.Name), safeEvidenceText(report.Artifact.Version),
		safeEvidenceText(report.Artifact.SHA256), report.Artifact.Size, len(report.Stages), len(report.Checks),
	)
	if len(value) > MaxEvidenceBytes {
		return value[:MaxEvidenceBytes]
	}
	return value
}

// RunEvidence executes one bounded clean-install, upgrade, rollback, and reset
// proof using the existing local persistence and upgrade contracts.
func RunEvidence(ctx context.Context, artifact Artifact) (EvidenceReport, error) {
	report := EvidenceReport{Status: EvidenceRunFailed}
	digest, err := validateEvidenceArtifact(artifact)
	if err != nil {
		return report, err
	}
	report.Artifact = ArtifactEvidence{
		Name:    strings.TrimSpace(artifact.Name),
		Version: strings.TrimSpace(artifact.Version),
		SHA256:  digest,
		Size:    int64(len(artifact.Content)),
	}
	if ctx == nil || ctx.Err() != nil {
		return report, fmt.Errorf("%w: %s", ErrEvidenceFailed, FailureCancelled)
	}

	root, err := os.MkdirTemp("", "ember-evidence-")
	if err != nil {
		return report, fmt.Errorf("%w: clean-install", ErrEvidenceFailed)
	}
	defer os.RemoveAll(root)

	resourcePath := filepath.Join(root, "resources.json")
	blobRoot := filepath.Join(root, "blobs")
	resources, err := persistence.NewFileResourceStore(resourcePath)
	if err != nil {
		return evidenceFailure(report, EvidenceStageCleanInstall, "resource-store")
	}
	blobs, err := persistence.NewFileBlobStore(blobRoot, 1024)
	if err != nil {
		return evidenceFailure(report, EvidenceStageCleanInstall, "blob-store")
	}

	group, err := resources.Create(ctx, models.ResourceSpec{
		Type: models.ResourceTypeGroup,
		Name: "evidence-platform",
		Tags: map[string]string{"evidence": "true"},
	})
	if err != nil {
		return evidenceFailure(report, EvidenceStageCleanInstall, "resource-create")
	}
	bucket, err := resources.Create(ctx, models.ResourceSpec{
		Type:     models.ResourceTypeBucket,
		Name:     "evidence-bucket",
		ParentID: group.ID,
	})
	if err != nil {
		return evidenceFailure(report, EvidenceStageCleanInstall, "bucket-create")
	}
	if _, err := blobs.Put(ctx, bucket.ID, "evidence.txt", []byte("evidence-payload")); err != nil {
		return evidenceFailure(report, EvidenceStageCleanInstall, "blob-put")
	}
	if _, content, err := blobs.Get(ctx, bucket.ID, "evidence.txt"); err != nil || string(content) != "evidence-payload" {
		return evidenceFailure(report, EvidenceStageCleanInstall, "blob-read")
	}
	topLevel, err := resources.List(ctx, "", persistence.MaxResourceListLimit)
	if err != nil {
		return evidenceFailure(report, EvidenceStageCleanInstall, "resource-list")
	}
	children, err := resources.List(ctx, group.ID, persistence.MaxResourceListLimit)
	if err != nil || len(topLevel) != 1 || len(children) != 1 {
		return evidenceFailure(report, EvidenceStageCleanInstall, "resource-state")
	}
	report.Stages = append(report.Stages, EvidenceStage{Name: EvidenceStageCleanInstall, Status: EvidenceSucceeded, Code: "verified"})

	legacy := Snapshot{Version: VersionV1, Resources: append(topLevel, children...)}
	data, err := EncodeSnapshot(legacy)
	if err != nil {
		return evidenceFailure(report, EvidenceStageUpgrade, "snapshot-encode")
	}
	upgraded, err := Upgrade(ctx, data, CurrentVersion)
	if err != nil || upgraded.Evidence.Status != EvidenceSucceeded || upgraded.Snapshot.Version != CurrentVersion {
		return evidenceFailure(report, EvidenceStageUpgrade, "upgrade")
	}
	report.Stages = append(report.Stages, EvidenceStage{Name: EvidenceStageUpgrade, Status: EvidenceSucceeded, Code: "verified"})

	journal := NewRollbackJournal()
	record, err := journal.Prepare(ctx, "evidence-rollback", legacy, CurrentVersion)
	if err != nil {
		return evidenceFailure(report, EvidenceStageRollback, "rollback-prepare")
	}
	current := upgraded.Snapshot
	current = Snapshot{Version: CurrentVersion}
	rollback, err := journal.Rollback(ctx, record.RequestID, func(_ context.Context, snapshot Snapshot) error {
		current = snapshot
		return nil
	})
	if err != nil || rollback.Record.Status != RollbackStatusSucceeded || !reflect.DeepEqual(current, legacy) {
		return evidenceFailure(report, EvidenceStageRollback, "rollback")
	}
	report.Stages = append(report.Stages, EvidenceStage{Name: EvidenceStageRollback, Status: EvidenceSucceeded, Code: "verified"})

	if err := resources.Reset(ctx); err != nil {
		return evidenceFailure(report, EvidenceStageReset, "resource-reset")
	}
	if err := blobs.Reset(ctx); err != nil {
		return evidenceFailure(report, EvidenceStageReset, "blob-reset")
	}
	if _, err := os.Stat(resourcePath); !errors.Is(err, os.ErrNotExist) {
		return evidenceFailure(report, EvidenceStageReset, "resource-residue")
	}
	for _, path := range []string{filepath.Join(blobRoot, "metadata.json"), filepath.Join(blobRoot, "objects")} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			return evidenceFailure(report, EvidenceStageReset, "blob-residue")
		}
	}
	report.Stages = append(report.Stages, EvidenceStage{Name: EvidenceStageReset, Status: EvidenceSucceeded, Code: "verified"})

	report.Checks = []EvidenceCheck{
		{Name: EvidenceCheckBounds, Status: EvidenceSucceeded, Code: "verified"},
		{Name: EvidenceCheckRedaction, Status: EvidenceSucceeded, Code: "verified"},
		{Name: EvidenceCheckProvenance, Status: EvidenceSucceeded, Code: "verified"},
	}
	report.Status = EvidenceRunSucceeded
	report.Verified = true
	report.Bounded = len(report.Stages) <= MaxEvidenceStages && len(report.Checks) <= MaxEvidenceChecks
	report.SecretFree = !strings.Contains(report.String(), string(artifact.Content))
	if !report.Bounded || !report.SecretFree {
		return evidenceFailure(report, EvidenceStageReset, "report-validation")
	}
	return report, nil
}

func validateEvidenceArtifact(artifact Artifact) (string, error) {
	if strings.TrimSpace(artifact.Name) == "" || len(artifact.Name) > MaxEvidenceNameLength ||
		strings.TrimSpace(artifact.Version) == "" || len(artifact.Version) > MaxEvidenceNameLength ||
		len(artifact.Content) == 0 || len(artifact.Content) > MaxEvidenceArtifactBytes ||
		len(artifact.ExpectedSHA256) != sha256.Size*2 {
		return "", ErrInvalidArtifact
	}
	digest := sha256.Sum256(artifact.Content)
	actual := hex.EncodeToString(digest[:])
	expected, err := hex.DecodeString(artifact.ExpectedSHA256)
	if err != nil || hex.EncodeToString(expected) != artifact.ExpectedSHA256 {
		return "", ErrInvalidArtifact
	}
	if actual != artifact.ExpectedSHA256 {
		return actual, ErrArtifactProvenanceMismatch
	}
	return actual, nil
}

func evidenceFailure(report EvidenceReport, stage, code string) (EvidenceReport, error) {
	report.Stages = append(report.Stages, EvidenceStage{Name: stage, Status: EvidenceFailed, Code: code})
	report.Status = EvidenceRunFailed
	report.Verified = false
	return report, fmt.Errorf("%w: %s", ErrEvidenceFailed, code)
}
