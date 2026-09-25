package ember

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/deployment"
	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

func newTestOperator(t *testing.T) *Operator {
	t.Helper()
	resourceStore, err := persistence.NewFileResourceStore(filepath.Join(t.TempDir(), "resources.json"))
	if err != nil {
		t.Fatalf("NewFileResourceStore() error = %v", err)
	}
	resourceManager, err := NewResourceManager(resourceStore)
	if err != nil {
		t.Fatalf("NewResourceManager() error = %v", err)
	}
	blobStore, err := persistence.NewFileBlobStore(filepath.Join(t.TempDir(), "blobs"), 64)
	if err != nil {
		t.Fatalf("NewFileBlobStore() error = %v", err)
	}
	operationStore, err := persistence.NewFileOperationStore(filepath.Join(t.TempDir(), "operations.json"))
	if err != nil {
		t.Fatalf("NewFileOperationStore() error = %v", err)
	}
	auditStore, err := persistence.NewFileAuditStore(filepath.Join(t.TempDir(), "audit.json"))
	if err != nil {
		t.Fatalf("NewFileAuditStore() error = %v", err)
	}
	operationCoordinator, err := NewResourceOperationCoordinator(operationStore, auditStore)
	if err != nil {
		t.Fatalf("NewResourceOperationCoordinator() error = %v", err)
	}
	operator, err := NewOperator(resourceManager, blobStore, operationCoordinator, func(context.Context) error { return nil })
	if err != nil {
		t.Fatalf("NewOperator() error = %v", err)
	}
	return operator
}

func TestNewFileOperatorResetClearsAllPersistentRuntimeState(t *testing.T) {
	root := t.TempDir()
	operator, err := NewFileOperator(root, 1<<20)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}

	for _, name := range []string{"work-queue.json", "event-dead-letters.json", "event-topology.json"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(`{"stale":true}`), 0o600); err != nil {
			t.Fatalf("seed %s: %v", name, err)
		}
	}
	if err := operator.reset(context.Background()); err != nil {
		t.Fatalf("reset() error = %v", err)
	}
	for _, name := range []string{"work-queue.json", "event-dead-letters.json", "event-topology.json"} {
		if _, err := os.Stat(filepath.Join(root, name)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("%s after reset: error = %v, want os.ErrNotExist", name, err)
		}
	}
}

func TestNewFileOperatorRejectsSecondOwnerOfStateDirectory(t *testing.T) {
	if os.Getenv("EMBER_LOCK_HELPER") == "1" {
		if _, err := NewFileOperator(os.Getenv("EMBER_LOCK_ROOT"), 64); err == nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	root := t.TempDir()
	first, err := NewFileOperator(root, 64)
	if err != nil {
		t.Fatalf("NewFileOperator(first) error = %v", err)
	}
	if first == nil {
		t.Fatal("NewFileOperator(first) returned nil operator")
	}
	cmd := exec.Command(os.Args[0], "-test.run", "^TestNewFileOperatorRejectsSecondOwnerOfStateDirectory$")
	cmd.Env = append(os.Environ(), "EMBER_LOCK_HELPER=1", "EMBER_LOCK_ROOT="+root)
	if err := cmd.Run(); err != nil {
		t.Fatalf("second process lock probe error = %v", err)
	}
}

func TestOperatorScopesResourceLifecycleAndProviderState(t *testing.T) {
	operator := newTestOperator(t)
	ctx := context.Background()
	provider := models.ProviderMetadata{Namespace: "Ember.Storage", Type: "buckets", Version: "v1"}

	alpha, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{
		Type:         models.ResourceTypeGroup,
		Name:         "alpha",
		Provider:     provider,
		DesiredState: models.ResourceStateReady,
	})
	if err != nil {
		t.Fatalf("CreateResource(alpha) error = %v", err)
	}
	beta, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{
		Type:         models.ResourceTypeGroup,
		Name:         "beta",
		Provider:     provider,
		DesiredState: models.ResourceStateReady,
	})
	if err != nil {
		t.Fatalf("CreateResource(beta) error = %v", err)
	}
	child, err := operator.CreateResource(ctx, OperatorPrincipal{ScopeID: alpha.ID}, models.ResourceSpec{
		Type:         models.ResourceTypeBucket,
		Name:         "assets",
		ParentID:     alpha.ID,
		Provider:     provider,
		DesiredState: models.ResourceStateReady,
	})
	if err != nil {
		t.Fatalf("CreateResource(child) error = %v", err)
	}

	got, err := operator.GetResource(ctx, OperatorPrincipal{ScopeID: alpha.ID}, child.ID)
	if err != nil {
		t.Fatalf("GetResource(same scope) error = %v", err)
	}
	if got.ID != child.ID || got.Spec.ParentID != alpha.ID || got.Spec.Provider != provider || got.Spec.DesiredState != models.ResourceStateReady || got.ObservedState != models.ResourceStateUnknown {
		t.Fatalf("GetResource(same scope) = %#v, want scoped provider and state", got)
	}
	if _, err := operator.GetResource(ctx, OperatorPrincipal{ScopeID: beta.ID}, child.ID); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("GetResource(cross scope) error = %v, want ErrOperatorScopeDenied", err)
	}
}

func TestOperatorCoordinatesIdempotentMutationAndAuditInspection(t *testing.T) {
	operator := newTestOperator(t)
	ctx := context.Background()
	resource, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{
		Type: models.ResourceTypeGroup,
		Name: "platform",
	})
	if err != nil {
		t.Fatalf("CreateResource() error = %v", err)
	}

	first, err := operator.UpdateResourceTags(ctx, OperatorPrincipal{}, resource.ID, map[string]string{"environment": "test"}, "request-1", "correlation-1")
	if err != nil {
		t.Fatalf("first UpdateResourceTags() error = %v", err)
	}
	if first.Resource == nil || first.Resource.Spec.Tags["environment"] != "test" || first.Operation == nil || first.Replayed {
		t.Fatalf("first mutation response = %#v, want updated resource and new operation", first)
	}

	replayed, err := operator.UpdateResourceTags(ctx, OperatorPrincipal{}, resource.ID, map[string]string{"environment": "different"}, "request-1", "correlation-2")
	if err != nil {
		t.Fatalf("replayed UpdateResourceTags() error = %v", err)
	}
	if replayed.Resource == nil || replayed.Resource.Spec.Tags["environment"] != "test" || replayed.Operation == nil || !replayed.Replayed || replayed.Operation.ID != first.Operation.ID {
		t.Fatalf("replayed mutation response = %#v, want original operation and state", replayed)
	}

	operation, err := operator.GetOperation(ctx, OperatorPrincipal{}, first.Operation.ID)
	if err != nil {
		t.Fatalf("GetOperation() error = %v", err)
	}
	if operation.ID != first.Operation.ID || operation.ResourceID != resource.ID {
		t.Fatalf("GetOperation() = %#v, want linked operation", operation)
	}
	history, err := operator.ListAuditHistory(ctx, OperatorPrincipal{}, resource.ID, persistence.MaxAuditListLimit)
	if err != nil {
		t.Fatalf("ListAuditHistory() error = %v", err)
	}
	if len(history) != 1 || history[0].OperationID != first.Operation.ID || history[0].ResourceID != resource.ID {
		t.Fatalf("ListAuditHistory() = %#v, want one linked entry", history)
	}
}

func TestOperatorListsBoundedOperationsWithinResourceScope(t *testing.T) {
	operator := newTestOperator(t)
	ctx := context.Background()
	root, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "root"})
	if err != nil {
		t.Fatalf("CreateResource(root) error = %v", err)
	}
	child, err := operator.CreateResource(ctx, OperatorPrincipal{ScopeID: root.ID}, models.ResourceSpec{Type: models.ResourceTypeBucket, Name: "child", ParentID: root.ID})
	if err != nil {
		t.Fatalf("CreateResource(child) error = %v", err)
	}
	other, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "other"})
	if err != nil {
		t.Fatalf("CreateResource(other) error = %v", err)
	}

	childMutation, err := operator.UpdateResourceTags(ctx, OperatorPrincipal{ScopeID: root.ID}, child.ID, map[string]string{"tier": "scoped"}, "request-scoped", "correlation-scoped")
	if err != nil {
		t.Fatalf("UpdateResourceTags(child) error = %v", err)
	}
	failedRecovery, recoveryErr := operator.RecoverBlob(ctx, OperatorPrincipal{ScopeID: root.ID}, child.ID, "missing", "not-the-trusted-content", []byte("not-the-trusted-content"), "request-recovery-failure", "correlation-recovery-failure")
	if !errors.Is(recoveryErr, ErrResourceOperationFailed) || failedRecovery == nil || failedRecovery.Operation == nil || failedRecovery.Operation.Status != models.OperationStatusFailed {
		t.Fatalf("RecoverBlob(failure) = (%#v, %v), want failed correlated operation", failedRecovery, recoveryErr)
	}
	otherMutation, err := operator.UpdateResourceTags(ctx, OperatorPrincipal{}, other.ID, map[string]string{"tier": "foreign"}, "request-foreign", "correlation-foreign")
	if err != nil {
		t.Fatalf("UpdateResourceTags(other) error = %v", err)
	}

	listed, err := operator.ListOperations(ctx, OperatorPrincipal{ScopeID: root.ID}, "", persistence.MaxOperationListLimit)
	if err != nil {
		t.Fatalf("ListOperations(scoped) error = %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("ListOperations(scoped) = %#v, want two scoped lifecycle operations", listed)
	}
	statuses := map[string]models.OperationStatus{}
	for _, operation := range listed {
		statuses[operation.ID] = operation.Status
	}
	if statuses[childMutation.Operation.ID] != models.OperationStatusSucceeded || statuses[failedRecovery.Operation.ID] != models.OperationStatusFailed {
		t.Fatalf("ListOperations(scoped) statuses = %#v, want success and failed recovery", statuses)
	}
	if _, err := operator.ListOperations(ctx, OperatorPrincipal{ScopeID: root.ID}, other.ID, persistence.MaxOperationListLimit); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("ListOperations(cross-scope) error = %v, want scope denial", err)
	}
	byResource, err := operator.ListOperations(ctx, OperatorPrincipal{}, other.ID, 1)
	if err != nil {
		t.Fatalf("ListOperations(resource) error = %v", err)
	}
	if len(byResource) != 1 || byResource[0].ID != otherMutation.Operation.ID || byResource[0].Status != models.OperationStatusSucceeded {
		t.Fatalf("ListOperations(resource) = %#v, want bounded successful operation", byResource)
	}
	if _, err := operator.ListOperations(ctx, OperatorPrincipal{}, "", 0); !errors.Is(err, persistence.ErrInvalidOperationListLimit) {
		t.Fatalf("ListOperations(zero limit) error = %v, want invalid limit", err)
	}
}

func TestOperatorScopesBlobLifecycleAndRangeReads(t *testing.T) {
	operator := newTestOperator(t)
	ctx := context.Background()
	group, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{
		Type: models.ResourceTypeGroup,
		Name: "platform",
	})
	if err != nil {
		t.Fatalf("CreateResource(group) error = %v", err)
	}
	bucket, err := operator.CreateResource(ctx, OperatorPrincipal{ScopeID: group.ID}, models.ResourceSpec{
		Type:     models.ResourceTypeBucket,
		Name:     "assets",
		ParentID: group.ID,
	})
	if err != nil {
		t.Fatalf("CreateResource(bucket) error = %v", err)
	}

	put, err := operator.PutBlob(ctx, OperatorPrincipal{ScopeID: group.ID}, bucket.ID, "nested/file.txt", []byte("hello world"))
	if err != nil {
		t.Fatalf("PutBlob() error = %v", err)
	}
	if put.Object == nil || put.Object.BucketID != bucket.ID || put.Object.Key != "nested/file.txt" || put.Object.Size != 11 {
		t.Fatalf("PutBlob() response = %#v, want bounded object metadata", put)
	}

	got, err := operator.GetBlob(ctx, OperatorPrincipal{ScopeID: group.ID}, bucket.ID, "nested/file.txt")
	if err != nil {
		t.Fatalf("GetBlob() error = %v", err)
	}
	if got.Object == nil || string(got.Content) != "hello world" {
		t.Fatalf("GetBlob() response = %#v, want object and payload", got)
	}
	rangeResult, err := operator.ReadBlobRange(ctx, OperatorPrincipal{ScopeID: group.ID}, bucket.ID, "nested/file.txt", 6, 11)
	if err != nil {
		t.Fatalf("ReadBlobRange() error = %v", err)
	}
	if string(rangeResult.Content) != "world" {
		t.Fatalf("ReadBlobRange() content = %q, want world", rangeResult.Content)
	}

	other, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{
		Type: models.ResourceTypeGroup,
		Name: "other",
	})
	if err != nil {
		t.Fatalf("CreateResource(other) error = %v", err)
	}
	if _, err := operator.GetBlob(ctx, OperatorPrincipal{ScopeID: other.ID}, bucket.ID, "nested/file.txt"); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("GetBlob(cross scope) error = %v, want ErrOperatorScopeDenied", err)
	}
}

func TestFileOperatorResetRemovesApplyProgressState(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	operator, err := NewFileOperator(root, 64)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}

	document := []byte(`{"version":"v1","resources":[{"type":"group","name":"platform","desiredState":"ready"}]}`)
	if _, _, err := operator.applyDeployment(ctx, OperatorPrincipal{}, document, nil, deployment.ApplyOptions{
		RequestID: "request-reset-progress", CorrelationID: "correlation-reset-progress",
	}); err != nil {
		t.Fatalf("applyDeployment() error = %v", err)
	}
	progressPath := filepath.Join(root, "apply-progress.json")
	if _, err := os.Stat(progressPath); err != nil {
		t.Fatalf("apply progress stat error = %v", err)
	}

	if err := operator.Reset(ctx, OperatorPrincipal{}); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if _, err := os.Stat(progressPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("apply progress after reset: error = %v, want os.ErrNotExist", err)
	}
}

func TestFileOperatorResetsOwnedStateAndReopensDeterministically(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	operator, err := NewFileOperator(root, 64)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	group, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{
		Type: models.ResourceTypeGroup,
		Name: "platform",
	})
	if err != nil {
		t.Fatalf("CreateResource(group) error = %v", err)
	}
	bucket, err := operator.CreateResource(ctx, OperatorPrincipal{ScopeID: group.ID}, models.ResourceSpec{
		Type:     models.ResourceTypeBucket,
		Name:     "assets",
		ParentID: group.ID,
	})
	if err != nil {
		t.Fatalf("CreateResource(bucket) error = %v", err)
	}
	if _, err := operator.PutBlob(ctx, OperatorPrincipal{ScopeID: group.ID}, bucket.ID, "object", []byte("payload")); err != nil {
		t.Fatalf("PutBlob() error = %v", err)
	}
	if _, err := operator.UpdateResourceTags(ctx, OperatorPrincipal{ScopeID: group.ID}, bucket.ID, map[string]string{"tier": "test"}, "request-reset", "correlation-reset"); err != nil {
		t.Fatalf("UpdateResourceTags() error = %v", err)
	}

	if err := operator.Reset(ctx, OperatorPrincipal{ScopeID: group.ID}); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("Reset(scoped) error = %v, want ErrOperatorScopeDenied", err)
	}
	if err := operator.Reset(ctx, OperatorPrincipal{}); err != nil {
		t.Fatalf("Reset(root) error = %v", err)
	}
	for _, path := range []string{
		filepath.Join(root, "resources.json"),
		filepath.Join(root, "operations.json"),
		filepath.Join(root, "audit.json"),
		filepath.Join(root, "blobs", "metadata.json"),
		filepath.Join(root, "blobs", "objects"),
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned path %q after reset: error = %v, want os.ErrNotExist", path, err)
		}
	}

	reopened, err := NewFileOperator(root, 64)
	if err != nil {
		t.Fatalf("NewFileOperator(reopen) error = %v", err)
	}
	newGroup, err := reopened.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{
		Type: models.ResourceTypeGroup,
		Name: "platform",
	})
	if err != nil {
		t.Fatalf("CreateResource(after reset) error = %v", err)
	}
	if newGroup.ID != "resource-00000001" {
		t.Fatalf("first resource ID after reset = %q, want resource-00000001", newGroup.ID)
	}
}

func TestOperatorExposesCompleteResourceLifecycleAndLockSurface(t *testing.T) {
	ctx := context.Background()
	operator := newTestOperator(t)

	root, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "root"})
	if err != nil {
		t.Fatalf("CreateResource(root) error = %v", err)
	}
	other, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "other"})
	if err != nil {
		t.Fatalf("CreateResource(other) error = %v", err)
	}
	child, err := operator.CreateResource(ctx, OperatorPrincipal{ScopeID: root.ID}, models.ResourceSpec{
		Type: models.ResourceTypeBucket, Name: "assets", ParentID: root.ID,
	})
	if err != nil {
		t.Fatalf("CreateResource(child) error = %v", err)
	}

	rootResources, err := operator.ListResources(ctx, OperatorPrincipal{}, persistence.MaxResourceListLimit)
	if err != nil {
		t.Fatalf("ListResources(root) error = %v", err)
	}
	if len(rootResources) != 2 || rootResources[0].ID != root.ID || rootResources[1].ID != other.ID {
		t.Fatalf("ListResources(root) = %#v, want deterministic root resources", rootResources)
	}
	childResources, err := operator.ListResources(ctx, OperatorPrincipal{ScopeID: root.ID}, persistence.MaxResourceListLimit)
	if err != nil {
		t.Fatalf("ListResources(child scope) error = %v", err)
	}
	if len(childResources) != 1 || childResources[0].ID != child.ID {
		t.Fatalf("ListResources(child scope) = %#v, want child resource", childResources)
	}

	updated, err := operator.UpdateResourceTags(ctx, OperatorPrincipal{ScopeID: root.ID}, child.ID, map[string]string{"tier": "test"}, "request-tags", "correlation-tags")
	if err != nil {
		t.Fatalf("UpdateResourceTags() error = %v", err)
	}
	if updated.Resource == nil || updated.Resource.ID != child.ID || updated.Resource.Spec.Tags["tier"] != "test" {
		t.Fatalf("UpdateResourceTags() = %#v, want immutable identity and updated tags", updated)
	}

	if err := operator.DeleteResource(ctx, OperatorPrincipal{}, root.ID); !errors.Is(err, persistence.ErrResourceHasDependents) {
		t.Fatalf("DeleteResource(root with child) error = %v, want dependent refusal", err)
	}

	lock := models.ResourceLock{Owner: "operator", Token: "lock-token"}
	if err := operator.AcquireResourceLock(ctx, OperatorPrincipal{}, root.ID, lock); err != nil {
		t.Fatalf("AcquireResourceLock() error = %v", err)
	}
	inspected, err := operator.InspectResourceLock(ctx, OperatorPrincipal{}, root.ID)
	if err != nil {
		t.Fatalf("InspectResourceLock() error = %v", err)
	}
	if inspected == nil || *inspected != lock {
		t.Fatalf("InspectResourceLock() = %#v, want %#v", inspected, lock)
	}
	if _, err := operator.GetResource(ctx, OperatorPrincipal{ScopeID: root.ID}, child.ID); err != nil {
		t.Fatalf("GetResource(same scope under ancestor lock) error = %v", err)
	}
	if _, err := operator.UpdateResourceTags(ctx, OperatorPrincipal{ScopeID: root.ID}, child.ID, map[string]string{"tier": "blocked"}, "request-locked", "correlation-locked"); !errors.Is(err, persistence.ErrResourceLocked) {
		t.Fatalf("UpdateResourceTags(locked) error = %v, want resource lock", err)
	}
	if _, err := operator.CreateResource(ctx, OperatorPrincipal{ScopeID: root.ID}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "blocked", ParentID: root.ID}); !errors.Is(err, persistence.ErrResourceLocked) {
		t.Fatalf("CreateResource(locked ancestor) error = %v, want resource lock", err)
	}
	if err := operator.DeleteResource(ctx, OperatorPrincipal{ScopeID: root.ID}, child.ID); !errors.Is(err, persistence.ErrResourceLocked) {
		t.Fatalf("DeleteResource(locked ancestor) error = %v, want resource lock", err)
	}
	if _, err := operator.InspectResourceLock(ctx, OperatorPrincipal{ScopeID: other.ID}, root.ID); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("InspectResourceLock(cross scope) error = %v, want scope denial", err)
	}
	if err := operator.ReleaseResourceLock(ctx, OperatorPrincipal{}, root.ID, models.ResourceLock{Owner: "other", Token: "wrong"}); !errors.Is(err, persistence.ErrResourceLockNotOwner) {
		t.Fatalf("ReleaseResourceLock(wrong owner) error = %v, want owner error", err)
	}
	if err := operator.ReleaseResourceLock(ctx, OperatorPrincipal{}, root.ID, lock); err != nil {
		t.Fatalf("ReleaseResourceLock(owner) error = %v", err)
	}
	if inspected, err := operator.InspectResourceLock(ctx, OperatorPrincipal{}, root.ID); err != nil || inspected != nil {
		t.Fatalf("InspectResourceLock(after release) = %#v, %v, want nil lock", inspected, err)
	}

	if err := operator.DeleteResource(ctx, OperatorPrincipal{ScopeID: root.ID}, child.ID); err != nil {
		t.Fatalf("DeleteResource(leaf) error = %v", err)
	}
	if err := operator.DeleteResource(ctx, OperatorPrincipal{}, root.ID); err != nil {
		t.Fatalf("DeleteResource(root after leaf) error = %v", err)
	}
}

func TestFileOperatorReopenPreservesResourcesAndPersistedLockContract(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	first, err := NewFileOperator(root, 64)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	resource, err := first.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "platform"})
	if err != nil {
		t.Fatalf("CreateResource() error = %v", err)
	}
	lock := models.ResourceLock{Owner: "operator", Token: "reopen-token"}
	if err := first.AcquireResourceLock(ctx, OperatorPrincipal{}, resource.ID, lock); err != nil {
		t.Fatalf("AcquireResourceLock() error = %v", err)
	}

	reopened, err := NewFileOperator(root, 64)
	if err != nil {
		t.Fatalf("NewFileOperator(reopen) error = %v", err)
	}
	got, err := reopened.GetResource(ctx, OperatorPrincipal{}, resource.ID)
	if err != nil {
		t.Fatalf("GetResource(reopen) error = %v", err)
	}
	if got.ID != resource.ID {
		t.Fatalf("GetResource(reopen) ID = %q, want %q", got.ID, resource.ID)
	}
	inspected, err := reopened.InspectResourceLock(ctx, OperatorPrincipal{}, resource.ID)
	if err != nil {
		t.Fatalf("InspectResourceLock(reopen) error = %v", err)
	}
	if inspected == nil || *inspected != lock {
		t.Fatalf("InspectResourceLock(reopen) = %#v, want %#v", inspected, lock)
	}
}

func TestOperatorExposesCompleteBlobLifecycle(t *testing.T) {
	operator := newTestOperator(t)
	ctx := context.Background()

	group, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "platform"})
	if err != nil {
		t.Fatalf("CreateResource(group) error = %v", err)
	}
	bucket, err := operator.CreateResource(ctx, OperatorPrincipal{ScopeID: group.ID}, models.ResourceSpec{Type: models.ResourceTypeBucket, Name: "assets", ParentID: group.ID})
	if err != nil {
		t.Fatalf("CreateResource(bucket) error = %v", err)
	}
	other, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "other"})
	if err != nil {
		t.Fatalf("CreateResource(other) error = %v", err)
	}

	for _, object := range []struct {
		key  string
		data string
	}{
		{key: "z.txt", data: "last"},
		{key: "a.txt", data: "first"},
	} {
		if _, err := operator.PutBlob(ctx, OperatorPrincipal{ScopeID: group.ID}, bucket.ID, object.key, []byte(object.data)); err != nil {
			t.Fatalf("PutBlob(%q) error = %v", object.key, err)
		}
	}

	listed, err := operator.ListBlobs(ctx, OperatorPrincipal{ScopeID: group.ID}, bucket.ID, persistence.MaxBlobListLimit)
	if err != nil {
		t.Fatalf("ListBlobs() error = %v", err)
	}
	if listed == nil || len(listed.Objects) != 2 || listed.Objects[0].Key != "a.txt" || listed.Objects[1].Key != "z.txt" {
		t.Fatalf("ListBlobs() = %#v, want sorted bounded objects", listed)
	}
	if _, err := operator.ListBlobs(ctx, OperatorPrincipal{ScopeID: other.ID}, bucket.ID, persistence.MaxBlobListLimit); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("ListBlobs(cross scope) error = %v, want scope denial", err)
	}
	if err := operator.DeleteBlob(ctx, OperatorPrincipal{ScopeID: other.ID}, bucket.ID, "a.txt"); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("DeleteBlob(cross scope) error = %v, want scope denial", err)
	}

	if err := operator.DeleteBlob(ctx, OperatorPrincipal{ScopeID: group.ID}, bucket.ID, "a.txt"); err != nil {
		t.Fatalf("DeleteBlob() error = %v", err)
	}
	if _, err := operator.GetBlob(ctx, OperatorPrincipal{ScopeID: group.ID}, bucket.ID, "a.txt"); !errors.Is(err, persistence.ErrBlobObjectNotFound) {
		t.Fatalf("GetBlob(deleted) error = %v, want object not found", err)
	}
	remaining, err := operator.ListBlobs(ctx, OperatorPrincipal{ScopeID: group.ID}, bucket.ID, persistence.MaxBlobListLimit)
	if err != nil {
		t.Fatalf("ListBlobs(after delete) error = %v", err)
	}
	if remaining == nil || len(remaining.Objects) != 1 || remaining.Objects[0].Key != "z.txt" {
		t.Fatalf("ListBlobs(after delete) = %#v, want remaining object", remaining)
	}
	if err := operator.DeleteBlob(ctx, OperatorPrincipal{ScopeID: group.ID}, bucket.ID, "a.txt"); !errors.Is(err, persistence.ErrBlobObjectNotFound) {
		t.Fatalf("DeleteBlob(missing) error = %v, want object not found", err)
	}
}
