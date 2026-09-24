package persistence

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	_ "modernc.org/sqlite"
)

func TestSQLiteResourceStorePersistsPlatformResourceLifecycle(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "platform.db")
	store, err := NewSQLiteResourceStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteResourceStore() error = %v", err)
	}

	root, err := store.Create(ctx, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "platform"})
	if err != nil {
		t.Fatalf("Create(root) error = %v", err)
	}
	child, err := store.Create(ctx, models.ResourceSpec{
		Type: models.ResourceTypeBucket, Name: "assets", ParentID: root.ID,
		Tags: map[string]string{"environment": "test"},
	})
	if err != nil {
		t.Fatalf("Create(child) error = %v", err)
	}
	updated, err := store.UpdateTags(ctx, root.ID, child.ID, map[string]string{"tier": "platform"})
	if err != nil {
		t.Fatalf("UpdateTags() error = %v", err)
	}
	if updated.Spec.Tags["tier"] != "platform" {
		t.Fatalf("updated tags = %#v, want platform tag", updated.Spec.Tags)
	}
	if _, err := store.UpdateObservedState(ctx, root.ID, child.ID, models.ResourceStateReady); err != nil {
		t.Fatalf("UpdateObservedState() error = %v", err)
	}
	if _, err := store.Get(ctx, "other-scope", child.ID); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("cross-scope Get() error = %v, want ErrResourceNotFound", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := NewSQLiteResourceStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteResourceStore(reopen) error = %v", err)
	}
	defer reopened.Close()
	got, err := reopened.Get(ctx, root.ID, child.ID)
	if err != nil {
		t.Fatalf("Get(reopen) error = %v", err)
	}
	if got.ID != child.ID || got.Spec.Tags["tier"] != "platform" || got.ObservedState != models.ResourceStateReady {
		t.Fatalf("reopened child = %#v, want persisted mutation", got)
	}
	if err := reopened.Delete(ctx, "", root.ID); !errors.Is(err, ErrResourceHasDependents) {
		t.Fatalf("Delete(root with child) error = %v, want dependency refusal", err)
	}
	if err := reopened.Delete(ctx, root.ID, child.ID); err != nil {
		t.Fatalf("Delete(child) error = %v", err)
	}
	if err := reopened.Delete(ctx, "", root.ID); err != nil {
		t.Fatalf("Delete(root) error = %v", err)
	}
	resources, err := reopened.List(ctx, "", MaxResourceListLimit)
	if err != nil {
		t.Fatalf("List(after delete) error = %v", err)
	}
	if len(resources) != 0 {
		t.Fatalf("resources after delete = %#v, want empty", resources)
	}
}

func TestSQLiteResourceStorePersistsAndCoordinatesResourceLocks(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "platform.db")
	first, err := NewSQLiteResourceStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteResourceStore(first) error = %v", err)
	}
	resource, err := first.Create(ctx, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "platform"})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	lock := models.ResourceLock{Owner: "controller-a", Token: "token-a"}
	conflict := models.ResourceLock{Owner: "controller-b", Token: "token-b"}
	if err := first.AcquireLock(ctx, "", resource.ID, lock); err != nil {
		t.Fatalf("AcquireLock(first) error = %v", err)
	}

	second, err := NewSQLiteResourceStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteResourceStore(second) error = %v", err)
	}
	defer second.Close()
	inspected, err := second.InspectLock(ctx, "", resource.ID)
	if err != nil || inspected == nil || *inspected != lock {
		t.Fatalf("InspectLock(second) = %#v, %v; want %#v", inspected, err, lock)
	}
	if err := second.AcquireLock(ctx, "", resource.ID, conflict); !errors.Is(err, ErrResourceLockConflict) {
		t.Fatalf("AcquireLock(second) error = %v, want ErrResourceLockConflict", err)
	}
	if err := second.ReleaseLock(ctx, "", resource.ID, conflict); !errors.Is(err, ErrResourceLockNotOwner) {
		t.Fatalf("ReleaseLock(second, non-owner) error = %v, want ErrResourceLockNotOwner", err)
	}
	if err := second.ReleaseLock(ctx, "", resource.ID, lock); err != nil {
		t.Fatalf("ReleaseLock(second) error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(first) error = %v", err)
	}
}

func TestSQLiteResourceStoreRejectsUnsupportedSchema(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "platform.db")
	store, err := NewSQLiteResourceStore(path)
	if err != nil {
		t.Fatalf("NewSQLiteResourceStore() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	database, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		t.Fatalf("sql.Open() error = %v", err)
	}
	if _, err := database.ExecContext(ctx,
		`UPDATE ember_schema SET version = 999 WHERE component = 'platform_resources'`); err != nil {
		t.Fatalf("schema update error = %v", err)
	}
	if err := database.Close(); err != nil {
		t.Fatalf("database.Close() error = %v", err)
	}
	if _, err := NewSQLiteResourceStore(path); !errors.Is(err, ErrUnsupportedSQLiteResourceSchema) {
		t.Fatalf("NewSQLiteResourceStore(unsupported) error = %v, want %v", err, ErrUnsupportedSQLiteResourceSchema)
	}
}

var _ ResourceStore = (*SQLiteResourceStore)(nil)
