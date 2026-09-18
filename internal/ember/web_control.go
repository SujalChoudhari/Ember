package ember

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/deployment"
	"github.com/SujalChoudhari/Ember/internal/ember/events"
	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
	"github.com/SujalChoudhari/Ember/internal/ember/queue"
)

func webControlURL(basePath, tenantID, scope, section string) string {
	values := url.Values{}
	if tenantID != "" {
		values.Set("tenant", tenantID)
	}
	if scope != "" {
		values.Set("scope", scope)
	}
	if section != "" {
		values.Set("section", section)
	}
	base := webURL(basePath, "/control")
	if encoded := values.Encode(); encoded != "" {
		return base + "?" + encoded
	}
	return base
}

func (handler *webHandler) control(writer http.ResponseWriter, request *http.Request, segments []string) {
	if len(segments) == 1 && request.Method == http.MethodGet {
		handler.controlPage(writer, request)
		return
	}
	if request.Method != http.MethodPost || len(segments) != 2 {
		handler.methodNotAllowed(writer, request, http.MethodGet+", "+http.MethodPost)
		return
	}
	switch segments[1] {
	case "networks":
		handler.controlCreateNetwork(writer, request)
	case "ports":
		handler.controlAllocatePort(writer, request)
	case "endpoints":
		handler.controlPublishEndpoint(writer, request)
	case "network-delete":
		handler.controlDeleteNetwork(writer, request)
	case "port-delete":
		handler.controlDeletePort(writer, request)
	case "endpoint-delete":
		handler.controlDeleteEndpoint(writer, request)
	case "topics":
		handler.controlCreateTopic(writer, request)
	case "topic-delete":
		handler.controlDeleteTopic(writer, request)
	case "subscriptions":
		handler.controlCreateSubscription(writer, request)
	case "subscription-delete":
		handler.controlDeleteSubscription(writer, request)
	case "publish":
		handler.controlPublishEvent(writer, request)
	case "workload-restart":
		handler.controlRestartWorkload(writer, request)
	case "volume":
		handler.controlAttachVolume(writer, request)
	case "volume-cleanup":
		handler.controlCleanupVolumes(writer, request)
	case "recover":
		handler.controlRecover(writer, request)
	case "deployment-apply":
		handler.controlDeploymentApply(writer, request)
	case "queue-enqueue":
		handler.controlQueueEnqueue(writer, request)
	case "queue-receive":
		handler.controlQueueReceive(writer, request)
	case "queue-ack":
		handler.controlQueueAck(writer, request)
	default:
		handler.renderError(writer, request, http.StatusNotFound, errors.New("control action not found"))
	}
}

func (handler *webHandler) controlPage(writer http.ResponseWriter, request *http.Request) {
	ctx := request.Context()
	tenantID := strings.TrimSpace(request.URL.Query().Get("tenant"))
	selectedScope := strings.TrimSpace(request.URL.Query().Get("scope"))
	principal := OperatorPrincipal{TenantID: tenantID, ScopeID: selectedScope}
	tenants, err := handler.operator.ListTenants(ctx, OperatorPrincipal{})
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	rootResources, err := handler.operator.ListResources(ctx, OperatorPrincipal{TenantID: tenantID}, persistence.MaxResourceListLimit)
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	scopeOptions := make([]models.Resource, 0)
	for _, resource := range rootResources {
		if resource.Spec.Type == models.ResourceTypeGroup {
			scopeOptions = append(scopeOptions, resource)
		}
	}
	resources := rootResources
	if selectedScope != "" {
		resources, err = handler.operator.ListResources(ctx, principal, persistence.MaxResourceListLimit)
		if err != nil && !errors.Is(err, persistence.ErrResourceNotFound) {
			handler.renderError(writer, request, webStatus(err), err)
			return
		}
	}
	workloads := make([]webWorkload, 0)
	for index := range resources {
		resource := resources[index]
		if resource.Spec.Type != models.ResourceTypeWorkload || selectedScope == "" {
			continue
		}
		view, inspectErr := handler.operator.InspectWorkload(ctx, principal, resource.ID, 10)
		if inspectErr != nil {
			continue
		}
		volumes, _ := handler.operator.ListWorkloadVolumes(ctx, principal, resource.ID, MaxWorkloadVolumeRecords)
		workloads = append(workloads, webWorkload{View: &WorkloadView{Resource: resource, Status: view.Status}, Logs: view.Logs, Volumes: volumes})
	}
	var networks []models.Network
	var ports []models.NetworkPort
	var endpoints []models.NetworkEndpoint
	var topics []events.Topic
	var subscriptions []events.Subscription
	var deadLetters []queue.DeadLetterRecord
	if selectedScope != "" {
		networks, err = handler.operator.ListNetworks(ctx, principal, persistence.MaxNetworkListLimit)
		if err != nil && !errors.Is(err, ErrOperatorNetworkUnavailable) && !errors.Is(err, persistence.ErrNetworkNotFound) {
			handler.renderError(writer, request, webStatus(err), err)
			return
		}
		for _, network := range networks {
			listedPorts, portErr := handler.operator.ListNetworkPorts(ctx, principal, network.ID, persistence.MaxNetworkListLimit)
			if portErr == nil {
				ports = append(ports, listedPorts...)
			}
			listedEndpoints, endpointErr := handler.operator.ListNetworkEndpoints(ctx, principal, network.ID, persistence.MaxNetworkListLimit)
			if endpointErr == nil {
				endpoints = append(endpoints, listedEndpoints...)
			}
		}
		topics, err = handler.operator.ListEventTopics(ctx, principal, events.MaxTopicCount)
		if err != nil && !errors.Is(err, ErrOperatorEventsUnavailable) {
			handler.renderError(writer, request, webStatus(err), err)
			return
		}
		subscriptions, err = handler.operator.ListEventSubscriptions(ctx, principal, events.MaxSubscriptionCount)
		if err != nil && !errors.Is(err, ErrOperatorEventsUnavailable) {
			handler.renderError(writer, request, webStatus(err), err)
			return
		}
		deadLetters, err = handler.operator.ListEventDeadLetters(ctx, principal, queue.MaxDeadLetterListLimit)
		if err != nil && !errors.Is(err, ErrOperatorEventsUnavailable) {
			handler.renderError(writer, request, webStatus(err), err)
			return
		}
	}
	operations, err := handler.operator.ListOperations(ctx, principal, "", persistence.MaxOperationListLimit)
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	audit := make([]models.AuditEntry, 0)
	for _, resource := range resources {
		entries, auditErr := handler.operator.ListAuditHistory(ctx, principal, resource.ID, persistence.MaxAuditListLimit)
		if auditErr == nil {
			audit = append(audit, entries...)
		}
	}
	progress, progressErr := handler.operator.ListApplyProgress(ctx, principal, persistence.MaxApplyProgressListLimit)
	if progressErr != nil && !errors.Is(progressErr, ErrOperatorDeploymentUnavailable) {
		handler.renderError(writer, request, webStatus(progressErr), progressErr)
		return
	}
	recoveries, recoveryErr := handler.operator.ListRecoveries(ctx, principal, persistence.MaxRecoveryListLimit)
	if recoveryErr != nil && !errors.Is(recoveryErr, ErrOperatorDeploymentUnavailable) {
		handler.renderError(writer, request, webStatus(recoveryErr), recoveryErr)
		return
	}
	metrics, metricsErr := handler.operator.EventMetrics()
	if metricsErr != nil {
		handler.renderError(writer, request, webStatus(metricsErr), metricsErr)
		return
	}
	var tenant *models.Tenant
	if tenantID != "" {
		tenant, err = handler.operator.GetTenant(ctx, OperatorPrincipal{}, tenantID)
		if err != nil {
			handler.renderError(writer, request, webStatus(err), err)
			return
		}
	}
	handler.render(writer, request, http.StatusOK, webPage{
		View: "control", Section: request.URL.Query().Get("section"), Title: "Control center",
		Tenant: tenant, Tenants: tenants, TenantID: tenantID, SelectedScope: selectedScope,
		Resources: resources, Workloads: workloads, Networks: networks, Ports: ports, Endpoints: endpoints,
		Operations: operations, Audit: audit, ApplyProgress: progress, Recoveries: recoveries,
		Topics: topics, Subscriptions: subscriptions, DeadLetters: deadLetters,
		Metrics: metrics, QueueStatus: handler.operator.QueueRuntimeStatus(), EventRuntime: handler.operator.EventRuntimeStatus(), Notice: webNotice(request),
	})
}

func (handler *webHandler) controlPrincipal(request *http.Request) OperatorPrincipal {
	return OperatorPrincipal{TenantID: strings.TrimSpace(request.FormValue("tenant")), ScopeID: strings.TrimSpace(request.FormValue("scope"))}
}

func (handler *webHandler) controlRedirect(writer http.ResponseWriter, request *http.Request, status, detail string) {
	values := url.Values{"tenant": {request.FormValue("tenant")}, "scope": {request.FormValue("scope")}, "section": {request.FormValue("section")}, "status": {status}, "noticeObject": {detail}}
	handler.redirect(writer, request, webURL(webBasePath(request), "/control")+"?"+values.Encode())
}

func (handler *webHandler) controlCreateNetwork(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if _, err := handler.operator.CreateNetwork(request.Context(), handler.controlPrincipal(request), request.FormValue("name")); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.controlRedirect(writer, request, "control", "Network created.")
}

func (handler *webHandler) controlAllocatePort(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	number, err := strconv.ParseUint(request.FormValue("number"), 10, 16)
	if err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if _, err = handler.operator.AllocateNetworkPort(request.Context(), handler.controlPrincipal(request), request.FormValue("networkID"), request.FormValue("workloadID"), uint16(number), models.NetworkProtocol(request.FormValue("protocol"))); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.controlRedirect(writer, request, "control", "Network port allocated.")
}

func (handler *webHandler) controlPublishEndpoint(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if _, err := handler.operator.PublishNetworkEndpoint(request.Context(), handler.controlPrincipal(request), request.FormValue("networkID"), request.FormValue("portID"), request.FormValue("name")); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.controlRedirect(writer, request, "control", "Network endpoint published.")
}

func (handler *webHandler) controlDeleteNetwork(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if !webConfirmed(request) {
		handler.renderError(writer, request, http.StatusConflict, ErrDestructiveConfirmationRequired)
		return
	}
	if err := handler.operator.DeleteNetwork(request.Context(), handler.controlPrincipal(request), request.FormValue("networkID")); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.controlRedirect(writer, request, "control", "Network deleted.")
}

func (handler *webHandler) controlDeletePort(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if !webConfirmed(request) {
		handler.renderError(writer, request, http.StatusConflict, ErrDestructiveConfirmationRequired)
		return
	}
	if err := handler.operator.DeleteNetworkPort(request.Context(), handler.controlPrincipal(request), request.FormValue("portID")); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.controlRedirect(writer, request, "control", "Network port deleted.")
}

func (handler *webHandler) controlDeleteEndpoint(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if !webConfirmed(request) {
		handler.renderError(writer, request, http.StatusConflict, ErrDestructiveConfirmationRequired)
		return
	}
	if err := handler.operator.DeleteNetworkEndpoint(request.Context(), handler.controlPrincipal(request), request.FormValue("endpointID")); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.controlRedirect(writer, request, "control", "Network endpoint deleted.")
}

func (handler *webHandler) controlDeleteTopic(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if !webConfirmed(request) {
		handler.renderError(writer, request, http.StatusConflict, ErrDestructiveConfirmationRequired)
		return
	}
	if err := handler.operator.DeleteEventTopic(request.Context(), handler.controlPrincipal(request), request.FormValue("topicID")); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.controlRedirect(writer, request, "control", "Topic deleted.")
}

func (handler *webHandler) controlDeleteSubscription(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if !webConfirmed(request) {
		handler.renderError(writer, request, http.StatusConflict, ErrDestructiveConfirmationRequired)
		return
	}
	if err := handler.operator.DeleteEventSubscription(request.Context(), handler.controlPrincipal(request), request.FormValue("subscriptionID")); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.controlRedirect(writer, request, "control", "Subscription deleted.")
}
func (handler *webHandler) controlCreateTopic(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if _, err := handler.operator.CreateEventTopic(request.Context(), handler.controlPrincipal(request), request.FormValue("owner"), request.FormValue("name")); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.controlRedirect(writer, request, "control", "Topic created.")
}

func (handler *webHandler) controlCreateSubscription(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if _, err := handler.operator.CreateEventSubscription(request.Context(), handler.controlPrincipal(request), request.FormValue("owner"), request.FormValue("topicID"), request.FormValue("name"), request.FormValue("eventType"), request.FormValue("correlationID")); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.controlRedirect(writer, request, "control", "Subscription created.")
}

func (handler *webHandler) controlPublishEvent(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	retries, err := strconv.Atoi(request.FormValue("retries"))
	if err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	_, err = handler.operator.PublishEvent(request.Context(), handler.controlPrincipal(request), request.FormValue("owner"), request.FormValue("topicID"), events.Event{ID: request.FormValue("eventID"), CorrelationID: request.FormValue("correlationID"), Type: request.FormValue("eventType"), Payload: []byte(request.FormValue("payload"))}, retries)
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.controlRedirect(writer, request, "control", "Event probe delivered.")
}

func (handler *webHandler) controlRestartWorkload(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if _, err := handler.operator.RestartWorkload(request.Context(), handler.controlPrincipal(request), request.FormValue("resourceID")); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.controlRedirect(writer, request, "control", "Workload restarted.")
}

func (handler *webHandler) controlAttachVolume(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	maxBytes, err := strconv.ParseInt(request.FormValue("maxBytes"), 10, 64)
	if err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if _, err := handler.operator.AttachWorkloadVolume(request.Context(), handler.controlPrincipal(request), request.FormValue("resourceID"), request.FormValue("name"), maxBytes); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.controlRedirect(writer, request, "control", "Workload volume attached.")
}

func (handler *webHandler) controlCleanupVolumes(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if !webConfirmed(request) {
		handler.renderError(writer, request, http.StatusConflict, ErrDestructiveConfirmationRequired)
		return
	}
	if err := handler.operator.CleanupWorkloadVolumes(request.Context(), handler.controlPrincipal(request), request.FormValue("resourceID")); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.controlRedirect(writer, request, "control", "Workload volumes cleaned up.")
}

func (handler *webHandler) controlDeploymentApply(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if !webConfirmed(request) {
		handler.renderError(writer, request, http.StatusConflict, deployment.ErrDestructiveApprovalRequired)
		return
	}
	requestID := strings.TrimSpace(request.FormValue("requestID"))
	if requestID == "" {
		requestID = fmt.Sprintf("web-deployment-%d", time.Now().UnixNano())
	}
	correlationID := strings.TrimSpace(request.FormValue("correlationID"))
	if correlationID == "" {
		correlationID = requestID
	}
	_, _, err := handler.operator.ApplyDeployment(request.Context(), handler.controlPrincipal(request), []byte(request.FormValue("document")), webParameters(request.FormValue("parameters")), deployment.ApplyOptions{RequestID: requestID, CorrelationID: correlationID, ApproveDestructive: true})
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.controlRedirect(writer, request, "control", "Deployment applied; inspect apply progress below.")
}

func webParameters(value string) map[string]string {
	parameters := make(map[string]string)
	for _, line := range strings.Split(value, "\\n") {
		parts := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(parts) == 2 && strings.TrimSpace(parts[0]) != "" {
			parameters[strings.TrimSpace(parts[0])] = strings.TrimSpace(parts[1])
		}
	}
	return parameters
}
func (handler *webHandler) controlRecover(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	action := models.RecoveryAction(request.FormValue("action"))
	_, err := handler.operator.Recover(request.Context(), handler.controlPrincipal(request), deployment.RecoveryRequest{RequestID: fmt.Sprintf("web-recovery-%d", time.Now().UnixNano()), ApplyProgressID: request.FormValue("applyProgressID"), Action: action})
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.controlRedirect(writer, request, "control", "Recovery requested.")
}
