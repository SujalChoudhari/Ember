package ember

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func TestHTTPWorkloadAcceptanceWalkthroughExposesHealthLogsRestartAndCorrelation(t *testing.T) {
	operator, err := NewFileOperator(filepath.Join(t.TempDir(), "state"), 64)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	handler := NewHTTPHandler(operator)
	root, err := operator.CreateResource(context.Background(), OperatorPrincipal{}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "compute"})
	if err != nil {
		t.Fatalf("CreateResource(root) error = %v", err)
	}

	createBody, err := json.Marshal(map[string]any{
		"name":         "api",
		"provider":     models.ProviderMetadata{Namespace: "Ember.Compute", Type: "workloads", Version: "v1"},
		"desiredState": models.ResourceStateReady,
	})
	if err != nil {
		t.Fatalf("Marshal(workload create) error = %v", err)
	}
	created := operatorHTTPCall(t, handler, http.MethodPost, "/v1/workloads", root.ID, createBody)
	if created.Code != http.StatusCreated {
		t.Fatalf("POST /v1/workloads status = %d, body = %s", created.Code, created.Body.String())
	}
	var createResponse struct {
		Workload *WorkloadView `json:"workload"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createResponse); err != nil {
		t.Fatalf("decode workload create response error = %v", err)
	}
	if createResponse.Workload == nil || createResponse.Workload.Resource.ID == "" || createResponse.Workload.Status.ExecutionID == "" {
		t.Fatalf("workload create response = %#v, want resource and execution correlation", createResponse)
	}

	restarted := operatorHTTPCall(t, handler, http.MethodPost, "/v1/workloads/"+createResponse.Workload.Resource.ID+"/restart", root.ID, nil)
	if restarted.Code != http.StatusOK {
		t.Fatalf("POST workload restart status = %d, body = %s", restarted.Code, restarted.Body.String())
	}
	var restartResponse struct {
		Workload *WorkloadView `json:"workload"`
	}
	if err := json.Unmarshal(restarted.Body.Bytes(), &restartResponse); err != nil {
		t.Fatalf("decode workload restart response error = %v", err)
	}
	if restartResponse.Workload == nil || restartResponse.Workload.Status.ExecutionID == createResponse.Workload.Status.ExecutionID {
		t.Fatalf("workload restart response = %#v, want a new execution correlation", restartResponse)
	}

	inspected := operatorHTTPCall(t, handler, http.MethodGet, "/v1/workloads/"+createResponse.Workload.Resource.ID+"/observability?limit=10", root.ID, nil)
	if inspected.Code != http.StatusOK {
		t.Fatalf("GET workload observability status = %d, body = %s", inspected.Code, inspected.Body.String())
	}
	var inspectResponse struct {
		Observability *RuntimeObservabilityReport `json:"observability"`
	}
	if err := json.Unmarshal(inspected.Body.Bytes(), &inspectResponse); err != nil {
		t.Fatalf("decode workload observability response error = %v", err)
	}
	if inspectResponse.Observability == nil || inspectResponse.Observability.Status.Health != models.WorkloadHealthHealthy || inspectResponse.Observability.Status.Readiness != models.WorkloadReadinessReady || len(inspectResponse.Observability.Logs) != 1 || inspectResponse.Observability.Logs[0].ExecutionID != restartResponse.Workload.Status.ExecutionID {
		t.Fatalf("workload observability response = %#v, want healthy ready bounded correlated logs", inspectResponse)
	}
	withoutConfirmation := operatorHTTPCall(t, handler, http.MethodDelete, "/v1/workloads/"+createResponse.Workload.Resource.ID, root.ID, nil)
	if withoutConfirmation.Code != http.StatusConflict {
		t.Fatalf("DELETE workload without confirmation status = %d, body = %s", withoutConfirmation.Code, withoutConfirmation.Body.String())
	}
	confirmed := operatorHTTPCall(t, handler, http.MethodDelete, "/v1/workloads/"+createResponse.Workload.Resource.ID+"?confirm=true", root.ID, nil)
	if confirmed.Code != http.StatusNoContent {
		t.Fatalf("DELETE workload with confirmation status = %d, body = %s", confirmed.Code, confirmed.Body.String())
	}
}

func TestHTTPExposesScopedNetworkLifecycle(t *testing.T) {
	operator, err := NewFileOperator(filepath.Join(t.TempDir(), "state"), 64)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	handler := NewHTTPHandler(operator)
	root, err := operator.CreateResource(context.Background(), OperatorPrincipal{}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "compute"})
	if err != nil {
		t.Fatalf("CreateResource(root) error = %v", err)
	}
	workload, err := operator.CreateWorkload(context.Background(), OperatorPrincipal{ScopeID: root.ID}, models.ResourceSpec{
		Type:         models.ResourceTypeWorkload,
		Name:         "api",
		ParentID:     root.ID,
		Provider:     models.ProviderMetadata{Namespace: "Ember.Compute", Type: "workloads", Version: "v1"},
		DesiredState: models.ResourceStateReady,
	})
	if err != nil {
		t.Fatalf("CreateWorkload() error = %v", err)
	}
	scopeID := root.ID

	createNetwork := operatorHTTPCall(t, handler, http.MethodPost, "/v1/networks", scopeID, []byte(`{"name":"frontend"}`))
	if createNetwork.Code != http.StatusCreated {
		t.Fatalf("POST /v1/networks status = %d, body = %s", createNetwork.Code, createNetwork.Body.String())
	}
	var networkResponse struct {
		Network *models.Network `json:"network"`
	}
	if err := json.Unmarshal(createNetwork.Body.Bytes(), &networkResponse); err != nil {
		t.Fatalf("decode network response error = %v", err)
	}
	if networkResponse.Network == nil {
		t.Fatalf("network response = %#v, want network", networkResponse)
	}

	networkID := networkResponse.Network.ID
	listedNetworks := operatorHTTPCall(t, handler, http.MethodGet, "/v1/networks?limit=10", scopeID, nil)
	if listedNetworks.Code != http.StatusOK || !strings.Contains(listedNetworks.Body.String(), networkID) {
		t.Fatalf("GET /v1/networks response = %d %s, want scoped network", listedNetworks.Code, listedNetworks.Body.String())
	}

	port := operatorHTTPCall(t, handler, http.MethodPost, "/v1/networks/"+networkID+"/ports", scopeID, []byte(`{"workloadId":"`+workload.Resource.ID+`","number":8080,"protocol":"tcp"}`))
	if port.Code != http.StatusCreated {
		t.Fatalf("POST network port status = %d, body = %s", port.Code, port.Body.String())
	}
	var portResponse struct {
		Port *models.NetworkPort `json:"port"`
	}
	if err := json.Unmarshal(port.Body.Bytes(), &portResponse); err != nil {
		t.Fatalf("decode port response error = %v", err)
	}
	if portResponse.Port == nil {
		t.Fatalf("port response = %#v, want port", portResponse)
	}
	if listedPorts := operatorHTTPCall(t, handler, http.MethodGet, "/v1/networks/"+networkID+"/ports?limit=10", scopeID, nil); listedPorts.Code != http.StatusOK || !strings.Contains(listedPorts.Body.String(), portResponse.Port.ID) {
		t.Fatalf("GET network ports response = %d %s, want scoped port", listedPorts.Code, listedPorts.Body.String())
	}
	if inspectedNetwork := operatorHTTPCall(t, handler, http.MethodGet, "/v1/networks/"+networkID, scopeID, nil); inspectedNetwork.Code != http.StatusOK || !strings.Contains(inspectedNetwork.Body.String(), networkID) {
		t.Fatalf("GET network response = %d %s, want network", inspectedNetwork.Code, inspectedNetwork.Body.String())
	}
	if inspectedPort := operatorHTTPCall(t, handler, http.MethodGet, "/v1/network-ports/"+portResponse.Port.ID, scopeID, nil); inspectedPort.Code != http.StatusOK || !strings.Contains(inspectedPort.Body.String(), portResponse.Port.ID) {
		t.Fatalf("GET network port response = %d %s, want port", inspectedPort.Code, inspectedPort.Body.String())
	}

	endpoint := operatorHTTPCall(t, handler, http.MethodPost, "/v1/networks/"+networkID+"/endpoints", scopeID, []byte(`{"portId":"`+portResponse.Port.ID+`","name":"api"}`))
	if endpoint.Code != http.StatusCreated {
		t.Fatalf("POST network endpoint status = %d, body = %s", endpoint.Code, endpoint.Body.String())
	}
	var endpointResponse struct {
		Endpoint *models.NetworkEndpoint `json:"endpoint"`
	}
	if err := json.Unmarshal(endpoint.Body.Bytes(), &endpointResponse); err != nil {
		t.Fatalf("decode endpoint response error = %v", err)
	}
	if endpointResponse.Endpoint == nil {
		t.Fatalf("endpoint response = %#v, want endpoint", endpointResponse)
	}
	if listedEndpoints := operatorHTTPCall(t, handler, http.MethodGet, "/v1/networks/"+networkID+"/endpoints?limit=10", scopeID, nil); listedEndpoints.Code != http.StatusOK || !strings.Contains(listedEndpoints.Body.String(), endpointResponse.Endpoint.ID) {
		t.Fatalf("GET network endpoints response = %d %s, want scoped endpoint", listedEndpoints.Code, listedEndpoints.Body.String())
	}
	if inspectedEndpoint := operatorHTTPCall(t, handler, http.MethodGet, "/v1/network-endpoints/"+endpointResponse.Endpoint.ID, scopeID, nil); inspectedEndpoint.Code != http.StatusOK || !strings.Contains(inspectedEndpoint.Body.String(), endpointResponse.Endpoint.ID) {
		t.Fatalf("GET network endpoint response = %d %s, want endpoint", inspectedEndpoint.Code, inspectedEndpoint.Body.String())
	}

	foreign := operatorHTTPCall(t, handler, http.MethodGet, "/v1/network-endpoints/"+endpointResponse.Endpoint.ID, "foreign-scope", nil)
	if foreign.Code != http.StatusNotFound || strings.Contains(foreign.Body.String(), endpointResponse.Endpoint.Address) {
		t.Fatalf("cross-scope endpoint response = %d %s, want redacted 404", foreign.Code, foreign.Body.String())
	}

	if deleted := operatorHTTPCall(t, handler, http.MethodDelete, "/v1/network-endpoints/"+endpointResponse.Endpoint.ID+"?confirm=true", scopeID, nil); deleted.Code != http.StatusNoContent {
		t.Fatalf("DELETE network endpoint status = %d, body = %s", deleted.Code, deleted.Body.String())
	}
	if deleted := operatorHTTPCall(t, handler, http.MethodDelete, "/v1/network-endpoints/"+endpointResponse.Endpoint.ID+"?confirm=true", scopeID, nil); deleted.Code != http.StatusNoContent {
		t.Fatalf("repeat DELETE network endpoint status = %d, body = %s", deleted.Code, deleted.Body.String())
	}
	if deleted := operatorHTTPCall(t, handler, http.MethodDelete, "/v1/network-ports/"+portResponse.Port.ID+"?confirm=true", scopeID, nil); deleted.Code != http.StatusNoContent {
		t.Fatalf("DELETE network port status = %d, body = %s", deleted.Code, deleted.Body.String())
	}
	if deleted := operatorHTTPCall(t, handler, http.MethodDelete, "/v1/networks/"+networkID+"?confirm=true", scopeID, nil); deleted.Code != http.StatusNoContent {
		t.Fatalf("DELETE network status = %d, body = %s", deleted.Code, deleted.Body.String())
	}
}

func TestHTTPWorkloadSafetyFailuresUseStableStatuses(t *testing.T) {
	operator, err := NewFileOperator(filepath.Join(t.TempDir(), "state"), 64)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	handler := NewHTTPHandler(operator)
	root, err := operator.CreateResource(context.Background(), OperatorPrincipal{}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "compute"})
	if err != nil {
		t.Fatalf("CreateResource(root) error = %v", err)
	}

	create := func(body map[string]any) *httptest.ResponseRecorder {
		t.Helper()
		payload, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("Marshal(workload) error = %v", err)
		}
		return operatorHTTPCall(t, handler, http.MethodPost, "/v1/workloads", root.ID, payload)
	}

	overLimit := create(map[string]any{
		"name":              "over-limit",
		"provider":          models.ProviderMetadata{Namespace: "Ember.Compute", Type: "workloads", Version: "v1"},
		"desiredState":      models.ResourceStateReady,
		"workloadResources": models.WorkloadResources{CPUMillis: models.MaxWorkloadCPUMillis + 1},
	})
	if overLimit.Code != http.StatusBadRequest || overLimit.Body.String() != "{\"error\":\"invalid workload spec\"}\n" {
		t.Fatalf("over-limit response = %d %q, want stable bad-request validation error", overLimit.Code, overLimit.Body.String())
	}

	privileged := create(map[string]any{
		"name":            "privileged",
		"provider":        models.ProviderMetadata{Namespace: "Ember.Compute", Type: "workloads", Version: "v1"},
		"desiredState":    models.ResourceStateReady,
		"securityContext": models.WorkloadSecurityContext{Privileged: true},
	})
	if privileged.Code != http.StatusForbidden || privileged.Body.String() != "{\"error\":\"workload privilege denied\"}\n" {
		t.Fatalf("privileged response = %d %q, want stable forbidden error", privileged.Code, privileged.Body.String())
	}
	if got := operatorErrorStatus(ErrWorkloadResourceLimit); got != http.StatusBadRequest {
		t.Fatalf("operatorErrorStatus(resource limit) = %d, want %d", got, http.StatusBadRequest)
	}
}

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

	if reset := operatorHTTPCall(t, handler, http.MethodPost, "/v1/reset?confirm=true", group.ID, nil); reset.Code != http.StatusForbidden {
		t.Fatalf("scoped reset status = %d, body = %s", reset.Code, reset.Body.String())
	}
	if reset := operatorHTTPCall(t, handler, http.MethodPost, "/v1/reset?confirm=true", "", nil); reset.Code != http.StatusNoContent {
		t.Fatalf("root reset status = %d, body = %s", reset.Code, reset.Body.String())
	}
	if missing := operatorHTTPCall(t, handler, http.MethodGet, "/v1/resources/"+group.ID, "", nil); missing.Code != http.StatusNotFound {
		t.Fatalf("GET after reset status = %d, body = %s", missing.Code, missing.Body.String())
	}
}

func TestHTTPOperationInspectionListsCorrelatedScopedHistory(t *testing.T) {
	operator := newTestOperator(t)
	handler := NewHTTPHandler(operator)
	ctx := context.Background()
	root, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "root"})
	if err != nil {
		t.Fatalf("CreateResource(root) error = %v", err)
	}
	child, err := operator.CreateResource(ctx, OperatorPrincipal{ScopeID: root.ID}, models.ResourceSpec{Type: models.ResourceTypeBucket, Name: "child", ParentID: root.ID})
	if err != nil {
		t.Fatalf("CreateResource(child) error = %v", err)
	}
	updated, err := operator.UpdateResourceTags(ctx, OperatorPrincipal{ScopeID: root.ID}, child.ID, map[string]string{"tier": "scoped"}, "request-http-scoped", "correlation-http-scoped")
	if err != nil {
		t.Fatalf("UpdateResourceTags() error = %v", err)
	}

	listed := operatorHTTPCall(t, handler, http.MethodGet, "/v1/operations?resourceId="+child.ID+"&limit=10", root.ID, nil)
	if listed.Code != http.StatusOK {
		t.Fatalf("GET operation list status = %d, body = %s", listed.Code, listed.Body.String())
	}
	var response OperatorResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode operation list response error = %v", err)
	}
	if len(response.Operations) != 1 || response.Operations[0].ID != updated.Operation.ID || response.Operations[0].CorrelationID != "correlation-http-scoped" || response.Operations[0].Status != models.OperationStatusSucceeded {
		t.Fatalf("operation list response = %#v, want one correlated scoped operation", response)
	}
	if invalid := operatorHTTPCall(t, handler, http.MethodGet, "/v1/operations?resourceId="+child.ID+"&limit=0", root.ID, nil); invalid.Code != http.StatusBadRequest {
		t.Fatalf("GET invalid operation list status = %d, body = %s", invalid.Code, invalid.Body.String())
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

func TestHTTPExposesCompleteResourceLifecycleAndLockSurface(t *testing.T) {
	operator := newTestOperator(t)
	handler := NewHTTPHandler(operator)

	create := func(scope string, resourceType models.ResourceType, name, parentID string) *models.Resource {
		t.Helper()
		body, err := json.Marshal(map[string]any{"type": resourceType, "name": name, "parentId": parentID})
		if err != nil {
			t.Fatalf("Marshal(create) error = %v", err)
		}
		response := operatorHTTPCall(t, handler, http.MethodPost, "/v1/resources", scope, body)
		if response.Code != http.StatusCreated {
			t.Fatalf("create status = %d, body = %s", response.Code, response.Body.String())
		}
		var envelope OperatorResponse
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode create response error = %v", err)
		}
		return envelope.Resource
	}

	root := create("", models.ResourceTypeGroup, "root", "")
	other := create("", models.ResourceTypeGroup, "other", "")
	child := create(root.ID, models.ResourceTypeBucket, "assets", root.ID)

	list := operatorHTTPCall(t, handler, http.MethodGet, "/v1/resources?limit=10", "", nil)
	if list.Code != http.StatusOK {
		t.Fatalf("GET root resources status = %d, body = %s", list.Code, list.Body.String())
	}
	var listResponse OperatorResponse
	if err := json.Unmarshal(list.Body.Bytes(), &listResponse); err != nil {
		t.Fatalf("decode root list response error = %v", err)
	}
	if len(listResponse.Resources) != 2 || listResponse.Resources[0].ID != root.ID || listResponse.Resources[1].ID != other.ID {
		t.Fatalf("HTTP root list = %#v, want deterministic root resources", listResponse)
	}

	scopedList := operatorHTTPCall(t, handler, http.MethodGet, "/v1/resources?limit=10", root.ID, nil)
	if scopedList.Code != http.StatusOK {
		t.Fatalf("GET scoped resources status = %d, body = %s", scopedList.Code, scopedList.Body.String())
	}
	var scopedResponse OperatorResponse
	if err := json.Unmarshal(scopedList.Body.Bytes(), &scopedResponse); err != nil {
		t.Fatalf("decode scoped list response error = %v", err)
	}
	if len(scopedResponse.Resources) != 1 || scopedResponse.Resources[0].ID != child.ID {
		t.Fatalf("HTTP scoped list = %#v, want child resource", scopedResponse)
	}

	dependentDelete := operatorHTTPCall(t, handler, http.MethodDelete, "/v1/resources/"+root.ID+"?confirm=true", "", nil)
	if dependentDelete.Code != http.StatusConflict {
		t.Fatalf("DELETE dependent root status = %d, body = %s", dependentDelete.Code, dependentDelete.Body.String())
	}

	lockBody, err := json.Marshal(map[string]string{"owner": "operator", "token": "lock-token"})
	if err != nil {
		t.Fatalf("Marshal(lock) error = %v", err)
	}
	acquire := operatorHTTPCall(t, handler, http.MethodPut, "/v1/resources/"+root.ID+"/lock", "", lockBody)
	if acquire.Code != http.StatusOK {
		t.Fatalf("PUT resource lock status = %d, body = %s", acquire.Code, acquire.Body.String())
	}
	var acquireResponse OperatorResponse
	if err := json.Unmarshal(acquire.Body.Bytes(), &acquireResponse); err != nil {
		t.Fatalf("decode lock acquire response error = %v", err)
	}
	if acquireResponse.Lock == nil || acquireResponse.Lock.Token != "lock-token" {
		t.Fatalf("HTTP lock acquire = %#v, want lock response", acquireResponse)
	}
	inspect := operatorHTTPCall(t, handler, http.MethodGet, "/v1/resources/"+root.ID+"/lock", "", nil)
	if inspect.Code != http.StatusOK {
		t.Fatalf("GET resource lock status = %d, body = %s", inspect.Code, inspect.Body.String())
	}
	var inspectResponse OperatorResponse
	if err := json.Unmarshal(inspect.Body.Bytes(), &inspectResponse); err != nil {
		t.Fatalf("decode lock inspect response error = %v", err)
	}
	if inspectResponse.Lock == nil || *inspectResponse.Lock != *acquireResponse.Lock {
		t.Fatalf("HTTP lock inspect = %#v, want acquired lock", inspectResponse)
	}

	read := operatorHTTPCall(t, handler, http.MethodGet, "/v1/resources/"+child.ID, root.ID, nil)
	if read.Code != http.StatusOK {
		t.Fatalf("GET same-scope child under lock status = %d, body = %s", read.Code, read.Body.String())
	}
	updateBody, err := json.Marshal(map[string]any{"tags": map[string]string{"tier": "blocked"}, "requestId": "request-http-locked", "correlationId": "correlation-http-locked"})
	if err != nil {
		t.Fatalf("Marshal(update) error = %v", err)
	}
	lockedUpdate := operatorHTTPCall(t, handler, http.MethodPatch, "/v1/resources/"+child.ID+"/tags", root.ID, updateBody)
	if lockedUpdate.Code != http.StatusConflict {
		t.Fatalf("PATCH locked child status = %d, body = %s", lockedUpdate.Code, lockedUpdate.Body.String())
	}
	lockedDelete := operatorHTTPCall(t, handler, http.MethodDelete, "/v1/resources/"+child.ID+"?confirm=true", root.ID, nil)
	if lockedDelete.Code != http.StatusConflict {
		t.Fatalf("DELETE locked child status = %d, body = %s", lockedDelete.Code, lockedDelete.Body.String())
	}
	crossScope := operatorHTTPCall(t, handler, http.MethodGet, "/v1/resources/"+root.ID+"/lock", other.ID, nil)
	if crossScope.Code != http.StatusForbidden {
		t.Fatalf("GET cross-scope lock status = %d, body = %s", crossScope.Code, crossScope.Body.String())
	}

	wrongReleaseBody, err := json.Marshal(map[string]string{"owner": "other", "token": "wrong"})
	if err != nil {
		t.Fatalf("Marshal(wrong release) error = %v", err)
	}
	wrongRelease := operatorHTTPCall(t, handler, http.MethodDelete, "/v1/resources/"+root.ID+"/lock", "", wrongReleaseBody)
	if wrongRelease.Code != http.StatusConflict {
		t.Fatalf("DELETE wrong lock owner status = %d, body = %s", wrongRelease.Code, wrongRelease.Body.String())
	}
	release := operatorHTTPCall(t, handler, http.MethodDelete, "/v1/resources/"+root.ID+"/lock", "", lockBody)
	if release.Code != http.StatusNoContent {
		t.Fatalf("DELETE owned lock status = %d, body = %s", release.Code, release.Body.String())
	}
	if response := operatorHTTPCall(t, handler, http.MethodDelete, "/v1/resources/"+child.ID+"?confirm=true", root.ID, nil); response.Code != http.StatusNoContent {
		t.Fatalf("DELETE leaf status = %d, body = %s", response.Code, response.Body.String())
	}
	if response := operatorHTTPCall(t, handler, http.MethodDelete, "/v1/resources/"+root.ID+"?confirm=true", "", nil); response.Code != http.StatusNoContent {
		t.Fatalf("DELETE root status = %d, body = %s", response.Code, response.Body.String())
	}
}

func TestHTTPDeploymentFlowsMatchCLIContract(t *testing.T) {
	document := `{"version":"v1","parameters":{"tier":{"type":"string"},"password":{"type":"secureString"}},"resources":[{"type":"group","name":"platform","tags":{"tier":"${parameters.tier}","password":"${parameters.password}"},"desiredState":"ready"}]}`
	parameters := map[string]string{"tier": "test", "password": "secret-value"}

	cliOperator := newTestOperator(t)
	cliPlan := runOperatorCLI(t, cliOperator, "deployment", "plan", "--document", document, "--parameter", "tier=test", "--parameter", "password=secret-value")

	httpOperator := newTestOperator(t)
	httpHandler := NewHTTPHandler(httpOperator)
	requestBody, err := json.Marshal(map[string]any{
		"document":      json.RawMessage(document),
		"parameters":    parameters,
		"requestId":     "request-http-deployment",
		"correlationId": "correlation-http-deployment",
	})
	if err != nil {
		t.Fatalf("Marshal(deployment request) error = %v", err)
	}
	httpPlanRecorder := operatorHTTPCall(t, httpHandler, http.MethodPost, "/v1/deployments/plan", "", requestBody)
	if httpPlanRecorder.Code != http.StatusOK {
		t.Fatalf("POST /v1/deployments/plan status = %d, body = %s", httpPlanRecorder.Code, httpPlanRecorder.Body.String())
	}
	var httpPlan OperatorResponse
	if err := json.Unmarshal(httpPlanRecorder.Body.Bytes(), &httpPlan); err != nil {
		t.Fatalf("decode HTTP plan response error = %v", err)
	}
	if !reflect.DeepEqual(httpPlan.Plan, cliPlan.Plan) || !reflect.DeepEqual(httpPlan.Resolution, cliPlan.Resolution) {
		t.Fatalf("HTTP plan = %#v, CLI plan = %#v; want shared safe contract", httpPlan, cliPlan)
	}
	if strings.Contains(httpPlanRecorder.Body.String(), "secret-value") || !strings.Contains(httpPlanRecorder.Body.String(), `"[REDACTED]"`) {
		t.Fatalf("HTTP plan leaked or omitted redaction: %s", httpPlanRecorder.Body.String())
	}

	cliApplyOperator := newTestOperator(t)
	cliApply := runOperatorCLI(t, cliApplyOperator, "deployment", "apply", "--document", document, "--parameter", "tier=test", "--parameter", "password=secret-value", "--request-id", "request-deployment", "--correlation-id", "correlation-deployment")
	httpApplyOperator := newTestOperator(t)
	httpApplyHandler := NewHTTPHandler(httpApplyOperator)
	applyBody, err := json.Marshal(map[string]any{
		"document":      json.RawMessage(document),
		"parameters":    parameters,
		"requestId":     "request-deployment",
		"correlationId": "correlation-deployment",
	})
	if err != nil {
		t.Fatalf("Marshal(apply request) error = %v", err)
	}
	httpApplyRecorder := operatorHTTPCall(t, httpApplyHandler, http.MethodPost, "/v1/deployments/apply", "", applyBody)
	if httpApplyRecorder.Code != http.StatusOK {
		t.Fatalf("POST /v1/deployments/apply status = %d, body = %s", httpApplyRecorder.Code, httpApplyRecorder.Body.String())
	}
	var httpApply OperatorResponse
	if err := json.Unmarshal(httpApplyRecorder.Body.Bytes(), &httpApply); err != nil {
		t.Fatalf("decode HTTP apply response error = %v", err)
	}
	if httpApply.Apply == nil || cliApply.Apply == nil || !reflect.DeepEqual(httpApply.Apply.Plan, cliApply.Apply.Plan) || !reflect.DeepEqual(httpApply.Resolution, cliApply.Resolution) || len(httpApply.Apply.Operations) != len(cliApply.Apply.Operations) {
		t.Fatalf("HTTP apply = %#v, CLI apply = %#v; want shared safe contract", httpApply, cliApply)
	}
	if httpApply.Apply.Operations[0].RequestID != cliApply.Apply.Operations[0].RequestID || httpApply.Apply.Operations[0].CorrelationID != cliApply.Apply.Operations[0].CorrelationID || strings.Contains(httpApplyRecorder.Body.String(), "secret-value") {
		t.Fatalf("HTTP apply contract = %s, want bounded redacted output", httpApplyRecorder.Body.String())
	}

	httpResource := operatorHTTPCall(t, httpApplyHandler, http.MethodGet, "/v1/resources/resource-00000001", "", nil)
	if httpResource.Code != http.StatusOK || !strings.Contains(httpResource.Body.String(), `"Name":"platform"`) {
		t.Fatalf("HTTP resource inspection = %d %s, want applied platform resource", httpResource.Code, httpResource.Body.String())
	}
	httpOperation := operatorHTTPCall(t, httpApplyHandler, http.MethodGet, "/v1/operations/"+httpApply.Apply.Operations[0].ID, "", nil)
	if httpOperation.Code != http.StatusOK {
		t.Fatalf("HTTP operation inspection status = %d, body = %s", httpOperation.Code, httpOperation.Body.String())
	}
	var inspectedOperation OperatorResponse
	if err := json.Unmarshal(httpOperation.Body.Bytes(), &inspectedOperation); err != nil {
		t.Fatalf("decode HTTP operation inspection error = %v", err)
	}
	if inspectedOperation.Operation == nil || inspectedOperation.Operation.RequestID != "request-deployment" || inspectedOperation.Operation.CorrelationID != "correlation-deployment" {
		t.Fatalf("HTTP operation inspection = %#v, want applied operation identifiers", inspectedOperation)
	}

	var invalidCLIOutput bytes.Buffer
	invalidDocument := `{"version":"unsupported","resources":[]}`
	cliErr := RunCLI(context.Background(), cliOperator, []string{"deployment", "plan", "--document", invalidDocument}, &invalidCLIOutput)
	if cliErr == nil {
		t.Fatal("RunCLI(invalid plan) error = nil, want validation error")
	}
	invalidBody, err := json.Marshal(map[string]any{"document": json.RawMessage(invalidDocument)})
	if err != nil {
		t.Fatalf("Marshal(invalid request) error = %v", err)
	}
	invalidHTTP := operatorHTTPCall(t, httpHandler, http.MethodPost, "/v1/deployments/plan", "", invalidBody)
	if invalidHTTP.Code != http.StatusBadRequest || invalidHTTP.Body.String() != `{"error":"`+cliErr.Error()+`"}`+"\n" {
		t.Fatalf("HTTP invalid plan = %d %s, CLI error = %v; want semantic parity", invalidHTTP.Code, invalidHTTP.Body.String(), cliErr)
	}
}

func TestHTTPExposesCompleteBlobLifecycle(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	operator, err := NewFileOperator(root, 64)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	handler := NewHTTPHandler(operator)

	create := func(scope string, resourceType models.ResourceType, name, parentID string) *models.Resource {
		t.Helper()
		body, err := json.Marshal(map[string]any{"type": resourceType, "name": name, "parentId": parentID})
		if err != nil {
			t.Fatalf("Marshal(create) error = %v", err)
		}
		response := operatorHTTPCall(t, handler, http.MethodPost, "/v1/resources", scope, body)
		if response.Code != http.StatusCreated {
			t.Fatalf("create status = %d, body = %s", response.Code, response.Body.String())
		}
		var envelope OperatorResponse
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("decode create response error = %v", err)
		}
		return envelope.Resource
	}

	group := create("", models.ResourceTypeGroup, "platform", "")
	bucket := create(group.ID, models.ResourceTypeBucket, "assets", group.ID)
	other := create("", models.ResourceTypeGroup, "other", "")

	put := operatorHTTPCall(t, handler, http.MethodPut, "/v1/buckets/"+bucket.ID+"/objects/nested/file.txt", group.ID, []byte("hello world"))
	if put.Code != http.StatusCreated {
		t.Fatalf("PUT blob status = %d, body = %s", put.Code, put.Body.String())
	}

	get := operatorHTTPCall(t, handler, http.MethodGet, "/v1/buckets/"+bucket.ID+"/objects/nested/file.txt", group.ID, nil)
	if get.Code != http.StatusOK {
		t.Fatalf("GET blob status = %d, body = %s", get.Code, get.Body.String())
	}
	var getResponse OperatorResponse
	if err := json.Unmarshal(get.Body.Bytes(), &getResponse); err != nil {
		t.Fatalf("decode GET blob response error = %v", err)
	}
	if getResponse.Object == nil || string(getResponse.Content) != "hello world" {
		t.Fatalf("GET blob response = %#v, want metadata and content", getResponse)
	}

	rangeResult := operatorHTTPCall(t, handler, http.MethodGet, "/v1/buckets/"+bucket.ID+"/objects/nested/file.txt?start=6&end=11", group.ID, nil)
	if rangeResult.Code != http.StatusOK {
		t.Fatalf("GET blob range status = %d, body = %s", rangeResult.Code, rangeResult.Body.String())
	}
	var rangeResponse OperatorResponse
	if err := json.Unmarshal(rangeResult.Body.Bytes(), &rangeResponse); err != nil {
		t.Fatalf("decode GET blob range response error = %v", err)
	}
	if string(rangeResponse.Content) != "world" {
		t.Fatalf("GET blob range content = %q, want world", rangeResponse.Content)
	}

	list := operatorHTTPCall(t, handler, http.MethodGet, "/v1/buckets/"+bucket.ID+"/objects?limit=10", group.ID, nil)
	if list.Code != http.StatusOK {
		t.Fatalf("GET blob list status = %d, body = %s", list.Code, list.Body.String())
	}
	var listResponse OperatorResponse
	if err := json.Unmarshal(list.Body.Bytes(), &listResponse); err != nil {
		t.Fatalf("decode GET blob list response error = %v", err)
	}
	if len(listResponse.Objects) != 1 || listResponse.Objects[0].Key != "nested/file.txt" {
		t.Fatalf("GET blob list = %#v, want one object", listResponse)
	}
	if crossScope := operatorHTTPCall(t, handler, http.MethodGet, "/v1/buckets/"+bucket.ID+"/objects?limit=10", other.ID, nil); crossScope.Code != http.StatusForbidden {
		t.Fatalf("GET cross-scope blob list status = %d, body = %s", crossScope.Code, crossScope.Body.String())
	}
	if invalidLimit := operatorHTTPCall(t, handler, http.MethodGet, "/v1/buckets/"+bucket.ID+"/objects?limit=0", group.ID, nil); invalidLimit.Code != http.StatusBadRequest {
		t.Fatalf("GET invalid blob list limit status = %d, body = %s", invalidLimit.Code, invalidLimit.Body.String())
	}

	deleted := operatorHTTPCall(t, handler, http.MethodDelete, "/v1/buckets/"+bucket.ID+"/objects/nested/file.txt?confirm=true", group.ID, nil)
	if deleted.Code != http.StatusNoContent {
		t.Fatalf("DELETE blob status = %d, body = %s", deleted.Code, deleted.Body.String())
	}
	if missing := operatorHTTPCall(t, handler, http.MethodGet, "/v1/buckets/"+bucket.ID+"/objects/nested/file.txt", group.ID, nil); missing.Code != http.StatusNotFound {
		t.Fatalf("GET deleted blob status = %d, body = %s", missing.Code, missing.Body.String())
	}
	if reset := operatorHTTPCall(t, handler, http.MethodPost, "/v1/reset?confirm=true", "", nil); reset.Code != http.StatusNoContent {
		t.Fatalf("POST reset status = %d, body = %s", reset.Code, reset.Body.String())
	}
	if missingResource := operatorHTTPCall(t, handler, http.MethodGet, "/v1/resources/"+group.ID, "", nil); missingResource.Code != http.StatusNotFound {
		t.Fatalf("GET resource after reset status = %d, body = %s", missingResource.Code, missingResource.Body.String())
	}
}

func TestHTTPDestructiveActionsRequireExplicitConfirmationAndSharedLimits(t *testing.T) {
	operator := newTestOperator(t)
	handler := NewHTTPHandler(operator)

	created := operatorHTTPCall(t, handler, http.MethodPost, "/v1/resources", "", []byte(`{"type":"group","name":"platform"}`))
	if created.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body = %s", created.Code, created.Body.String())
	}
	var envelope OperatorResponse
	if err := json.Unmarshal(created.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode create response error = %v", err)
	}

	if rejected := operatorHTTPCall(t, handler, http.MethodDelete, "/v1/resources/"+envelope.Resource.ID, "", nil); rejected.Code != http.StatusConflict {
		t.Fatalf("DELETE without confirmation status = %d, body = %s", rejected.Code, rejected.Body.String())
	}
	if rejected := operatorHTTPCall(t, handler, http.MethodGet, "/v1/resources/"+envelope.Resource.ID, "", nil); rejected.Code != http.StatusOK {
		t.Fatalf("resource after rejected delete status = %d, body = %s", rejected.Code, rejected.Body.String())
	}
	if confirmed := operatorHTTPCall(t, handler, http.MethodDelete, "/v1/resources/"+envelope.Resource.ID+"?confirm=true", "", nil); confirmed.Code != http.StatusNoContent {
		t.Fatalf("DELETE with confirmation status = %d, body = %s", confirmed.Code, confirmed.Body.String())
	}
	if invalidLimit := operatorHTTPCall(t, handler, http.MethodGet, "/v1/resources?limit=101", "", nil); invalidLimit.Code != http.StatusBadRequest {
		t.Fatalf("GET over-limit status = %d, body = %s", invalidLimit.Code, invalidLimit.Body.String())
	}

	if rejected := operatorHTTPCall(t, handler, http.MethodPost, "/v1/reset", "", nil); rejected.Code != http.StatusConflict {
		t.Fatalf("POST reset without confirmation status = %d, body = %s", rejected.Code, rejected.Body.String())
	}
	if confirmed := operatorHTTPCall(t, handler, http.MethodPost, "/v1/reset?confirm=true", "", nil); confirmed.Code != http.StatusNoContent {
		t.Fatalf("POST reset with confirmation status = %d, body = %s", confirmed.Code, confirmed.Body.String())
	}
}

func TestHTTPDeploymentDestructiveApplyRequiresConfirmation(t *testing.T) {
	operator := newTestOperator(t)
	handler := NewHTTPHandler(operator)
	if _, err := operator.CreateResource(context.Background(), OperatorPrincipal{}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "platform"}); err != nil {
		t.Fatalf("CreateResource() error = %v", err)
	}
	body := []byte(`{"document":{"version":"v1","resources":[]}}`)
	if rejected := operatorHTTPCall(t, handler, http.MethodPost, "/v1/deployments/apply", "", body); rejected.Code != http.StatusConflict {
		t.Fatalf("deployment apply without confirmation status = %d, body = %s", rejected.Code, rejected.Body.String())
	}
	confirmedBody := []byte(`{"document":{"version":"v1","resources":[]},"confirm":true}`)
	if confirmed := operatorHTTPCall(t, handler, http.MethodPost, "/v1/deployments/apply", "", confirmedBody); confirmed.Code != http.StatusOK {
		t.Fatalf("deployment apply with confirmation status = %d, body = %s", confirmed.Code, confirmed.Body.String())
	}
}
