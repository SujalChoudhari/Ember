package postgres_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"ember.local/ember/internal/ember"
	"ember.local/ember/internal/persistence/postgres"
)

func TestPostgresGoldenFlowPersistsControlState(t *testing.T) {
	store, fileStore := integrationStore(t)
	server := httptest.NewServer(ember.NewServer(store, ember.DefaultTestAuth()).Handler())
	defer server.Close()
	client := server.Client()
	postJSON := func(path, token, idempotencyKey, body string) (*http.Response, []byte) {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, server.URL+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Idempotency-Key", idempotencyKey)
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		responseBody, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		return response, responseBody
	}
	groupResponse, groupBody := postJSON("/api/v1/instances/i/tenants/t/subscriptions/s/resourceGroups", "owner-test-token", "group-1", `{"name":"demo"}`)
	if groupResponse.StatusCode != http.StatusCreated {
		t.Fatalf("group status=%d body=%s", groupResponse.StatusCode, groupBody)
	}
	var groupResponseDocument struct {
		Resource ember.Resource `json:"resource"`
	}
	if err := json.Unmarshal(groupBody, &groupResponseDocument); err != nil {
		t.Fatal(err)
	}
	groupID := groupResponseDocument.Resource.ID
	bucketResponse, bucketBody := postJSON("/api/v1/resourceGroups/"+groupID+"/providers/Ember.Blob/buckets", "owner-test-token", "bucket-1", `{"name":"assets"}`)
	if bucketResponse.StatusCode != http.StatusCreated {
		t.Fatalf("bucket status=%d body=%s", bucketResponse.StatusCode, bucketBody)
	}
	var bucketResponseDocument struct {
		Resource ember.Resource `json:"resource"`
	}
	if err := json.Unmarshal(bucketBody, &bucketResponseDocument); err != nil {
		t.Fatal(err)
	}
	payload := []byte("postgres-backed ember")
	payloadDigest := sha256.Sum256(payload)
	putRequest, _ := http.NewRequest(http.MethodPut, server.URL+"/api/v1/data/buckets/"+bucketResponseDocument.Resource.ID+"/objects/greeting.txt", strings.NewReader(string(payload)))
	putRequest.Header.Set("Authorization", "Bearer editor-test-token")
	putRequest.Header.Set("Content-Length", "21")
	putRequest.Header.Set("Content-Digest", `sha-256="`+hex.EncodeToString(payloadDigest[:])+`"`)
	putRequest.Header.Set("Idempotency-Key", "object-1")
	putResponse, err := client.Do(putRequest)
	if err != nil {
		t.Fatal(err)
	}
	if putResponse.StatusCode != http.StatusCreated {
		responseBody, _ := io.ReadAll(putResponse.Body)
		_ = putResponse.Body.Close()
		t.Fatalf("put status=%d body=%s", putResponse.StatusCode, responseBody)
	}
	_ = putResponse.Body.Close()

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	testContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	fileStoreAfterRestart, err := ember.NewFileStore(fileStore.Root(), true)
	if err != nil {
		t.Fatal(err)
	}
	storeAfterRestart, err := postgres.Open(testContext, os.Getenv("EMBER_TEST_DATABASE_URL"), fileStoreAfterRestart)
	if err != nil {
		t.Fatal(err)
	}
	defer storeAfterRestart.Close()
	server.Close()
	serverAfterRestart := httptest.NewServer(ember.NewServer(storeAfterRestart, ember.DefaultTestAuth()).Handler())
	defer serverAfterRestart.Close()
	getRequest, _ := http.NewRequest(http.MethodGet, serverAfterRestart.URL+"/api/v1/data/buckets/"+bucketResponseDocument.Resource.ID+"/objects/greeting.txt", nil)
	getRequest.Header.Set("Authorization", "Bearer reader-test-token")
	getResponse, err := serverAfterRestart.Client().Do(getRequest)
	if err != nil {
		t.Fatal(err)
	}
	responseBody, _ := io.ReadAll(getResponse.Body)
	_ = getResponse.Body.Close()
	if getResponse.StatusCode != http.StatusOK || string(responseBody) != string(payload) {
		t.Fatalf("persistent get status=%d body=%q", getResponse.StatusCode, responseBody)
	}
}
