package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"ember.local/ember/internal/ember"
)

func TestPostgresCreateGroupAndBucketIdempotency(t *testing.T) {
	url := os.Getenv("EMBER_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("EMBER_TEST_DATABASE_URL is not set")
	}
	files, err := ember.NewFileStore(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := Open(ctx, url, files)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := store.DB().ExecContext(ctx, "TRUNCATE repair_findings, blob_objects, locks, idempotency_records, audit_events, operations, resources CASCADE"); err != nil {
		t.Fatal(err)
	}
	p := ember.Principal{Name: "local-owner", Role: "owner", Scope: "*"}
	group, groupOp, err := store.CreateGroup(p, "demo", "i/t/s/demo", "group-1", []byte("demo"), "req", "corr")
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	groupReplay, groupReplayOp, err := store.CreateGroup(p, "demo", "i/t/s/demo", "group-1", []byte("demo"), "req", "corr")
	if err != nil {
		t.Fatalf("replay group: %v", err)
	}
	if group == nil || groupOp == nil || groupReplay == nil || groupReplayOp == nil || groupReplay.ID != group.ID || groupReplayOp.ID != groupOp.ID {
		t.Fatalf("group replay mismatch: first=(%v,%v) replay=(%v,%v)", group, groupOp, groupReplay, groupReplayOp)
	}
	bucket, bucketOp, err := store.CreateBucket(p, group.ID, "assets", group.Scope, "bucket-1", []byte("assets"), "req", "corr")
	if err != nil {
		t.Fatalf("create bucket: %v", err)
	}
	bucketReplay, bucketReplayOp, err := store.CreateBucket(p, group.ID, "assets", group.Scope, "bucket-1", []byte("assets"), "req", "corr")
	if err != nil {
		t.Fatalf("replay bucket: %v", err)
	}
	if bucket == nil || bucketOp == nil || bucketReplay == nil || bucketReplayOp == nil || bucketReplay.ID != bucket.ID || bucketReplayOp.ID != bucketOp.ID {
		t.Fatalf("bucket replay mismatch: first=(%v,%v) replay=(%v,%v)", bucket, bucketOp, bucketReplay, bucketReplayOp)
	}
}
