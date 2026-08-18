package ember

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testServer(t *testing.T) (*httptest.Server, *Store) {
	t.Helper()
	rootPath := t.TempDir()
	fileStore, err := NewFileStore(rootPath, false)
	if err != nil {
		t.Fatal(err)
	}
	store := NewStore(fileStore)
	testServer := httptest.NewServer(NewServer(store, DefaultTestAuth()).Handler())
	t.Cleanup(testServer.Close)
	return testServer, store
}

func doJSONRequest(t *testing.T, client *http.Client, method, url, token, idempotencyKey string, body any) (*http.Response, map[string]any) {
	t.Helper()
	requestBody, _ := json.Marshal(body)
	request, _ := http.NewRequest(method, url, strings.NewReader(string(requestBody)))
	request.Header.Set("Authorization", "Bearer "+token)
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	responseBytes, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	var responseJSON map[string]any
	if len(responseBytes) > 0 {
		_ = json.Unmarshal(responseBytes, &responseJSON)
	}
	return response, responseJSON
}

func TestGoldenHTTPFlow(t *testing.T) {
	testHTTPServer, store := testServer(t)
	client := testHTTPServer.Client()
	baseURL := testHTTPServer.URL
	groupResponse, groupJSON := doJSONRequest(t, client, http.MethodPost, baseURL+"/api/v1/instances/i/tenants/t/subscriptions/s/resourceGroups", "owner-test-token", "g1", map[string]string{"name": "demo"})
	if groupResponse.StatusCode != http.StatusCreated {
		t.Fatalf("group status=%d body=%v", groupResponse.StatusCode, groupJSON)
	}
	groupID := groupJSON["resource"].(map[string]any)["id"].(string)
	replayedGroupResponse, replayedGroupJSON := doJSONRequest(t, client, http.MethodPost, baseURL+"/api/v1/instances/i/tenants/t/subscriptions/s/resourceGroups", "owner-test-token", "g1", map[string]string{"name": "demo"})
	if replayedGroupResponse.StatusCode != http.StatusCreated || replayedGroupJSON["resource"].(map[string]any)["id"] != groupJSON["resource"].(map[string]any)["id"] {
		t.Fatalf("idempotent replay failed: %d %v", replayedGroupResponse.StatusCode, replayedGroupJSON)
	}
	conflictingGroupResponse, _ := doJSONRequest(t, client, http.MethodPost, baseURL+"/api/v1/instances/i/tenants/s/subscriptions/s/resourceGroups", "owner-test-token", "g1", map[string]string{"name": "different"})
	if conflictingGroupResponse.StatusCode != http.StatusConflict {
		t.Fatalf("expected idempotency conflict, got %d", conflictingGroupResponse.StatusCode)
	}
	bucketResponse, bucketJSON := doJSONRequest(t, client, http.MethodPost, baseURL+"/api/v1/resourceGroups/"+groupID+"/providers/Ember.Blob/buckets", "owner-test-token", "b1", map[string]string{"name": "assets"})
	if bucketResponse.StatusCode != http.StatusCreated {
		t.Fatalf("bucket status=%d body=%v", bucketResponse.StatusCode, bucketJSON)
	}
	bucketID := bucketJSON["resource"].(map[string]any)["id"].(string)
	payload := []byte("hello ember")
	payloadDigest := sha256.Sum256(payload)
	putRequest, _ := http.NewRequest(http.MethodPut, baseURL+"/api/v1/data/buckets/"+bucketID+"/objects/greeting.txt", strings.NewReader(string(payload)))
	putRequest.Header.Set("Authorization", "Bearer editor-test-token")
	putRequest.Header.Set("Content-Length", "11")
	putRequest.Header.Set("Content-Digest", "sha-256=\""+hex.EncodeToString(payloadDigest[:])+"\"")
	putRequest.Header.Set("Idempotency-Key", "o1")
	putResponse, err := client.Do(putRequest)
	if err != nil {
		t.Fatal(err)
	}
	if putResponse.StatusCode != http.StatusCreated {
		t.Fatalf("put status=%d", putResponse.StatusCode)
	}
	etag := putResponse.Header.Get("ETag")
	_ = putResponse.Body.Close()
	getRequest, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/data/buckets/"+bucketID+"/objects/greeting.txt", nil)
	getRequest.Header.Set("Authorization", "Bearer reader-test-token")
	getResponse, err := client.Do(getRequest)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(getResponse.Body)
	_ = getResponse.Body.Close()
	if string(responseBody) != string(payload) || getResponse.Header.Get("ETag") != etag {
		t.Fatalf("round trip mismatch: %q %q", responseBody, etag)
	}
	lockResponse, _ := doJSONRequest(t, client, http.MethodPost, baseURL+"/api/v1/resources/"+bucketID+"/locks", "owner-test-token", "", map[string]string{"kind": "CanNotDelete"})
	if lockResponse.StatusCode != http.StatusCreated {
		t.Fatalf("lock status=%d", lockResponse.StatusCode)
	}
	deleteRequest, _ := http.NewRequest(http.MethodDelete, baseURL+"/api/v1/resources/"+bucketID, nil)
	deleteRequest.Header.Set("Authorization", "Bearer owner-test-token")
	deleteResponse, _ := client.Do(deleteRequest)
	if deleteResponse.StatusCode != http.StatusConflict {
		t.Fatalf("locked delete status=%d", deleteResponse.StatusCode)
	}
	_ = deleteResponse.Body.Close()
	readerPutRequest, _ := http.NewRequest(http.MethodPut, baseURL+"/api/v1/data/buckets/"+bucketID+"/objects/nope", strings.NewReader("x"))
	readerPutRequest.Header.Set("Authorization", "Bearer reader-test-token")
	readerPutRequest.Header.Set("Content-Length", "1")
	readerPutRequest.Header.Set("Content-Digest", "sha-256=\"2d711642b726b04401627ca9fbac32f5c8530fb1903cc4db022b521f4e3e7c2\"")
	readerPutRequest.Header.Set("Idempotency-Key", "r1")
	deniedResponse, _ := client.Do(readerPutRequest)
	if deniedResponse.StatusCode != http.StatusForbidden {
		t.Fatalf("reader mutation status=%d", deniedResponse.StatusCode)
	}
	_ = deniedResponse.Body.Close()
	auditRequest, _ := http.NewRequest(http.MethodGet, baseURL+"/api/v1/audit?scope=i/t/s/demo", nil)
	auditRequest.Header.Set("Authorization", "Bearer reader-test-token")
	auditResponse, _ := client.Do(auditRequest)
	if auditResponse.StatusCode != http.StatusOK {
		t.Fatalf("audit status=%d", auditResponse.StatusCode)
	}
	auditBody, _ := io.ReadAll(auditResponse.Body)
	_ = auditResponse.Body.Close()
	if strings.Contains(string(auditBody), store.files.Root()) || strings.Contains(string(auditBody), "hello ember") {
		t.Fatalf("audit leaked private path or payload: %s", auditBody)
	}
}

func TestPutRequiresContentLength(t *testing.T) {
	testHTTPServer, _ := testServer(t)
	client := testHTTPServer.Client()
	baseURL := testHTTPServer.URL
	groupResponse, groupJSON := doJSONRequest(t, client, http.MethodPost, baseURL+"/api/v1/instances/i/tenants/t/subscriptions/s/resourceGroups", "owner-test-token", "length-group", map[string]string{"name": "length"})
	if groupResponse.StatusCode != http.StatusCreated {
		t.Fatalf("group status=%d", groupResponse.StatusCode)
	}
	groupID := groupJSON["resource"].(map[string]any)["id"].(string)
	bucketResponse, bucketJSON := doJSONRequest(t, client, http.MethodPost, baseURL+"/api/v1/resourceGroups/"+groupID+"/providers/Ember.Blob/buckets", "owner-test-token", "length-bucket", map[string]string{"name": "assets"})
	if bucketResponse.StatusCode != http.StatusCreated {
		t.Fatalf("bucket status=%d", bucketResponse.StatusCode)
	}
	bucketID := bucketJSON["resource"].(map[string]any)["id"].(string)
	putRequest, err := http.NewRequest(http.MethodPut, baseURL+"/api/v1/data/buckets/"+bucketID+"/objects/no-length", strings.NewReader("payload"))
	if err != nil {
		t.Fatal(err)
	}
	putRequest.Header.Set("Authorization", "Bearer editor-test-token")
	putRequest.Header.Set("Content-Digest", "sha-256=\"239f59ed55e737c77147cf55ad0c1b03077a7d6f5f8a4d2c83b3d6f8f2a1c7f1\"")
	putRequest.Header.Set("Idempotency-Key", "length-object")
	putRequest.ContentLength = -1
	response, err := client.Do(putRequest)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing content length status=%d", response.StatusCode)
	}
}
