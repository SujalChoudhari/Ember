package persistence

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func TestFileResourceStorePersistsScopedResourcesAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.json")
	ctx := context.Background()

	store, err := NewFileResourceStore(path)
	if err != nil {
		t.Fatalf("NewFileResourceStore() error = %v", err)
	}

	parent, err := store.Create(ctx, models.ResourceSpec{
		Type:         models.ResourceTypeGroup,
		Name:         "platform",
		Provider:     models.ProviderMetadata{Namespace: "Ember.Resource", Type: "groups", Version: "v1"},
		DesiredState: models.ResourceStateReady,
	})
	if err != nil {
		t.Fatalf("Create(parent) error = %v", err)
	}
	childSpec := models.ResourceSpec{
		Type:         models.ResourceTypeBucket,
		Name:         "assets",
		ParentID:     parent.ID,
		Tags:         map[string]string{"environment": "test"},
		Provider:     models.ProviderMetadata{Namespace: "Ember.Storage", Type: "buckets", Version: "v1"},
		DesiredState: models.ResourceStateReady,
	}
	child, err := store.Create(ctx, childSpec)
	if err != nil {
		t.Fatalf("Create(child) error = %v", err)
	}

	reopened, err := NewFileResourceStore(path)
	if err != nil {
		t.Fatalf("NewFileResourceStore(reopen) error = %v", err)
	}

	got, err := reopened.Get(ctx, parent.ID, child.ID)
	if err != nil {
		t.Fatalf("Get(reopened, child) error = %v", err)
	}
	if got.ID != child.ID {
		t.Fatalf("reopened child ID = %q, want stable ID %q", got.ID, child.ID)
	}
	if !reflect.DeepEqual(got.Spec, childSpec) {
		t.Fatalf("reopened child spec = %#v, want %#v", got.Spec, childSpec)
	}
	if got.ObservedState != models.ResourceStateUnknown {
		t.Fatalf("reopened observed state = %q, want %q", got.ObservedState, models.ResourceStateUnknown)
	}

	if _, err := reopened.Get(ctx, "wrong-scope", child.ID); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("cross-scope Get() error = %v, want ErrResourceNotFound", err)
	}
	rootResources, err := reopened.List(ctx, "", MaxResourceListLimit)
	if err != nil {
		t.Fatalf("List(reopened, root) error = %v", err)
	}
	if gotIDs := resourceIDs(rootResources); !reflect.DeepEqual(gotIDs, []string{parent.ID}) {
		t.Fatalf("reopened root IDs = %v, want %v", gotIDs, []string{parent.ID})
	}
}

func TestFileResourceStoreMutationAndDeletePersistAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.json")
	ctx := context.Background()
	store, err := NewFileResourceStore(path)
	if err != nil {
		t.Fatalf("NewFileResourceStore() error = %v", err)
	}
	parent, err := store.Create(ctx, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "platform"})
	if err != nil {
		t.Fatalf("Create(parent) error = %v", err)
	}
	child, err := store.Create(ctx, models.ResourceSpec{Type: models.ResourceTypeBucket, Name: "assets", ParentID: parent.ID})
	if err != nil {
		t.Fatalf("Create(child) error = %v", err)
	}

	inputTags := map[string]string{"environment": "test"}
	updated, err := store.UpdateTags(ctx, parent.ID, child.ID, inputTags)
	if err != nil {
		t.Fatalf("UpdateTags() error = %v", err)
	}
	inputTags["environment"] = "caller-mutated"
	updated.Spec.Tags["environment"] = "returned-mutated"
	if err := store.Delete(ctx, "", parent.ID); !errors.Is(err, ErrResourceHasDependents) {
		t.Fatalf("Delete(parent with child) error = %v, want ErrResourceHasDependents", err)
	}

	reopened, err := NewFileResourceStore(path)
	if err != nil {
		t.Fatalf("NewFileResourceStore(reopen) error = %v", err)
	}
	stored, err := reopened.Get(ctx, parent.ID, child.ID)
	if err != nil {
		t.Fatalf("Get(reopened, child) error = %v", err)
	}
	if stored.Spec.Tags["environment"] != "test" {
		t.Fatalf("persisted tag = %q, want %q", stored.Spec.Tags["environment"], "test")
	}
	if stored.ID != child.ID || stored.Spec.Name != child.Spec.Name || stored.Spec.ParentID != parent.ID {
		t.Fatalf("immutable child fields changed after UpdateTags(): %#v", stored)
	}
	if err := reopened.Delete(ctx, parent.ID, child.ID); err != nil {
		t.Fatalf("Delete(child) error = %v", err)
	}
	if err := reopened.Delete(ctx, "", parent.ID); err != nil {
		t.Fatalf("Delete(parent after child) error = %v", err)
	}

	finalStore, err := NewFileResourceStore(path)
	if err != nil {
		t.Fatalf("NewFileResourceStore(final reopen) error = %v", err)
	}
	if _, err := finalStore.Get(ctx, "", parent.ID); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("Get(deleted parent) error = %v, want ErrResourceNotFound", err)
	}
	rootResources, err := finalStore.List(ctx, "", MaxResourceListLimit)
	if err != nil {
		t.Fatalf("List(final root) error = %v", err)
	}
	if len(rootResources) != 0 {
		t.Fatalf("final root resources = %#v, want no residue", rootResources)
	}
}

func TestFileResourceStoreLockStateIsProcessLocal(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.json")
	ctx := context.Background()
	store, err := NewFileResourceStore(path)
	if err != nil {
		t.Fatalf("NewFileResourceStore() error = %v", err)
	}
	resource, err := store.Create(ctx, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "platform"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	lock := models.ResourceLock{Owner: "controller-a", Token: "token-a"}
	conflict := models.ResourceLock{Owner: "controller-b", Token: "token-b"}
	if err := store.AcquireLock(ctx, "", resource.ID, lock); err != nil {
		t.Fatalf("AcquireLock() error = %v", err)
	}
	if err := store.AcquireLock(ctx, "", resource.ID, conflict); !errors.Is(err, ErrResourceLockConflict) {
		t.Fatalf("conflicting AcquireLock() error = %v, want ErrResourceLockConflict", err)
	}

	reopened, err := NewFileResourceStore(path)
	if err != nil {
		t.Fatalf("NewFileResourceStore(reopen) error = %v", err)
	}
	inspected, err := reopened.InspectLock(ctx, "", resource.ID)
	if inspected == nil || *inspected != lock {
		t.Fatalf("reopened lock = %#v, %v; want %#v", inspected, err, lock)
	}
	if err := reopened.AcquireLock(ctx, "", resource.ID, conflict); !errors.Is(err, ErrResourceLockConflict) {
		t.Fatalf("cross-store conflicting AcquireLock() error = %v, want ErrResourceLockConflict", err)
	}
	if err := store.ReleaseLock(ctx, "", resource.ID, lock); err != nil {
		t.Fatalf("ReleaseLock() error = %v", err)
	}
}

func TestFileResourceStoreLockStatePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "resources.json")
	ctx := context.Background()
	store, err := NewFileResourceStore(path)
	if err != nil {
		t.Fatalf("NewFileResourceStore() error = %v", err)
	}
	resource, err := store.Create(ctx, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "platform"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	lock := models.ResourceLock{Owner: "controller-a", Token: "token-a"}
	if err := store.AcquireLock(ctx, "", resource.ID, lock); err != nil {
		t.Fatalf("AcquireLock() error = %v", err)
	}
	reopened, err := NewFileResourceStore(path)
	if err != nil {
		t.Fatalf("NewFileResourceStore(reopen) error = %v", err)
	}
	inspected, err := reopened.InspectLock(ctx, "", resource.ID)
	if err != nil || inspected == nil || *inspected != lock {
		t.Fatalf("InspectLock(reopen) = %#v, %v; want %#v", inspected, err, lock)
	}
}

func TestFileResourceStoreRejectsCorruptAndOversizedSnapshots(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want error
	}{
		{name: "malformed json", data: []byte("{not-json"), want: ErrResourceStoreCorrupt},
		{name: "unsupported version", data: []byte(`{"version":2,"next_id":0,"resources":[]}`), want: ErrResourceStoreCorrupt},
		{name: "oversized snapshot", data: bytes.Repeat([]byte("x"), MaxResourceStoreFileBytes+1), want: ErrResourceStoreTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "resources.json")
			if err := os.WriteFile(path, tt.data, 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}
			if _, err := NewFileResourceStore(path); !errors.Is(err, tt.want) {
				t.Fatalf("NewFileResourceStore() error = %v, want %v", err, tt.want)
			}
		})
	}
}
