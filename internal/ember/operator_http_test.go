package ember

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func TestHTTPResourceLifecycleUsesScopedOperatorContract(t *testing.T) {
	operator := newTestOperator(t)
	handler := NewHTTPHandler(operator)

	create := func(scope string, resourceType models.ResourceType, name, parentID string) *models.Resource {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"type":         resourceType,
			"name":         name,
			"parentId":     parentID,
			"provider":     models.ProviderMetadata{Namespace: "Ember.Storage", Type: "buckets", Version: "v1"},
			"desiredState": models.ResourceStateReady,
		})
		if err != nil {
			t.Fatalf("Marshal(create) error = %v", err)
		}
		request := httptest.NewRequest(http.MethodPost, "/v1/resources", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Ember-Scope", scope)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("POST /v1/resources status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		var response OperatorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode create response error = %v", err)
		}
		if response.Resource == nil {
			t.Fatalf("create response = %#v, want resource", response)
		}
		return response.Resource
	}

	alpha := create("", models.ResourceTypeGroup, "alpha", "")
	beta := create("", models.ResourceTypeGroup, "beta", "")
	child := create(alpha.ID, models.ResourceTypeBucket, "assets", alpha.ID)
	if child.Spec.Provider.Namespace != "Ember.Storage" || child.Spec.DesiredState != models.ResourceStateReady || child.ObservedState != models.ResourceStateUnknown {
		t.Fatalf("created child = %#v, want provider and desired/observed state", child)
	}

	get := httptest.NewRequest(http.MethodGet, "/v1/resources/"+child.ID, nil)
	get.Header.Set("X-Ember-Scope", alpha.ID)
	getRecorder := httptest.NewRecorder()
	handler.ServeHTTP(getRecorder, get)
	if getRecorder.Code != http.StatusOK {
		t.Fatalf("GET same-scope status = %d, body = %s", getRecorder.Code, getRecorder.Body.String())
	}

	crossScope := httptest.NewRequest(http.MethodGet, "/v1/resources/"+child.ID, nil)
	crossScope.Header.Set("X-Ember-Scope", beta.ID)
	crossRecorder := httptest.NewRecorder()
	handler.ServeHTTP(crossRecorder, crossScope)
	if crossRecorder.Code != http.StatusForbidden {
		t.Fatalf("GET cross-scope status = %d, body = %s", crossRecorder.Code, crossRecorder.Body.String())
	}
	var errorResponse map[string]string
	if err := json.Unmarshal(crossRecorder.Body.Bytes(), &errorResponse); err != nil {
		t.Fatalf("decode cross-scope response error = %v", err)
	}
	if errorResponse["error"] != ErrOperatorScopeDenied.Error() {
		t.Fatalf("cross-scope error = %#v, want %q", errorResponse, ErrOperatorScopeDenied)
	}
}

func operatorHTTPCall(t *testing.T, handler http.Handler, method, path, scope string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, bytes.NewReader(body))
	request.Header.Set("X-Ember-Scope", scope)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func TestHTTPOperationBlobAndResetFlow(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	operator, err := NewFileOperator(root, 64)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	handler := NewHTTPHandler(operator)

	create := func(scope string, resourceType models.ResourceType, name, parentID string) *models.Resource {
		t.Helper()
		body, err := json.Marshal(map[string]any{
			"type":         resourceType,
			"name":         name,
			"parentId":     parentID,
			"desiredState": models.ResourceStateReady,
		})
		if err != nil {
			t.Fatalf("Marshal(create) error = %v", err)
		}
		recorder := operatorHTTPCall(t, handler, http.MethodPost, "/v1/resources", scope, body)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("create status = %d, body = %s", recorder.Code, recorder.Body.String())
		}
		var response OperatorResponse
		if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode create response error = %v", err)
		}
		return response.Resource
	}

	group := create("", models.ResourceTypeGroup, "platform", "")
	bucket := create(group.ID, models.ResourceTypeBucket, "assets", group.ID)
	other := create("", models.ResourceTypeGroup, "other", "")

	updateBody, err := json.Marshal(map[string]any{
		"tags":          map[string]string{"tier": "test"},
		"requestId":     "request-http-1",
		"correlationId": "correlation-http-1",
	})
	if err != nil {
		t.Fatalf("Marshal(update) error = %v", err)
	}
	updated := operatorHTTPCall(t, handler, http.MethodPatch, "/v1/resources/"+bucket.ID+"/tags", group.ID, updateBody)
	if updated.Code != http.StatusOK {
		t.Fatalf("PATCH tags status = %d, body = %s", updated.Code, updated.Body.String())
	}
	var updateResponse OperatorResponse
	if err := json.Unmarshal(updated.Body.Bytes(), &updateResponse); err != nil {
		t.Fatalf("decode update response error = %v", err)
	}
	if updateResponse.Resource == nil || updateResponse.Operation == nil || updateResponse.Replayed || updateResponse.Resource.Spec.Tags["tier"] != "test" {
		t.Fatalf("first update response = %#v, want new operation and tag", updateResponse)
	}

	replayedBody, err := json.Marshal(map[string]any{
		"tags":          map[string]string{"tier": "different"},
		"requestId":     "request-http-1",
		"correlationId": "correlation-http-2",
	})
	if err != nil {
		t.Fatalf("Marshal(replay) error = %v", err)
	}
	replayed := operatorHTTPCall(t, handler, http.MethodPatch, "/v1/resources/"+bucket.ID+"/tags", group.ID, replayedBody)
	if replayed.Code != http.StatusOK {
		t.Fatalf("PATCH replay status = %d, body = %s", replayed.Code, replayed.Body.String())
	}
	var replayResponse OperatorResponse
	if err := json.Unmarshal(replayed.Body.Bytes(), &replayResponse); err != nil {
		t.Fatalf("decode replay response error = %v", err)
	}
	if !replayResponse.Replayed || replayResponse.Operation == nil || replayResponse.Operation.ID != updateResponse.Operation.ID || replayResponse.Resource.Spec.Tags["tier"] != "test" {
		t.Fatalf("replay response = %#v, want original operation and state", replayResponse)
	}

	deniedBody, err := json.Marshal(map[string]any{
		"tags":          map[string]string{"tier": "foreign"},
		"requestId":     "request-http-foreign",
		"correlationId": "correlation-http-foreign",
	})
	if err != nil {
		t.Fatalf("Marshal(denied) error = %v", err)
	}
	denied := operatorHTTPCall(t, handler, http.MethodPatch, "/v1/resources/"+bucket.ID+"/tags", other.ID, deniedBody)
	if denied.Code != http.StatusForbidden || strings.Contains(denied.Body.String(), "foreign") {
		t.Fatalf("cross-scope mutation response = %d %s, want redacted 403", denied.Code, denied.Body.String())
	}

	operation := operatorHTTPCall(t, handler, http.MethodGet, "/v1/operations/"+updateResponse.Operation.ID, group.ID, nil)
	if operation.Code != http.StatusOK {
		t.Fatalf("GET operation status = %d, body = %s", operation.Code, operation.Body.String())
	}
	var operationResponse OperatorResponse
	if err := json.Unmarshal(operation.Body.Bytes(), &operationResponse); err != nil {
		t.Fatalf("decode operation response error = %v", err)
	}
	if operationResponse.Operation == nil || operationResponse.Operation.ID != updateResponse.Operation.ID {
		t.Fatalf("operation response = %#v, want linked operation", operationResponse)
	}

	audit := operatorHTTPCall(t, handler, http.MethodGet, "/v1/resources/"+bucket.ID+"/audit?limit=10", group.ID, nil)
	if audit.Code != http.StatusOK {
		t.Fatalf("GET audit status = %d, body = %s", audit.Code, audit.Body.String())
	}
	var auditResponse OperatorResponse
	if err := json.Unmarshal(audit.Body.Bytes(), &auditResponse); err != nil {
		t.Fatalf("decode audit response error = %v", err)
	}
	if len(auditResponse.Audit) != 1 || auditResponse.Audit[0].OperationID != updateResponse.Operation.ID {
		t.Fatalf("audit response = %#v, want one linked entry", auditResponse)
	}
	ownerView := operatorHTTPCall(t, handler, http.MethodGet, "/v1/resources/"+bucket.ID, group.ID, nil)
	if ownerView.Code != http.StatusOK || !strings.Contains(ownerView.Body.String(), `"tier":"test"`) {
		t.Fatalf("owner resource after denial = %d %s, want unchanged tag", ownerView.Code, ownerView.Body.String())
	}

	blob := operatorHTTPCall(t, handler, http.MethodPut, "/v1/buckets/"+bucket.ID+"/objects/nested/file.txt", group.ID, []byte("hello world"))
	if blob.Code != http.StatusCreated {
		t.Fatalf("PUT blob status = %d, body = %s", blob.Code, blob.Body.String())
	}
	var blobResponse OperatorResponse
	if err := json.Unmarshal(blob.Body.Bytes(), &blobResponse); err != nil {
		t.Fatalf("decode blob response error = %v", err)
	}
	if blobResponse.Object == nil || blobResponse.Object.Size != 11 {
		t.Fatalf("blob response = %#v, want bounded metadata", blobResponse)
	}

	rangeResult := operatorHTTPCall(t, handler, http.MethodGet, "/v1/buckets/"+bucket.ID+"/objects/nested/file.txt?start=6&end=11", group.ID, nil)
	if rangeResult.Code != http.StatusOK {
		t.Fatalf("GET blob range status = %d, body = %s", rangeResult.Code, rangeResult.Body.String())
	}
	var rangeResponse OperatorResponse
	if err := json.Unmarshal(rangeResult.Body.Bytes(), &rangeResponse); err != nil {
		t.Fatalf("decode range response error = %v", err)
	}
	if string(rangeResponse.Content) != "world" {
		t.Fatalf("range response content = %q, want world", rangeResponse.Content)
	}

	if reset := operatorHTTPCall(t, handler, http.MethodPost, "/v1/reset", group.ID, nil); reset.Code != http.StatusForbidden {
		t.Fatalf("scoped reset status = %d, body = %s", reset.Code, reset.Body.String())
	}
	if reset := operatorHTTPCall(t, handler, http.MethodPost, "/v1/reset", "", nil); reset.Code != http.StatusNoContent {
		t.Fatalf("root reset status = %d, body = %s", reset.Code, reset.Body.String())
	}
	if missing := operatorHTTPCall(t, handler, http.MethodGet, "/v1/resources/"+group.ID, "", nil); missing.Code != http.StatusNotFound {
		t.Fatalf("GET after reset status = %d, body = %s", missing.Code, missing.Body.String())
	}
}

func TestHTTPRejectsMalformedOversizedAndInvalidRangeRequests(t *testing.T) {
	operator := newTestOperator(t)
	handler := NewHTTPHandler(operator)
	malformed := operatorHTTPCall(t, handler, http.MethodPost, "/v1/resources", "", []byte(`{"type":"group","name":"platform","credential":"fixture-only-value-123"}`))
	if malformed.Code != http.StatusBadRequest {
		t.Fatalf("malformed JSON status = %d, body = %s", malformed.Code, malformed.Body.String())
	}
	if strings.Contains(malformed.Body.String(), "fixture-only-value-123") {
		t.Fatalf("malformed JSON response leaked request value: %s", malformed.Body.String())
	}

	oversizedBody := append([]byte(`{"type":"group","name":"`), bytes.Repeat([]byte("x"), int(maxOperatorJSONBodyBytes))...)
	oversizedBody = append(oversizedBody, []byte(`"}`)...)
	oversized := operatorHTTPCall(t, handler, http.MethodPost, "/v1/resources", "", oversizedBody)
	if oversized.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized JSON status = %d, body = %s", oversized.Code, oversized.Body.String())
	}

	group, err := operator.CreateResource(context.Background(), OperatorPrincipal{}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "platform"})
	if err != nil {
		t.Fatalf("CreateResource(group) error = %v", err)
	}
	bucket, err := operator.CreateResource(context.Background(), OperatorPrincipal{ScopeID: group.ID}, models.ResourceSpec{Type: models.ResourceTypeBucket, Name: "assets", ParentID: group.ID})
	if err != nil {
		t.Fatalf("CreateResource(bucket) error = %v", err)
	}
	if _, err := operator.PutBlob(context.Background(), OperatorPrincipal{ScopeID: group.ID}, bucket.ID, "object", []byte("payload")); err != nil {
		t.Fatalf("PutBlob() error = %v", err)
	}
	invalidRange := operatorHTTPCall(t, handler, http.MethodGet, "/v1/buckets/"+bucket.ID+"/objects/object?start=6&end=2", group.ID, nil)
	if invalidRange.Code != http.StatusBadRequest {
		t.Fatalf("invalid blob range status = %d, body = %s", invalidRange.Code, invalidRange.Body.String())
	}
}
