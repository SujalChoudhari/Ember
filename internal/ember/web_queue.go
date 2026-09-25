package ember

import (
	"net/http"
	"net/url"
	"strings"
)

func controlQueueRedirect(request *http.Request, notice string) string {
	if receiptIndex := strings.Index(notice, "; receipt "); receiptIndex >= 0 {
		notice = notice[:receiptIndex] + "."
	}
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
	handler.receiptMu.Lock()
	if len(handler.pendingReceipts) >= 128 {
		for reference := range handler.pendingReceipts {
			delete(handler.pendingReceipts, reference)
			break
		}
	}
	handler.pendingReceipts[message.ID] = message.Receipt
	handler.receiptMu.Unlock()
	notice := "Queue received " + message.ID + ". Use the protected acknowledgement control below."
	location := controlQueueRedirect(request, notice)
	parsed, _ := url.Parse(location)
	query := parsed.Query()
	query.Set("receiptRef", message.ID)
	parsed.RawQuery = query.Encode()
	handler.redirect(writer, request, parsed.String())
}

func (handler *webHandler) controlQueueAck(writer http.ResponseWriter, request *http.Request) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	receipt := strings.TrimSpace(request.FormValue("receipt"))
	if err := handler.operator.AcknowledgeOperatorMessage(request.Context(), OperatorPrincipal{TenantID: request.FormValue("tenant"), ScopeID: request.FormValue("scope")}, receipt); err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.receiptMu.Lock()
	for reference, pending := range handler.pendingReceipts {
		if pending == receipt {
			delete(handler.pendingReceipts, reference)
		}
	}
	handler.receiptMu.Unlock()
	handler.redirect(writer, request, controlQueueRedirect(request, "Queue message acknowledged."))
}
