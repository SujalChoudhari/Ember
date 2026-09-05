package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func blobDigest(data []byte) string {
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func putBlob(t *testing.T, store BlobStore, bucketID, objectKey string, data []byte) *models.BlobObject {
	t.Helper()
	object, err := store.Put(context.Background(), bucketID, objectKey, data)
	if err != nil {
		t.Fatalf("Put(%q) error = %v", objectKey, err)
	}
	return object
}

func TestBlobObjectValidationRejectsUnsafeAndUnboundedMetadata(t *testing.T) {
	digest := strings.Repeat("a", 64)
	valid := models.BlobObject{
		BucketID:  "resource-1",
		Key:       "nested/file.txt",
		VersionID: digest,
		SHA256:    digest,
		ETag:      digest,
		Size:      3,
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid BlobObject.Validate() error = %v", err)
	}

	for _, bucketID := range []string{"", " ", ".", "..", "nested/bucket", "nested\\bucket", string([]byte{'b', 0, 'u'})} {
		if err := models.ValidateBlobBucketID(bucketID); !errors.Is(err, models.ErrInvalidBlobBucketID) {
			t.Errorf("ValidateBlobBucketID(%q) error = %v, want ErrInvalidBlobBucketID", bucketID, err)
		}
	}
	for _, objectKey := range []string{"", ".", "..", "../escape", "nested/../../escape", "/absolute", "nested//file", "nested\\file", string([]byte{'k', 0, 'e'}), strings.Repeat("k", models.MaxBlobObjectKeyBytes+1)} {
		if err := models.ValidateBlobObjectKey(objectKey); !errors.Is(err, models.ErrInvalidBlobObjectKey) {
			t.Errorf("ValidateBlobObjectKey(%q) error = %v, want ErrInvalidBlobObjectKey", objectKey, err)
		}
	}

	invalid := valid
	invalid.SHA256 = "not-a-digest"
	if err := invalid.Validate(); !errors.Is(err, models.ErrInvalidBlobObject) {
		t.Fatalf("invalid digest Validate() error = %v, want ErrInvalidBlobObject", err)
	}
	invalid = valid
	invalid.Size = models.MaxBlobObjectSize + 1
	if err := invalid.Validate(); !errors.Is(err, models.ErrInvalidBlobObject) {
		t.Fatalf("oversized BlobObject.Validate() error = %v, want ErrInvalidBlobObject", err)
	}
}

func TestFileBlobStorePersistsObjectLifecycleWithRangeAndMetadata(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewFileBlobStore(root, 64)
	if err != nil {
		t.Fatalf("NewFileBlobStore() error = %v", err)
	}

	payload := []byte("hello world")
	object := putBlob(t, store, "resource-1", "nested/hello.txt", payload)
	wantDigest := blobDigest(payload)
	wantObject := &models.BlobObject{
		BucketID:  "resource-1",
		Key:       "nested/hello.txt",
		VersionID: wantDigest,
		SHA256:    wantDigest,
		ETag:      wantDigest,
		Size:      int64(len(payload)),
	}
	if !reflect.DeepEqual(object, wantObject) {
		t.Fatalf("Put() object = %#v, want %#v", object, wantObject)
	}
	payload[0] = 'H'

	putBlob(t, store, "resource-1", "a.txt", []byte("a"))
	putBlob(t, store, "resource-1", "z.txt", []byte("z"))
	putBlob(t, store, "resource-2", "other.txt", []byte("other"))

	listed, err := store.List(ctx, "resource-1", 2)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	wantListed := []models.BlobObject{
		{BucketID: "resource-1", Key: "a.txt", VersionID: blobDigest([]byte("a")), SHA256: blobDigest([]byte("a")), ETag: blobDigest([]byte("a")), Size: 1},
		{BucketID: "resource-1", Key: "nested/hello.txt", VersionID: wantDigest, SHA256: wantDigest, ETag: wantDigest, Size: int64(len(payload))},
	}
	if !reflect.DeepEqual(listed, wantListed) {
		t.Fatalf("List() = %#v, want %#v", listed, wantListed)
	}

	reopened, err := NewFileBlobStore(root, 64)
	if err != nil {
		t.Fatalf("NewFileBlobStore(reopen) error = %v", err)
	}
	gotObject, gotPayload, err := reopened.Get(ctx, "resource-1", "nested/hello.txt")
	if err != nil {
		t.Fatalf("Get(reopen) error = %v", err)
	}
	if !reflect.DeepEqual(gotObject, wantObject) || !bytes.Equal(gotPayload, []byte("hello world")) {
		t.Fatalf("Get(reopen) = %#v, %q; want %#v, %q", gotObject, gotPayload, wantObject, "hello world")
	}

	rangePayload, err := reopened.ReadRange(ctx, "resource-1", "nested/hello.txt", 6, 11)
	if err != nil {
		t.Fatalf("ReadRange() error = %v", err)
	}
	if string(rangePayload) != "world" {
		t.Fatalf("ReadRange() = %q, want %q", rangePayload, "world")
	}
	emptyRange, err := reopened.ReadRange(ctx, "resource-1", "nested/hello.txt", 4, 4)
	if err != nil {
		t.Fatalf("ReadRange(empty) error = %v", err)
	}
	if len(emptyRange) != 0 {
		t.Fatalf("ReadRange(empty) = %q, want empty", emptyRange)
	}
	for _, bounds := range [][2]int64{{-1, 1}, {8, 7}, {0, 12}} {
		if _, err := reopened.ReadRange(ctx, "resource-1", "nested/hello.txt", bounds[0], bounds[1]); !errors.Is(err, ErrInvalidBlobRange) {
			t.Errorf("ReadRange(%d,%d) error = %v, want ErrInvalidBlobRange", bounds[0], bounds[1], err)
		}
	}

	if err := reopened.Delete(ctx, "resource-1", "a.txt"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, _, err := reopened.Get(ctx, "resource-1", "a.txt"); !errors.Is(err, ErrBlobObjectNotFound) {
		t.Fatalf("Get(deleted) error = %v, want ErrBlobObjectNotFound", err)
	}
	finalStore, err := NewFileBlobStore(root, 64)
	if err != nil {
		t.Fatalf("NewFileBlobStore(final reopen) error = %v", err)
	}
	if _, _, err := finalStore.Get(ctx, "resource-1", "a.txt"); !errors.Is(err, ErrBlobObjectNotFound) {
		t.Fatalf("Get(deleted after reopen) error = %v, want ErrBlobObjectNotFound", err)
	}
}

func TestFileBlobStoreEnforcesObjectAndQuotaBounds(t *testing.T) {
	ctx := context.Background()
	store, err := NewFileBlobStore(t.TempDir(), 5)
	if err != nil {
		t.Fatalf("NewFileBlobStore() error = %v", err)
	}
	putBlob(t, store, "resource-1", "object", []byte("four"))
	if _, err := store.Put(ctx, "resource-1", "other", []byte("two")); !errors.Is(err, ErrBlobQuotaExceeded) {
		t.Fatalf("Put(quota) error = %v, want ErrBlobQuotaExceeded", err)
	}
	if _, err := store.Put(ctx, "resource-1", "object", []byte("five!")); err != nil {
		t.Fatalf("Put(replace within quota) error = %v", err)
	}
	if _, err := store.Put(ctx, "resource-1", "other", []byte("a")); !errors.Is(err, ErrBlobQuotaExceeded) {
		t.Fatalf("Put(full quota) error = %v, want ErrBlobQuotaExceeded", err)
	}
	if _, err := store.List(ctx, "resource-1", 0); !errors.Is(err, ErrInvalidBlobListLimit) {
		t.Fatalf("List(zero limit) error = %v, want ErrInvalidBlobListLimit", err)
	}
	if _, err := store.List(ctx, "resource-1", MaxBlobListLimit+1); !errors.Is(err, ErrInvalidBlobListLimit) {
		t.Fatalf("List(over limit) error = %v, want ErrInvalidBlobListLimit", err)
	}

	largeStore, err := NewFileBlobStore(t.TempDir(), MaxBlobStoreQuota)
	if err != nil {
		t.Fatalf("NewFileBlobStore(large) error = %v", err)
	}
	largePayload := make([]byte, int(models.MaxBlobObjectSize)+1)
	if _, err := largeStore.Put(ctx, "resource-1", "large", largePayload); !errors.Is(err, ErrBlobObjectTooLarge) {
		t.Fatalf("Put(oversized) error = %v, want ErrBlobObjectTooLarge", err)
	}
	if _, err := NewFileBlobStore(t.TempDir(), 0); !errors.Is(err, ErrInvalidBlobQuota) {
		t.Fatalf("NewFileBlobStore(zero quota) error = %v, want ErrInvalidBlobQuota", err)
	}
	path := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := NewFileBlobStore(path, 1); !errors.Is(err, ErrInvalidBlobStorePath) {
		t.Fatalf("NewFileBlobStore(file path) error = %v, want ErrInvalidBlobStorePath", err)
	}
}

func TestFileBlobStoreDetectsCorruptionAndResetsOwnedResidue(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewFileBlobStore(root, 64)
	if err != nil {
		t.Fatalf("NewFileBlobStore() error = %v", err)
	}
	putBlob(t, store, "resource-1", "object", []byte("payload"))
	keepPath := filepath.Join(root, "keep.txt")
	if err := os.WriteFile(keepPath, []byte("unrelated"), 0o600); err != nil {
		t.Fatalf("WriteFile(keep) error = %v", err)
	}
	objectPath := store.objectPath("resource-1", "object")
	if err := os.WriteFile(objectPath, []byte("payloAd"), 0o600); err != nil {
		t.Fatalf("WriteFile(corruption) error = %v", err)
	}
	if _, _, err := store.Get(ctx, "resource-1", "object"); !errors.Is(err, ErrBlobObjectCorrupt) {
		t.Fatalf("Get(corrupt) error = %v, want ErrBlobObjectCorrupt", err)
	}

	if err := store.Reset(ctx); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if _, err := os.Stat(keepPath); err != nil {
		t.Fatalf("unrelated residue stat error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "metadata.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("metadata after Reset() error = %v, want os.ErrNotExist", err)
	}
	if _, err := os.Stat(filepath.Join(root, "objects")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("objects after Reset() error = %v, want os.ErrNotExist", err)
	}

	reopened, err := NewFileBlobStore(root, 64)
	if err != nil {
		t.Fatalf("NewFileBlobStore(after reset) error = %v", err)
	}
	objects, err := reopened.List(ctx, "resource-1", MaxBlobListLimit)
	if err != nil {
		t.Fatalf("List(after reset) error = %v", err)
	}
	if len(objects) != 0 {
		t.Fatalf("List(after reset) = %#v, want empty", objects)
	}
}

func TestFileBlobStoreAppliesRetentionAcrossRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	start := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	store, err := NewFileBlobStoreWithRetention(root, 64, time.Hour)
	if err != nil {
		t.Fatalf("NewFileBlobStoreWithRetention() error = %v", err)
	}
	store.now = func() time.Time { return start }
	putBlob(t, store, "resource-1", "retained", []byte("payload"))
	if _, _, err := store.Get(ctx, "resource-1", "retained"); err != nil {
		t.Fatalf("Get(before retention deadline) error = %v", err)
	}

	reopened, err := NewFileBlobStoreWithRetention(root, 64, time.Hour)
	if err != nil {
		t.Fatalf("NewFileBlobStoreWithRetention(reopen) error = %v", err)
	}
	reopened.now = func() time.Time { return start.Add(time.Hour) }
	if _, _, err := reopened.Get(ctx, "resource-1", "retained"); !errors.Is(err, ErrBlobObjectNotFound) {
		t.Fatalf("Get(after retention deadline) error = %v, want ErrBlobObjectNotFound", err)
	}
	objects, err := reopened.List(ctx, "resource-1", MaxBlobListLimit)
	if err != nil {
		t.Fatalf("List(after retention deadline) error = %v", err)
	}
	if len(objects) != 0 {
		t.Fatalf("List(after retention deadline) = %#v, want empty", objects)
	}
}

func TestFileBlobStoreRejectsUnsafeObjectInputs(t *testing.T) {
	store, err := NewFileBlobStore(t.TempDir(), 64)
	if err != nil {
		t.Fatalf("NewFileBlobStore() error = %v", err)
	}
	ctx := context.Background()
	for _, objectKey := range []string{"../escape", "nested/../../escape", "/absolute", "nested\\escape"} {
		if _, err := store.Put(ctx, "resource-1", objectKey, []byte("payload")); !errors.Is(err, models.ErrInvalidBlobObjectKey) {
			t.Errorf("Put(%q) error = %v, want ErrInvalidBlobObjectKey", objectKey, err)
		}
	}
	if _, _, err := store.Get(ctx, "../bucket", "object"); !errors.Is(err, models.ErrInvalidBlobBucketID) {
		t.Fatalf("Get(unsafe bucket) error = %v, want ErrInvalidBlobBucketID", err)
	}
}
