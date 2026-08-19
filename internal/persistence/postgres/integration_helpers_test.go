package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"ember.local/ember/internal/ember"
	"ember.local/ember/internal/persistence/postgres"
	_ "github.com/lib/pq"
)

var integrationSchemaSequence uint64

func integrationStore(t *testing.T) (*postgres.Store, *ember.FileStore, string) {
	t.Helper()
	return integrationStoreAtURL(t, os.Getenv("EMBER_TEST_DATABASE_URL"))
}

func integrationStoreAtURL(t *testing.T, callerDatabaseURL string) (*postgres.Store, *ember.FileStore, string) {
	t.Helper()
	if callerDatabaseURL == "" {
		t.Skip("EMBER_TEST_DATABASE_URL is not set")
	}
	isolatedURL := isolatedDatabaseURLAtBase(t, callerDatabaseURL)
	store, fileStore := openStoreAtURL(t, isolatedURL)
	truncateIsolatedStore(t, store)
	return store, fileStore, isolatedURL
}

func openStoreAtURL(t *testing.T, databaseURL string) (*postgres.Store, *ember.FileStore) {
	t.Helper()
	rootPath := t.TempDir()
	fileStore, err := ember.NewFileStore(rootPath, true)
	if err != nil {
		t.Fatal(err)
	}
	contextWithTimeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := postgres.Open(contextWithTimeout, databaseURL, fileStore)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store, fileStore
}

func truncateIsolatedStore(t *testing.T, store *postgres.Store) {
	t.Helper()
	contextWithTimeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := store.DB().ExecContext(contextWithTimeout, "TRUNCATE repair_findings, blob_objects, locks, idempotency_records, audit_events, operations, resources CASCADE"); err != nil {
		t.Fatal(err)
	}
}

func isolatedDatabaseURL(t *testing.T) string {
	t.Helper()
	return isolatedDatabaseURLAtBase(t, os.Getenv("EMBER_TEST_DATABASE_URL"))
}

func isolatedDatabaseURLAtBase(t *testing.T, baseDatabaseURL string) string {
	t.Helper()
	if baseDatabaseURL == "" {
		t.Skip("EMBER_TEST_DATABASE_URL is not set")
	}
	parsedDatabaseURL, err := url.Parse(baseDatabaseURL)
	if err != nil {
		t.Skipf("safe PostgreSQL isolation unavailable: parse test database URL: %v", err)
	}
	databaseName := strings.TrimPrefix(path.Clean(parsedDatabaseURL.Path), "/")
	if databaseName == "." || databaseName == "" {
		if queryDatabaseName := parsedDatabaseURL.Query().Get("dbname"); queryDatabaseName != "" {
			databaseName = queryDatabaseName
		}
	}
	if databaseName == "" {
		t.Skip("safe PostgreSQL isolation unavailable: database name is missing")
	}
	switch strings.ToLower(databaseName) {
	case "ember_phase1", "production", "prod", "staging":
		t.Skipf("safe PostgreSQL isolation unavailable: refusing shared or protected database %q", databaseName)
	}
	databaseConnection, err := sql.Open("postgres", baseDatabaseURL)
	if err != nil {
		t.Skipf("safe PostgreSQL isolation unavailable: open base connection: %v", err)
	}
	schemaName := "ember_test_" + strconv.FormatInt(time.Now().UnixNano(), 10) + "_" + strconv.FormatUint(atomic.AddUint64(&integrationSchemaSequence, 1), 10)
	contextWithTimeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	quotedSchemaName := `"` + schemaName + `"`
	if _, err := databaseConnection.ExecContext(contextWithTimeout, "CREATE SCHEMA "+quotedSchemaName); err != nil {
		_ = databaseConnection.Close()
		t.Skipf("safe PostgreSQL isolation unavailable: create disposable schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		if _, err := databaseConnection.ExecContext(cleanupContext, "DROP SCHEMA "+quotedSchemaName+" CASCADE"); err != nil {
			t.Errorf("drop disposable PostgreSQL schema %s: %v", schemaName, err)
		}
		if err := databaseConnection.Close(); err != nil {
			t.Errorf("close disposable PostgreSQL isolation connection: %v", err)
		}
	})
	queryValues := parsedDatabaseURL.Query()
	queryValues.Set("options", fmt.Sprintf("-csearch_path=%s", schemaName))
	parsedDatabaseURL.RawQuery = queryValues.Encode()
	return parsedDatabaseURL.String()
}
