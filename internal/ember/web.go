package ember

import (
	"bytes"
	"context"
	"embed"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

//go:embed web_assets.html web_assets.css
var webAssets embed.FS

var (
	errInvalidWebAddress = errors.New("web server address must be loopback-only")
	errInvalidWebForm    = errors.New("invalid web form")
)

type webHandler struct {
	operator  *Operator
	templates *template.Template
}

type webPage struct {
	View        string
	Title       string
	Tenant      *models.Tenant
	Tenants     []models.Tenant
	TenantID    string
	Resources   []models.Resource
	Resource    *models.Resource
	Children    []models.Resource
	ParentScope string
	Operations  []models.Operation
	Audit       []models.AuditEntry
	Error       string
}

func NewWebHandler(operator *Operator) http.Handler {
	return &webHandler{
		operator: operator,
		templates: template.Must(template.New("page").Funcs(template.FuncMap{
			"resourceURL":       webResourceURL,
			"resourceDeleteURL": webResourceDeleteURL,
			"resourceTagsURL":   webResourceTagsURL,
			"tenantURL":         webTenantURL,
			"tagsValue":         webTagsValue,
			"formatTime":        webFormatTime,
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
	handler.render(writer, request, http.StatusOK, webPage{
		View: "platform", Title: "Platform management", Tenants: tenants,
		Resources: resources,
	})
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
	handler.redirect(writer, webTenantURL(tenant.ID))
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
	case len(segments) == 5 && segments[2] == "resources" && segments[4] == "delete" && request.Method == http.MethodPost:
		handler.deleteResource(writer, request, tenantID, segments[3])
	case len(segments) == 5 && segments[2] == "resources" && segments[4] == "tags" && request.Method == http.MethodPost:
		handler.updateTags(writer, request, tenantID, segments[3])
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
	handler.render(writer, request, http.StatusOK, webPage{
		View: "tenant", Title: tenant.DisplayName, Tenant: tenant, TenantID: tenantID,
		Tenants: tenants, Resources: resources,
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
	handler.redirect(writer, "/")
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
	if len(segments) == 3 && segments[2] == "delete" && request.Method == http.MethodPost {
		handler.deleteResource(writer, request, "", segments[1])
		return
	}
	if len(segments) == 3 && segments[2] == "tags" && request.Method == http.MethodPost {
		handler.updateTags(writer, request, "", segments[1])
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
		TenantID: tenantID, Resource: resource, Children: children, ParentScope: principal.ScopeID,
		Operations: operations, Audit: audit,
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
		handler.redirect(writer, "/")
		return
	}
	if parentID == "" {
		if tenantID == "" {
			handler.redirect(writer, "/")
		} else {
			handler.redirect(writer, webTenantURL(tenantID))
		}
		return
	}
	handler.redirect(writer, webResourceURL(tenantID, resource.ID, resourceParentScope(parentID)))
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
		handler.redirect(writer, "/")
		return
	}
	handler.redirect(writer, webTenantURL(tenantID))
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
	handler.redirect(writer, webResourceURL(tenantID, resourceID, principal.ScopeID))
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

func (handler *webHandler) render(writer http.ResponseWriter, request *http.Request, status int, page webPage) {
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

func (handler *webHandler) redirect(writer http.ResponseWriter, location string) {
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
	if errors.Is(err, persistence.ErrInvalidTenant) || errors.Is(err, persistence.ErrInvalidTenantListLimit) || errors.Is(err, persistence.ErrInvalidScope) || errors.Is(err, models.ErrInvalidResourceSpec) || errors.Is(err, models.ErrInvalidResource) {
		return http.StatusBadRequest
	}
	status := operatorErrorStatus(err)
	if status != http.StatusInternalServerError {
		return status
	}
	return http.StatusInternalServerError
}

func webTenantURL(tenantID string) string {
	return "/tenants/" + url.PathEscape(tenantID)
}

func webResourceURL(tenantID, resourceID, scope string) string {
	base := "/resources/" + url.PathEscape(resourceID)
	if tenantID != "" {
		base = webTenantURL(tenantID) + "/resources/" + url.PathEscape(resourceID)
	}
	if scope != "" {
		base += "?scope=" + url.QueryEscape(scope)
	}
	return base
}

func webResourceDeleteURL(tenantID, resourceID, scope string) string {
	base := webResourcePath(tenantID, resourceID) + "/delete"
	return webWithScope(base, scope)
}

func webResourceTagsURL(tenantID, resourceID, scope string) string {
	base := webResourcePath(tenantID, resourceID) + "/tags"
	return webWithScope(base, scope)
}

func webResourcePath(tenantID, resourceID string) string {
	if tenantID == "" {
		return "/resources/" + url.PathEscape(resourceID)
	}
	return webTenantURL(tenantID) + "/resources/" + url.PathEscape(resourceID)
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
