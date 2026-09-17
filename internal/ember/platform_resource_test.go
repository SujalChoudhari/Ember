package ember

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func TestFileOperatorStoresPlatformResourcesInPlatformDatabase(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	operator, err := NewFileOperator(root, 64)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}

	created, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{
		Type: models.ResourceTypeGroup,
		Name: "platform",
	})
	if err != nil {
		t.Fatalf("CreateResource() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "resources.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy resources.json stat error = %v, want os.ErrNotExist", err)
	}
	if _, err := os.Stat(filepath.Join(root, "platform.db")); err != nil {
		t.Fatalf("platform.db stat error = %v", err)
	}

	reopened, err := NewFileOperator(root, 64)
	if err != nil {
		t.Fatalf("NewFileOperator(reopen) error = %v", err)
	}
	got, err := reopened.GetResource(ctx, OperatorPrincipal{}, created.ID)
	if err != nil {
		t.Fatalf("GetResource(reopen) error = %v", err)
	}
	if got.ID != created.ID || got.Spec.Name != "platform" {
		t.Fatalf("reopened resource = %#v, want %q", got, created.ID)
	}
}

func TestPlatformResourceOperationsRejectTenantScopedPrincipal(t *testing.T) {
	ctx := context.Background()
	operator, err := NewFileOperator(filepath.Join(t.TempDir(), "state"), 64)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	tenant, err := operator.CreateTenant(ctx, OperatorPrincipal{}, models.Tenant{ID: "tenant-a", DisplayName: "Tenant A"})
	if err != nil {
		t.Fatalf("CreateTenant() error = %v", err)
	}
	platformResource, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{
		Type: models.ResourceTypeGroup,
		Name: "platform",
	})
	if err != nil {
		t.Fatalf("CreateResource() error = %v", err)
	}
	principal := OperatorPrincipal{ScopeID: tenant.ID}
	if _, err := operator.GetResource(ctx, principal, platformResource.ID); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("GetResource(tenant against platform) error = %v, want scope denial", err)
	}
	if _, err := operator.UpdateResourceTags(ctx, principal, platformResource.ID, map[string]string{"blocked": "true"}, "tenant-update", "tenant-update"); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("UpdateResourceTags(tenant against platform) error = %v, want scope denial", err)
	}
	if err := operator.DeleteResource(ctx, principal, platformResource.ID); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("DeleteResource(tenant against platform) error = %v, want scope denial", err)
	}
}
