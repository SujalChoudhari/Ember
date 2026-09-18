package ember

import (
	"errors"
	"net/http"
	"strings"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func (handler *webHandler) resourceLockAction(writer http.ResponseWriter, request *http.Request, tenantID, resourceID, action string) {
	if err := request.ParseForm(); err != nil {
		handler.renderError(writer, request, http.StatusBadRequest, errInvalidWebForm)
		return
	}
	principal := OperatorPrincipal{TenantID: tenantID, ScopeID: request.FormValue("scope")}
	lock := models.ResourceLock{Owner: strings.TrimSpace(request.FormValue("owner")), Token: strings.TrimSpace(request.FormValue("token"))}
	var err error
	switch action {
	case "acquire":
		err = handler.operator.AcquireResourceLock(request.Context(), principal, resourceID, lock)
	case "release":
		err = handler.operator.ReleaseResourceLock(request.Context(), principal, resourceID, lock)
	default:
		err = errors.New("unknown resource lock action")
	}
	if err != nil {
		handler.renderError(writer, request, webStatus(err), err)
		return
	}
	handler.redirect(writer, request, webResourceURL(webBasePath(request), tenantID, resourceID, principal.ScopeID))
}
