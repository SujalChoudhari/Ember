package ember

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestFilesystemContainmentAndReset(t *testing.T) {
	rootPath := t.TempDir()
	fileStore, err := NewFileStore(rootPath, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fileStore.Write("bucket", "../escape", []byte("x"), 1, "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db022b521f4e3e7c2"); err != ErrPathUnsafe {
		t.Fatalf("expected unsafe key, got %v", err)
	}
	payloadDigest := sha256.Sum256([]byte("safe"))
	objectVersion, err := fileStore.Write("bucket", "nested/key", []byte("safe"), 4, hex.EncodeToString(payloadDigest[:]))
	if err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(filepath.Dir(rootPath), "ember-escape-marker")
	_ = os.Remove(outsidePath)
	if err := fileStore.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(rootPath, objectVersion.Path)); !os.IsNotExist(err) {
		t.Fatalf("reset left committed bytes: %v", err)
	}
	if _, err := os.Stat(outsidePath); !os.IsNotExist(err) {
		t.Fatalf("write escaped root")
	}
}

func TestCorruptCommittedBytesAreNotReadable(t *testing.T) {
	rootPath := t.TempDir()
	fileStore, _ := NewFileStore(rootPath, false)
	store := NewStore(fileStore)
	editorPrincipal := DefaultTestAuth().byToken["editor-test-token"]
	payloadDigest := sha256.Sum256([]byte("good"))
	objectVersion, _, err := store.PutObject(editorPrincipal, "missing-bucket", "key", []byte("good"), 4, hex.EncodeToString(payloadDigest[:]), "i", "r", "c")
	if err == nil || objectVersion != nil {
		t.Fatal("missing bucket should not mutate")
	}
	ownerPrincipal := DefaultTestAuth().byToken["owner-test-token"]
	groupResource, _, _ := store.CreateGroup(ownerPrincipal, "g", "i/t/s/g", "g", []byte("g"), "r", "c")
	bucketResource, _, _ := store.CreateBucket(ownerPrincipal, groupResource.ID, "b", groupResource.Scope, "b", []byte("b"), "r", "c")
	objectVersion, _, err = store.PutObject(editorPrincipal, bucketResource.ID, "key", []byte("good"), 4, hex.EncodeToString(payloadDigest[:]), "i2", "r", "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rootPath, objectVersion.Path), []byte("bad"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetObject(editorPrincipal, bucketResource.ID, "key", "r", "c"); err != ErrObjectCorrupt {
		t.Fatalf("expected corruption error, got %v", err)
	}
}

func TestProductionFileStoreRejectsSymlinkEscape(t *testing.T) {
	rootPath := t.TempDir()
	outsidePath := t.TempDir()
	fileStore, err := NewFileStore(rootPath, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(rootPath, "objects", "bucket")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outsidePath, filepath.Join(rootPath, "objects", "bucket")); err != nil {
		t.Fatal(err)
	}
	payloadDigest := sha256.Sum256([]byte("escape"))
	if _, err := fileStore.Write("bucket", "key", []byte("escape"), 6, hex.EncodeToString(payloadDigest[:])); err == nil {
		t.Fatal("symlink escape unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(outsidePath, "key")); !os.IsNotExist(err) {
		t.Fatalf("symlink escape wrote outside provider root: %v", err)
	}
}
