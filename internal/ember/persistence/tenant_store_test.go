package persistence

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	_ "modernc.org/sqlite"
)

func TestTenantStoreCreatesSQLiteRegistryAndPersistsTenantIdentity(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewTenantStore(root)
	if err != nil {
		t.Fatalf("NewTenantStore() error = %v", err)
	}

	if _, err := os.Stat(filepath.Join(root, "platform.db")); err != nil {
		t.Fatalf("platform.db stat error = %v", err)
	}
	created, err := store.CreateTenant(ctx, models.Tenant{ID: "alpha", DisplayName: "Alpha"})
	if err != nil {
		t.Fatalf("CreateTenant() error = %v", err)
	}
	if created.ID != "alpha" || created.DisplayName != "Alpha" || created.CreatedAt.IsZero() {
		t.Fatalf("created tenant = %#v, want assigned identity", created)
	}
	if _, err := os.Stat(filepath.Join(root, "tenants", "alpha", "tenant.db")); err != nil {
		t.Fatalf("tenant.db stat error = %v", err)
	}

	opened, err := store.OpenTenant(ctx, "alpha")
	if err != nil {
		t.Fatalf("OpenTenant() error = %v", err)
	}
	if opened.ID() != "alpha" {
		t.Fatalf("opened tenant ID = %q, want alpha", opened.ID())
	}
	if err := opened.Close(); err != nil {
		t.Fatalf("TenantDatabase.Close() error = %v", err)
	}

	listed, err := store.ListTenants(ctx, MaxTenantListLimit)
	if err != nil {
		t.Fatalf("ListTenants() error = %v", err)
	}
	if len(listed) != 1 || listed[0].ID != "alpha" {
		t.Fatalf("ListTenants() = %#v, want alpha", listed)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("TenantStore.Close() error = %v", err)
	}
	reopened, err := NewTenantStore(root)
	if err != nil {
		t.Fatalf("reopen NewTenantStore() error = %v", err)
	}
	deferredClose := reopened.Close
	defer deferredClose()
	fetched, err := reopened.GetTenant(ctx, "alpha")
	if err != nil {
		t.Fatalf("reopened GetTenant() error = %v", err)
	}
	if fetched.ID != created.ID || fetched.DisplayName != created.DisplayName || !fetched.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("reopened tenant = %#v, want %#v", fetched, created)
	}
}

func TestTenantStoreRejectsUnsafeDuplicateAndInvalidTenantRegistration(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewTenantStore(root)
	if err != nil {
		t.Fatalf("NewTenantStore() error = %v", err)
	}
	defer store.Close()

	invalid := []models.Tenant{
		{ID: "", DisplayName: "empty"},
		{ID: "../escape", DisplayName: "traversal"},
		{ID: "tenant/child", DisplayName: "separator"},
		{ID: "tenant space", DisplayName: "space"},
		{ID: "valid", DisplayName: "   "},
	}
	for _, tenant := range invalid {
		if _, err := store.CreateTenant(ctx, tenant); !errors.Is(err, ErrInvalidTenant) {
			t.Fatalf("CreateTenant(%#v) error = %v, want ErrInvalidTenant", tenant, err)
		}
	}

	if _, err := store.CreateTenant(ctx, models.Tenant{ID: "duplicate", DisplayName: "first"}); err != nil {
		t.Fatalf("first CreateTenant() error = %v", err)
	}
	if _, err := store.CreateTenant(ctx, models.Tenant{ID: "duplicate", DisplayName: "second"}); !errors.Is(err, ErrDuplicateTenant) {
		t.Fatalf("duplicate CreateTenant() error = %v, want ErrDuplicateTenant", err)
	}

	external := t.TempDir()
	if err := os.Symlink(external, filepath.Join(root, "tenants", "linked")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	if _, err := store.CreateTenant(ctx, models.Tenant{ID: "linked", DisplayName: "linked"}); !errors.Is(err, ErrTenantPathEscape) {
		t.Fatalf("symlink CreateTenant() error = %v, want ErrTenantPathEscape", err)
	}
}

func TestTenantStoreFailsClosedForUnsupportedSchemaVersions(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewTenantStore(root)
	if err != nil {
		t.Fatalf("NewTenantStore() error = %v", err)
	}
	if _, err := store.CreateTenant(ctx, models.Tenant{ID: "alpha", DisplayName: "Alpha"}); err != nil {
		t.Fatalf("CreateTenant() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("TenantStore.Close() error = %v", err)
	}

	db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(root, "platform.db")))
	if err != nil {
		t.Fatalf("sql.Open(platform.db) error = %v", err)
	}
	if _, err := db.Exec(`UPDATE ember_schema SET version = 999 WHERE component = 'platform'`); err != nil {
		t.Fatalf("schema update error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("schema db close error = %v", err)
	}

	if _, err := NewTenantStore(root); !errors.Is(err, ErrUnsupportedTenantSchema) {
		t.Fatalf("unsupported platform schema error = %v, want ErrUnsupportedTenantSchema", err)
	}
}

func TestTenantStoreRejectsUnsupportedTenantSchemaOnOpen(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewTenantStore(root)
	if err != nil {
		t.Fatalf("NewTenantStore() error = %v", err)
	}
	if _, err := store.CreateTenant(ctx, models.Tenant{ID: "alpha", DisplayName: "Alpha"}); err != nil {
		t.Fatalf("CreateTenant() error = %v", err)
	}
	db, err := sql.Open("sqlite", sqliteDSN(filepath.Join(root, "tenants", "alpha", "tenant.db")))
	if err != nil {
		t.Fatalf("sql.Open(tenant.db) error = %v", err)
	}
	if _, err := db.Exec(`UPDATE ember_schema SET version = 999 WHERE component = 'tenant'`); err != nil {
		t.Fatalf("tenant schema update error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("tenant schema db close error = %v", err)
	}
	if _, err := store.OpenTenant(ctx, "alpha"); !errors.Is(err, ErrUnsupportedTenantSchema) {
		t.Fatalf("unsupported tenant schema error = %v, want ErrUnsupportedTenantSchema", err)
	}
	store.Close()
}
