package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"ember.local/ember/internal/ember"
	"ember.local/ember/internal/persistence/postgres"
)

func TestOpenControlPlaneRequiresPostgresByDefault(t *testing.T) {
	files, err := ember.NewFileStore(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = openControlPlane(context.Background(), "", files, false)
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL control plane requires") {
		t.Fatalf("error=%v, want missing database URL", err)
	}
}

func TestOpenControlPlaneUsesMemoryOnlyWhenExplicit(t *testing.T) {
	files, err := ember.NewFileStore(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	control, closeControl, err := openControlPlane(context.Background(), "", files, true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeControl()
	if _, ok := control.(*ember.Store); !ok {
		t.Fatalf("control plane type=%T, want *ember.Store", control)
	}
}

func TestOpenControlPlaneUsesPostgresWhenConfigured(t *testing.T) {
	url := os.Getenv("EMBER_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("EMBER_TEST_DATABASE_URL is not set")
	}
	files, err := ember.NewFileStore(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	control, closeControl, err := openControlPlane(context.Background(), url, files, false)
	if err != nil {
		t.Fatal(err)
	}
	defer closeControl()
	if _, ok := control.(*postgres.Store); !ok {
		t.Fatalf("control plane type=%T, want *postgres.Store", control)
	}
}
