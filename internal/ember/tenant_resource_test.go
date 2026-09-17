package ember

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

func TestTenantResourceSelectionUsesDedicatedSQLiteState(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	operator, err := NewFileOperator(root, 64)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	defer func() {
		if err := operator.Close(); err != nil {
			t.Errorf("Operator.Close() error = %v", err)
		}
	}()
	for _, tenant := range []models.Tenant{
		{ID: "alpha", DisplayName: "Alpha"},
		{ID: "beta", DisplayName: "Beta"},
	} {
		if _, err := operator.CreateTenant(ctx, OperatorPrincipal{}, tenant); err != nil {
			t.Fatalf("CreateTenant(%q) error = %v", tenant.ID, err)
		}
	}

	var cliOutput bytes.Buffer
	if err := RunCLI(ctx, operator, []string{
		"resource", "create", "--tenant", "alpha", "--type", "group", "--name", "shared",
	}, &cliOutput); err != nil {
		t.Fatalf("tenant resource create error = %v", err)
	}
	var cliResponse OperatorResponse
	if err := json.Unmarshal(cliOutput.Bytes(), &cliResponse); err != nil {
		t.Fatalf("decode tenant resource response error = %v", err)
	}
	if cliResponse.Resource == nil {
		t.Fatalf("tenant resource response = %#v, want resource", cliResponse)
	}

	other, err := operator.CreateResource(ctx, OperatorPrincipal{TenantID: "beta"}, models.ResourceSpec{
		Type: models.ResourceTypeGroup,
		Name: "shared",
	})
	if err != nil {
		t.Fatalf("CreateResource(beta) error = %v", err)
	}
	if other.ID == cliResponse.Resource.ID {
		t.Fatalf("tenant resource IDs = %q and %q, want globally distinguishable IDs", cliResponse.Resource.ID, other.ID)
	}

	alphaResources, err := operator.ListResources(ctx, OperatorPrincipal{TenantID: "alpha"}, 10)
	if err != nil {
		t.Fatalf("ListResources(alpha) error = %v", err)
	}
	if len(alphaResources) != 1 || alphaResources[0].Spec.Name != "shared" {
		t.Fatalf("alpha resources = %#v, want one shared resource", alphaResources)
	}
	betaResources, err := operator.ListResources(ctx, OperatorPrincipal{TenantID: "beta"}, 10)
	if err != nil {
		t.Fatalf("ListResources(beta) error = %v", err)
	}
	if len(betaResources) != 1 || betaResources[0].Spec.Name != "shared" {
		t.Fatalf("beta resources = %#v, want one shared resource", betaResources)
	}
	platformResources, err := operator.ListResources(ctx, OperatorPrincipal{}, 10)
	if err != nil {
		t.Fatalf("ListResources(platform) error = %v", err)
	}
	if len(platformResources) != 0 {
		t.Fatalf("platform resources = %#v, want tenant resources excluded", platformResources)
	}

	if _, err := operator.GetResource(ctx, OperatorPrincipal{TenantID: "beta"}, cliResponse.Resource.ID); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("GetResource(cross-tenant) error = %v, want scope denial", err)
	}
	if _, err := operator.GetResource(ctx, OperatorPrincipal{TenantID: "beta"}, other.ID); err != nil {
		t.Fatalf("GetResource(beta) error = %v, want own resource", err)
	}

	handler := NewHTTPHandler(operator)
	body := []byte(`{"type":"group","name":"http-resource"}`)
	request := httptest.NewRequest(http.MethodPost, "/v1/resources", bytes.NewReader(body))
	request.Header.Set("X-Ember-Tenant", "alpha")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("HTTP tenant resource create status = %d, body = %s", recorder.Code, recorder.Body.String())
	}

	var listed OperatorResponse
	cliOutput.Reset()
	if err := RunCLI(ctx, operator, []string{"resource", "list", "--tenant", "alpha", "--limit", "10"}, &cliOutput); err != nil {
		t.Fatalf("tenant resource list error = %v", err)
	}
	if err := json.Unmarshal(cliOutput.Bytes(), &listed); err != nil {
		t.Fatalf("decode tenant resource list error = %v", err)
	}
	if len(listed.Resources) != 2 {
		t.Fatalf("tenant resource list = %#v, want CLI-selected tenant state", listed)
	}

	crossTenant := httptest.NewRequest(http.MethodGet, "/v1/resources/"+cliResponse.Resource.ID, nil)
	crossTenant.Header.Set("X-Ember-Tenant", "beta")
	crossRecorder := httptest.NewRecorder()
	handler.ServeHTTP(crossRecorder, crossTenant)
	if crossRecorder.Code != http.StatusForbidden {
		t.Fatalf("HTTP cross-tenant resource status = %d, body = %s", crossRecorder.Code, crossRecorder.Body.String())
	}
}

func TestTenantResourceLifecyclePreservesPersistenceLocksReplayAndAudit(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	operator, err := NewFileOperator(root, 64)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	for _, tenant := range []models.Tenant{
		{ID: "alpha", DisplayName: "Alpha"},
		{ID: "beta", DisplayName: "Beta"},
	} {
		if _, err := operator.CreateTenant(ctx, OperatorPrincipal{}, tenant); err != nil {
			t.Fatalf("CreateTenant(%q) error = %v", tenant.ID, err)
		}
	}

	alpha := OperatorPrincipal{TenantID: "alpha"}
	rootResource, err := operator.CreateResource(ctx, alpha, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "root"})
	if err != nil {
		t.Fatalf("CreateResource(alpha root) error = %v", err)
	}
	child, err := operator.CreateResource(ctx, OperatorPrincipal{TenantID: "alpha", ScopeID: rootResource.ID}, models.ResourceSpec{
		Type: models.ResourceTypeBucket, Name: "assets", ParentID: rootResource.ID,
	})
	if err != nil {
		t.Fatalf("CreateResource(alpha child) error = %v", err)
	}
	betaRoot, err := operator.CreateResource(ctx, OperatorPrincipal{TenantID: "beta"}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "root"})
	if err != nil {
		t.Fatalf("CreateResource(beta root) error = %v", err)
	}
	if betaRoot.ID == rootResource.ID {
		t.Fatalf("tenant resource IDs = %q and %q, want isolated identifiers", rootResource.ID, betaRoot.ID)
	}

	listed, err := operator.ListResources(ctx, alpha, 10)
	if err != nil || len(listed) != 1 || listed[0].ID != rootResource.ID {
		t.Fatalf("ListResources(alpha root) = %#v, %v", listed, err)
	}
	if _, err := operator.GetResource(ctx, OperatorPrincipal{TenantID: "beta"}, rootResource.ID); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("GetResource(cross-tenant) error = %v, want scope denial", err)
	}
	if _, err := operator.UpdateResourceTags(ctx, OperatorPrincipal{TenantID: "beta"}, rootResource.ID, map[string]string{"tier": "cross-tenant"}, "cross-tenant-request", "cross-tenant-correlation"); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("UpdateResourceTags(cross-tenant) error = %v, want scope denial", err)
	}

	updated, err := operator.UpdateResourceTags(ctx, OperatorPrincipal{TenantID: "alpha", ScopeID: rootResource.ID}, child.ID, map[string]string{"tier": "tenant"}, "tenant-request-1", "tenant-correlation-1")
	if err != nil {
		t.Fatalf("UpdateResourceTags() error = %v", err)
	}
	replayed, err := operator.UpdateResourceTags(ctx, OperatorPrincipal{TenantID: "alpha", ScopeID: rootResource.ID}, child.ID, map[string]string{"tier": "different"}, "tenant-request-1", "tenant-correlation-replay")
	if err != nil || replayed == nil || !replayed.Replayed || replayed.Resource.Spec.Tags["tier"] != "tenant" || replayed.Operation.ID != updated.Operation.ID {
		t.Fatalf("replayed UpdateResourceTags() = %#v, %v", replayed, err)
	}
	operation, err := operator.GetOperation(ctx, OperatorPrincipal{TenantID: "alpha", ScopeID: rootResource.ID}, updated.Operation.ID)
	if err != nil || operation.ResourceID != child.ID {
		t.Fatalf("GetOperation(tenant) = %#v, %v", operation, err)
	}
	history, err := operator.ListAuditHistory(ctx, OperatorPrincipal{TenantID: "alpha", ScopeID: rootResource.ID}, child.ID, 10)
	if err != nil || len(history) != 1 || history[0].OperationID != updated.Operation.ID {
		t.Fatalf("ListAuditHistory(tenant) = %#v, %v", history, err)
	}

	lock := models.ResourceLock{Owner: "tenant-test", Token: "tenant-lock"}
	if err := operator.AcquireResourceLock(ctx, alpha, rootResource.ID, lock); err != nil {
		t.Fatalf("AcquireResourceLock() error = %v", err)
	}
	if _, err := operator.UpdateResourceTags(ctx, OperatorPrincipal{TenantID: "alpha", ScopeID: rootResource.ID}, child.ID, map[string]string{"tier": "blocked"}, "tenant-request-2", "tenant-correlation-2"); !errors.Is(err, persistence.ErrResourceLocked) {
		t.Fatalf("locked UpdateResourceTags() error = %v, want resource lock", err)
	}
	if err := operator.ReleaseResourceLock(ctx, alpha, rootResource.ID, lock); err != nil {
		t.Fatalf("ReleaseResourceLock() error = %v", err)
	}
	if err := operator.Close(); err != nil {
		t.Fatalf("Operator.Close() error = %v", err)
	}

	reopened, err := NewFileOperator(root, 64)
	if err != nil {
		t.Fatalf("NewFileOperator(reopen) error = %v", err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("reopened.Close() error = %v", err)
		}
	}()
	persisted, err := reopened.GetResource(ctx, OperatorPrincipal{TenantID: "alpha", ScopeID: rootResource.ID}, child.ID)
	if err != nil || persisted.Spec.Tags["tier"] != "tenant" {
		t.Fatalf("reopened tenant resource = %#v, %v", persisted, err)
	}
	if _, err := reopened.GetResource(ctx, OperatorPrincipal{TenantID: "beta"}, child.ID); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("reopened cross-tenant read error = %v, want scope denial", err)
	}

	if err := reopened.DeleteResource(ctx, OperatorPrincipal{TenantID: "alpha", ScopeID: rootResource.ID}, child.ID); err != nil {
		t.Fatalf("DeleteResource(child) error = %v", err)
	}
	if err := reopened.DeleteResource(ctx, alpha, rootResource.ID); err != nil {
		t.Fatalf("DeleteResource(root) error = %v", err)
	}
	if err := reopened.DeleteTenant(ctx, OperatorPrincipal{}, "alpha", true); err != nil {
		t.Fatalf("DeleteTenant(alpha) error = %v", err)
	}
	if _, err := reopened.GetTenant(ctx, OperatorPrincipal{}, "alpha"); !errors.Is(err, persistence.ErrTenantNotFound) {
		t.Fatalf("GetTenant(deleted alpha) error = %v, want not found", err)
	}
}
