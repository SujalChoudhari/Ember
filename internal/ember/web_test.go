package ember

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func TestWebManagementFlowKeepsPlatformAndTenantResourcesSeparate(t *testing.T) {
	operator, err := NewFileOperator(t.TempDir(), 64<<20)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	defer operator.Close()
	handler := NewWebHandler(operator)

	root := webRequest(t, handler, http.MethodGet, "/", nil)
	if root.Code != http.StatusOK || !strings.Contains(root.Body.String(), "Ember management") {
		t.Fatalf("GET / = %d %q, want management page", root.Code, root.Body.String())
	}

	hostile := "<script>alert(1)</script>"
	createdTenant := webForm(t, handler, http.MethodPost, "/tenants", url.Values{
		"id": {"alpha"}, "displayName": {hostile},
	})
	if createdTenant.Code != http.StatusSeeOther || createdTenant.Header().Get("Location") != "/tenants/alpha" {
		t.Fatalf("POST /tenants = %d location %q, want tenant redirect", createdTenant.Code, createdTenant.Header().Get("Location"))
	}

	tenantPage := webRequest(t, handler, http.MethodGet, "/tenants/alpha", nil)
	if tenantPage.Code != http.StatusOK || !strings.Contains(tenantPage.Body.String(), "&lt;script&gt;alert(1)&lt;/script&gt;") || strings.Contains(tenantPage.Body.String(), hostile) {
		t.Fatalf("tenant page = %d %q, want escaped tenant identity", tenantPage.Code, tenantPage.Body.String())
	}

	platform := webForm(t, handler, http.MethodPost, "/resources", url.Values{
		"type": {"group"}, "name": {"platform-root"}, "desiredState": {"ready"},
	})
	if platform.Code != http.StatusSeeOther || platform.Header().Get("Location") != "/" {
		t.Fatalf("POST /resources = %d location %q, want root redirect", platform.Code, platform.Header().Get("Location"))
	}

	tenantResource := webForm(t, handler, http.MethodPost, "/tenants/alpha/resources", url.Values{
		"type": {"group"}, "name": {"tenant-root"}, "desiredState": {"ready"},
	})
	if tenantResource.Code != http.StatusSeeOther || tenantResource.Header().Get("Location") != "/tenants/alpha" {
		t.Fatalf("POST tenant resource = %d location %q, want tenant redirect", tenantResource.Code, tenantResource.Header().Get("Location"))
	}

	var tenants []models.Tenant
	if listed, err := operator.ListTenants(context.Background(), OperatorPrincipal{}); err != nil || len(listed) != 1 {
		t.Fatalf("ListTenants() = %#v, %v, want one tenant", listed, err)
	} else {
		tenants = listed
	}
	if tenants[0].ID != "alpha" {
		t.Fatalf("tenant = %#v, want alpha", tenants[0])
	}

	tenantsPage := webRequest(t, handler, http.MethodGet, "/tenants/alpha", nil)
	if tenantsPage.Code != http.StatusOK || !strings.Contains(tenantsPage.Body.String(), "tenant-root") {
		t.Fatalf("tenant page after resource = %d %q, want scoped resource management", tenantsPage.Code, tenantsPage.Body.String())
	}
	tenantResources, err := operator.ListResources(context.Background(), OperatorPrincipal{TenantID: "alpha"}, 10)
	if err != nil || len(tenantResources) != 1 {
		t.Fatalf("tenant ListResources() = %#v, %v, want one resource", tenantResources, err)
	}
	resourcePage := webRequest(t, handler, http.MethodGet, "/tenants/alpha/resources/"+tenantResources[0].ID, nil)
	if resourcePage.Code != http.StatusOK || !strings.Contains(resourcePage.Body.String(), "Operations") || !strings.Contains(resourcePage.Body.String(), "Audit") {
		t.Fatalf("tenant resource page = %d %q, want operation and audit views", resourcePage.Code, resourcePage.Body.String())
	}
	updated := webForm(t, handler, http.MethodPost, "/tenants/alpha/resources/"+tenantResources[0].ID+"/tags", url.Values{"tags": {"environment=test"}})
	if updated.Code != http.StatusSeeOther {
		t.Fatalf("POST tenant resource tags = %d body %q, want redirect", updated.Code, updated.Body.String())
	}
	resourcePage = webRequest(t, handler, http.MethodGet, "/tenants/alpha/resources/"+tenantResources[0].ID, nil)
	if resourcePage.Code != http.StatusOK || !strings.Contains(resourcePage.Body.String(), "resource.update.tags") || !strings.Contains(resourcePage.Body.String(), "succeeded") {
		t.Fatalf("tenant resource trace page = %d %q, want operation outcome", resourcePage.Code, resourcePage.Body.String())
	}
}

func TestWebDestructiveActionsRequireConfirmedPOST(t *testing.T) {
	operator, err := NewFileOperator(t.TempDir(), 64<<20)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	defer operator.Close()
	handler := NewWebHandler(operator)

	created := webForm(t, handler, http.MethodPost, "/resources", url.Values{
		"type": {"group"}, "name": {"delete-me"},
	})
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create resource status = %d, body = %s", created.Code, created.Body.String())
	}
	resource, err := operator.ListResources(context.Background(), OperatorPrincipal{}, 10)
	if err != nil || len(resource) != 1 {
		t.Fatalf("ListResources() = %#v, %v, want one resource", resource, err)
	}

	withoutConfirmation := webForm(t, handler, http.MethodPost, "/resources/"+resource[0].ID+"/delete", nil)
	if withoutConfirmation.Code != http.StatusConflict || !strings.Contains(withoutConfirmation.Body.String(), "confirmation") {
		t.Fatalf("unconfirmed delete = %d %q, want confirmation conflict", withoutConfirmation.Code, withoutConfirmation.Body.String())
	}
	confirmed := webForm(t, handler, http.MethodPost, "/resources/"+resource[0].ID+"/delete", url.Values{"confirm": {"true"}})
	if confirmed.Code != http.StatusSeeOther || confirmed.Header().Get("Location") != "/" {
		t.Fatalf("confirmed delete = %d location %q, want root redirect", confirmed.Code, confirmed.Header().Get("Location"))
	}
}

func TestWebServeAddressRejectsNonLoopbackBinds(t *testing.T) {
	for _, address := range []string{"0.0.0.0:8080", ":8080", "[::]:8080"} {
		if err := validateLoopbackAddress(address); err == nil {
			t.Fatalf("validateLoopbackAddress(%q) = nil, want rejection", address)
		}
	}
	for _, address := range []string{"127.0.0.1:0", "localhost:8080", "[::1]:8080"} {
		if err := validateLoopbackAddress(address); err != nil {
			t.Fatalf("validateLoopbackAddress(%q) error = %v, want loopback acceptance", address, err)
		}
	}
}

func webRequest(t *testing.T, handler http.Handler, method, path string, body *strings.Reader) *httptest.ResponseRecorder {
	t.Helper()
	if body == nil {
		body = strings.NewReader("")
	}
	request := httptest.NewRequest(method, path, body)
	request.Header.Set("Accept", "text/html")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}

func webForm(t *testing.T, handler http.Handler, method, path string, values url.Values) *httptest.ResponseRecorder {
	t.Helper()
	var body *strings.Reader
	if values == nil {
		body = strings.NewReader("")
	} else {
		body = strings.NewReader(values.Encode())
	}
	request := httptest.NewRequest(method, path, body)
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	return recorder
}
