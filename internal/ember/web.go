package ember

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/events"
	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
	"github.com/SujalChoudhari/Ember/internal/ember/queue"
)

//go:embed web_assets.html web_assets.css
var webAssets embed.FS

var (
	errInvalidWebAddress = errors.New("web server address must be loopback-only")
	errInvalidWebForm    = errors.New("invalid web form")
)

const webTenantActivityLimit = 10

type webHandler struct {
	operator  *Operator
	templates *template.Template
}

type webPage struct {
	BasePath         string
	View             string
	Section          string
	Title            string
	Tenant           *models.Tenant
	Tenants          []models.Tenant
	TenantSummary    webTenantSummary
	TenantSummaries  []webTenantSummary
	TenantID         string
	SelectedScope    string
	ScopeOptions     []models.Resource
	Resources        []models.Resource
	TenantActivity   []webTenantActivity
	Resource         *models.Resource
	Children         []models.Resource
	ParentScope      string
	Lock             *models.ResourceLock
	Operations       []models.Operation
	Audit            []models.AuditEntry
	Objects          []models.BlobObject
	ObjectBytes      int64
	Workloads        []webWorkload
	Networks         []models.Network
	Ports            []models.NetworkPort
	Endpoints        []models.NetworkEndpoint
	ApplyProgress    []models.ApplyProgressRecord
	Recoveries       []models.RecoveryRecord
	Topics           []events.Topic
	Subscriptions    []events.Subscription
	DeadLetters      []queue.DeadLetterRecord
	QueueStatus      string
	Metrics          events.MetricsSnapshot
	EventRuntime     string
	PlatformOverview webPlatformOverview
	Notice           string
	Error            string
}

type webPlatformOverview struct {
	TenantCount     int
	ResourceCount   int
	OperationCount  int
	Resources       []webOverviewResource
	Operations      []webOverviewOperation
	TenantSummaries []webTenantSummary
	Health          webPlatformHealth
	Inventory       webResourceInventory
}

type webTenantSummary struct {
	Tenant        models.Tenant
	ResourceCount int
	GroupCount    int
	BucketCount   int
	WorkloadCount int
	Status        string
}

type webTenantActivity struct {
	Operation models.Operation
	Resource  models.Resource
}

type webOverviewResource struct {
	Resource models.Resource
	TenantID string
	Parent   *models.Resource
}

const (
	webResourceInventoryDefaultPageSize = 10
	webResourceInventoryMaxPageSize     = 25
)

type webResourceInventoryFilter struct {
	Search   string
	Tenant   string
	Type     string
	Desired  string
	Observed string
	Sort     string
	Order    string
	Page     int
	PageSize int
}

type webResourceInventory struct {
	Rows        []webOverviewResource
	Total       int
	FirstRow    int
	LastRow     int
	Page        int
	PageSize    int
	PageCount   int
	HasPrevious bool
	HasNext     bool
	PreviousURL string
	NextURL     string
	Filter      webResourceInventoryFilter
}

type webOverviewOperation struct {
	Operation models.Operation
	TenantID  string
}

type webPlatformHealth struct {
	Label        string
	Detail       string
	ReadyCount   int
	PendingCount int
	FailedCount  int
	UnknownCount int
}

type webWorkload struct {
	View    *WorkloadView
	Logs    []models.WorkloadLog
	Volumes []models.WorkloadVolume
}

func NewWebHandler(operator *Operator) http.Handler {
	return &webHandler{
		operator: operator,
		templates: template.Must(template.New("page").Funcs(template.FuncMap{
			"baseURL":                 webURL,
			"resourceURL":             webResourceURL,
			"resourceDeleteURL":       webResourceDeleteURL,
			"resourceTagsURL":         webResourceTagsURL,
			"resourceObjectsURL":      webResourceObjectsURL,
			"resourceObjectURL":       webResourceObjectURL,
			"resourceDeleteObjectURL": webResourceDeleteObjectURL,
			"tenantURL":               webTenantURL,
			"controlURL":              webControlURL,
			"resourceLockURL":         webResourceLockURL,
			"resourceTypeLabel":       webResourceTypeLabel,
			"inventorySortURL":        webInventorySortURL,
			"tagsValue":               webTagsValue,
			"formatTime":              webFormatTime,
			"formatBytes":             webFormatBytes,
		}).ParseFS(webAssets, "web_assets.html")),
	}
}

// Serve starts the standard-library management server on an explicitly
// loopback-only address. The context is used to stop the server during clean
// shutdowns; no public interface is accepted.
func Serve(ctx context.Context, operator *Operator, address string) error {
	if operator == nil {
		return ErrInvalidOperator
	}
	if err := validateLoopbackAddress(address); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	server := &http.Server{Handler: NewWebHandler(operator)}
	if ctx != nil {
		go func() {
			<-ctx.Done()
			_ = server.Shutdown(context.Background())
		}()
	}
	serveErr := server.Serve(listener)
	if errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return serveErr
}

func validateLoopbackAddress(address string) error {
	host, port, err := net.SplitHostPort(address)
	if err != nil || strings.TrimSpace(host) == "" {
		return errInvalidWebAddress
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return errInvalidWebAddress
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return errInvalidWebAddress
	}
	return nil
}

func (handler *webHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if handler.operator == nil {
		handler.renderError(writer, request, http.StatusInternalServerError, ErrInvalidOperator)
		return
	}
	if request.URL.Path == "/static/style.css" {
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			writer.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		data, err := webAssets.ReadFile("web_assets.css")
		if err != nil {
			handler.renderError(writer, request, http.StatusInternalServerError, err)
			return
		}
		writer.Header().Set("Content-Type", "text/css; charset=utf-8")
		_, _ = writer.Write(data)
		return
	}

	segments, ok := webPathSegments(request.URL.Path)
	if !ok {
		handler.renderError(writer, request, http.StatusNotFound, errors.New("page not found"))
		return
	}
	switch segments[0] {
	case "":
		handler.platform(writer, request, segments)
	case "control":
		handler.control(writer, request, segments)
	case "tenants":
		if len(segments) == 1 && request.Method == http.MethodPost {
			handler.createTenant(writer, request)
			return
		}
		handler.tenant(writer, request, segments)
	case "resources":
		handler.platformResource(writer, request, segments)
	default:
		handler.renderError(writer, request, http.StatusNotFound, errors.New("page not found"))
	}
}

func webPathSegments(pathValue string) ([]string, bool) {
	trimmed := strings.Trim(pathValue, "/")
	if trimmed == "" {
		return []string{""}, true
	}
	parts := strings.Split(trimmed, "/")
	segments := make([]string, len(parts))
	for index, part := range parts {
		decoded, err := url.PathUnescape(part)
		if err != nil || decoded == "" {
			return nil, false
		}
		segments[index] = decoded
	}
	return segments, true
}

func (handler *webHandler) platform(writer http.ResponseWriter, request *http.Request, segments []string) {
	if len(segments) != 1 || request.Method != http.MethodGet {
		if len(segments) == 1 && request.Method == http.MethodPost {
			handler.createTenant(writer, request)
			return
		}
		handler.methodNotAllowed(writer, request, http.MethodGet+", "+http.MethodPost)
		return
	}
	tenants, err := handler.operator.ListTenants(request.Context(), OperatorPrincipal{})
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	resources, err := handler.operator.ListResources(request.Context(), OperatorPrincipal{}, persistence.MaxResourceListLimit)
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	overview, err := handler.platformOverview(request.Context(), tenants, resources)
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	overview.Inventory = webFilterResourceInventory(webBasePath(request), request.URL.Query(), overview.Resources)
	handler.render(writer, request, http.StatusOK, webPage{
		View: "platform", Title: "Platform overview", Tenants: tenants,
		Resources: resources, TenantSummaries: overview.TenantSummaries, PlatformOverview: overview,
	})
}

func (handler *webHandler) platformOverview(ctx context.Context, tenants []models.Tenant, platformResources []models.Resource) (webPlatformOverview, error) {
	overview := webPlatformOverview{TenantCount: len(tenants)}
	appendResources := func(tenantID string, resources []models.Resource) error {
		inventory, err := handler.tenantResourceInventory(ctx, tenantID, resources)
		if err != nil {
			return err
		}
		for _, resource := range inventory {
			overview.Resources = append(overview.Resources, webOverviewResource{Resource: resource, TenantID: tenantID})
			switch resource.ObservedState {
			case models.ResourceStateReady:
				overview.Health.ReadyCount++
			case models.ResourceStatePending:
				overview.Health.PendingCount++
			case models.ResourceStateFailed, models.ResourceStateDeleting:
				overview.Health.FailedCount++
			default:
				overview.Health.UnknownCount++
			}

			operations, err := handler.operator.ListOperations(ctx, OperatorPrincipal{TenantID: tenantID, ScopeID: resource.Spec.ParentID}, resource.ID, persistence.MaxOperationListLimit)
			if err != nil {
				return err
			}
			for _, operation := range operations {
				overview.Operations = append(overview.Operations, webOverviewOperation{Operation: operation, TenantID: tenantID})
			}
		}
		return nil
	}
	if err := appendResources("", platformResources); err != nil {
		return webPlatformOverview{}, err
	}
	for _, tenant := range tenants {
		resources, err := handler.operator.ListResources(ctx, OperatorPrincipal{TenantID: tenant.ID}, persistence.MaxResourceListLimit)
		if err != nil {
			return webPlatformOverview{}, err
		}
		overview.TenantSummaries = append(overview.TenantSummaries, webTenantSummary{
			Tenant: tenant, ResourceCount: len(resources), Status: webTenantStatus(resources),
		})
		if err := appendResources(tenant.ID, resources); err != nil {
			return webPlatformOverview{}, err
		}
	}

	sort.SliceStable(overview.Operations, func(left, right int) bool {
		return overview.Operations[left].Operation.UpdatedAt.After(overview.Operations[right].Operation.UpdatedAt)
	})
	if len(overview.Operations) > persistence.MaxOperationListLimit {
		overview.Operations = overview.Operations[:persistence.MaxOperationListLimit]
	}
	resourceByKey := make(map[string]models.Resource, len(overview.Resources))
	for _, row := range overview.Resources {
		resourceByKey[row.TenantID+"\x00"+row.Resource.ID] = row.Resource
	}
	for index := range overview.Resources {
		parentID := overview.Resources[index].Resource.Spec.ParentID
		if parentID == "" {
			continue
		}
		parent, ok := resourceByKey[overview.Resources[index].TenantID+"\x00"+parentID]
		if ok {
			overview.Resources[index].Parent = &parent
		}
	}
	overview.ResourceCount = len(overview.Resources)
	overview.OperationCount = len(overview.Operations)
	switch {
	case overview.ResourceCount == 0:
		overview.Health.Label = "No observed state"
		overview.Health.Detail = "Create a platform or tenant resource to begin observing control-plane state."
	case overview.Health.FailedCount > 0:
		overview.Health.Label = "Attention needed"
		overview.Health.Detail = "One or more resources report a failed or deleting observed state."
	case overview.Health.PendingCount > 0 || overview.Health.UnknownCount > 0:
		overview.Health.Label = "Awaiting observed state"
		overview.Health.Detail = "Some resources have not reported a ready observed state yet."
	default:
		overview.Health.Label = "Healthy"
		overview.Health.Detail = "Every listed resource reports a ready observed state."
	}
	return overview, nil
}

func webTenantStatus(resources []models.Resource) string {
	if len(resources) == 0 {
		return "No resources"
	}
	pending := false
	for _, resource := range resources {
		switch resource.ObservedState {
		case models.ResourceStateFailed, models.ResourceStateDeleting:
			return "Attention"
		case models.ResourceStatePending, models.ResourceStateUnknown, "":
			pending = true
		}
	}
	if pending {
		return "Pending"
	}
	return "Ready"
}

func (handler *webHandler) tenantResourceInventory(ctx context.Context, tenantID string, roots []models.Resource) ([]models.Resource, error) {
	pending := append([]models.Resource(nil), roots...)
	inventory := make([]models.Resource, 0, len(roots))
	visited := make(map[string]struct{})
	for len(pending) > 0 {
		resource := pending[0]
		pending = pending[1:]
		if _, seen := visited[resource.ID]; seen {
			continue
		}
		visited[resource.ID] = struct{}{}
		inventory = append(inventory, resource)
		if resource.Spec.Type != models.ResourceTypeGroup {
			continue
		}
		children, err := handler.operator.ListResources(ctx, OperatorPrincipal{TenantID: tenantID, ScopeID: resource.ID}, persistence.MaxResourceListLimit)
		if errors.Is(err, persistence.ErrResourceNotFound) {
			continue
		}
		if err != nil {
			return nil, err
		}
		pending = append(pending, children...)
	}
	return inventory, nil
}

func webTenantSummaryFor(tenant models.Tenant, resources []models.Resource) webTenantSummary {
	summary := webTenantSummary{Tenant: tenant, ResourceCount: len(resources), Status: webTenantStatus(resources)}
	for _, resource := range resources {
		switch resource.Spec.Type {
		case models.ResourceTypeGroup:
			summary.GroupCount++
		case models.ResourceTypeBucket:
			summary.BucketCount++
		case models.ResourceTypeWorkload:
			summary.WorkloadCount++
		}
	}
	return summary
}

func (handler *webHandler) tenantActivity(ctx context.Context, tenantID string, resources []models.Resource) ([]webTenantActivity, error) {
	activity := make([]webTenantActivity, 0)
	principal := OperatorPrincipal{TenantID: tenantID}
	for _, resource := range resources {
		principal.ScopeID = resource.Spec.ParentID
		operations, err := handler.operator.ListOperations(ctx, principal, resource.ID, webTenantActivityLimit)
		if err != nil {
			return nil, err
		}
		for _, operation := range operations {
			activity = append(activity, webTenantActivity{Operation: operation, Resource: resource})
		}
	}
	sort.SliceStable(activity, func(left, right int) bool {
		return activity[left].Operation.UpdatedAt.After(activity[right].Operation.UpdatedAt)
	})
	if len(activity) > webTenantActivityLimit {
		activity = activity[:webTenantActivityLimit]
	}
	return activity, nil
}

func (handler *webHandler) createTenant(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	tenant, err := handler.operator.CreateTenant(request.Context(), OperatorPrincipal{}, models.Tenant{
		ID: request.FormValue("id"), DisplayName: request.FormValue("displayName"),
	})
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.redirect(writer, request, webTenantURL(webBasePath(request), tenant.ID))
}

func (handler *webHandler) tenant(writer http.ResponseWriter, request *http.Request, segments []string) {
	if len(segments) < 2 {
		handler.renderError(writer, request, http.StatusNotFound, errors.New("tenant page not found"))
		return
	}
	tenantID := segments[1]
	switch {
	case len(segments) == 2 && request.Method == http.MethodGet:
		handler.tenantPage(writer, request, tenantID)
	case len(segments) == 3 && segments[2] == "delete" && request.Method == http.MethodPost:
		handler.deleteTenant(writer, request, tenantID)
	case len(segments) == 3 && segments[2] == "resources":
		if request.Method == http.MethodGet {
			handler.tenantPage(writer, request, tenantID)
		} else if request.Method == http.MethodPost {
			handler.createResource(writer, request, tenantID, "")
		} else {
			handler.methodNotAllowed(writer, request, http.MethodGet+", "+http.MethodPost)
		}
	case len(segments) == 4 && segments[2] == "resources":
		if request.Method == http.MethodGet {
			handler.resourcePage(writer, request, tenantID, segments[3])
		} else if request.Method == http.MethodPost {
			handler.createResource(writer, request, tenantID, segments[3])
		} else {
			handler.methodNotAllowed(writer, request, http.MethodGet+", "+http.MethodPost)
		}
	case len(segments) == 5 && segments[2] == "resources" && segments[4] == "objects" && request.Method == http.MethodPost:
		handler.uploadBlob(writer, request, tenantID, segments[3])
	case len(segments) == 6 && segments[2] == "resources" && segments[4] == "objects" && segments[5] == "delete" && request.Method == http.MethodPost:
		handler.deleteBlob(writer, request, tenantID, segments[3])
	case len(segments) == 5 && segments[2] == "resources" && segments[4] == "delete" && request.Method == http.MethodPost:
		handler.deleteResource(writer, request, tenantID, segments[3])
	case len(segments) == 5 && segments[2] == "resources" && segments[4] == "tags" && request.Method == http.MethodPost:
		handler.updateTags(writer, request, tenantID, segments[3])
	case len(segments) == 6 && segments[2] == "resources" && segments[4] == "lock" && request.Method == http.MethodPost:
		handler.resourceLockAction(writer, request, tenantID, segments[3], segments[5])
	default:
		handler.renderError(writer, request, http.StatusNotFound, errors.New("tenant page not found"))
	}
}

func (handler *webHandler) tenantPage(writer http.ResponseWriter, request *http.Request, tenantID string) {
	tenant, err := handler.operator.GetTenant(request.Context(), OperatorPrincipal{}, tenantID)
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	tenants, err := handler.operator.ListTenants(request.Context(), OperatorPrincipal{})
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	resources, err := handler.operator.ListResources(request.Context(), OperatorPrincipal{TenantID: tenantID}, persistence.MaxResourceListLimit)
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	inventory, err := handler.tenantResourceInventory(request.Context(), tenantID, resources)
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	scopeOptions := make([]models.Resource, 0)
	for _, resource := range inventory {
		if resource.Spec.Type == models.ResourceTypeGroup {
			scopeOptions = append(scopeOptions, resource)
		}
	}
	selectedScope := strings.TrimSpace(request.URL.Query().Get("scope"))
	if selectedScope != "" {
		validScope := false
		for _, scope := range scopeOptions {
			if scope.ID == selectedScope {
				validScope = true
				break
			}
		}
		if !validScope {
			handler.renderError(writer, request, http.StatusBadRequest, errors.New("selected scope is not a tenant group"))
			return
		}
	}
	activity, err := handler.tenantActivity(request.Context(), tenantID, inventory)
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.render(writer, request, http.StatusOK, webPage{
		View: "tenant", Title: tenant.DisplayName, Tenant: tenant, TenantID: tenantID,
		Tenants: tenants, Resources: resources, ScopeOptions: scopeOptions, SelectedScope: selectedScope,
		TenantActivity: activity, TenantSummary: webTenantSummaryFor(*tenant, inventory),
	})
}

func (handler *webHandler) deleteTenant(writer http.ResponseWriter, request *http.Request, tenantID string) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if !webConfirmed(request) {
		handler.renderError(writer, request, http.StatusConflict, ErrDestructiveConfirmationRequired)
		return
	}
	if err := handler.operator.DeleteTenant(request.Context(), OperatorPrincipal{}, tenantID, true); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.redirect(writer, request, webURL(webBasePath(request), "/"))
}

func (handler *webHandler) platformResource(writer http.ResponseWriter, request *http.Request, segments []string) {
	if len(segments) == 1 {
		if request.Method == http.MethodGet {
			handler.platform(writer, request, []string{""})
		} else if request.Method == http.MethodPost {
			handler.createResource(writer, request, "", "")
		} else {
			handler.methodNotAllowed(writer, request, http.MethodGet+", "+http.MethodPost)
		}
		return
	}
	if len(segments) == 2 {
		if request.Method == http.MethodGet {
			handler.resourcePage(writer, request, "", segments[1])
		} else if request.Method == http.MethodPost {
			handler.createResource(writer, request, "", segments[1])
		} else {
			handler.methodNotAllowed(writer, request, http.MethodGet+", "+http.MethodPost)
		}
		return
	}
	if len(segments) == 3 && segments[2] == "objects" && request.Method == http.MethodPost {
		handler.uploadBlob(writer, request, "", segments[1])
		return
	}
	if len(segments) == 4 && segments[2] == "objects" && segments[3] == "delete" && request.Method == http.MethodPost {
		handler.deleteBlob(writer, request, "", segments[1])
		return
	}
	if len(segments) == 3 && segments[2] == "delete" && request.Method == http.MethodPost {
		handler.deleteResource(writer, request, "", segments[1])
		return
	}
	if len(segments) == 3 && segments[2] == "tags" && request.Method == http.MethodPost {
		handler.updateTags(writer, request, "", segments[1])
		return
	}
	if len(segments) == 4 && segments[2] == "lock" && request.Method == http.MethodPost {
		handler.resourceLockAction(writer, request, "", segments[1], segments[3])
		return
	}
	handler.renderError(writer, request, http.StatusNotFound, errors.New("resource page not found"))
}

func (handler *webHandler) resourcePage(writer http.ResponseWriter, request *http.Request, tenantID, resourceID string) {
	principal := OperatorPrincipal{TenantID: tenantID, ScopeID: request.URL.Query().Get("scope")}
	resource, err := handler.operator.GetResource(request.Context(), principal, resourceID)
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	var objects []models.BlobObject
	var objectBytes int64
	if resource.Spec.Type == models.ResourceTypeBucket {
		bucketPrincipal := OperatorPrincipal{TenantID: tenantID, ScopeID: resource.Spec.ParentID}
		if objectKey := request.URL.Query().Get("object"); objectKey != "" {
			handler.downloadBlob(writer, request, tenantID, resourceID, bucketPrincipal, objectKey)
			return
		}
		response, listErr := handler.operator.ListBlobs(request.Context(), bucketPrincipal, resourceID, persistence.MaxBlobListLimit)
		if listErr != nil {
			handler.renderError(writer, request, webStatus(listErr), listErr)
			return
		}
		objects = response.Objects
		for _, object := range objects {
			objectBytes += object.Size
		}
	}
	lock, lockErr := handler.operator.InspectResourceLock(request.Context(), principal, resourceID)
	if lockErr != nil && !errors.Is(lockErr, persistence.ErrResourceLockNotHeld) {
		handler.renderError(writer, request, webStatus(lockErr), lockErr)
		return
	}
	children, err := handler.operator.ListResources(request.Context(), OperatorPrincipal{TenantID: tenantID, ScopeID: resource.ID}, persistence.MaxResourceListLimit)
	if err != nil && !errors.Is(err, persistence.ErrResourceNotFound) {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	operations, err := handler.operator.ListOperations(request.Context(), principal, resourceID, persistence.MaxOperationListLimit)
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	audit, err := handler.operator.ListAuditHistory(request.Context(), principal, resourceID, persistence.MaxAuditListLimit)
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	var tenants []models.Tenant
	if tenantID != "" {
		tenants, err = handler.operator.ListTenants(request.Context(), OperatorPrincipal{})
		if err != nil {
			handler.renderError(writer, request, webStatus(err), err)
			return
		}
	}
	var tenant *models.Tenant
	if tenantID != "" {
		tenant, err = handler.operator.GetTenant(request.Context(), OperatorPrincipal{}, tenantID)
		if err != nil {
			handler.renderError(writer, request, webStatus(err), err)
			return
		}
	}
	handler.render(writer, request, http.StatusOK, webPage{
		View: "resource", Title: resource.Spec.Name, Tenant: tenant, Tenants: tenants,
		TenantID: tenantID, Resource: resource, Children: children, ParentScope: resource.Spec.ParentID, Lock: lock,
		Operations: operations, Audit: audit, Objects: objects, ObjectBytes: objectBytes,
		Notice: webNotice(request),
	})
}

func (handler *webHandler) createResource(writer http.ResponseWriter, request *http.Request, tenantID, parentID string) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if value := strings.TrimSpace(request.FormValue("parentID")); value != "" {
		parentID = value
	}
	spec, err := webResourceSpec(request, parentID)
	if err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, err)
		return
	}
	principal := OperatorPrincipal{TenantID: tenantID, ScopeID: parentID}
	resource, err := handler.operator.CreateResource(request.Context(), principal, spec)
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	if tenantID == "" {
		handler.redirect(writer, request, webURL(webBasePath(request), "/"))
		return
	}
	if parentID == "" {
		if tenantID == "" {
			handler.redirect(writer, request, webURL(webBasePath(request), "/"))
		} else {
			handler.redirect(writer, request, webTenantURL(webBasePath(request), tenantID))
		}
		return
	}
	handler.redirect(writer, request, webResourceURL(webBasePath(request), tenantID, resource.ID, resourceParentScope(parentID)))
}

func webResourceSpec(request *http.Request, parentID string) (models.ResourceSpec, error) {
	resourceType := models.ResourceType(strings.TrimSpace(request.FormValue("type")))
	if resourceType == "" {
		return models.ResourceSpec{}, errInvalidWebForm
	}
	desiredState := models.ResourceState(strings.TrimSpace(request.FormValue("desiredState")))
	return models.ResourceSpec{
		Type: resourceType, Name: request.FormValue("name"), ParentID: parentID,
		DesiredState: desiredState,
		Provider: models.ProviderMetadata{
			Namespace: request.FormValue("providerNamespace"), Type: request.FormValue("providerType"), Version: request.FormValue("providerVersion"),
		},
	}, nil
}

func (handler *webHandler) deleteResource(writer http.ResponseWriter, request *http.Request, tenantID, resourceID string) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if !webConfirmed(request) {
		handler.renderError(writer, request, http.StatusConflict, ErrDestructiveConfirmationRequired)
		return
	}
	principal := OperatorPrincipal{TenantID: tenantID, ScopeID: request.URL.Query().Get("scope")}
	if err := handler.operator.DeleteResource(request.Context(), principal, resourceID); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	if tenantID == "" {
		handler.redirect(writer, request, webURL(webBasePath(request), "/"))
		return
	}
	handler.redirect(writer, request, webTenantURL(webBasePath(request), tenantID))
}

func (handler *webHandler) updateTags(writer http.ResponseWriter, request *http.Request, tenantID, resourceID string) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	tags, err := webParseTags(request.FormValue("tags"))
	if err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, err)
		return
	}
	principal := OperatorPrincipal{TenantID: tenantID, ScopeID: request.URL.Query().Get("scope")}
	requestID := fmt.Sprintf("web-%d", time.Now().UnixNano())
	if _, err := handler.operator.UpdateResourceTags(request.Context(), principal, resourceID, tags, requestID, requestID); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.redirect(writer, request, webResourceURL(webBasePath(request), tenantID, resourceID, principal.ScopeID))
}

func (handler *webHandler) uploadBlob(writer http.ResponseWriter, request *http.Request, tenantID, bucketID string) {
	if err := request.ParseMultipartForm(1 << 20); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	objectKey := strings.TrimSpace(request.FormValue("objectKey"))
	file, _, err := request.FormFile("contentFile")
	if err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errors.New("an object file is required"))
		return
	}
	defer file.Close()
	content, err := io.ReadAll(io.LimitReader(file, models.MaxBlobObjectSize+1))
	if err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if int64(len(content)) > models.MaxBlobObjectSize {
		handler.renderError(writer, request, http.StatusRequestEntityTooLarge, errors.New("object exceeds the 10 MiB limit"))
		return
	}
	principal := OperatorPrincipal{TenantID: tenantID, ScopeID: request.URL.Query().Get("scope")}
	if _, err := handler.operator.PutBlob(request.Context(), principal, bucketID, objectKey, content); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.redirect(writer, request, webResourceNoticeURL(webBasePath(request), tenantID, bucketID, principal.ScopeID, "uploaded", objectKey))
}

func (handler *webHandler) downloadBlob(writer http.ResponseWriter, request *http.Request, tenantID, bucketID string, principal OperatorPrincipal, objectKey string) {
	response, err := handler.operator.GetBlob(request.Context(), principal, bucketID, objectKey)
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	writer.Header().Set("Content-Type", "application/octet-stream")
	writer.Header().Set("Content-Disposition", "attachment; filename=\"blob\"")
	writer.Header().Set("Cache-Control", "private, no-store")
	writer.Header().Set("Content-Length", strconv.Itoa(len(response.Content)))
	writer.WriteHeader(http.StatusOK)
	_, _ = writer.Write(response.Content)
}

func (handler *webHandler) deleteBlob(writer http.ResponseWriter, request *http.Request, tenantID, bucketID string) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if !webConfirmed(request) {
		handler.renderError(writer, request, http.StatusConflict, ErrDestructiveConfirmationRequired)
		return
	}
	principal := OperatorPrincipal{TenantID: tenantID, ScopeID: request.URL.Query().Get("scope")}
	if err := handler.operator.DeleteBlob(request.Context(), principal, bucketID, request.FormValue("objectKey")); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.redirect(writer, request, webResourceNoticeURL(webBasePath(request), tenantID, bucketID, principal.ScopeID, "deleted", request.FormValue("objectKey")))
}

func webParseTags(value string) (map[string]string, error) {
	tags := make(map[string]string)
	if strings.TrimSpace(value) == "" {
		return tags, nil
	}
	for _, pair := range strings.Split(value, ",") {
		parts := strings.SplitN(pair, "=", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" {
			return nil, errors.New("tags must use key=value pairs separated by commas")
		}
		tags[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
	}
	return tags, nil
}

func webConfirmed(request *http.Request) bool {
	value := request.FormValue("confirm")
	confirmed, err := strconv.ParseBool(value)
	return err == nil && confirmed
}

func webNotice(request *http.Request) string {
	status := request.URL.Query().Get("status")
	objectKey := request.URL.Query().Get("noticeObject")
	switch status {
	case "uploaded":
		return fmt.Sprintf("Object %q uploaded.", objectKey)
	case "deleted":
		return fmt.Sprintf("Object %q deleted.", objectKey)
	case "control":
		return objectKey
	default:
		return ""
	}
}

func (handler *webHandler) render(writer http.ResponseWriter, request *http.Request, status int, page webPage) {
	page.BasePath = webBasePath(request)
	if page.Title == "" {
		page.Title = "Ember management"
	}
	var body bytes.Buffer
	if err := handler.templates.ExecuteTemplate(&body, "page", page); err != nil {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.WriteHeader(http.StatusInternalServerError)
		_, _ = writer.Write([]byte("internal server error\n"))
		return
	}
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(status)
	_, _ = writer.Write(body.Bytes())
}

func (handler *webHandler) renderError(writer http.ResponseWriter, request *http.Request, status int, err error) {
	if err == nil {
		err = errors.New("request failed")
	}
	handler.render(writer, request, status, webPage{Title: "Request error", Error: err.Error()})
}

func (handler *webHandler) redirect(writer http.ResponseWriter, request *http.Request, location string) {
	writer.Header().Set("Location", location)
	writer.WriteHeader(http.StatusSeeOther)
}

func (handler *webHandler) methodNotAllowed(writer http.ResponseWriter, request *http.Request, allowed string) {
	writer.Header().Set("Allow", allowed)
	handler.renderError(writer, request, http.StatusMethodNotAllowed, errors.New("method not allowed"))
}

func webStatus(err error) int {
	if err == nil {
		return http.StatusOK
	}
	if errors.Is(err, ErrOperatorScopeDenied) {
		return http.StatusForbidden
	}
	if errors.Is(err, ErrDestructiveConfirmationRequired) || errors.Is(err, persistence.ErrDuplicateTenant) || errors.Is(err, persistence.ErrDuplicateResource) || errors.Is(err, persistence.ErrResourceHasDependents) {
		return http.StatusConflict
	}
	if errors.Is(err, persistence.ErrTenantNotFound) || errors.Is(err, persistence.ErrResourceNotFound) {
		return http.StatusNotFound
	}
	if errors.Is(err, persistence.ErrInvalidTenant) || errors.Is(err, persistence.ErrInvalidTenantListLimit) || errors.Is(err, persistence.ErrInvalidScope) || errors.Is(err, models.ErrInvalidResourceSpec) || errors.Is(err, models.ErrInvalidResource) || errors.Is(err, ErrOperatorBucketRequired) || errors.Is(err, models.ErrInvalidBlobObjectKey) || errors.Is(err, persistence.ErrBlobObjectTooLarge) || errors.Is(err, persistence.ErrBlobQuotaExceeded) {
		return http.StatusBadRequest
	}
	status := operatorErrorStatus(err)
	if status != http.StatusInternalServerError {
		return status
	}
	return http.StatusInternalServerError
}

func webBasePath(request *http.Request) string {
	if request == nil {
		return ""
	}
	prefix := strings.TrimSpace(request.Header.Get("X-Forwarded-Prefix"))
	if prefix == "" || !strings.HasPrefix(prefix, "/") || strings.Contains(prefix, "//") {
		return ""
	}
	return "/" + strings.Trim(prefix, "/")
}

func webFilterResourceInventory(basePath string, values url.Values, resources []webOverviewResource) webResourceInventory {
	filter := webResourceInventoryFilter{
		Search:   strings.TrimSpace(values.Get("q")),
		Tenant:   strings.TrimSpace(values.Get("tenant")),
		Type:     strings.ToLower(strings.TrimSpace(values.Get("type"))),
		Desired:  strings.ToLower(strings.TrimSpace(values.Get("desired"))),
		Observed: strings.ToLower(strings.TrimSpace(values.Get("observed"))),
		Sort:     strings.ToLower(strings.TrimSpace(values.Get("sort"))),
		Order:    strings.ToLower(strings.TrimSpace(values.Get("order"))),
		Page:     1,
		PageSize: webResourceInventoryDefaultPageSize,
	}
	if filter.Sort != "tenant" && filter.Sort != "parent" && filter.Sort != "type" && filter.Sort != "desired" && filter.Sort != "observed" {
		filter.Sort = "name"
	}
	if filter.Order != "desc" {
		filter.Order = "asc"
	}
	if value, err := strconv.Atoi(values.Get("page")); err == nil && value > 0 {
		filter.Page = value
	}
	if value, err := strconv.Atoi(values.Get("pageSize")); err == nil && value > 0 {
		filter.PageSize = value
	}
	if filter.PageSize > webResourceInventoryMaxPageSize {
		filter.PageSize = webResourceInventoryMaxPageSize
	}

	search := strings.ToLower(filter.Search)
	filtered := make([]webOverviewResource, 0, len(resources))
	for _, row := range resources {
		if filter.Tenant != "" && !strings.EqualFold(filter.Tenant, row.TenantID) {
			continue
		}
		if filter.Type != "" && !strings.EqualFold(filter.Type, string(row.Resource.Spec.Type)) {
			continue
		}
		if filter.Desired != "" && !strings.EqualFold(filter.Desired, string(row.Resource.Spec.DesiredState)) {
			continue
		}
		if filter.Observed != "" && !strings.EqualFold(filter.Observed, string(row.Resource.ObservedState)) {
			continue
		}
		if search != "" {
			name := strings.ToLower(row.Resource.Spec.Name)
			id := strings.ToLower(row.Resource.ID)
			parent := ""
			if row.Parent != nil {
				parent = strings.ToLower(row.Parent.Spec.Name)
			}
			if !strings.Contains(name, search) && !strings.Contains(id, search) && !strings.Contains(parent, search) {
				continue
			}
		}
		filtered = append(filtered, row)
	}

	inventoryValue := func(row webOverviewResource) string {
		switch filter.Sort {
		case "tenant":
			if row.TenantID == "" {
				return "platform"
			}
			return row.TenantID
		case "parent":
			if row.Parent != nil {
				return row.Parent.Spec.Name
			}
			return ""
		case "type":
			return string(row.Resource.Spec.Type)
		case "desired":
			return string(row.Resource.Spec.DesiredState)
		case "observed":
			return string(row.Resource.ObservedState)
		default:
			return row.Resource.Spec.Name
		}
	}
	sort.SliceStable(filtered, func(left, right int) bool {
		leftValue := strings.ToLower(inventoryValue(filtered[left]))
		rightValue := strings.ToLower(inventoryValue(filtered[right]))
		if leftValue == rightValue {
			return filtered[left].Resource.ID < filtered[right].Resource.ID
		}
		if filter.Order == "desc" {
			return leftValue > rightValue
		}
		return leftValue < rightValue
	})

	inventory := webResourceInventory{Total: len(filtered), Filter: filter}
	if inventory.Total > 0 {
		inventory.PageCount = (inventory.Total + filter.PageSize - 1) / filter.PageSize
		if filter.Page > inventory.PageCount {
			filter.Page = inventory.PageCount
			inventory.Filter.Page = filter.Page
		}
		start := (filter.Page - 1) * filter.PageSize
		end := start + filter.PageSize
		if end > inventory.Total {
			end = inventory.Total
		}
		inventory.Rows = filtered[start:end]
		inventory.FirstRow = start + 1
		inventory.LastRow = end
	} else {
		inventory.PageCount = 1
	}
	inventory.Page = filter.Page
	inventory.PageSize = filter.PageSize
	inventory.HasPrevious = filter.Page > 1
	inventory.HasNext = filter.Page < inventory.PageCount
	if inventory.HasPrevious {
		inventory.PreviousURL = webInventoryURL(basePath, filter, filter.Page-1)
	}
	if inventory.HasNext {
		inventory.NextURL = webInventoryURL(basePath, filter, filter.Page+1)
	}
	return inventory
}

func webInventorySortURL(basePath string, inventory webResourceInventory, field string) string {
	filter := inventory.Filter
	if filter.Sort == field {
		if filter.Order == "asc" {
			filter.Order = "desc"
		} else {
			filter.Order = "asc"
		}
	} else {
		filter.Sort = field
		filter.Order = "asc"
	}
	filter.Page = 1
	return webInventoryURL(basePath, filter, 1)
}

func webInventoryURL(basePath string, filter webResourceInventoryFilter, page int) string {
	query := url.Values{}
	if filter.Search != "" {
		query.Set("q", filter.Search)
	}
	if filter.Tenant != "" {
		query.Set("tenant", filter.Tenant)
	}
	if filter.Type != "" {
		query.Set("type", filter.Type)
	}
	if filter.Desired != "" {
		query.Set("desired", filter.Desired)
	}
	if filter.Observed != "" {
		query.Set("observed", filter.Observed)
	}
	query.Set("sort", filter.Sort)
	query.Set("order", filter.Order)
	query.Set("pageSize", strconv.Itoa(filter.PageSize))
	if page > 1 {
		query.Set("page", strconv.Itoa(page))
	}
	return webURL(basePath, "/") + "?" + query.Encode() + "#resources"
}

func webURL(basePath, path string) string {
	if basePath == "" {
		return path
	}
	if path == "/" {
		return basePath + "/"
	}
	return basePath + path
}

func webTenantURL(basePath, tenantID string) string {
	return webURL(basePath, "/tenants/"+url.PathEscape(tenantID))
}

func webResourceURL(basePath, tenantID, resourceID, scope string) string {
	base := webURL(basePath, "/resources/"+url.PathEscape(resourceID))
	if tenantID != "" {
		base = webTenantURL(basePath, tenantID) + "/resources/" + url.PathEscape(resourceID)
	}
	if scope != "" {
		base += "?scope=" + url.QueryEscape(scope)
	}
	return base
}

func webResourceDeleteURL(basePath, tenantID, resourceID, scope string) string {
	base := webResourcePath(basePath, tenantID, resourceID) + "/delete"
	return webWithScope(base, scope)
}

func webResourceTagsURL(basePath, tenantID, resourceID, scope string) string {
	base := webResourcePath(basePath, tenantID, resourceID) + "/tags"
	return webWithScope(base, scope)
}

func webResourceObjectsURL(basePath, tenantID, resourceID, scope string) string {
	base := webResourcePath(basePath, tenantID, resourceID) + "/objects"
	return webWithScope(base, scope)
}

func webResourceDeleteObjectURL(basePath, tenantID, resourceID, scope string) string {
	base := webResourcePath(basePath, tenantID, resourceID) + "/objects/delete"
	return webWithScope(base, scope)
}

func webResourceLockURL(basePath, tenantID, resourceID, scope, action string) string {
	base := webResourcePath(basePath, tenantID, resourceID) + "/lock/" + url.PathEscape(action)
	return webWithScope(base, scope)
}

func webResourceObjectURL(basePath, tenantID, resourceID, scope, objectKey string) string {
	values := url.Values{"object": {objectKey}}
	if scope != "" {
		values.Set("scope", scope)
	}
	return webResourcePath(basePath, tenantID, resourceID) + "?" + values.Encode()
}

func webResourceNoticeURL(basePath, tenantID, resourceID, scope, status, objectKey string) string {
	values := url.Values{"status": {status}, "noticeObject": {objectKey}}
	if scope != "" {
		values.Set("scope", scope)
	}
	return webResourcePath(basePath, tenantID, resourceID) + "?" + values.Encode()
}

func webResourcePath(basePath, tenantID, resourceID string) string {
	if tenantID == "" {
		return webURL(basePath, "/resources/"+url.PathEscape(resourceID))
	}
	return webTenantURL(basePath, tenantID) + "/resources/" + url.PathEscape(resourceID)
}

func webWithScope(base, scope string) string {
	if scope == "" {
		return base
	}
	return base + "?scope=" + url.QueryEscape(scope)
}

func resourceParentScope(parentID string) string {
	return parentID
}

func webResourceTypeLabel(resourceType models.ResourceType) string {
	value := string(resourceType)
	if value == "" {
		return "Resource"
	}
	return strings.ToUpper(value[:1]) + value[1:]
}

func webTagsValue(tags map[string]string) string {
	if len(tags) == 0 {
		return ""
	}
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	for index := 1; index < len(keys); index++ {
		for position := index; position > 0 && keys[position] < keys[position-1]; position-- {
			keys[position], keys[position-1] = keys[position-1], keys[position]
		}
	}
	pairs := make([]string, 0, len(keys))
	for _, key := range keys {
		pairs = append(pairs, key+"="+tags[key])
	}
	return strings.Join(pairs, ",")
}

func webFormatTime(value time.Time) string {
	if value.IsZero() {
		return "-"
	}
	return value.UTC().Format("2006-01-02 15:04:05Z")
}

func webFormatBytes(value int64) string {
	if value < 1024 {
		return fmt.Sprintf("%d B", value)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	amount := float64(value)
	for _, unit := range units {
		amount /= 1024
		if amount < 1024 || unit == units[len(units)-1] {
			return fmt.Sprintf("%.1f %s", amount, unit)
		}
	}
	return fmt.Sprintf("%d B", value)
}
