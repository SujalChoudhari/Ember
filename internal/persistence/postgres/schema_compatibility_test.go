package postgres_test

import (
	"context"
	"testing"
	"time"

	"ember.local/ember/internal/persistence/postgres"
)

func TestPostgresOpenRefusesIncompatibleSchema(t *testing.T) {
	databaseURL := isolatedDatabaseURL(t)
	store, fileStore := openStoreAtURL(t, databaseURL)
	testContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := store.DB().ExecContext(testContext, "UPDATE schema_meta SET version=99, compatible_min=99, compatible_max=99 WHERE component='phase1'"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := postgres.Open(testContext, databaseURL, fileStore)
	if err == nil {
		t.Fatal("expected incompatible schema to refuse startup")
	}
}
