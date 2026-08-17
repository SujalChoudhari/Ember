package ember

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testServer(t *testing.T) (*httptest.Server, *Store) {
	t.Helper()
	root := t.TempDir()
	files, err := NewFileStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(files)
	ts := httptest.NewServer(NewServer(store, DefaultTestAuth()).Handler())
	t.Cleanup(ts.Close)
	return ts, store
}
func doJSON(t *testing.T, client *http.Client, method, url, token, idem string, body any) (*http.Response, map[string]any) {
	t.Helper()
	b, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, url, strings.NewReader(string(b)))
	req.Header.Set("Authorization", "Bearer "+token)
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	var out map[string]any
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out)
	}
	return resp, out
}

func TestGoldenHTTPFlow(t *testing.T) {
	ts, store := testServer(t)
	c := ts.Client()
	base := ts.URL
	resp, group := doJSON(t, c, http.MethodPost, base+"/api/v1/instances/i/tenants/t/subscriptions/s/resourceGroups", "owner-test-token", "g1", map[string]string{"name": "demo"})
	if resp.StatusCode != 201 {
		t.Fatalf("group status=%d body=%v", resp.StatusCode, group)
	}
	groupID := group["resource"].(map[string]any)["id"].(string)
	resp2, group2 := doJSON(t, c, http.MethodPost, base+"/api/v1/instances/i/tenants/t/subscriptions/s/resourceGroups", "owner-test-token", "g1", map[string]string{"name": "demo"})
	if resp2.StatusCode != 201 || group2["resource"].(map[string]any)["id"] != group["resource"].(map[string]any)["id"] {
		t.Fatalf("idempotent replay failed: %d %v", resp2.StatusCode, group2)
	}
	resp3, _ := doJSON(t, c, http.MethodPost, base+"/api/v1/instances/i/tenants/s/subscriptions/s/resourceGroups", "owner-test-token", "g1", map[string]string{"name": "different"})
	if resp3.StatusCode != 409 {
		t.Fatalf("expected idempotency conflict, got %d", resp3.StatusCode)
	}
	resp, bucket := doJSON(t, c, http.MethodPost, base+"/api/v1/resourceGroups/"+groupID+"/providers/Ember.Blob/buckets", "owner-test-token", "b1", map[string]string{"name": "assets"})
	if resp.StatusCode != 201 {
		t.Fatalf("bucket status=%d body=%v", resp.StatusCode, bucket)
	}
	bucketID := bucket["resource"].(map[string]any)["id"].(string)
	payload := []byte("hello ember")
	h := sha256.Sum256(payload)
	req, _ := http.NewRequest(http.MethodPut, base+"/api/v1/data/buckets/"+bucketID+"/objects/greeting.txt", strings.NewReader(string(payload)))
	req.Header.Set("Authorization", "Bearer editor-test-token")
	req.Header.Set("Content-Length", "11")
	req.Header.Set("Content-Digest", "sha-256=\""+hex.EncodeToString(h[:])+"\"")
	req.Header.Set("Idempotency-Key", "o1")
	put, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if put.StatusCode != 201 {
		t.Fatalf("put status=%d", put.StatusCode)
	}
	etag := put.Header.Get("ETag")
	_ = put.Body.Close()
	getReq, _ := http.NewRequest(http.MethodGet, base+"/api/v1/data/buckets/"+bucketID+"/objects/greeting.txt", nil)
	getReq.Header.Set("Authorization", "Bearer reader-test-token")
	get, err := c.Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(get.Body)
	_ = get.Body.Close()
	if string(got) != string(payload) || get.Header.Get("ETag") != etag {
		t.Fatalf("round trip mismatch: %q %q", got, etag)
	}
	delLock, _ := doJSON(t, c, http.MethodPost, base+"/api/v1/resources/"+bucketID+"/locks", "owner-test-token", "", map[string]string{"kind": "CanNotDelete"})
	if delLock.StatusCode != 201 {
		t.Fatalf("lock status=%d", delLock.StatusCode)
	}
	delReq, _ := http.NewRequest(http.MethodDelete, base+"/api/v1/resources/"+bucketID, nil)
	delReq.Header.Set("Authorization", "Bearer owner-test-token")
	del, _ := c.Do(delReq)
	if del.StatusCode != 409 {
		t.Fatalf("locked delete status=%d", del.StatusCode)
	}
	_ = del.Body.Close()
	readerReq, _ := http.NewRequest(http.MethodPut, base+"/api/v1/data/buckets/"+bucketID+"/objects/nope", strings.NewReader("x"))
	readerReq.Header.Set("Authorization", "Bearer reader-test-token")
	readerReq.Header.Set("Content-Length", "1")
	readerReq.Header.Set("Content-Digest", "sha-256=\"2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db022b521f4e3e7c2\"")
	readerReq.Header.Set("Idempotency-Key", "r1")
	denied, _ := c.Do(readerReq)
	if denied.StatusCode != 403 {
		t.Fatalf("reader mutation status=%d", denied.StatusCode)
	}
	_ = denied.Body.Close()
	auditReq, _ := http.NewRequest(http.MethodGet, base+"/api/v1/audit?scope=i/t/s/demo", nil)
	auditReq.Header.Set("Authorization", "Bearer reader-test-token")
	audit, _ := c.Do(auditReq)
	if audit.StatusCode != 200 {
		t.Fatalf("audit status=%d", audit.StatusCode)
	}
	raw, _ := io.ReadAll(audit.Body)
	_ = audit.Body.Close()
	if strings.Contains(string(raw), store.files.Root()) || strings.Contains(string(raw), "hello ember") {
		t.Fatalf("audit leaked private path or payload: %s", raw)
	}
}

func TestFilesystemContainmentAndReset(t *testing.T) {
	root := t.TempDir()
	files, err := NewFileStore(root, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := files.Write("bucket", "../escape", []byte("x"), 1, "2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db022b521f4e3e7c2"); err != ErrPathUnsafe {
		t.Fatalf("expected unsafe key, got %v", err)
	}
	h := sha256.Sum256([]byte("safe"))
	obj, err := files.Write("bucket", "nested/key", []byte("safe"), 4, hex.EncodeToString(h[:]))
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(filepath.Dir(root), "ember-escape-marker")
	_ = os.Remove(outside)
	if err := files.Reset(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, obj.Path)); !os.IsNotExist(err) {
		t.Fatalf("reset left committed bytes: %v", err)
	}
	if _, err := os.Stat(outside); !os.IsNotExist(err) {
		t.Fatalf("write escaped root")
	}
}

func TestCorruptCommittedBytesAreNotReadable(t *testing.T) {
	root := t.TempDir()
	files, _ := NewFileStore(root, false)
	store := NewStore(files)
	p := DefaultTestAuth().byToken["editor-test-token"]
	h := sha256.Sum256([]byte("good"))
	obj, _, err := store.PutObject(p, "missing-bucket", "key", []byte("good"), 4, hex.EncodeToString(h[:]), "i", "r", "c")
	if err == nil || obj != nil {
		t.Fatal("missing bucket should not mutate")
	}
	g, _, _ := store.CreateGroup(DefaultTestAuth().byToken["owner-test-token"], "g", "i/t/s/g", "g", []byte("g"), "r", "c")
	b, _, _ := store.CreateBucket(DefaultTestAuth().byToken["owner-test-token"], g.ID, "b", g.Scope, "b", []byte("b"), "r", "c")
	obj, _, err = store.PutObject(p, b.ID, "key", []byte("good"), 4, hex.EncodeToString(h[:]), "i2", "r", "c")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, obj.Path), []byte("bad"), 0o640); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.GetObject(p, b.ID, "key", "r", "c"); err != ErrObjectCorrupt {
		t.Fatalf("expected corruption error, got %v", err)
	}
}

func TestPutRequiresContentLength(t *testing.T) {
	ts, _ := testServer(t)
	defer ts.Close()
	client := ts.Client()
	base := ts.URL
	resp, group := doJSON(t, client, http.MethodPost, base+"/api/v1/instances/i/tenants/t/subscriptions/s/resourceGroups", "owner-test-token", "length-group", map[string]string{"name": "length"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("group status=%d", resp.StatusCode)
	}
	gid := group["resource"].(map[string]any)["id"].(string)
	resp, bucket := doJSON(t, client, http.MethodPost, base+"/api/v1/resourceGroups/"+gid+"/providers/Ember.Blob/buckets", "owner-test-token", "length-bucket", map[string]string{"name": "assets"})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("bucket status=%d", resp.StatusCode)
	}
	bid := bucket["resource"].(map[string]any)["id"].(string)
	req, err := http.NewRequest(http.MethodPut, base+"/api/v1/data/buckets/"+bid+"/objects/no-length", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer editor-test-token")
	req.Header.Set("Content-Digest", "sha-256=\"239f59ed55e737c77147cf55ad0c1b03077a7d6f5f8a4d2c83b3d6f8f2a1c7f1\"")
	req.Header.Set("Idempotency-Key", "length-object")
	req.ContentLength = -1
	got, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer got.Body.Close()
	if got.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing content length status=%d", got.StatusCode)
	}
}

func TestProductionFileStoreRejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	files, err := NewFileStore(root, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(root, "objects", "bucket")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "objects", "bucket")); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256([]byte("escape"))
	if _, err := files.Write("bucket", "key", []byte("escape"), 6, hex.EncodeToString(h[:])); err == nil {
		t.Fatal("symlink escape unexpectedly succeeded")
	}
	if _, err := os.Stat(filepath.Join(outside, "key")); !os.IsNotExist(err) {
		t.Fatalf("symlink escape wrote outside provider root: %v", err)
	}
}
