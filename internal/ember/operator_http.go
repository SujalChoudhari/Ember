package ember

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/SujalChoudhari/Ember/internal/ember/deployment"
	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

const maxOperatorJSONBodyBytes int64 = 64 << 10

type operatorHTTPHandler struct {
	operator *Operator
}

func NewHTTPHandler(operator *Operator) http.Handler {
	return &operatorHTTPHandler{operator: operator}
}

type resourceCreateRequest struct {
	Type         models.ResourceType     `json:"type"`
	Name         string                  `json:"name"`
	ParentID     string                  `json:"parentId"`
	Tags         map[string]string       `json:"tags"`
	Provider     models.ProviderMetadata `json:"provider"`
	DesiredState models.ResourceState    `json:"desiredState"`
}

func (request resourceCreateRequest) resourceSpec() models.ResourceSpec {
	return models.ResourceSpec{
		Type:         request.Type,
		Name:         request.Name,
		ParentID:     request.ParentID,
		Tags:         request.Tags,
		Provider:     request.Provider,
		DesiredState: request.DesiredState,
	}
}

type workloadCreateRequest struct {
	Name              string                         `json:"name"`
	Provider          models.ProviderMetadata        `json:"provider"`
	DesiredState      models.ResourceState           `json:"desiredState"`
	WorkloadResources models.WorkloadResources       `json:"workloadResources"`
	SecurityContext   models.WorkloadSecurityContext `json:"securityContext"`
}

func (request workloadCreateRequest) resourceSpec(scopeID string) models.ResourceSpec {
	return models.ResourceSpec{
		Type:              models.ResourceTypeWorkload,
		Name:              request.Name,
		ParentID:          scopeID,
		Provider:          request.Provider,
		DesiredState:      request.DesiredState,
		WorkloadResources: request.WorkloadResources,
		SecurityContext:   request.SecurityContext,
	}
}

type resourceTagsRequest struct {
	Tags          map[string]string `json:"tags"`
	RequestID     string            `json:"requestId"`
	CorrelationID string            `json:"correlationId"`
}

type resourceLockRequest struct {
	Owner string `json:"owner"`
	Token string `json:"token"`
}

type deploymentRequest struct {
	Document      json.RawMessage   `json:"document"`
	Parameters    map[string]string `json:"parameters"`
	RequestID     string            `json:"requestId"`
	CorrelationID string            `json:"correlationId"`
	Confirm       bool              `json:"confirm"`
}

func (handler *operatorHTTPHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if handler.operator == nil {
		writeOperatorError(writer, http.StatusInternalServerError, ErrInvalidOperator)
		return
	}
	if request.URL.Path == "/v1/workloads" {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeOperatorError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		handler.createWorkload(writer, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/v1/workloads/") {
		handler.workloadSubpath(writer, request)
		return
	}
	if request.URL.Path == "/v1/resources" {
		switch request.Method {
		case http.MethodPost:
			handler.createResource(writer, request)
			return
		case http.MethodGet:
			handler.listResources(writer, request)
			return
		default:
			writer.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
			writeOperatorError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
	}
	if request.URL.Path == "/v1/deployments/plan" {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeOperatorError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		handler.planDeployment(writer, request)
		return
	}
	if request.URL.Path == "/v1/deployments/apply" {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeOperatorError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		handler.applyDeployment(writer, request)
		return
	}
	if request.URL.Path == "/v1/operations" && request.Method == http.MethodGet {
		handler.listOperations(writer, request)
		return
	}
	if request.URL.Path == "/v1/reset" {
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeOperatorError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		handler.reset(writer, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/v1/resources/") {
		handler.resourceSubpath(writer, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/v1/operations/") && request.Method == http.MethodGet {
		handler.getOperation(writer, request)
		return
	}
	if request.URL.Path == "/v1/apply-progress" {
		handler.listApplyProgress(writer, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/v1/apply-progress/") && request.Method == http.MethodGet {
		handler.getApplyProgress(writer, request)
		return
	}
	if request.URL.Path == "/v1/recoveries" {
		handler.recoveries(writer, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/v1/recoveries/") && request.Method == http.MethodGet {
		handler.getRecovery(writer, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/v1/buckets/") {
		handler.blob(writer, request)
		return
	}
	http.NotFound(writer, request)
}

func operatorPrincipal(request *http.Request) OperatorPrincipal {
	return OperatorPrincipal{ScopeID: request.Header.Get("X-Ember-Scope")}
}

func decodeOperatorJSON(writer http.ResponseWriter, request *http.Request, destination any) error {
	request.Body = http.MaxBytesReader(writer, request.Body, maxOperatorJSONBodyBytes)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("request body must contain one JSON value")
		}
		return err
	}
	return nil
}

func (handler *operatorHTTPHandler) createResource(writer http.ResponseWriter, request *http.Request) {
	var body resourceCreateRequest
	if err := decodeOperatorJSON(writer, request, &body); err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	resource, err := handler.operator.CreateResource(request.Context(), operatorPrincipal(request), body.resourceSpec())
	if err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	writeOperatorJSON(writer, http.StatusCreated, &OperatorResponse{Resource: resource})
}

func (handler *operatorHTTPHandler) decodeDeploymentRequest(writer http.ResponseWriter, request *http.Request) (deploymentRequest, error) {
	var body deploymentRequest
	if err := decodeOperatorJSON(writer, request, &body); err != nil {
		return deploymentRequest{}, err
	}
	if len(body.Document) == 0 {
		return deploymentRequest{}, deployment.ErrMalformedDocument
	}
	return body, nil
}

func (handler *operatorHTTPHandler) planDeployment(writer http.ResponseWriter, request *http.Request) {
	body, err := handler.decodeDeploymentRequest(writer, request)
	if err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	resolution, plan, err := handler.operator.planDeployment(request.Context(), operatorPrincipal(request), body.Document, body.Parameters)
	if err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	resolved := resolution.Document()
	writeOperatorJSON(writer, http.StatusOK, &OperatorResponse{Plan: &plan, Resolution: &resolved})
}

func (handler *operatorHTTPHandler) applyDeployment(writer http.ResponseWriter, request *http.Request) {
	body, err := handler.decodeDeploymentRequest(writer, request)
	if err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	result, resolution, err := handler.operator.applyDeployment(request.Context(), operatorPrincipal(request), body.Document, body.Parameters, deployment.ApplyOptions{
		RequestID:          body.RequestID,
		CorrelationID:      body.CorrelationID,
		ApproveDestructive: body.Confirm,
	})
	if result == nil {
		if err == nil {
			err = ErrOperatorDeploymentApply
		}
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	if err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	resolved := resolution.Document()
	writeOperatorJSON(writer, http.StatusOK, &OperatorResponse{Apply: result, Resolution: &resolved})
}

func (handler *operatorHTTPHandler) listResources(writer http.ResponseWriter, request *http.Request) {
	limit, err := queryLimit(request, persistence.MaxResourceListLimit)
	if err != nil {
		writeOperatorError(writer, http.StatusBadRequest, err)
		return
	}
	resources, err := handler.operator.ListResources(request.Context(), operatorPrincipal(request), limit)
	if err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	writeOperatorJSON(writer, http.StatusOK, &OperatorResponse{Resources: resources})
}

func (handler *operatorHTTPHandler) createWorkload(writer http.ResponseWriter, request *http.Request) {
	var body workloadCreateRequest
	if err := decodeOperatorJSON(writer, request, &body); err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	workload, err := handler.operator.CreateWorkload(request.Context(), operatorPrincipal(request), body.resourceSpec(request.Header.Get("X-Ember-Scope")))
	if err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	writeOperatorJSON(writer, http.StatusCreated, &OperatorResponse{Workload: workload})
}

func workloadSubpathFromPath(path string) (string, string, error) {
	rest := strings.TrimPrefix(path, "/v1/workloads/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("invalid workload path")
	}
	resourceID, err := url.PathUnescape(parts[0])
	if err != nil || resourceID == "" {
		return "", "", errors.New("invalid workload path")
	}
	return resourceID, parts[1], nil
}

func (handler *operatorHTTPHandler) workloadSubpath(writer http.ResponseWriter, request *http.Request) {
	resourceID, suffix, err := workloadSubpathFromPath(request.URL.Path)
	if err != nil {
		writeOperatorError(writer, http.StatusBadRequest, err)
		return
	}
	principal := operatorPrincipal(request)
	switch suffix {
	case "restart":
		if request.Method != http.MethodPost {
			writer.Header().Set("Allow", http.MethodPost)
			writeOperatorError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		workload, err := handler.operator.RestartWorkload(request.Context(), principal, resourceID)
		if err != nil {
			writeOperatorError(writer, operatorErrorStatus(err), err)
			return
		}
		writeOperatorJSON(writer, http.StatusOK, &OperatorResponse{Workload: workload})
	case "observability":
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			writeOperatorError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		limit, err := queryLimit(request, MaxObservabilityLogLimit)
		if err != nil {
			writeOperatorError(writer, http.StatusBadRequest, err)
			return
		}
		report, err := handler.operator.InspectWorkload(request.Context(), principal, resourceID, limit)
		if err != nil {
			writeOperatorError(writer, operatorErrorStatus(err), err)
			return
		}
		writeOperatorJSON(writer, http.StatusOK, &OperatorResponse{Observability: report})
	default:
		http.NotFound(writer, request)
	}
}

func resourceIDFromPath(path string) (string, error) {
	encoded := strings.TrimPrefix(path, "/v1/resources/")
	if encoded == "" || strings.Contains(encoded, "/") {
		return "", errors.New("invalid resource path")
	}
	resourceID, err := url.PathUnescape(encoded)
	if err != nil || resourceID == "" {
		return "", errors.New("invalid resource path")
	}
	return resourceID, nil
}

func (handler *operatorHTTPHandler) getResource(writer http.ResponseWriter, request *http.Request) {
	resourceID, err := resourceIDFromPath(request.URL.Path)
	if err != nil {
		writeOperatorError(writer, http.StatusBadRequest, err)
		return
	}
	resource, err := handler.operator.GetResource(request.Context(), operatorPrincipal(request), resourceID)
	if err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	writeOperatorJSON(writer, http.StatusOK, &OperatorResponse{Resource: resource})
}

func resourceSubpathFromPath(path string) (string, string, error) {
	rest := strings.TrimPrefix(path, "/v1/resources/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("invalid resource path")
	}
	resourceID, err := url.PathUnescape(parts[0])
	if err != nil || resourceID == "" {
		return "", "", errors.New("invalid resource path")
	}
	return resourceID, parts[1], nil
}

func (handler *operatorHTTPHandler) resourceSubpath(writer http.ResponseWriter, request *http.Request) {
	rest := strings.TrimPrefix(request.URL.Path, "/v1/resources/")
	if !strings.Contains(rest, "/") {
		switch request.Method {
		case http.MethodGet:
			handler.getResource(writer, request)
		case http.MethodDelete:
			handler.deleteResource(writer, request)
		default:
			writer.Header().Set("Allow", http.MethodGet+", "+http.MethodDelete)
			writeOperatorError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
		}
		return
	}
	resourceID, suffix, err := resourceSubpathFromPath(request.URL.Path)
	if err != nil {
		writeOperatorError(writer, http.StatusBadRequest, err)
		return
	}
	switch suffix {
	case "tags":
		if request.Method != http.MethodPatch {
			writer.Header().Set("Allow", http.MethodPatch)
			writeOperatorError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		handler.updateResourceTags(writer, request, resourceID)
	case "audit":
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			writeOperatorError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		handler.listAuditHistory(writer, request, resourceID)
	case "lock":
		handler.resourceLock(writer, request, resourceID)
	default:
		http.NotFound(writer, request)
	}
}

func (handler *operatorHTTPHandler) updateResourceTags(writer http.ResponseWriter, request *http.Request, resourceID string) {
	var body resourceTagsRequest
	if err := decodeOperatorJSON(writer, request, &body); err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	response, err := handler.operator.UpdateResourceTags(request.Context(), operatorPrincipal(request), resourceID, body.Tags, body.RequestID, body.CorrelationID)
	if err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	writeOperatorJSON(writer, http.StatusOK, response)
}

func (handler *operatorHTTPHandler) deleteResource(writer http.ResponseWriter, request *http.Request) {
	if err := requireHTTPConfirmation(request); err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	resourceID, err := resourceIDFromPath(request.URL.Path)
	if err != nil {
		writeOperatorError(writer, http.StatusBadRequest, err)
		return
	}
	if err := handler.operator.DeleteResource(request.Context(), operatorPrincipal(request), resourceID); err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func (handler *operatorHTTPHandler) resourceLock(writer http.ResponseWriter, request *http.Request, resourceID string) {
	principal := operatorPrincipal(request)
	switch request.Method {
	case http.MethodGet:
		lock, err := handler.operator.InspectResourceLock(request.Context(), principal, resourceID)
		if err != nil {
			writeOperatorError(writer, operatorErrorStatus(err), err)
			return
		}
		writeOperatorJSON(writer, http.StatusOK, &OperatorResponse{Lock: lock})
	case http.MethodPut:
		var body resourceLockRequest
		if err := decodeOperatorJSON(writer, request, &body); err != nil {
			writeOperatorError(writer, operatorErrorStatus(err), err)
			return
		}
		lock := models.ResourceLock{Owner: body.Owner, Token: body.Token}
		if err := handler.operator.AcquireResourceLock(request.Context(), principal, resourceID, lock); err != nil {
			writeOperatorError(writer, operatorErrorStatus(err), err)
			return
		}
		writeOperatorJSON(writer, http.StatusOK, &OperatorResponse{Lock: &lock})
	case http.MethodDelete:
		var body resourceLockRequest
		if err := decodeOperatorJSON(writer, request, &body); err != nil {
			writeOperatorError(writer, operatorErrorStatus(err), err)
			return
		}
		if err := handler.operator.ReleaseResourceLock(request.Context(), principal, resourceID, models.ResourceLock{Owner: body.Owner, Token: body.Token}); err != nil {
			writeOperatorError(writer, operatorErrorStatus(err), err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	default:
		writer.Header().Set("Allow", http.MethodGet+", "+http.MethodPut+", "+http.MethodDelete)
		writeOperatorError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func operationIDFromPath(path string) (string, error) {
	encoded := strings.TrimPrefix(path, "/v1/operations/")
	if encoded == "" || strings.Contains(encoded, "/") {
		return "", errors.New("invalid operation path")
	}
	operationID, err := url.PathUnescape(encoded)
	if err != nil || operationID == "" {
		return "", errors.New("invalid operation path")
	}
	return operationID, nil
}

func (handler *operatorHTTPHandler) getOperation(writer http.ResponseWriter, request *http.Request) {
	operationID, err := operationIDFromPath(request.URL.Path)
	if err != nil {
		writeOperatorError(writer, http.StatusBadRequest, err)
		return
	}
	operation, err := handler.operator.GetOperation(request.Context(), operatorPrincipal(request), operationID)
	if err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	writeOperatorJSON(writer, http.StatusOK, &OperatorResponse{Operation: operation})
}

func (handler *operatorHTTPHandler) listOperations(writer http.ResponseWriter, request *http.Request) {
	limit, err := queryLimit(request, persistence.MaxOperationListLimit)
	if err != nil {
		writeOperatorError(writer, http.StatusBadRequest, err)
		return
	}
	operations, err := handler.operator.ListOperations(request.Context(), operatorPrincipal(request), request.URL.Query().Get("resourceId"), limit)
	if err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	writeOperatorJSON(writer, http.StatusOK, &OperatorResponse{Operations: operations})
}

func deploymentRecordIDFromPath(path, prefix, message string) (string, error) {
	encoded := strings.TrimPrefix(path, prefix)
	if encoded == "" || strings.Contains(encoded, "/") {
		return "", errors.New(message)
	}
	recordID, err := url.PathUnescape(encoded)
	if err != nil || recordID == "" {
		return "", errors.New(message)
	}
	return recordID, nil
}

func (handler *operatorHTTPHandler) getApplyProgress(writer http.ResponseWriter, request *http.Request) {
	recordID, err := deploymentRecordIDFromPath(request.URL.Path, "/v1/apply-progress/", "invalid apply progress path")
	if err != nil {
		writeOperatorError(writer, http.StatusBadRequest, err)
		return
	}
	record, err := handler.operator.GetApplyProgress(request.Context(), operatorPrincipal(request), recordID)
	if err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	writeOperatorJSON(writer, http.StatusOK, &OperatorResponse{ApplyProgress: record})
}

func (handler *operatorHTTPHandler) listApplyProgress(writer http.ResponseWriter, request *http.Request) {
	limit, err := queryLimit(request, persistence.MaxApplyProgressListLimit)
	if err != nil {
		writeOperatorError(writer, http.StatusBadRequest, err)
		return
	}
	operationID := request.URL.Query().Get("operationId")
	if operationID != "" {
		record, lookupErr := handler.operator.GetApplyProgressByOperation(request.Context(), operatorPrincipal(request), operationID)
		if lookupErr != nil {
			writeOperatorError(writer, operatorErrorStatus(lookupErr), lookupErr)
			return
		}
		writeOperatorJSON(writer, http.StatusOK, &OperatorResponse{ApplyProgress: record})
		return
	}
	records, err := handler.operator.ListApplyProgress(request.Context(), operatorPrincipal(request), limit)
	if err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	writeOperatorJSON(writer, http.StatusOK, &OperatorResponse{ApplyProgresses: records})
}

type recoveryRequestBody struct {
	RequestID       string                `json:"requestId"`
	ApplyProgressID string                `json:"applyProgressId"`
	Action          models.RecoveryAction `json:"action"`
}

func (handler *operatorHTTPHandler) getRecovery(writer http.ResponseWriter, request *http.Request) {
	recordID, err := deploymentRecordIDFromPath(request.URL.Path, "/v1/recoveries/", "invalid recovery path")
	if err != nil {
		writeOperatorError(writer, http.StatusBadRequest, err)
		return
	}
	record, err := handler.operator.GetRecovery(request.Context(), operatorPrincipal(request), recordID)
	if err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	writeOperatorJSON(writer, http.StatusOK, &OperatorResponse{Recovery: record})
}

func (handler *operatorHTTPHandler) recoveries(writer http.ResponseWriter, request *http.Request) {
	switch request.Method {
	case http.MethodGet:
		limit, err := queryLimit(request, persistence.MaxRecoveryListLimit)
		if err != nil {
			writeOperatorError(writer, http.StatusBadRequest, err)
			return
		}
		records, err := handler.operator.ListRecoveries(request.Context(), operatorPrincipal(request), limit)
		if err != nil {
			writeOperatorError(writer, operatorErrorStatus(err), err)
			return
		}
		writeOperatorJSON(writer, http.StatusOK, &OperatorResponse{Recoveries: records})
	case http.MethodPost:
		var body recoveryRequestBody
		if err := decodeOperatorJSON(writer, request, &body); err != nil {
			writeOperatorError(writer, operatorErrorStatus(err), err)
			return
		}
		response, err := handler.operator.Recover(request.Context(), operatorPrincipal(request), deployment.RecoveryRequest{
			RequestID: body.RequestID, ApplyProgressID: body.ApplyProgressID, Action: body.Action,
		})
		if err != nil {
			writeOperatorError(writer, operatorErrorStatus(err), err)
			return
		}
		writeOperatorJSON(writer, http.StatusOK, response)
	default:
		writer.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeOperatorError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func queryLimit(request *http.Request, defaultLimit int) (int, error) {
	value := request.URL.Query().Get("limit")
	if value == "" {
		if err := validateOperatorListLimit(defaultLimit); err != nil {
			return 0, err
		}
		return defaultLimit, nil
	}
	limit, err := strconv.Atoi(value)
	if err != nil {
		return 0, errors.New("invalid query limit")
	}
	if err := validateOperatorListLimit(limit); err != nil {
		return 0, err
	}
	return limit, nil
}

func (handler *operatorHTTPHandler) listAuditHistory(writer http.ResponseWriter, request *http.Request, resourceID string) {
	limit, err := queryLimit(request, persistence.MaxAuditListLimit)
	if err != nil {
		writeOperatorError(writer, http.StatusBadRequest, err)
		return
	}
	history, err := handler.operator.ListAuditHistory(request.Context(), operatorPrincipal(request), resourceID, limit)
	if err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	writeOperatorJSON(writer, http.StatusOK, &OperatorResponse{Audit: history})
}

func (handler *operatorHTTPHandler) reset(writer http.ResponseWriter, request *http.Request) {
	if err := requireHTTPConfirmation(request); err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	if err := handler.operator.Reset(request.Context(), operatorPrincipal(request)); err != nil {
		writeOperatorError(writer, operatorErrorStatus(err), err)
		return
	}
	writer.WriteHeader(http.StatusNoContent)
}

func blobPathFromPath(path string) (string, string, error) {
	rest := strings.TrimPrefix(path, "/v1/buckets/")
	parts := strings.SplitN(rest, "/objects/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", errors.New("invalid blob path")
	}
	bucketID, err := url.PathUnescape(parts[0])
	if err != nil || bucketID == "" {
		return "", "", errors.New("invalid blob path")
	}
	objectKey, err := url.PathUnescape(parts[1])
	if err != nil || objectKey == "" {
		return "", "", errors.New("invalid blob path")
	}
	return bucketID, objectKey, nil
}

func blobBucketPathFromPath(path string) (string, error) {
	rest := strings.TrimPrefix(path, "/v1/buckets/")
	if !strings.HasSuffix(rest, "/objects") {
		return "", errors.New("invalid blob path")
	}
	encoded := strings.TrimSuffix(rest, "/objects")
	if encoded == "" || strings.Contains(encoded, "/") {
		return "", errors.New("invalid blob path")
	}
	bucketID, err := url.PathUnescape(encoded)
	if err != nil || bucketID == "" {
		return "", errors.New("invalid blob path")
	}
	return bucketID, nil
}

func (handler *operatorHTTPHandler) blob(writer http.ResponseWriter, request *http.Request) {
	if strings.HasSuffix(request.URL.Path, "/objects") {
		bucketID, err := blobBucketPathFromPath(request.URL.Path)
		if err != nil {
			writeOperatorError(writer, http.StatusBadRequest, err)
			return
		}
		if request.Method != http.MethodGet {
			writer.Header().Set("Allow", http.MethodGet)
			writeOperatorError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
			return
		}
		limit, err := queryLimit(request, persistence.MaxBlobListLimit)
		if err != nil {
			writeOperatorError(writer, http.StatusBadRequest, err)
			return
		}
		response, err := handler.operator.ListBlobs(request.Context(), operatorPrincipal(request), bucketID, limit)
		if err != nil {
			writeOperatorError(writer, operatorErrorStatus(err), err)
			return
		}
		writeOperatorJSON(writer, http.StatusOK, response)
		return
	}
	bucketID, objectKey, err := blobPathFromPath(request.URL.Path)
	if err != nil {
		writeOperatorError(writer, http.StatusBadRequest, err)
		return
	}
	switch request.Method {
	case http.MethodPut:
		request.Body = http.MaxBytesReader(writer, request.Body, models.MaxBlobObjectSize+1)
		content, err := io.ReadAll(request.Body)
		if err != nil {
			writeOperatorError(writer, operatorErrorStatus(err), err)
			return
		}
		response, err := handler.operator.PutBlob(request.Context(), operatorPrincipal(request), bucketID, objectKey, content)
		if err != nil {
			writeOperatorError(writer, operatorErrorStatus(err), err)
			return
		}
		writeOperatorJSON(writer, http.StatusCreated, response)
	case http.MethodGet:
		startValue, endValue := request.URL.Query().Get("start"), request.URL.Query().Get("end")
		if (startValue == "") != (endValue == "") {
			writeOperatorError(writer, http.StatusBadRequest, errors.New("both range bounds are required"))
			return
		}
		if startValue != "" {
			start, startErr := strconv.ParseInt(startValue, 10, 64)
			end, endErr := strconv.ParseInt(endValue, 10, 64)
			if startErr != nil || endErr != nil {
				writeOperatorError(writer, http.StatusBadRequest, errors.New("invalid blob range"))
				return
			}
			response, err := handler.operator.ReadBlobRange(request.Context(), operatorPrincipal(request), bucketID, objectKey, start, end)
			if err != nil {
				writeOperatorError(writer, operatorErrorStatus(err), err)
				return
			}
			writeOperatorJSON(writer, http.StatusOK, response)
			return
		}
		response, err := handler.operator.GetBlob(request.Context(), operatorPrincipal(request), bucketID, objectKey)
		if err != nil {
			writeOperatorError(writer, operatorErrorStatus(err), err)
			return
		}
		writeOperatorJSON(writer, http.StatusOK, response)
	case http.MethodDelete:
		if err := requireHTTPConfirmation(request); err != nil {
			writeOperatorError(writer, operatorErrorStatus(err), err)
			return
		}
		if err := handler.operator.DeleteBlob(request.Context(), operatorPrincipal(request), bucketID, objectKey); err != nil {
			writeOperatorError(writer, operatorErrorStatus(err), err)
			return
		}
		writer.WriteHeader(http.StatusNoContent)
	default:
		writer.Header().Set("Allow", http.MethodDelete+", "+http.MethodGet+", "+http.MethodPut)
		writeOperatorError(writer, http.StatusMethodNotAllowed, errors.New("method not allowed"))
	}
}

func requireHTTPConfirmation(request *http.Request) error {
	value := request.URL.Query().Get("confirm")
	confirmed, err := strconv.ParseBool(value)
	if err != nil || !confirmed {
		return ErrDestructiveConfirmationRequired
	}
	return nil
}

func operatorErrorStatus(err error) int {
	if errors.Is(err, ErrOperatorScopeDenied) || errors.Is(err, ErrWorkloadScopeDenied) || errors.Is(err, ErrWorkloadPrivilegeDenied) {
		return http.StatusForbidden
	}
	if errors.Is(err, persistence.ErrResourceNotFound) || errors.Is(err, persistence.ErrOperationNotFound) || errors.Is(err, persistence.ErrBlobObjectNotFound) || errors.Is(err, persistence.ErrApplyProgressNotFound) || errors.Is(err, persistence.ErrRecoveryNotFound) || errors.Is(err, ErrWorkloadNotFound) || errors.Is(err, ErrWorkloadProviderNotFound) {
		return http.StatusNotFound
	}
	if errors.Is(err, persistence.ErrResourceLockConflict) || errors.Is(err, persistence.ErrResourceLockNotHeld) || errors.Is(err, persistence.ErrResourceLockNotOwner) || errors.Is(err, persistence.ErrResourceLocked) || errors.Is(err, persistence.ErrResourceHasDependents) || errors.Is(err, persistence.ErrOperationRequestConflict) || errors.Is(err, persistence.ErrRecoveryRequestConflict) || errors.Is(err, deployment.ErrDestructiveApprovalRequired) || errors.Is(err, ErrDestructiveConfirmationRequired) {
		return http.StatusConflict
	}
	if errors.Is(err, ErrInvalidOperator) || errors.Is(err, ErrInvalidOperatorPrincipal) || errors.Is(err, ErrOperatorBucketRequired) || errors.Is(err, ErrInvalidOperatorListLimit) || errors.Is(err, ErrInvalidWorkloadSpec) || errors.Is(err, ErrInvalidWorkloadLogLimit) || errors.Is(err, ErrInvalidObservabilityLogLimit) || errors.Is(err, ErrWorkloadResourceLimit) || errors.Is(err, models.ErrInvalidResourceSpec) || errors.Is(err, models.ErrInvalidResource) || errors.Is(err, models.ErrInvalidResourceLock) || errors.Is(err, models.ErrInvalidBlobObject) || errors.Is(err, models.ErrInvalidBlobBucketID) || errors.Is(err, models.ErrInvalidBlobObjectKey) || errors.Is(err, persistence.ErrInvalidScope) || errors.Is(err, persistence.ErrInvalidResourceListLimit) || errors.Is(err, persistence.ErrInvalidOperationListLimit) || errors.Is(err, persistence.ErrInvalidAuditListLimit) || errors.Is(err, persistence.ErrInvalidBlobRange) || errors.Is(err, persistence.ErrInvalidBlobListLimit) || errors.Is(err, persistence.ErrInvalidApplyProgressListLimit) || errors.Is(err, persistence.ErrInvalidRecoveryListLimit) || errors.Is(err, deployment.ErrMalformedDocument) || errors.Is(err, deployment.ErrDocumentTooLarge) || errors.Is(err, deployment.ErrInvalidDocument) || errors.Is(err, deployment.ErrInvalidResolution) || errors.Is(err, deployment.ErrInvalidApplyRequest) || errors.Is(err, deployment.ErrApplyDependency) || errors.Is(err, deployment.ErrInvalidRecoveryRequest) || errors.Is(err, deployment.ErrUnsupportedRecoveryFailure) || errors.Is(err, deployment.ErrRecoveryApplyNotReady) {
		return http.StatusBadRequest
	}
	if errors.Is(err, persistence.ErrBlobObjectTooLarge) || errors.Is(err, persistence.ErrBlobQuotaExceeded) {
		return http.StatusRequestEntityTooLarge
	}
	var maxBytesError *http.MaxBytesError
	if errors.As(err, &maxBytesError) {
		return http.StatusRequestEntityTooLarge
	}
	if errors.Is(err, io.EOF) || strings.HasPrefix(err.Error(), "json:") || err.Error() == "request body must contain one JSON value" || err.Error() == "both range bounds are required" || err.Error() == "invalid blob range" {
		return http.StatusBadRequest
	}
	if err.Error() == "method not allowed" {
		return http.StatusMethodNotAllowed
	}
	return http.StatusInternalServerError
}

func writeOperatorJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func writeOperatorError(writer http.ResponseWriter, status int, err error) {
	writeOperatorJSON(writer, status, map[string]string{"error": err.Error()})
}
