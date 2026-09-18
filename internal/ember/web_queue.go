package ember

import (
	"net/http"
	"net/url"
	"strings"
)

func controlQueueRedirect(request *http.Request, notice string) string {
	location, _ := url.Parse(webControlURL(webBasePath(request), request.FormValue("tenant"), request.FormValue("scope"), "queue"))
	query := location.Query()
	query.Set("status", "control")
	query.Set("noticeObject", notice)
	location.RawQuery = query.Encode()
	return location.String()
}

func (handler *webHandler) controlQueueEnqueue(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	correlationID := strings.TrimSpace(request.FormValue("correlationID"))
	payload := []byte(request.FormValue("payload"))
	messageID, err := handler.operator.EnqueueOperatorMessage(request.Context(), OperatorPrincipal{TenantID: request.FormValue("tenant"), ScopeID: request.FormValue("scope")}, correlationID, payload)
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.redirect(writer, request, controlQueueRedirect(request, "Queue message "+messageID+" enqueued."))
}

func (handler *webHandler) controlQueueReceive(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	message, err := handler.operator.ReceiveOperatorMessage(request.Context(), OperatorPrincipal{TenantID: request.FormValue("tenant"), ScopeID: request.FormValue("scope")})
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	notice := "Queue received " + message.ID + "; receipt " + message.Receipt + ". Payload is not shown in the URL."
	handler.redirect(writer, request, controlQueueRedirect(request, notice))
}

func (handler *webHandler) controlQueueAck(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	if err := handler.operator.AcknowledgeOperatorMessage(request.Context(), OperatorPrincipal{TenantID: request.FormValue("tenant"), ScopeID: request.FormValue("scope")}, strings.TrimSpace(request.FormValue("receipt"))); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.redirect(writer, request, controlQueueRedirect(request, "Queue message acknowledged."))
}
