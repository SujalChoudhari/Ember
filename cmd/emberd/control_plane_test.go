package main

import (
	"context"
	"strings"
	"testing"

	"ember.local/ember/internal/ember"
	"ember.local/ember/internal/persistence/postgres"
)

func TestOpenControlPlaneRequiresPostgresByDefault(t *testing.T) {
	fileStore, err := ember.NewFileStore(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = openControlPlane(context.Background(), "", fileStore, false)
	if err == nil || !strings.Contains(err.Error(), "PostgreSQL control plane requires") {
		t.Fatalf("error=%v, want missing database URL", err)
	}
}

func TestOpenControlPlaneUsesMemoryOnlyWhenExplicit(t *testing.T) {
	fileStore, err := ember.NewFileStore(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	controlPlane, closeControlPlane, err := openControlPlane(context.Background(), "", fileStore, true)
	if err != nil {
		t.Fatal(err)
	}
	defer closeControlPlane()
	if _, ok := controlPlane.(*ember.Store); !ok {
		t.Fatalf("control plane type=%T, want *ember.Store", controlPlane)
	}
}

func TestOpenControlPlaneUsesPostgresWhenConfigured(t *testing.T) {
	databaseURL := isolatedControlPlaneDatabaseURL(t)
	fileStore, err := ember.NewFileStore(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	controlPlane, closeControlPlane, err := openControlPlane(context.Background(), databaseURL, fileStore, false)
	if err != nil {
		t.Fatal(err)
	}
	defer closeControlPlane()
	if _, ok := controlPlane.(*postgres.Store); !ok {
		t.Fatalf("control plane type=%T, want *postgres.Store", controlPlane)
	}
}
