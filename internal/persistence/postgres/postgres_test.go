package postgres_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"ember.local/ember/internal/ember"
	"ember.local/ember/internal/persistence/postgres"
	_ "github.com/lib/pq"
)

func integrationStore(t *testing.T) (*postgres.Store, *ember.FileStore) {
	t.Helper()
	return integrationStoreAtURL(t, os.Getenv("EMBER_TEST_DATABASE_URL"))
}

func integrationStoreAtURL(t *testing.T, databaseURL string) (*postgres.Store, *ember.FileStore) {
	t.Helper()
	if databaseURL == "" {
		t.Skip("EMBER_TEST_DATABASE_URL is not set")
	}
	root := t.TempDir()
	files, err := ember.NewFileStore(root, true)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store, err := postgres.Open(ctx, databaseURL, files)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := store.DB().ExecContext(ctx, "TRUNCATE repair_findings, blob_objects, locks, idempotency_records, audit_events, operations, resources CASCADE"); err != nil {
		t.Fatal(err)
	}
	return store, files
}

func isolatedDatabaseURL(t *testing.T) string {
	t.Helper()
	baseURL := os.Getenv("EMBER_TEST_DATABASE_URL")
	if baseURL == "" {
		t.Skip("EMBER_TEST_DATABASE_URL is not set")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		t.Fatalf("parse test database URL: %v", err)
	}
	rawDB, err := sql.Open("postgres", baseURL)
	if err != nil {
		t.Fatalf("open test database for isolated schema: %v", err)
	}
	schema := "ember_test_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := rawDB.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		_ = rawDB.Close()
		t.Fatalf("create isolated test schema: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		_, _ = rawDB.ExecContext(cleanupCtx, "DROP SCHEMA "+schema+" CASCADE")
		_ = rawDB.Close()
	})
	query := parsed.Query()
	query.Set("options", fmt.Sprintf("-csearch_path=%s", schema))
	parsed.RawQuery = query.Encode()
	return parsed.String()
}

func TestPostgresGoldenFlowPersistsControlState(t *testing.T) {
	store, files := integrationStore(t)
	server := httptest.NewServer(ember.NewServer(store, ember.DefaultTestAuth()).Handler())
	defer server.Close()
	client := server.Client()
	post := func(path, token, idem, body string) (*http.Response, []byte) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Idempotency-Key", idem)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		return resp, data
	}
	groupResp, groupBody := post("/api/v1/instances/i/tenants/t/subscriptions/s/resourceGroups", "owner-test-token", "group-1", `{"name":"demo"}`)
	if groupResp.StatusCode != http.StatusCreated {
		t.Fatalf("group status=%d body=%s", groupResp.StatusCode, groupBody)
	}
	groupID := ""
	for _, token := range []string{"\"id\":\"", "id:"} {
		_ = token
	}
	// The exact resource ID is obtained through the operation response without relying on a test-only cache.
	var group struct {
		Resource ember.Resource `json:"resource"`
	}
	if err := jsonUnmarshal(groupBody, &group); err != nil {
		t.Fatal(err)
	}
	groupID = group.Resource.ID
	bucketResp, bucketBody := post("/api/v1/resourceGroups/"+groupID+"/providers/Ember.Blob/buckets", "owner-test-token", "bucket-1", `{"name":"assets"}`)
	if bucketResp.StatusCode != http.StatusCreated {
		t.Fatalf("bucket status=%d body=%s", bucketResp.StatusCode, bucketBody)
	}
	var bucket struct {
		Resource ember.Resource `json:"resource"`
	}
	if err := jsonUnmarshal(bucketBody, &bucket); err != nil {
		t.Fatal(err)
	}
	payload := []byte("postgres-backed ember")
	digest := sha256.Sum256(payload)
	putReq, _ := http.NewRequest(http.MethodPut, server.URL+"/api/v1/data/buckets/"+bucket.Resource.ID+"/objects/greeting.txt", strings.NewReader(string(payload)))
	putReq.Header.Set("Authorization", "Bearer editor-test-token")
	putReq.Header.Set("Content-Length", "21")
	putReq.Header.Set("Content-Digest", `sha-256="`+hex.EncodeToString(digest[:])+`"`)
	putReq.Header.Set("Idempotency-Key", "object-1")
	putResp, err := client.Do(putReq)
	if err != nil {
		t.Fatal(err)
	}
	if putResp.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(putResp.Body)
		_ = putResp.Body.Close()
		t.Fatalf("put status=%d body=%s", putResp.StatusCode, data)
	}
	_ = putResp.Body.Close()

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	files2, err := ember.NewFileStore(files.Root(), true)
	if err != nil {
		t.Fatal(err)
	}
	store2, err := postgres.Open(ctx, os.Getenv("EMBER_TEST_DATABASE_URL"), files2)
	if err != nil {
		t.Fatal(err)
	}
	defer store2.Close()
	server.Close()
	server2 := httptest.NewServer(ember.NewServer(store2, ember.DefaultTestAuth()).Handler())
	defer server2.Close()
	getReq, _ := http.NewRequest(http.MethodGet, server2.URL+"/api/v1/data/buckets/"+bucket.Resource.ID+"/objects/greeting.txt", nil)
	getReq.Header.Set("Authorization", "Bearer reader-test-token")
	getResp, err := server2.Client().Do(getReq)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(getResp.Body)
	_ = getResp.Body.Close()
	if getResp.StatusCode != http.StatusOK || string(data) != string(payload) {
		t.Fatalf("persistent get status=%d body=%q", getResp.StatusCode, data)
	}
}

func TestPostgresOpenRefusesIncompatibleSchema(t *testing.T) {
	databaseURL := isolatedDatabaseURL(t)
	store, files := integrationStoreAtURL(t, databaseURL)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := store.DB().ExecContext(ctx, "UPDATE schema_meta SET version=99, compatible_min=99, compatible_max=99 WHERE component='phase1'"); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := postgres.Open(ctx, databaseURL, files)
	if err == nil {
		t.Fatal("expected incompatible schema to refuse startup")
	}
}

// Keep JSON parsing local to this test package while avoiding a test helper dependency.
func jsonUnmarshal(data []byte, target any) error {
	return json.Unmarshal(data, target)
}
