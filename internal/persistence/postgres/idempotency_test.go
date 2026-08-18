package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"ember.local/ember/internal/ember"
)

func TestPostgresCreateGroupAndBucketIdempotency(t *testing.T) {
	databaseURL := os.Getenv("EMBER_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("EMBER_TEST_DATABASE_URL is not set")
	}
	fileStore, err := ember.NewFileStore(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	testContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := Open(testContext, databaseURL, fileStore)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB().ExecContext(testContext, "TRUNCATE repair_findings, blob_objects, locks, idempotency_records, audit_events, operations, resources CASCADE"); err != nil {
		t.Fatal(err)
	}
	ownerPrincipal := ember.Principal{Name: "local-owner", Role: "owner", Scope: "*"}
	group, groupOperation, err := store.CreateGroup(ownerPrincipal, "demo", "i/t/s/demo", "group-1", []byte("demo"), "req", "corr")
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	replayedGroup, replayedGroupOperation, err := store.CreateGroup(ownerPrincipal, "demo", "i/t/s/demo", "group-1", []byte("demo"), "req", "corr")
	if err != nil {
		t.Fatalf("replay group: %v", err)
	}
	if group == nil || groupOperation == nil || replayedGroup == nil || replayedGroupOperation == nil || replayedGroup.ID != group.ID || replayedGroupOperation.ID != groupOperation.ID {
		t.Fatalf("group replay mismatch: first=(%v,%v) replay=(%v,%v)", group, groupOperation, replayedGroup, replayedGroupOperation)
	}
	bucket, bucketOperation, err := store.CreateBucket(ownerPrincipal, group.ID, "assets", group.Scope, "bucket-1", []byte("assets"), "req", "corr")
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	replayedBucket, replayedBucketOperation, err := store.CreateBucket(ownerPrincipal, group.ID, "assets", group.Scope, "bucket-1", []byte("assets"), "req", "corr")
	if err != nil {
		t.Fatalf("replay bucket: %v", err)
	}
	if bucket == nil || bucketOperation == nil || replayedBucket == nil || replayedBucketOperation == nil || replayedBucket.ID != bucket.ID || replayedBucketOperation.ID != bucketOperation.ID {
		t.Fatalf("bucket replay mismatch: first=(%v,%v) replay=(%v,%v)", bucket, bucketOperation, replayedBucket, replayedBucketOperation)
	}
}
