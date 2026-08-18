package postgres_test

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"testing"
	"time"

	"ember.local/ember/internal/ember"
	"ember.local/ember/internal/persistence/postgres"
	_ "github.com/lib/pq"
)

func integrationStore(t *testing.T) (*postgres.Store, *ember.FileStore) {
	t.Helper()
	return integrationStoreAtURL(t, os.Getenv("EMBER_TEST_DATABASE_URL"))
}

func integrationStoreAtURL(t *testing.T, databaseURL string) (*postgres.Store, *ember.FileStore) {
	t.Helper()
	if databaseURL == "" {
		t.Skip("EMBER_TEST_DATABASE_URL is not set")
	}
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
	if _, err := store.DB().ExecContext(contextWithTimeout, "TRUNCATE repair_findings, blob_objects, locks, idempotency_records, audit_events, operations, resources CASCADE"); err != nil {
		t.Fatal(err)
	}
	return store, fileStore
}

func isolatedDatabaseURL(t *testing.T) string {
	t.Helper()
	baseDatabaseURL := os.Getenv("EMBER_TEST_DATABASE_URL")
	if baseDatabaseURL == "" {
		t.Skip("EMBER_TEST_DATABASE_URL is not set")
	}
	parsedDatabaseURL, err := url.Parse(baseDatabaseURL)
	if err != nil {
		t.Fatalf("parse test database URL: %v", err)
	}
	databaseConnection, err := sql.Open("postgres", baseDatabaseURL)
	if err != nil {
		t.Fatalf("open test database for isolated schema: %v", err)
	}
	schemaName := "ember_test_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	contextWithTimeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := databaseConnection.ExecContext(contextWithTimeout, "CREATE SCHEMA "+schemaName); err != nil {
		_ = databaseConnection.Close()
		t.Fatalf("create isolated test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = databaseConnection.ExecContext(cleanupContext, "DROP SCHEMA "+schemaName+" CASCADE")
		_ = databaseConnection.Close()
	})
	queryValues := parsedDatabaseURL.Query()
	queryValues.Set("options", fmt.Sprintf("-csearch_path=%s", schemaName))
	parsedDatabaseURL.RawQuery = queryValues.Encode()
	return parsedDatabaseURL.String()
}
