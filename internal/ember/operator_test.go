package ember

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
