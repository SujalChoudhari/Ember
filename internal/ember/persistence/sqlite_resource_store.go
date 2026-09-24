package persistence

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const sqliteResourceStoreSchemaVersion = 1

var (
	ErrInvalidSQLiteResourceStorePath  = errors.New("invalid SQLite resource store path")
	ErrUnsupportedSQLiteResourceSchema = errors.New("unsupported SQLite resource schema version")
)

// SQLiteResourceStore persists one resource scope in a SQLite database. Lock
// ownership intentionally remains process-local, matching FileResourceStore;
// resource identity and desired/observed state are durable.
type SQLiteResourceStore struct {
	mu       sync.Mutex
	db       *sql.DB
	locks    map[string]models.ResourceLock
	idPrefix string
	closed   bool
}

type sqliteResourcePayload struct {
	Spec          models.ResourceSpec  `json:"spec"`
	ObservedState models.ResourceState `json:"observed_state"`
}

type sqliteQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func NewSQLiteResourceStore(path string) (*SQLiteResourceStore, error) {
	return newSQLiteResourceStore(path, "")
}

func NewSQLiteResourceStoreWithIDPrefix(path, idPrefix string) (*SQLiteResourceStore, error) {
	return newSQLiteResourceStore(path, idPrefix)
}

func newSQLiteResourceStore(path, idPrefix string) (*SQLiteResourceStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrInvalidSQLiteResourceStorePath
	}
	path = filepath.Clean(path)
	if info, err := os.Lstat(path); err == nil {
		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrInvalidSQLiteResourceStorePath
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, ErrResourceStoreIO
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, ErrResourceStoreIO
	}

	database, err := openSQLite(path)
	if err != nil {
		return nil, ErrResourceStoreIO
	}
	store := &SQLiteResourceStore{db: database, locks: make(map[string]models.ResourceLock), idPrefix: idPrefix}
	if err := store.ensureSchema(context.Background()); err != nil {
		_ = database.Close()
		return nil, err
	}
	return store, nil
}

func (store *SQLiteResourceStore) Close() error {
	if store == nil {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.closed {
		return nil
	}
	store.closed = true
	if err := store.db.Close(); err != nil {
		return ErrResourceStoreIO
	}
	return nil
}

func (store *SQLiteResourceStore) ensureSchema(ctx context.Context) error {
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return sqliteResourceStoreError(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS ember_schema (
		component TEXT PRIMARY KEY,
		version INTEGER NOT NULL
	)`); err != nil {
		return sqliteResourceStoreError(err)
	}
	var version int
	err = transaction.QueryRowContext(ctx, `SELECT version FROM ember_schema WHERE component = 'platform_resources'`).Scan(&version)
	if errors.Is(err, sql.ErrNoRows) {
		if _, err := transaction.ExecContext(ctx,
			`INSERT INTO ember_schema (component, version) VALUES ('platform_resources', ?)`, sqliteResourceStoreSchemaVersion); err != nil {
			return sqliteResourceStoreError(err)
		}
	} else if err != nil {
		return sqliteResourceStoreError(err)
	} else if version != sqliteResourceStoreSchemaVersion {
		return ErrUnsupportedSQLiteResourceSchema
	}
	if _, err := transaction.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS ember_platform_resources (
		id TEXT PRIMARY KEY,
		parent_id TEXT NOT NULL,
		resource_type TEXT NOT NULL,
		resource_name TEXT NOT NULL,
		payload TEXT NOT NULL,
		observed_state TEXT NOT NULL
	)`); err != nil {
		return sqliteResourceStoreError(err)
	}
	if _, err := transaction.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS ember_platform_resources_scope_name
		ON ember_platform_resources (parent_id, resource_type, resource_name)`); err != nil {
		return sqliteResourceStoreError(err)
	}
	if _, err := transaction.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS ember_platform_resource_sequence (
		sequence_id INTEGER PRIMARY KEY CHECK (sequence_id = 1),
		next_id INTEGER NOT NULL
	)`); err != nil {
		return sqliteResourceStoreError(err)
	}
	if _, err := transaction.ExecContext(ctx,
		`INSERT OR IGNORE INTO ember_platform_resource_sequence (sequence_id, next_id) VALUES (1, 0)`); err != nil {
		return sqliteResourceStoreError(err)
	}
	if _, err := transaction.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS ember_platform_resource_locks (
		resource_id TEXT PRIMARY KEY,
		owner TEXT NOT NULL,
		token TEXT NOT NULL
	)`); err != nil {
		return sqliteResourceStoreError(err)
	}
	if err := transaction.Commit(); err != nil {
		return sqliteResourceStoreError(err)
	}
	return nil
}

func sqliteResourceStoreError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return ErrResourceStoreIO
}

func validateSQLiteResourceScope(scopeID string) error {
	if scopeID != "" && strings.TrimSpace(scopeID) == "" {
		return ErrInvalidScope
	}
	return nil
}

func encodeSQLiteResource(resource models.Resource) ([]byte, error) {
	payload, err := json.Marshal(sqliteResourcePayload{
		Spec:          resource.Spec,
		ObservedState: resource.ObservedState,
	})
	if err != nil {
		return nil, ErrResourceStoreCorrupt
	}
	return payload, nil
}

func decodeSQLiteResource(id, parentID, payload string) (*models.Resource, error) {
	var stored sqliteResourcePayload
	if err := json.Unmarshal([]byte(payload), &stored); err != nil {
		return nil, ErrResourceStoreCorrupt
	}
	resource := &models.Resource{ID: id, Spec: stored.Spec, ObservedState: stored.ObservedState}
	if resource.Spec.ParentID != parentID || resource.Validate() != nil {
		return nil, ErrResourceStoreCorrupt
	}
	return resource, nil
}

func (store *SQLiteResourceStore) getResourceWithScope(ctx context.Context, queryer sqliteQueryer, scopeID, resourceID string) (*models.Resource, error) {
	var id, parentID, payload string
	err := queryer.QueryRowContext(ctx,
		`SELECT id, parent_id, payload FROM ember_platform_resources WHERE id = ? AND parent_id = ?`, resourceID, scopeID).
		Scan(&id, &parentID, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrResourceNotFound
	}
	if err != nil {
		return nil, sqliteResourceStoreError(err)
	}
	return decodeSQLiteResource(id, parentID, payload)
}

func (store *SQLiteResourceStore) getResourceByID(ctx context.Context, queryer sqliteQueryer, resourceID string) (*models.Resource, error) {
	var id, parentID, payload string
	err := queryer.QueryRowContext(ctx,
		`SELECT id, parent_id, payload FROM ember_platform_resources WHERE id = ?`, resourceID).
		Scan(&id, &parentID, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrResourceNotFound
	}
	if err != nil {
		return nil, sqliteResourceStoreError(err)
	}
	return decodeSQLiteResource(id, parentID, payload)
}

func (store *SQLiteResourceStore) hasReadOnlyLock(ctx context.Context, queryer sqliteQueryer, resourceID string) (bool, error) {
	visited := make(map[string]struct{})
	for resourceID != "" {
		if _, seen := visited[resourceID]; seen {
			return true, nil
		}
		visited[resourceID] = struct{}{}
		err := queryer.QueryRowContext(ctx,
			`SELECT 1 FROM ember_platform_resource_locks WHERE resource_id = ?`, resourceID).Scan(new(int))
		if err == nil {
			return true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return false, sqliteResourceStoreError(err)
		}
		resource, err := store.getResourceByID(ctx, queryer, resourceID)
		if errors.Is(err, ErrResourceNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		resourceID = resource.Spec.ParentID
	}
	return false, nil
}

func (store *SQLiteResourceStore) Create(ctx context.Context, spec models.ResourceSpec) (*models.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSQLiteResourceScope(spec.ParentID); err != nil {
		return nil, err
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, sqliteResourceStoreError(err)
	}
	defer transaction.Rollback()
	if spec.ParentID != "" {
		if _, err := store.getResourceByID(ctx, transaction, spec.ParentID); err != nil {
			return nil, err
		}
	}
	locked, err := store.hasReadOnlyLock(ctx, transaction, spec.ParentID)
	if err != nil {
		return nil, err
	}
	if locked {
		return nil, ErrResourceLocked
	}
	var duplicate int
	err = transaction.QueryRowContext(ctx,
		`SELECT 1 FROM ember_platform_resources WHERE parent_id = ? AND resource_type = ? AND resource_name = ?`,
		spec.ParentID, spec.Type, spec.Name).Scan(&duplicate)
	if err == nil {
		return nil, ErrDuplicateResource
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, sqliteResourceStoreError(err)
	}

	var nextID int64
	if err := transaction.QueryRowContext(ctx,
		`SELECT next_id FROM ember_platform_resource_sequence WHERE sequence_id = 1`).Scan(&nextID); err != nil {
		return nil, sqliteResourceStoreError(err)
	}
	if nextID < 0 || nextID == math.MaxInt64 {
		return nil, ErrResourceStoreIDExhausted
	}
	nextID++
	resource := &models.Resource{
		ID: store.resourceID(nextID),
		Spec: models.ResourceSpec{
			Type:              spec.Type,
			Name:              spec.Name,
			ParentID:          spec.ParentID,
			Tags:              cloneStoredTags(spec.Tags),
			Provider:          spec.Provider,
			DesiredState:      spec.DesiredState,
			WorkloadResources: spec.WorkloadResources,
			SecurityContext:   spec.SecurityContext,
		},
		ObservedState: models.ResourceStateUnknown,
	}
	if err := resource.Validate(); err != nil {
		return nil, err
	}
	payload, err := encodeSQLiteResource(*resource)
	if err != nil {
		return nil, err
	}
	if _, err := transaction.ExecContext(ctx,
		`UPDATE ember_platform_resource_sequence SET next_id = ? WHERE sequence_id = 1`, nextID); err != nil {
		return nil, sqliteResourceStoreError(err)
	}
	if _, err := transaction.ExecContext(ctx,
		`INSERT INTO ember_platform_resources (id, parent_id, resource_type, resource_name, payload, observed_state) VALUES (?, ?, ?, ?, ?, ?)`,
		resource.ID, resource.Spec.ParentID, resource.Spec.Type, resource.Spec.Name, payload, resource.ObservedState); err != nil {
		return nil, sqliteResourceStoreError(err)
	}
	if err := transaction.Commit(); err != nil {
		return nil, sqliteResourceStoreError(err)
	}
	return cloneSQLiteResource(resource), nil
}

func nextResourceID(nextID int64) string {
	return fmt.Sprintf("resource-%08d", nextID)
}

func (store *SQLiteResourceStore) resourceID(nextID int64) string {
	if store == nil || store.idPrefix == "" {
		return nextResourceID(nextID)
	}
	return fmt.Sprintf("resource-%s%08d", store.idPrefix, nextID)
}

func cloneSQLiteResource(resource *models.Resource) *models.Resource {
	if resource == nil {
		return nil
	}
	copy := *resource
	copy.Spec.Tags = cloneStoredTags(resource.Spec.Tags)
	return &copy
}

func (store *SQLiteResourceStore) Get(ctx context.Context, scopeID, resourceID string) (*models.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSQLiteResourceScope(scopeID); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.getResourceWithScope(ctx, store.db, scopeID, resourceID)
}

func (store *SQLiteResourceStore) List(ctx context.Context, scopeID string, limit int) ([]models.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSQLiteResourceScope(scopeID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxResourceListLimit {
		return nil, ErrInvalidResourceListLimit
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if scopeID != "" {
		if _, err := store.getResourceByID(ctx, store.db, scopeID); err != nil {
			return nil, err
		}
	}
	rows, err := store.db.QueryContext(ctx,
		`SELECT id, parent_id, payload FROM ember_platform_resources WHERE parent_id = ? ORDER BY id LIMIT ?`, scopeID, limit)
	if err != nil {
		return nil, sqliteResourceStoreError(err)
	}
	defer rows.Close()
	resources := make([]models.Resource, 0, limit)
	for rows.Next() {
		var id, parentID, payload string
		if err := rows.Scan(&id, &parentID, &payload); err != nil {
			return nil, sqliteResourceStoreError(err)
		}
		resource, err := decodeSQLiteResource(id, parentID, payload)
		if err != nil {
			return nil, err
		}
		resources = append(resources, *resource)
	}
	if err := rows.Err(); err != nil {
		return nil, sqliteResourceStoreError(err)
	}
	return resources, nil
}

func (store *SQLiteResourceStore) UpdateTags(ctx context.Context, scopeID, resourceID string, tags map[string]string) (*models.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSQLiteResourceScope(scopeID); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, sqliteResourceStoreError(err)
	}
	defer transaction.Rollback()
	resource, err := store.getResourceWithScope(ctx, transaction, scopeID, resourceID)
	if err != nil {
		return nil, err
	}
	locked, err := store.hasReadOnlyLock(ctx, transaction, resourceID)
	if err != nil {
		return nil, err
	}
	if locked {
		return nil, ErrResourceLocked
	}
	resource.Spec.Tags = cloneStoredTags(tags)
	if err := resource.Spec.Validate(); err != nil {
		return nil, err
	}
	if err := store.updateResourcePayload(ctx, transaction, resource); err != nil {
		return nil, err
	}
	if err := transaction.Commit(); err != nil {
		return nil, sqliteResourceStoreError(err)
	}
	return cloneSQLiteResource(resource), nil
}

func (store *SQLiteResourceStore) UpdateObservedState(ctx context.Context, scopeID, resourceID string, state models.ResourceState) (*models.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSQLiteResourceScope(scopeID); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, sqliteResourceStoreError(err)
	}
	defer transaction.Rollback()
	resource, err := store.getResourceWithScope(ctx, transaction, scopeID, resourceID)
	if err != nil {
		return nil, err
	}
	resource.ObservedState = state
	if err := resource.Validate(); err != nil {
		return nil, err
	}
	if err := store.updateResourcePayload(ctx, transaction, resource); err != nil {
		return nil, err
	}
	if err := transaction.Commit(); err != nil {
		return nil, sqliteResourceStoreError(err)
	}
	return cloneSQLiteResource(resource), nil
}

func (store *SQLiteResourceStore) updateResourcePayload(ctx context.Context, queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, resource *models.Resource) error {
	payload, err := encodeSQLiteResource(*resource)
	if err != nil {
		return err
	}
	result, err := queryer.ExecContext(ctx,
		`UPDATE ember_platform_resources SET resource_type = ?, resource_name = ?, payload = ?, observed_state = ? WHERE id = ? AND parent_id = ?`,
		resource.Spec.Type, resource.Spec.Name, payload, resource.ObservedState, resource.ID, resource.Spec.ParentID)
	if err != nil {
		return sqliteResourceStoreError(err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return sqliteResourceStoreError(err)
	}
	if updated != 1 {
		return ErrResourceNotFound
	}
	return nil
}

func (store *SQLiteResourceStore) Delete(ctx context.Context, scopeID, resourceID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSQLiteResourceScope(scopeID); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return sqliteResourceStoreError(err)
	}
	defer transaction.Rollback()
	resource, err := store.getResourceWithScope(ctx, transaction, scopeID, resourceID)
	if err != nil {
		return err
	}
	locked, err := store.hasReadOnlyLock(ctx, transaction, resourceID)
	if err != nil {
		return err
	}
	if locked {
		return ErrResourceLocked
	}
	var childID string
	err = transaction.QueryRowContext(ctx,
		`SELECT id FROM ember_platform_resources WHERE parent_id = ? LIMIT 1`, resourceID).Scan(&childID)
	if err == nil {
		return ErrResourceHasDependents
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return sqliteResourceStoreError(err)
	}
	result, err := transaction.ExecContext(ctx,
		`DELETE FROM ember_platform_resources WHERE id = ? AND parent_id = ?`, resource.ID, scopeID)
	if err != nil {
		return sqliteResourceStoreError(err)
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		return sqliteResourceStoreError(err)
	}
	if deleted != 1 {
		return ErrResourceNotFound
	}
	if _, err := transaction.ExecContext(ctx,
		`DELETE FROM ember_platform_resource_locks WHERE resource_id = ?`, resource.ID); err != nil {
		return sqliteResourceStoreError(err)
	}
	if err := transaction.Commit(); err != nil {
		return sqliteResourceStoreError(err)
	}
	delete(store.locks, resourceID)
	return nil
}

func (store *SQLiteResourceStore) AcquireLock(ctx context.Context, scopeID, resourceID string, lock models.ResourceLock) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSQLiteResourceScope(scopeID); err != nil {
		return err
	}
	if err := lock.Validate(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, err := store.getResourceWithScope(ctx, store.db, scopeID, resourceID); err != nil {
		return err
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return sqliteResourceStoreError(err)
	}
	defer transaction.Rollback()
	var current models.ResourceLock
	err = transaction.QueryRowContext(ctx,
		`SELECT owner, token FROM ember_platform_resource_locks WHERE resource_id = ?`, resourceID).
		Scan(&current.Owner, &current.Token)
	if err == nil {
		if current == lock {
			return nil
		}
		return ErrResourceLockConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return sqliteResourceStoreError(err)
	}
	if _, err := transaction.ExecContext(ctx,
		`INSERT INTO ember_platform_resource_locks (resource_id, owner, token) VALUES (?, ?, ?)`,
		resourceID, lock.Owner, lock.Token); err != nil {
		return ErrResourceLockConflict
	}
	if err := transaction.Commit(); err != nil {
		return sqliteResourceStoreError(err)
	}
	return nil
}

func (store *SQLiteResourceStore) ReleaseLock(ctx context.Context, scopeID, resourceID string, lock models.ResourceLock) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateSQLiteResourceScope(scopeID); err != nil {
		return err
	}
	if err := lock.Validate(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, err := store.getResourceWithScope(ctx, store.db, scopeID, resourceID); err != nil {
		return err
	}
	var current models.ResourceLock
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return sqliteResourceStoreError(err)
	}
	defer transaction.Rollback()
	err = transaction.QueryRowContext(ctx,
		`SELECT owner, token FROM ember_platform_resource_locks WHERE resource_id = ?`, resourceID).
		Scan(&current.Owner, &current.Token)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrResourceLockNotHeld
	}
	if err != nil {
		return sqliteResourceStoreError(err)
	}
	if current != lock {
		return ErrResourceLockNotOwner
	}
	if _, err := transaction.ExecContext(ctx,
		`DELETE FROM ember_platform_resource_locks WHERE resource_id = ?`, resourceID); err != nil {
		return sqliteResourceStoreError(err)
	}
	if err := transaction.Commit(); err != nil {
		return sqliteResourceStoreError(err)
	}
	return nil
}

func (store *SQLiteResourceStore) InspectLock(ctx context.Context, scopeID, resourceID string) (*models.ResourceLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSQLiteResourceScope(scopeID); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, err := store.getResourceWithScope(ctx, store.db, scopeID, resourceID); err != nil {
		return nil, err
	}
	var lock models.ResourceLock
	err := store.db.QueryRowContext(ctx,
		`SELECT owner, token FROM ember_platform_resource_locks WHERE resource_id = ?`, resourceID).
		Scan(&lock.Owner, &lock.Token)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, sqliteResourceStoreError(err)
	}
	return &lock, nil
}

func (store *SQLiteResourceStore) Reset(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return sqliteResourceStoreError(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `DELETE FROM ember_platform_resources`); err != nil {
		return sqliteResourceStoreError(err)
	}
	if _, err := transaction.ExecContext(ctx, `UPDATE ember_platform_resource_sequence SET next_id = 0 WHERE sequence_id = 1`); err != nil {
		return sqliteResourceStoreError(err)
	}
	if err := transaction.Commit(); err != nil {
		return sqliteResourceStoreError(err)
	}
	store.locks = make(map[string]models.ResourceLock)
	return nil
}

var _ ResourceStore = (*SQLiteResourceStore)(nil)
var _ interface{ Reset(context.Context) error } = (*SQLiteResourceStore)(nil)
