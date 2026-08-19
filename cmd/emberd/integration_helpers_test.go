package main

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

	_ "github.com/lib/pq"
)

var controlPlaneSchemaSequence uint64

func isolatedControlPlaneDatabaseURL(t *testing.T) string {
	t.Helper()
	baseDatabaseURL := os.Getenv("EMBER_TEST_DATABASE_URL")
	if baseDatabaseURL == "" {
		t.Skip("EMBER_TEST_DATABASE_URL is not set")
	}
	parsedDatabaseURL, err := url.Parse(baseDatabaseURL)
	if err != nil {
		t.Skipf("safe PostgreSQL isolation unavailable: parse test database URL: %v", err)
	}
	databaseName := strings.TrimPrefix(path.Clean(parsedDatabaseURL.Path), "/")
	if databaseName == "." || databaseName == "" {
		databaseName = parsedDatabaseURL.Query().Get("dbname")
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
	schemaName := "ember_control_test_" + strconv.FormatInt(time.Now().UnixNano(), 10) + "_" + strconv.FormatUint(atomic.AddUint64(&controlPlaneSchemaSequence, 1), 10)
	quotedSchemaName := `"` + schemaName + `"`
	contextWithTimeout, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
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
