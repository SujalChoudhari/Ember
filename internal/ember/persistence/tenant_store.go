package persistence

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	_ "modernc.org/sqlite"
)

const (
	tenantStoreSchemaVersion = 1
	MaxTenantListLimit       = 100
)

var (
	ErrInvalidTenantStorePath  = errors.New("invalid tenant store path")
	ErrTenantStoreCorrupt      = errors.New("corrupt tenant store")
	ErrTenantStoreIO           = errors.New("tenant store I/O failure")
	ErrUnsupportedTenantSchema = errors.New("unsupported tenant schema version")
	ErrInvalidTenant           = errors.New("invalid tenant")
	ErrDuplicateTenant         = errors.New("duplicate tenant")
	ErrTenantNotFound          = errors.New("tenant not found")
	ErrTenantDatabaseMissing   = errors.New("tenant database missing")
	ErrTenantPathEscape        = errors.New("tenant path escapes owned root")
	ErrInvalidTenantListLimit  = errors.New("invalid tenant list limit")
)

type TenantStore struct {
	mu         sync.Mutex
	root       string
	tenantRoot string
	platformDB *sql.DB
	closed     bool
}

// TenantDatabase is a validated handle to one tenant-owned SQLite database.
// Callers must close it when finished; no path supplied by a caller is used to
// open the database.
type TenantDatabase struct {
	db *sql.DB
	id string
}

func (database *TenantDatabase) ID() string {
	if database == nil {
		return ""
	}
	return database.id
}

func (database *TenantDatabase) Close() error {
	if database == nil || database.db == nil {
		return nil
	}
	return database.db.Close()
}

func NewTenantStore(root string) (*TenantStore, error) {
	normalizedRoot, err := prepareOwnedDirectory(root)
	if err != nil {
		return nil, err
	}
	tenantRoot := filepath.Join(normalizedRoot, "tenants")
	if err := prepareChildDirectory(normalizedRoot, tenantRoot); err != nil {
		return nil, err
	}
	platformPath := filepath.Join(normalizedRoot, "platform.db")
	if err := rejectSymlink(platformPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	platformDB, err := openSQLite(platformPath)
	if err != nil {
		return nil, err
	}
	store := &TenantStore{root: normalizedRoot, tenantRoot: tenantRoot, platformDB: platformDB}
	if err := ensurePlatformSchema(context.Background(), platformDB); err != nil {
		_ = platformDB.Close()
		return nil, err
	}
	return store, nil
}

func (store *TenantStore) Close() error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return nil
	}
	store.closed = true
	if err := store.platformDB.Close(); err != nil {
		return ErrTenantStoreIO
	}
	return nil
}

func (store *TenantStore) CreateTenant(ctx context.Context, tenant models.Tenant) (*models.Tenant, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateTenant(tenant); err != nil {
		return nil, err
	}
	tenant.DisplayName = strings.TrimSpace(tenant.DisplayName)
	tenant.CreatedAt = time.Now().UTC()

	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ensureOpen(); err != nil {
		return nil, err
	}
	if _, err := store.getTenantLocked(ctx, tenant.ID); err == nil {
		return nil, ErrDuplicateTenant
	} else if !errors.Is(err, ErrTenantNotFound) {
		return nil, err
	}
	tenantDirectory, databasePath, err := store.tenantPathsLocked(tenant.ID, true)
	if err != nil {
		return nil, err
	}
	if err := os.Mkdir(tenantDirectory, 0o700); err != nil {
		return nil, ErrTenantStoreIO
	}
	database, err := openSQLite(databasePath)
	if err != nil {
		_ = os.Remove(tenantDirectory)
		return nil, err
	}
	if err := initializeTenantSchema(ctx, database, tenant); err != nil {
		_ = database.Close()
		_ = removeTenantDirectory(tenantDirectory)
		return nil, err
	}
	if _, err := store.platformDB.ExecContext(ctx,
		`INSERT INTO tenants (id, display_name, created_at) VALUES (?, ?, ?)`,
		tenant.ID, tenant.DisplayName, tenant.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		_ = database.Close()
		_ = removeTenantDirectory(tenantDirectory)
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, ErrDuplicateTenant
		}
		return nil, ErrTenantStoreIO
	}
	if err := database.Close(); err != nil {
		return nil, ErrTenantStoreIO
	}
	return &tenant, nil
}

func (store *TenantStore) GetTenant(ctx context.Context, id string) (*models.Tenant, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateTenantID(id); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ensureOpen(); err != nil {
		return nil, err
	}
	return store.getTenantLocked(ctx, id)
}

func (store *TenantStore) ListTenants(ctx context.Context, limit int) ([]models.Tenant, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxTenantListLimit {
		return nil, ErrInvalidTenantListLimit
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ensureOpen(); err != nil {
		return nil, err
	}
	rows, err := store.platformDB.QueryContext(ctx,
		`SELECT id, display_name, created_at FROM tenants ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, ErrTenantStoreIO
	}
	defer rows.Close()
	var tenants []models.Tenant
	for rows.Next() {
		var tenant models.Tenant
		var createdAt string
		if err := rows.Scan(&tenant.ID, &tenant.DisplayName, &createdAt); err != nil {
			return nil, ErrTenantStoreCorrupt
		}
		parsed, err := time.Parse(time.RFC3339Nano, createdAt)
		if err != nil {
			return nil, ErrTenantStoreCorrupt
		}
		tenant.CreatedAt = parsed
		if err := tenant.Validate(); err != nil {
			return nil, ErrTenantStoreCorrupt
		}
		tenants = append(tenants, tenant)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrTenantStoreIO
	}
	return tenants, nil
}

func (store *TenantStore) DeleteTenant(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateTenantID(id); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ensureOpen(); err != nil {
		return err
	}
	if _, err := store.getTenantLocked(ctx, id); err != nil {
		return err
	}
	tenantDirectory, _, err := store.tenantPathsLocked(id, false)
	if err != nil {
		return err
	}
	if err := removeTenantDirectory(tenantDirectory); err != nil {
		return err
	}
	if _, err := store.platformDB.ExecContext(ctx, `DELETE FROM tenants WHERE id = ?`, id); err != nil {
		return ErrTenantStoreIO
	}
	return nil
}

// Reset removes every tenant database owned by the store and clears the
// platform registry while preserving the versioned platform.db boundary.
func (store *TenantStore) Reset(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ensureOpen(); err != nil {
		return err
	}
	rows, err := store.platformDB.QueryContext(ctx, `SELECT id FROM tenants ORDER BY id`)
	if err != nil {
		return ErrTenantStoreIO
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			return ErrTenantStoreCorrupt
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return ErrTenantStoreIO
	}
	if err := rows.Close(); err != nil {
		return ErrTenantStoreIO
	}
	for _, id := range ids {
		tenantDirectory, _, err := store.tenantPathsLocked(id, false)
		if err != nil {
			return err
		}
		if err := removeTenantDirectory(tenantDirectory); err != nil {
			return err
		}
	}
	if _, err := store.platformDB.ExecContext(ctx, `DELETE FROM tenants`); err != nil {
		return ErrTenantStoreIO
	}
	return nil
}

func (store *TenantStore) OpenTenant(ctx context.Context, id string) (*TenantDatabase, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateTenantID(id); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.ensureOpen(); err != nil {
		return nil, err
	}
	tenant, err := store.getTenantLocked(ctx, id)
	if err != nil {
		return nil, err
	}
	_, databasePath, err := store.tenantPathsLocked(id, false)
	if err != nil {
		return nil, err
	}
	database, err := openSQLiteExisting(databasePath)
	if err != nil {
		return nil, err
	}
	if err := validateTenantSchema(ctx, database, *tenant); err != nil {
		_ = database.Close()
		return nil, err
	}
	return &TenantDatabase{db: database, id: id}, nil
}

func (store *TenantStore) getTenantLocked(ctx context.Context, id string) (*models.Tenant, error) {
	var tenant models.Tenant
	var createdAt string
	err := store.platformDB.QueryRowContext(ctx,
		`SELECT id, display_name, created_at FROM tenants WHERE id = ?`, id).
		Scan(&tenant.ID, &tenant.DisplayName, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrTenantNotFound
	}
	if err != nil {
		return nil, ErrTenantStoreIO
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return nil, ErrTenantStoreCorrupt
	}
	tenant.CreatedAt = parsed
	if err := tenant.Validate(); err != nil {
		return nil, ErrTenantStoreCorrupt
	}
	return &tenant, nil
}

func (store *TenantStore) tenantPathsLocked(id string, create bool) (string, string, error) {
	if err := validateTenantID(id); err != nil {
		return "", "", err
	}
	tenantDirectory := filepath.Join(store.tenantRoot, id)
	relative, err := filepath.Rel(store.root, tenantDirectory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", "", ErrTenantPathEscape
	}
	if err := rejectSymlink(tenantDirectory); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	if create {
		if info, err := os.Lstat(tenantDirectory); err == nil && !info.IsDir() {
			return "", "", ErrTenantPathEscape
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", "", ErrTenantStoreIO
		}
	}
	databasePath := filepath.Join(tenantDirectory, "tenant.db")
	if err := rejectSymlink(databasePath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", "", err
	}
	return tenantDirectory, databasePath, nil
}

func (store *TenantStore) ensureOpen() error {
	if store == nil || store.platformDB == nil || store.closed {
		return ErrTenantStoreIO
	}
	return nil
}

func validateTenant(tenant models.Tenant) error {
	if err := tenant.Validate(); err != nil {
		return ErrInvalidTenant
	}
	return nil
}

func validateTenantID(id string) error {
	if err := (models.Tenant{ID: id, DisplayName: "valid"}).Validate(); err != nil {
		return ErrInvalidTenant
	}
	return nil
}

func prepareOwnedDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", ErrInvalidTenantStorePath
	}
	absolute, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return "", ErrInvalidTenantStorePath
	}
	if info, err := os.Lstat(absolute); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return "", ErrTenantPathEscape
		}
	} else if errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(absolute, 0o700); err != nil {
			return "", ErrTenantStoreIO
		}
	} else {
		return "", ErrTenantStoreIO
	}
	return absolute, nil
}

func prepareChildDirectory(root, child string) error {
	relative, err := filepath.Rel(root, child)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return ErrTenantPathEscape
	}
	if info, err := os.Lstat(child); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return ErrTenantPathEscape
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrTenantStoreIO
	}
	if err := os.Mkdir(child, 0o700); err != nil {
		return ErrTenantStoreIO
	}
	return nil
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrTenantPathEscape
	}
	return nil
}

func openSQLite(path string) (*sql.DB, error) {
	database, err := sql.Open("sqlite", sqliteDSN(path))
	if err != nil {
		return nil, ErrTenantStoreIO
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := database.Ping(); err != nil {
		_ = database.Close()
		return nil, ErrTenantStoreIO
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = database.Close()
		return nil, ErrTenantStoreIO
	}
	return database, nil
}

func openSQLiteExisting(path string) (*sql.DB, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrTenantDatabaseMissing
	}
	if err != nil {
		return nil, ErrTenantStoreIO
	}
	if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, ErrTenantPathEscape
	}
	return openSQLite(path)
}

func sqliteDSN(path string) string {
	location := &url.URL{Scheme: "file", Path: filepath.ToSlash(path)}
	query := url.Values{}
	query.Set("_pragma", "foreign_keys(1)")
	location.RawQuery = query.Encode()
	return location.String()
}

func ensurePlatformSchema(ctx context.Context, database *sql.DB) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return ErrTenantStoreIO
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS ember_schema (
		component TEXT PRIMARY KEY,
		version INTEGER NOT NULL
	)`); err != nil {
		return ErrTenantStoreIO
	}
	var version int
	err = transaction.QueryRowContext(ctx, `SELECT version FROM ember_schema WHERE component = 'platform'`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := transaction.ExecContext(ctx, `INSERT INTO ember_schema (component, version) VALUES ('platform', ?)`, tenantStoreSchemaVersion); err != nil {
			return ErrTenantStoreIO
		}
	} else if err != nil {
		return ErrTenantStoreIO
	} else if version != tenantStoreSchemaVersion {
		return ErrUnsupportedTenantSchema
	}
	if _, err := transaction.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS platform_metadata (
		key TEXT PRIMARY KEY,
		value TEXT NOT NULL
	)`); err != nil {
		return ErrTenantStoreIO
	}
	if _, err := transaction.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS tenants (
		id TEXT PRIMARY KEY,
		display_name TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`); err != nil {
		return ErrTenantStoreIO
	}
	if err := transaction.Commit(); err != nil {
		return ErrTenantStoreIO
	}
	return nil
}

func initializeTenantSchema(ctx context.Context, database *sql.DB, tenant models.Tenant) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return ErrTenantStoreIO
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS ember_schema (
		component TEXT PRIMARY KEY,
		version INTEGER NOT NULL
	)`); err != nil {
		return ErrTenantStoreIO
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO ember_schema (component, version) VALUES ('tenant', ?)`, tenantStoreSchemaVersion); err != nil {
		return ErrTenantStoreIO
	}
	if _, err := transaction.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS tenant_metadata (
		id TEXT PRIMARY KEY,
		display_name TEXT NOT NULL,
		created_at TEXT NOT NULL
	)`); err != nil {
		return ErrTenantStoreIO
	}
	if _, err := transaction.ExecContext(ctx,
		`INSERT INTO tenant_metadata (id, display_name, created_at) VALUES (?, ?, ?)`,
		tenant.ID, tenant.DisplayName, tenant.CreatedAt.Format(time.RFC3339Nano)); err != nil {
		return ErrTenantStoreIO
	}
	if err := transaction.Commit(); err != nil {
		return ErrTenantStoreIO
	}
	return nil
}

func validateTenantSchema(ctx context.Context, database *sql.DB, expected models.Tenant) error {
	var version int
	if err := database.QueryRowContext(ctx, `SELECT version FROM ember_schema WHERE component = 'tenant'`).Scan(&version); errors.Is(err, sql.ErrNoRows) {
		return ErrTenantStoreCorrupt
	} else if err != nil {
		return ErrTenantStoreIO
	} else if version != tenantStoreSchemaVersion {
		return ErrUnsupportedTenantSchema
	}
	var actual models.Tenant
	var createdAt string
	if err := database.QueryRowContext(ctx, `SELECT id, display_name, created_at FROM tenant_metadata`).Scan(&actual.ID, &actual.DisplayName, &createdAt); err != nil {
		return ErrTenantStoreCorrupt
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return ErrTenantStoreCorrupt
	}
	actual.CreatedAt = parsed
	if err := actual.Validate(); err != nil || actual.ID != expected.ID || actual.DisplayName != expected.DisplayName || !actual.CreatedAt.Equal(expected.CreatedAt) {
		return ErrTenantStoreCorrupt
	}
	return nil
}

func removeTenantDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if errors.Is(err, os.ErrNotExist) {
		return ErrTenantDatabaseMissing
	}
	if err != nil {
		return ErrTenantStoreIO
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return ErrTenantPathEscape
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return ErrTenantStoreIO
	}
	for _, entry := range entries {
		name := entry.Name()
		if name != "tenant.db" && name != "tenant.db-shm" && name != "tenant.db-wal" && name != "tenant.db-journal" {
			return ErrTenantPathEscape
		}
		entryPath := filepath.Join(directory, name)
		if err := rejectSymlink(entryPath); err != nil {
			return err
		}
		if err := os.Remove(entryPath); err != nil {
			return ErrTenantStoreIO
		}
	}
	if err := os.Remove(directory); err != nil {
		return ErrTenantStoreIO
	}
	return nil
}
