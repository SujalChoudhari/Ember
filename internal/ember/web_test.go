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
	if platform.Code != http.StatusSeeOther || !strings.HasPrefix(platform.Header().Get("Location"), "/resources/") {
		t.Fatalf("POST /resources = %d location %q, want inspectable resource redirect", platform.Code, platform.Header().Get("Location"))
	}

	tenantResource := webForm(t, handler, http.MethodPost, "/tenants/alpha/resources", url.Values{
		"type": {"group"}, "name": {"tenant-root"}, "desiredState": {"ready"},
	})
	if tenantResource.Code != http.StatusSeeOther || !strings.HasPrefix(tenantResource.Header().Get("Location"), "/tenants/alpha/resources/") {
		t.Fatalf("POST tenant resource = %d location %q, want inspectable resource redirect", tenantResource.Code, tenantResource.Header().Get("Location"))
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

	bucketResponse := webForm(t, handler, http.MethodPost, "/resources/"+groups[0].ID, url.Values{
		"type": {"bucket"}, "name": {"assets"},
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
	createdBucket := webForm(t, handler, http.MethodPost, "/tenants/alpha/resources/"+groups[0].ID, url.Values{
		"type": {"bucket"}, "name": {"artifacts"}, "desiredState": {"ready"},
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

func TestWebPlatformOverviewShowsStateFactsAndActionableEmptyState(t *testing.T) {
	operator, err := NewFileOperator(t.TempDir(), 64<<20)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	defer operator.Close()
	handler := NewWebHandler(operator)

	empty := webRequest(t, handler, http.MethodGet, "/", nil)
	if empty.Code != http.StatusOK {
		t.Fatalf("empty platform overview = %d %q, want success", empty.Code, empty.Body.String())
	}
	for _, want := range []string{"Platform overview", "No tenants yet", "Create a tenant to start"} {
		if !strings.Contains(empty.Body.String(), want) {
			t.Fatalf("empty platform overview missing %q: %q", want, empty.Body.String())
		}
	}

	if _, err := operator.CreateTenant(context.Background(), OperatorPrincipal{}, models.Tenant{ID: "alpha", DisplayName: "Alpha"}); err != nil {
		t.Fatalf("CreateTenant() error = %v", err)
	}
	resource, err := operator.CreateResource(context.Background(), OperatorPrincipal{}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "platform-root"})
	if err != nil {
		t.Fatalf("CreateResource() error = %v", err)
	}
	stateStore, ok := operator.resources.(interface {
		UpdateResourceObservedState(context.Context, string, string, models.ResourceState) (*models.Resource, error)
	})
	if !ok {
		t.Fatal("operator resource plane does not expose observed-state test control")
	}
	if _, err := stateStore.UpdateResourceObservedState(context.Background(), "", resource.ID, models.ResourceStateReady); err != nil {
		t.Fatalf("UpdateObservedState() error = %v", err)
	}
	updated, err := operator.UpdateResourceTags(context.Background(), OperatorPrincipal{}, resource.ID, map[string]string{"environment": "test"}, "request-overview", "correlation-overview")
	if err != nil || updated == nil || updated.Operation == nil {
		t.Fatalf("UpdateResourceTags() = %#v, %v, want operation evidence", updated, err)
	}

	overview := webRequest(t, handler, http.MethodGet, "/", nil)
	html := overview.Body.String()
	if overview.Code != http.StatusOK {
		t.Fatalf("populated platform overview = %d %q, want success", overview.Code, html)
	}
	for _, want := range []string{
		"Platform overview",
		`aria-label="Platform health"`,
		"Healthy",
		"1 tenant",
		"1 resource",
		"platform-root",
		`href="/tenants/alpha"`,
		`href="/resources/` + resource.ID + `"`,
		updated.Operation.ID,
		`href="/control?section=overview"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("platform overview missing %q: %q", want, html)
		}
	}
	if strings.Contains(html, "<canvas") || strings.Contains(html, "name=\"search\"") {
		t.Fatalf("platform overview contains decorative or unimplemented controls: %q", html)
	}
}

func TestWebResourceHeaderPreservesHierarchyAndSupportedActions(t *testing.T) {
	operator, err := NewFileOperator(t.TempDir(), 64<<20)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	defer operator.Close()
	handler := NewWebHandler(operator)

	createdTenant := webForm(t, handler, http.MethodPost, "/tenants", url.Values{
		"id": {"alpha"}, "displayName": {"<Alpha & Ops>"},
	})
	if createdTenant.Code != http.StatusSeeOther {
		t.Fatalf("POST /tenants = %d location %q, want tenant redirect", createdTenant.Code, createdTenant.Header().Get("Location"))
	}
	createdGroup := webForm(t, handler, http.MethodPost, "/tenants/alpha/resources", url.Values{
		"type": {"group"}, "name": {"<Operations>"}, "desiredState": {"ready"},
	})
	if createdGroup.Code != http.StatusSeeOther {
		t.Fatalf("POST tenant group = %d location %q, want tenant redirect", createdGroup.Code, createdGroup.Header().Get("Location"))
	}
	groups, err := operator.ListResources(context.Background(), OperatorPrincipal{TenantID: "alpha"}, 10)
	if err != nil || len(groups) != 1 {
		t.Fatalf("ListResources(groups) = %#v, %v, want one tenant group", groups, err)
	}
	createdWorkload := webForm(t, handler, http.MethodPost, "/tenants/alpha/resources/"+url.PathEscape(groups[0].ID), url.Values{
		"type": {"workload"}, "name": {"<worker>"}, "parentID": {groups[0].ID}, "desiredState": {"ready"},
	})
	if createdWorkload.Code != http.StatusSeeOther {
		t.Fatalf("POST child workload = %d location %q, want resource redirect", createdWorkload.Code, createdWorkload.Header().Get("Location"))
	}
	children, err := operator.ListResources(context.Background(), OperatorPrincipal{TenantID: "alpha", ScopeID: groups[0].ID}, 10)
	if err != nil || len(children) != 1 {
		t.Fatalf("ListResources(children) = %#v, %v, want one child workload", children, err)
	}

	resourcePath := "/tenants/alpha/resources/" + url.PathEscape(children[0].ID) + "?scope=" + url.QueryEscape(groups[0].ID)
	page := webRequest(t, handler, http.MethodGet, resourcePath, nil)
	html := page.Body.String()
	if page.Code != http.StatusOK {
		t.Fatalf("GET child resource = %d %q, want detail page", page.Code, html)
	}
	for _, want := range []string{
		`class="portal-breadcrumbs"`,
		`class="resource-header"`,
		`<p class="resource-kicker resource-type">Workload</p>`,
		`Desired state`,
		`Observed state`,
		children[0].ID,
		`class="resource-actions"`,
		`class="danger-panel"`,
		`scope=` + url.QueryEscape(groups[0].ID),
		`/lock/acquire?scope=` + url.QueryEscape(groups[0].ID),
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("resource page missing %q: %q", want, html)
		}
	}
	if !strings.Contains(html, "&lt;worker&gt;") || strings.Contains(html, "<worker>") {
		t.Fatalf("resource page = %q, want escaped resource identity", html)
	}
	for _, unsupported := range []string{"Search", "Start", "Stop", "Restart", "Provision"} {
		if strings.Contains(html, unsupported) {
			t.Fatalf("resource page contains unsupported action %q: %q", unsupported, html)
		}
	}

	unsupportedAction := webRequest(t, handler, http.MethodGet, resourcePath+"&action=restart", nil)
	if unsupportedAction.Code != http.StatusOK || strings.Contains(unsupportedAction.Body.String(), "Restart") {
		t.Fatalf("unsupported action query = %d %q, want unchanged detail page without unsupported control", unsupportedAction.Code, unsupportedAction.Body.String())
	}
}

func TestWebResourceDetailsShareCommonShellAndProperties(t *testing.T) {
	operator, err := NewFileOperator(t.TempDir(), 64<<20)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	defer operator.Close()
	handler := NewWebHandler(operator)

	if _, err := operator.CreateTenant(context.Background(), OperatorPrincipal{}, models.Tenant{ID: "alpha", DisplayName: "Alpha"}); err != nil {
		t.Fatalf("CreateTenant() error = %v", err)
	}
	group, err := operator.CreateResource(context.Background(), OperatorPrincipal{TenantID: "alpha"}, models.ResourceSpec{
		Type: models.ResourceTypeGroup, Name: "operations", Tags: map[string]string{"environment": "prod"},
		Provider: models.ProviderMetadata{Namespace: "ember.local", Type: "control.group", Version: "v1"},
	})
	if err != nil {
		t.Fatalf("CreateResource(group) error = %v", err)
	}
	bucket, err := operator.CreateResource(context.Background(), OperatorPrincipal{TenantID: "alpha", ScopeID: group.ID}, models.ResourceSpec{
		Type: models.ResourceTypeBucket, Name: "artifacts", ParentID: group.ID,
		Provider: models.ProviderMetadata{Namespace: "storage.local", Type: "blob.bucket", Version: "v1"},
	})
	if err != nil {
		t.Fatalf("CreateResource(bucket) error = %v", err)
	}
	workload, err := operator.CreateResource(context.Background(), OperatorPrincipal{TenantID: "alpha", ScopeID: group.ID}, models.ResourceSpec{
		Type: models.ResourceTypeWorkload, Name: "worker", ParentID: group.ID,
		Provider: models.ProviderMetadata{Namespace: "compute.local", Type: "compute.workload", Version: "v1"},
	})
	if err != nil {
		t.Fatalf("CreateResource(workload) error = %v", err)
	}

	paths := []struct {
		path string
		slot string
	}{
		{path: "/tenants/alpha/resources/" + group.ID, slot: `href="#children"`},
		{path: "/tenants/alpha/resources/" + bucket.ID + "?scope=" + url.QueryEscape(group.ID), slot: `href="#objects"`},
		{path: "/tenants/alpha/resources/" + workload.ID + "?scope=" + url.QueryEscape(group.ID), slot: `href="#children"`},
	}
	for _, resourcePath := range paths {
		page := webRequest(t, handler, http.MethodGet, resourcePath.path, nil)
		html := page.Body.String()
		if page.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %q, want resource detail", resourcePath.path, page.Code, html)
		}
		for _, want := range []string{
			`class="resource-layout"`,
			`class="resource-sidebar"`,
			`class="resource-header"`,
			`href="#configuration"`,
			resourcePath.slot,
			`href="#activity"`,
			"Configuration",
			"Provider namespace",
			"Provider type",
			"Provider version",
			"Tags",
			"Activity log",
			"Open operations &amp; audit",
		} {
			if !strings.Contains(html, want) {
				t.Fatalf("GET %s missing common detail marker %q: %q", resourcePath.path, want, html)
			}
		}
	}

	groupPage := webRequest(t, handler, http.MethodGet, paths[0].path, nil)
	for _, want := range []string{"environment=prod", "artifacts", "worker", "Children"} {
		if !strings.Contains(groupPage.Body.String(), want) {
			t.Fatalf("group detail missing %q: %q", want, groupPage.Body.String())
		}
	}
}

func TestWebTenantDirectoryShowsIdentityStatusCountsAndSafeActions(t *testing.T) {
	operator, err := NewFileOperator(t.TempDir(), 64<<20)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	defer operator.Close()
	handler := NewWebHandler(operator)

	for _, tenant := range []models.Tenant{
		{ID: "alpha", DisplayName: "Alpha <Ops>"},
		{ID: "beta", DisplayName: "Beta"},
	} {
		if _, err := operator.CreateTenant(context.Background(), OperatorPrincipal{}, tenant); err != nil {
			t.Fatalf("CreateTenant(%q) error = %v", tenant.ID, err)
		}
	}
	resource, err := operator.CreateResource(context.Background(), OperatorPrincipal{TenantID: "alpha"}, models.ResourceSpec{
		Type: models.ResourceTypeGroup, Name: "alpha-root", DesiredState: models.ResourceStateReady,
	})
	if err != nil {
		t.Fatalf("CreateResource(alpha) error = %v", err)
	}
	alphaResources, err := operator.tenantResourceManager(context.Background(), "alpha")
	if err != nil {
		t.Fatalf("tenantResourceManager(alpha) error = %v", err)
	}
	if _, err := alphaResources.UpdateResourceObservedState(context.Background(), "", resource.ID, models.ResourceStateReady); err != nil {
		t.Fatalf("UpdateResourceObservedState() error = %v", err)
	}
	if _, err := operator.CreateResource(context.Background(), OperatorPrincipal{TenantID: "beta"}, models.ResourceSpec{
		Type: models.ResourceTypeGroup, Name: "beta-private", DesiredState: models.ResourceStatePending,
	}); err != nil {
		t.Fatalf("CreateResource(beta) error = %v", err)
	}

	page := webRequest(t, handler, http.MethodGet, "/", nil)
	html := page.Body.String()
	if page.Code != http.StatusOK {
		t.Fatalf("GET / = %d %q, want tenant directory", page.Code, html)
	}
	for _, want := range []string{
		`class="card tenant-directory"`,
		"Alpha &lt;Ops&gt;",
		"alpha-root",
		"1 resource",
		"Ready",
		"Pending",
		`action="/tenants/alpha/delete"`,
		`action="/tenants/beta/delete"`,
		`href="/tenants/alpha"`,
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("tenant directory missing %q: %q", want, html)
		}
	}
	if strings.Contains(html, "<Ops>") {
		t.Fatalf("tenant directory leaked unescaped or cross-tenant data: %q", html)
	}

	alphaPage := webRequest(t, handler, http.MethodGet, "/tenants/alpha", nil)
	if alphaPage.Code != http.StatusOK || !strings.Contains(alphaPage.Body.String(), "alpha-root") || strings.Contains(alphaPage.Body.String(), "beta-private") {
		t.Fatalf("alpha tenant page = %d %q, want isolated tenant entry page", alphaPage.Code, alphaPage.Body.String())
	}
	unconfirmed := webForm(t, handler, http.MethodPost, "/tenants/alpha/delete", nil)
	if unconfirmed.Code != http.StatusConflict || !strings.Contains(unconfirmed.Body.String(), "confirmation") {
		t.Fatalf("unconfirmed tenant delete = %d %q, want confirmation conflict", unconfirmed.Code, unconfirmed.Body.String())
	}
	confirmed := webForm(t, handler, http.MethodPost, "/tenants/alpha/delete", url.Values{"confirm": {"true"}})
	if confirmed.Code != http.StatusSeeOther || confirmed.Header().Get("Location") != "/" {
		t.Fatalf("confirmed tenant delete = %d location %q, want root redirect", confirmed.Code, confirmed.Header().Get("Location"))
	}
	if _, err := operator.GetTenant(context.Background(), OperatorPrincipal{}, "alpha"); err == nil {
		t.Fatal("deleted tenant still exists")
	}
}

func TestWebTenantOverviewShowsStateSummaryActivityAndExplicitScopeSelection(t *testing.T) {
	operator, err := NewFileOperator(t.TempDir(), 64<<20)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	defer operator.Close()
	handler := NewWebHandler(operator)

	if _, err := operator.CreateTenant(context.Background(), OperatorPrincipal{}, models.Tenant{ID: "alpha", DisplayName: "Alpha"}); err != nil {
		t.Fatalf("CreateTenant() error = %v", err)
	}
	group, err := operator.CreateResource(context.Background(), OperatorPrincipal{TenantID: "alpha"}, models.ResourceSpec{
		Type: models.ResourceTypeGroup, Name: "operations", DesiredState: models.ResourceStateReady,
	})
	if err != nil {
		t.Fatalf("CreateResource(group) error = %v", err)
	}
	bucket, err := operator.CreateResource(context.Background(), OperatorPrincipal{TenantID: "alpha", ScopeID: group.ID}, models.ResourceSpec{
		Type: models.ResourceTypeBucket, Name: "artifacts", ParentID: group.ID, DesiredState: models.ResourceStateReady,
	})
	if err != nil {
		t.Fatalf("CreateResource(bucket) error = %v", err)
	}
	if _, err := operator.CreateResource(context.Background(), OperatorPrincipal{TenantID: "alpha", ScopeID: group.ID}, models.ResourceSpec{
		Type: models.ResourceTypeWorkload, Name: "worker", ParentID: group.ID, DesiredState: models.ResourceStateReady,
	}); err != nil {
		t.Fatalf("CreateResource(workload) error = %v", err)
	}
	activity, err := operator.UpdateResourceTags(context.Background(), OperatorPrincipal{TenantID: "alpha", ScopeID: group.ID}, bucket.ID, map[string]string{"environment": "test"}, "tenant-overview-request", "tenant-overview-correlation")
	if err != nil || activity == nil || activity.Operation == nil {
		t.Fatalf("UpdateResourceTags() = %#v, %v, want activity operation", activity, err)
	}

	page := webRequest(t, handler, http.MethodGet, "/tenants/alpha", nil)
	html := page.Body.String()
	if page.Code != http.StatusOK {
		t.Fatalf("GET /tenants/alpha = %d %q, want tenant overview", page.Code, html)
	}
	for _, want := range []string{
		"Tenant overview",
		"Alpha",
		"3 resources in this tenant",
		`<span class="metric-label">Groups</span><strong>1</strong>`,
		`<span class="metric-label">Buckets</span><strong>1</strong>`,
		`<span class="metric-label">Workloads</span><strong>1</strong>`,
		"Recent activity",
		activity.Operation.ID,
		"artifacts",
		`action="/control"`,
		`name="tenant" value="alpha"`,
		`name="scope" required`,
		`value="` + group.ID + `"`,
		"Choose a group scope",
		"No group scope is selected",
	} {
		if !strings.Contains(html, want) {
			t.Fatalf("tenant overview missing %q: %q", want, html)
		}
	}

	scopedPage := webRequest(t, handler, http.MethodGet, "/tenants/alpha?scope="+url.QueryEscape(group.ID), nil)
	scopedHTML := scopedPage.Body.String()
	if scopedPage.Code != http.StatusOK || !strings.Contains(scopedHTML, `option value="`+group.ID+`" selected`) || !strings.Contains(scopedHTML, group.ID) {
		t.Fatalf("scoped tenant overview = %d %q, want selected group context", scopedPage.Code, scopedHTML)
	}

	controlPage := webRequest(t, handler, http.MethodGet, "/control?tenant=alpha&scope="+url.QueryEscape(group.ID)+"&section=overview", nil)
	controlHTML := controlPage.Body.String()
	if controlPage.Code != http.StatusOK || !strings.Contains(controlHTML, "Alpha") || !strings.Contains(controlHTML, group.ID) || !strings.Contains(controlHTML, "Tenant:") || !strings.Contains(controlHTML, "Scope:") {
		t.Fatalf("scoped control center = %d %q, want explicit tenant and group context", controlPage.Code, controlHTML)
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

func TestWebResourceInventoryFiltersSortsAndPaginatesHierarchy(t *testing.T) {
	operator, err := NewFileOperator(t.TempDir(), 64<<20)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	defer operator.Close()
	handler := NewWebHandler(operator)

	if _, err := operator.CreateTenant(context.Background(), OperatorPrincipal{}, models.Tenant{ID: "alpha", DisplayName: "Alpha"}); err != nil {
		t.Fatalf("CreateTenant() error = %v", err)
	}
	group, err := operator.CreateResource(context.Background(), OperatorPrincipal{TenantID: "alpha"}, models.ResourceSpec{
		Type: models.ResourceTypeGroup, Name: "operations", DesiredState: models.ResourceStateReady,
	})
	if err != nil {
		t.Fatalf("CreateResource(group) error = %v", err)
	}
	for _, name := range []string{"backups", "artifacts"} {
		if _, err := operator.CreateResource(context.Background(), OperatorPrincipal{TenantID: "alpha", ScopeID: group.ID}, models.ResourceSpec{
			Type: models.ResourceTypeBucket, Name: name, ParentID: group.ID, DesiredState: models.ResourceStateReady,
		}); err != nil {
			t.Fatalf("CreateResource(%q) error = %v", name, err)
		}
	}
	if _, err := operator.CreateResource(context.Background(), OperatorPrincipal{TenantID: "alpha"}, models.ResourceSpec{
		Type: models.ResourceTypeGroup, Name: "other", DesiredState: models.ResourceStateReady,
	}); err != nil {
		t.Fatalf("CreateResource(other) error = %v", err)
	}

	first := webRequest(t, handler, http.MethodGet, "/?tenant=alpha&type=bucket&sort=name&order=asc&page=1&pageSize=1", nil)
	firstHTML := first.Body.String()
	if first.Code != http.StatusOK {
		t.Fatalf("filtered inventory page 1 = %d %q, want success", first.Code, firstHTML)
	}
	for _, want := range []string{
		`class="inventory-filters"`,
		"inventory-table",
		"Parent",
		"Tenant",
		"Desired",
		"Observed",
		"Alpha",
		"artifacts",
		"Showing 1–1 of 2 resources",
		`aria-label="Next resource inventory page"`,
		`href="/tenants/alpha/resources/` + group.ID + `"`,
	} {
		if !strings.Contains(firstHTML, want) {
			t.Fatalf("filtered inventory page 1 missing %q: %q", want, firstHTML)
		}
	}
	if strings.Contains(firstHTML, "backups") || strings.Contains(firstHTML, "other") {
		t.Fatalf("filtered inventory page 1 contains rows outside the filter/page: %q", firstHTML)
	}

	second := webRequest(t, handler, http.MethodGet, "/?tenant=alpha&type=bucket&sort=name&order=asc&page=2&pageSize=1", nil)
	secondHTML := second.Body.String()
	if second.Code != http.StatusOK || !strings.Contains(secondHTML, "backups") || strings.Contains(secondHTML, "artifacts") || !strings.Contains(secondHTML, "Showing 2–2 of 2 resources") {
		t.Fatalf("filtered inventory page 2 = %d %q, want second sorted row", second.Code, secondHTML)
	}

	style := webRequest(t, handler, http.MethodGet, "/static/style.css", nil)
	styleHTML := style.Body.String()
	if style.Code != http.StatusOK || !strings.Contains(styleHTML, ".inventory-table") || !strings.Contains(styleHTML, "overflow-x: auto") {
		t.Fatalf("inventory stylesheet = %d %q, want horizontally scrollable mobile table", style.Code, styleHTML)
	}
}

func TestWebResourceCreationUsesContextAndNavigatesToInspectableResource(t *testing.T) {
	operator, err := NewFileOperator(t.TempDir(), 64<<20)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	defer operator.Close()
	handler := NewWebHandler(operator)

	for _, tenant := range []models.Tenant{
		{ID: "alpha", DisplayName: "Alpha"},
		{ID: "beta", DisplayName: "Beta"},
	} {
		if _, err := operator.CreateTenant(context.Background(), OperatorPrincipal{}, tenant); err != nil {
			t.Fatalf("CreateTenant(%q) error = %v", tenant.ID, err)
		}
	}

	createdGroup := webForm(t, handler, http.MethodPost, "/tenants/alpha/resources", url.Values{
		"type": {"group"}, "name": {"operations"}, "desiredState": {"ready"},
		"providerNamespace": {"ember.local"}, "providerType": {"control.group"}, "providerVersion": {"v1"},
	})
	if createdGroup.Code != http.StatusSeeOther || !strings.HasPrefix(createdGroup.Header().Get("Location"), "/tenants/alpha/resources/") {
		t.Fatalf("create group = %d location %q, want inspectable tenant resource redirect", createdGroup.Code, createdGroup.Header().Get("Location"))
	}
	groupPage := webRequest(t, handler, http.MethodGet, createdGroup.Header().Get("Location"), nil)
	groupHTML := groupPage.Body.String()
	for _, want := range []string{"Alpha", "ember.local", "control.group", "v1", "Create child resource", "Group", "Bucket", "Workload"} {
		if groupPage.Code != http.StatusOK || !strings.Contains(groupHTML, want) {
			t.Fatalf("created group page missing %q: %d %q", want, groupPage.Code, groupHTML)
		}
	}

	groups, err := operator.ListResources(context.Background(), OperatorPrincipal{TenantID: "alpha"}, 10)
	if err != nil || len(groups) != 1 {
		t.Fatalf("ListResources(groups) = %#v, %v, want one group", groups, err)
	}
	createdOther := webForm(t, handler, http.MethodPost, "/tenants/alpha/resources", url.Values{
		"type": {"group"}, "name": {"other"}, "desiredState": {"ready"},
	})
	if createdOther.Code != http.StatusSeeOther {
		t.Fatalf("create second group = %d location %q, want redirect", createdOther.Code, createdOther.Header().Get("Location"))
	}
	groups, err = operator.ListResources(context.Background(), OperatorPrincipal{TenantID: "alpha"}, 10)
	if err != nil || len(groups) != 2 {
		t.Fatalf("ListResources(groups after second create) = %#v, %v, want two groups", groups, err)
	}

	child := webForm(t, handler, http.MethodPost, "/tenants/alpha/resources/"+groups[0].ID, url.Values{
		"type": {"bucket"}, "name": {"artifacts"}, "desiredState": {"ready"},
		"parentID": {groups[1].ID}, "tenantID": {"beta"},
		"providerNamespace": {"storage.local"}, "providerType": {"blob.bucket"}, "providerVersion": {"v1"},
	})
	expectedChildPrefix := "/tenants/alpha/resources/"
	if child.Code != http.StatusSeeOther || !strings.HasPrefix(child.Header().Get("Location"), expectedChildPrefix) || !strings.Contains(child.Header().Get("Location"), "scope="+url.QueryEscape(groups[0].ID)) {
		t.Fatalf("create child = %d location %q, want route-scoped inspectable redirect", child.Code, child.Header().Get("Location"))
	}
	childPage := webRequest(t, handler, http.MethodGet, child.Header().Get("Location"), nil)
	childHTML := childPage.Body.String()
	for _, want := range []string{"Alpha", "artifacts", "storage.local", "blob.bucket", "v1", "Parent scope"} {
		if childPage.Code != http.StatusOK || !strings.Contains(childHTML, want) {
			t.Fatalf("created child page missing %q: %d %q", want, childPage.Code, childHTML)
		}
	}
	children, err := operator.ListResources(context.Background(), OperatorPrincipal{TenantID: "alpha", ScopeID: groups[0].ID}, 10)
	if err != nil || len(children) != 1 || children[0].Spec.ParentID != groups[0].ID {
		t.Fatalf("route-scoped children = %#v, %v, want child under route parent %q", children, err, groups[0].ID)
	}
	otherChildren, err := operator.ListResources(context.Background(), OperatorPrincipal{TenantID: "alpha", ScopeID: groups[1].ID}, 10)
	if err != nil || len(otherChildren) != 0 {
		t.Fatalf("form parent override created resources in wrong scope: %#v, %v", otherChildren, err)
	}

	for _, invalid := range []struct {
		name   string
		values url.Values
	}{
		{name: "type", values: url.Values{"type": {"not-supported"}, "name": {"named"}}},
		{name: "name", values: url.Values{"type": {"group"}, "name": {"   "}}},
	} {
		response := webForm(t, handler, http.MethodPost, "/tenants/alpha/resources", invalid.values)
		body := response.Body.String()
		if response.Code != http.StatusBadRequest || !strings.Contains(body, invalid.name) || strings.Contains(body, "invalid resource spec") {
			t.Fatalf("invalid %s response = %d %q, want field-specific safe validation", invalid.name, response.Code, body)
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
