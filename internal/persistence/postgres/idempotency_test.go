package postgres_test

import (
	"testing"

	"ember.local/ember/internal/ember"
)

func TestPostgresCreateGroupAndBucketIdempotency(t *testing.T) {
	store, _, _ := integrationStore(t)
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
