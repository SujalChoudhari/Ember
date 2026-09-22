package ember

import (
	"bytes"
	"context"
	"mime/multipart"
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

func TestWebBucketStoreUploadsListsAndDownloadsObjects(t *testing.T) {
	operator, err := NewFileOperator(t.TempDir(), 64<<20)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	defer operator.Close()
	handler := NewWebHandler(operator)

	groupResponse := webForm(t, handler, http.MethodPost, "/resources", url.Values{
		"type": {"group"}, "name": {"content"},
	})
	if groupResponse.Code != http.StatusSeeOther {
		t.Fatalf("create group = %d %q, want redirect", groupResponse.Code, groupResponse.Body.String())
	}
	groups, err := operator.ListResources(context.Background(), OperatorPrincipal{}, 10)
	if err != nil || len(groups) != 1 {
		t.Fatalf("ListResources(groups) = %#v, %v, want one group", groups, err)
	}

	bucketResponse := webForm(t, handler, http.MethodPost, "/resources", url.Values{
		"type": {"bucket"}, "name": {"assets"}, "parentID": {groups[0].ID},
	})
	if bucketResponse.Code != http.StatusSeeOther {
		t.Fatalf("create bucket = %d %q, want redirect", bucketResponse.Code, bucketResponse.Body.String())
	}
	buckets, err := operator.ListResources(context.Background(), OperatorPrincipal{ScopeID: groups[0].ID}, 10)
	if err != nil || len(buckets) != 1 {
		t.Fatalf("ListResources(buckets) = %#v, %v, want one bucket", buckets, err)
	}

	var body bytes.Buffer
	multipartWriter := multipart.NewWriter(&body)
	if err := multipartWriter.WriteField("objectKey", "hello.txt"); err != nil {
		t.Fatalf("WriteField() error = %v", err)
	}
	part, err := multipartWriter.CreateFormFile("contentFile", "hello.txt")
	if err != nil {
		t.Fatalf("CreateFormFile() error = %v", err)
	}
	if _, err := part.Write([]byte("hello from Ember")); err != nil {
		t.Fatalf("part.Write() error = %v", err)
	}
	if err := multipartWriter.Close(); err != nil {
		t.Fatalf("multipartWriter.Close() error = %v", err)
	}
	uploadRequest := httptest.NewRequest(http.MethodPost, "/resources/"+url.PathEscape(buckets[0].ID)+"/objects?scope="+url.QueryEscape(groups[0].ID), &body)
	uploadRequest.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	uploadResponse := httptest.NewRecorder()
	handler.ServeHTTP(uploadResponse, uploadRequest)
	if uploadResponse.Code != http.StatusSeeOther {
		t.Fatalf("upload object = %d %q, want redirect", uploadResponse.Code, uploadResponse.Body.String())
	}
	if !strings.Contains(uploadResponse.Header().Get("Location"), "status=uploaded") {
		t.Fatalf("upload location = %q, want success status", uploadResponse.Header().Get("Location"))
	}
	uploadNotice := webRequest(t, handler, http.MethodGet, uploadResponse.Header().Get("Location"), nil)
	if uploadNotice.Code != http.StatusOK || !strings.Contains(uploadNotice.Body.String(), "uploaded.") {
		t.Fatalf("upload notice = %d %q, want success notice", uploadNotice.Code, uploadNotice.Body.String())
	}

	bucketPage := webRequest(t, handler, http.MethodGet, "/resources/"+url.PathEscape(buckets[0].ID)+"?scope="+url.QueryEscape(groups[0].ID), nil)
	if bucketPage.Code != http.StatusOK || !strings.Contains(bucketPage.Body.String(), "Bucket contents") || !strings.Contains(bucketPage.Body.String(), "hello.txt") {
		t.Fatalf("bucket page = %d %q, want object store listing", bucketPage.Code, bucketPage.Body.String())
	}
	download := webRequest(t, handler, http.MethodGet, "/resources/"+url.PathEscape(buckets[0].ID)+"?scope="+url.QueryEscape(groups[0].ID)+"&object=hello.txt", nil)
	if download.Code != http.StatusOK || download.Body.String() != "hello from Ember" {
		t.Fatalf("download = %d %q, want uploaded object", download.Code, download.Body.String())
	}
	deleted := webForm(t, handler, http.MethodPost, "/resources/"+url.PathEscape(buckets[0].ID)+"/objects/delete?scope="+url.QueryEscape(groups[0].ID), url.Values{
		"objectKey": {"hello.txt"}, "confirm": {"true"},
	})
	if deleted.Code != http.StatusSeeOther || !strings.Contains(deleted.Header().Get("Location"), "status=deleted") {
		t.Fatalf("delete object = %d location %q, want success redirect", deleted.Code, deleted.Header().Get("Location"))
	}
	deleteNotice := webRequest(t, handler, http.MethodGet, deleted.Header().Get("Location"), nil)
	if deleteNotice.Code != http.StatusOK || !strings.Contains(deleteNotice.Body.String(), "deleted.") || !strings.Contains(deleteNotice.Body.String(), "No objects yet") {
		t.Fatalf("delete notice = %d %q, want deletion feedback without object row", deleteNotice.Code, deleteNotice.Body.String())
	}
}

func TestWebShellKeepsNavigationAndContextAcrossPages(t *testing.T) {
	operator, err := NewFileOperator(t.TempDir(), 64<<20)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	defer operator.Close()
	handler := NewWebHandler(operator)

	root := webRequest(t, handler, http.MethodGet, "/", nil)
	rootHTML := root.Body.String()
	if root.Code != http.StatusOK || !strings.Contains(rootHTML, `class="site-header console-shell"`) || !strings.Contains(rootHTML, `aria-label="Primary navigation"`) || !strings.Contains(rootHTML, `aria-label="Context selector"`) || !strings.Contains(rootHTML, `class="navigation-toggle"`) {
		t.Fatalf("GET / shell = %d %q, want persistent accessible shell", root.Code, rootHTML)
	}
	if strings.Contains(rootHTML, `name="search"`) || strings.Contains(rootHTML, `name="query"`) {
		t.Fatalf("GET / shell = %q, want no unimplemented search control", rootHTML)
	}

	createdTenant := webForm(t, handler, http.MethodPost, "/tenants", url.Values{
		"id": {"alpha"}, "displayName": {"Alpha"},
	})
	if createdTenant.Code != http.StatusSeeOther {
		t.Fatalf("POST /tenants = %d location %q, want tenant redirect", createdTenant.Code, createdTenant.Header().Get("Location"))
	}
	createdGroup := webForm(t, handler, http.MethodPost, "/tenants/alpha/resources", url.Values{
		"type": {"group"}, "name": {"operations"}, "desiredState": {"ready"},
	})
	if createdGroup.Code != http.StatusSeeOther {
		t.Fatalf("POST tenant group = %d location %q, want tenant redirect", createdGroup.Code, createdGroup.Header().Get("Location"))
	}
	groups, err := operator.ListResources(context.Background(), OperatorPrincipal{TenantID: "alpha"}, 10)
	if err != nil || len(groups) != 1 {
		t.Fatalf("ListResources(groups) = %#v, %v, want one tenant group", groups, err)
	}
	createdBucket := webForm(t, handler, http.MethodPost, "/tenants/alpha/resources", url.Values{
		"type": {"bucket"}, "name": {"artifacts"}, "parentID": {groups[0].ID}, "desiredState": {"ready"},
	})
	if createdBucket.Code != http.StatusSeeOther {
		t.Fatalf("POST tenant bucket = %d location %q, want resource redirect", createdBucket.Code, createdBucket.Header().Get("Location"))
	}
	children, err := operator.ListResources(context.Background(), OperatorPrincipal{TenantID: "alpha", ScopeID: groups[0].ID}, 10)
	if err != nil || len(children) != 1 {
		t.Fatalf("ListResources(children) = %#v, %v, want one tenant child", children, err)
	}

	tenantPage := webRequest(t, handler, http.MethodGet, "/tenants/alpha", nil)
	tenantHTML := tenantPage.Body.String()
	if tenantPage.Code != http.StatusOK || !strings.Contains(tenantHTML, `aria-label="Context selector"`) || !strings.Contains(tenantHTML, "Alpha") || !strings.Contains(tenantHTML, `href="/control?section=overview&amp;tenant=alpha`) {
		t.Fatalf("tenant shell = %d %q, want tenant context and preserved navigation", tenantPage.Code, tenantHTML)
	}

	resourcePath := "/tenants/alpha/resources/" + url.PathEscape(children[0].ID) + "?scope=" + url.QueryEscape(groups[0].ID)
	resourcePage := webRequest(t, handler, http.MethodGet, resourcePath, nil)
	resourceHTML := resourcePage.Body.String()
	if resourcePage.Code != http.StatusOK || !strings.Contains(resourceHTML, "Alpha") || !strings.Contains(resourceHTML, groups[0].ID) || !strings.Contains(resourceHTML, "scope="+url.QueryEscape(groups[0].ID)) {
		t.Fatalf("resource shell = %d %q, want visible tenant/scope context and preserved scope links", resourcePage.Code, resourceHTML)
	}

	style := webRequest(t, handler, http.MethodGet, "/static/style.css", nil)
	styleHTML := style.Body.String()
	if style.Code != http.StatusOK || !strings.Contains(styleHTML, ".navigation-toggle") || !strings.Contains(styleHTML, "@media (max-width: 26rem)") {
		t.Fatalf("shell stylesheet = %d %q, want compact mobile navigation rules", style.Code, styleHTML)
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
