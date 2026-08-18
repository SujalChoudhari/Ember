package ember

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type Server struct {
	Store ControlPlane
	Auth  Auth
}

func NewServer(store ControlPlane, auth Auth) *Server { return &Server{Store: store, Auth: auth} }

func (server *Server) Handler() http.Handler { return http.HandlerFunc(server.serveHTTP) }

func requestIdentifiers(request *http.Request) (string, string) {
	requestID := request.Header.Get("X-Request-ID")
	if requestID == "" {
		requestID = generateRandomID("req")
	}
	correlationID := request.Header.Get("X-Correlation-ID")
	if correlationID == "" {
		correlationID = requestID
	}
	return requestID, correlationID
}

func (server *Server) principal(responseWriter http.ResponseWriter, request *http.Request, correlationID string) (Principal, bool) {
	principal, err := server.Auth.Authenticate(request.Header.Get("Authorization"))
	if err != nil {
		writeAPIError(responseWriter, apiErr(err, correlationID))
		return Principal{}, false
	}
	return principal, true
}

func writeJSON(responseWriter http.ResponseWriter, statusCode int, value any) {
	responseWriter.Header().Set("Content-Type", "application/json")
	responseWriter.WriteHeader(statusCode)
	_ = json.NewEncoder(responseWriter).Encode(value)
}

func writeAPIError(responseWriter http.ResponseWriter, apiError *APIError) {
	if apiError.Status == 0 {
		apiError.Status = 500
	}
	writeJSON(responseWriter, apiError.Status, map[string]any{"error": apiError})
}

func operationResponse(operation *Operation, statusCode int) map[string]any {
	return map[string]any{
		"operation":     operation,
		"statusUrl":     "/api/v1/operations/" + operation.ID,
		"requestId":     operation.RequestID,
		"correlationId": operation.CorrelationID,
		"status":        statusCode,
	}
}

func (server *Server) serveHTTP(responseWriter http.ResponseWriter, request *http.Request) {
	requestID, correlationID := requestIdentifiers(request)
	responseWriter.Header().Set("X-Request-ID", requestID)
	responseWriter.Header().Set("X-Correlation-ID", correlationID)
	if request.URL.Path == "/healthz" {
		writeJSON(responseWriter, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	if !strings.HasPrefix(request.URL.Path, "/api/v1/") {
		writeAPIError(responseWriter, apiErr(ErrNotFound, correlationID))
		return
	}
	principal, authenticated := server.principal(responseWriter, request, correlationID)
	if !authenticated {
		return
	}
	relativePath := strings.TrimPrefix(request.URL.Path, "/api/v1/")
	pathParts := strings.Split(strings.Trim(relativePath, "/"), "/")
	if len(pathParts) >= 2 && pathParts[0] == "operations" && request.Method == http.MethodGet {
		operation, err := server.Store.GetOperation(principal, pathParts[1], requestID, correlationID)
		if err != nil {
			writeAPIError(responseWriter, apiErr(err, correlationID))
			return
		}
		writeJSON(responseWriter, http.StatusOK, operation)
		return
	}
	if len(pathParts) >= 2 && pathParts[0] == "resources" {
		if len(pathParts) == 2 && request.Method == http.MethodGet {
			resource, err := server.Store.GetResource(principal, pathParts[1], requestID, correlationID)
			if err != nil {
				writeAPIError(responseWriter, apiErr(err, correlationID))
				return
			}
			writeJSON(responseWriter, http.StatusOK, resource)
			return
		}
		if len(pathParts) == 3 && pathParts[2] == "locks" && request.Method == http.MethodPost {
			var lockRequest struct {
				Kind string
				Note string
			}
			if json.NewDecoder(request.Body).Decode(&lockRequest) != nil {
				writeAPIError(responseWriter, apiErr(ErrInvalidRequest, correlationID))
				return
			}
			if err := server.Store.AddLock(principal, pathParts[1], lockRequest.Kind, lockRequest.Note, requestID, correlationID); err != nil {
				writeAPIError(responseWriter, apiErr(err, correlationID))
				return
			}
			writeJSON(responseWriter, http.StatusCreated, map[string]string{"status": "created"})
			return
		}
		if len(pathParts) == 4 && pathParts[2] == "locks" && request.Method == http.MethodDelete {
			if err := server.Store.RemoveLock(principal, pathParts[1], pathParts[3], requestID, correlationID); err != nil {
				writeAPIError(responseWriter, apiErr(err, correlationID))
				return
			}
			responseWriter.WriteHeader(http.StatusNoContent)
			return
		}
		if len(pathParts) == 2 && request.Method == http.MethodDelete {
			if err := server.Store.DeleteResource(principal, pathParts[1], requestID, correlationID); err != nil {
				writeAPIError(responseWriter, apiErr(err, correlationID))
				return
			}
			responseWriter.WriteHeader(http.StatusNoContent)
			return
		}
	}
	if len(pathParts) == 7 && pathParts[0] == "instances" && pathParts[2] == "tenants" && pathParts[4] == "subscriptions" && pathParts[6] == "resourceGroups" && request.Method == http.MethodPost {
		var groupRequest struct {
			Name string `json:"name"`
		}
		if json.NewDecoder(request.Body).Decode(&groupRequest) != nil {
			writeAPIError(responseWriter, apiErr(ErrInvalidRequest, correlationID))
			return
		}
		groupScope := scopeString(pathParts[1], pathParts[3], pathParts[5], groupRequest.Name)
		resource, operation, err := server.Store.CreateGroup(principal, groupRequest.Name, groupScope, request.Header.Get("Idempotency-Key"), []byte(groupRequest.Name+groupScope), requestID, correlationID)
		if err != nil {
			writeAPIError(responseWriter, apiErr(err, correlationID))
			return
		}
		writeJSON(responseWriter, http.StatusCreated, map[string]any{"resource": resource, "operation": operationResponse(operation, http.StatusCreated)})
		return
	}
	if len(pathParts) == 5 && pathParts[0] == "resourceGroups" && pathParts[2] == "providers" && pathParts[3] == "Ember.Blob" && pathParts[4] == "buckets" && request.Method == http.MethodPost {
		var bucketRequest struct {
			Name string `json:"name"`
		}
		if json.NewDecoder(request.Body).Decode(&bucketRequest) != nil {
			writeAPIError(responseWriter, apiErr(ErrInvalidRequest, correlationID))
			return
		}
		groupResource, err := server.Store.GetResource(principal, pathParts[1], requestID, correlationID)
		if err != nil {
			writeAPIError(responseWriter, apiErr(err, correlationID))
			return
		}
		resource, operation, err := server.Store.CreateBucket(principal, groupResource.ID, bucketRequest.Name, groupResource.Scope, request.Header.Get("Idempotency-Key"), []byte(bucketRequest.Name+groupResource.ID), requestID, correlationID)
		if err != nil {
			writeAPIError(responseWriter, apiErr(err, correlationID))
			return
		}
		writeJSON(responseWriter, http.StatusCreated, map[string]any{"resource": resource, "operation": operationResponse(operation, http.StatusCreated)})
		return
	}
	if len(pathParts) >= 5 && pathParts[0] == "data" && pathParts[1] == "buckets" && pathParts[3] == "objects" {
		bucketID, objectKey := pathParts[2], strings.Join(pathParts[4:], "/")
		switch request.Method {
		case http.MethodPut:
			if request.ContentLength < 0 {
				writeAPIError(responseWriter, apiErr(ErrInvalidRequest, correlationID))
				return
			}
			if request.ContentLength > MaxObjectSize {
				writeAPIError(responseWriter, apiErr(ErrPayloadTooLarge, correlationID))
				return
			}
			expectedSHA256 := request.Header.Get("Content-Digest")
			expectedSHA256 = strings.TrimPrefix(expectedSHA256, "sha-256=")
			expectedSHA256 = strings.Trim(expectedSHA256, "\"")
			objectVersion, operation, err := server.Store.PutObjectStream(principal, bucketID, objectKey, request.Body, request.ContentLength, expectedSHA256, request.Header.Get("Idempotency-Key"), requestID, correlationID)
			if err != nil {
				writeAPIError(responseWriter, apiErr(err, correlationID))
				return
			}
			responseWriter.Header().Set("ETag", objectVersion.ETag)
			responseWriter.Header().Set("X-Ember-Version-ID", objectVersion.VersionID)
			writeJSON(responseWriter, http.StatusCreated, map[string]any{"object": objectVersion, "operation": operationResponse(operation, http.StatusCreated)})
			return
		case http.MethodGet, http.MethodHead:
			objectVersion, objectBytes, err := server.Store.GetObject(principal, bucketID, objectKey, requestID, correlationID)
			if err != nil {
				writeAPIError(responseWriter, apiErr(err, correlationID))
				return
			}
			responseWriter.Header().Set("ETag", objectVersion.ETag)
			responseWriter.Header().Set("X-Ember-Version-ID", objectVersion.VersionID)
			responseWriter.Header().Set("Content-Length", fmt.Sprintf("%d", objectVersion.Size))
			responseWriter.Header().Set("Content-Digest", "sha-256=\""+objectVersion.SHA256+"\"")
			if request.Method == http.MethodHead {
				responseWriter.WriteHeader(http.StatusOK)
				return
			}
			responseWriter.WriteHeader(http.StatusOK)
			_, _ = responseWriter.Write(objectBytes)
			return
		case http.MethodDelete:
			if err := server.Store.DeleteObject(principal, bucketID, objectKey, request.Header.Get("Idempotency-Key"), requestID, correlationID); err != nil {
				writeAPIError(responseWriter, apiErr(err, correlationID))
				return
			}
			responseWriter.WriteHeader(http.StatusNoContent)
			return
		}
	}
	if len(pathParts) == 1 && pathParts[0] == "audit" && request.Method == http.MethodGet {
		auditEvents, err := server.Store.Audit(principal, request.URL.Query().Get("scope"))
		if err != nil {
			writeAPIError(responseWriter, apiErr(err, correlationID))
			return
		}
		writeJSON(responseWriter, http.StatusOK, auditEvents)
		return
	}
	if len(pathParts) == 2 && pathParts[0] == "repair" && pathParts[1] == "findings" && request.Method == http.MethodGet {
		findings, err := server.Store.Findings(principal)
		if err != nil {
			writeAPIError(responseWriter, apiErr(err, correlationID))
			return
		}
		writeJSON(responseWriter, http.StatusOK, findings)
		return
	}
	if len(pathParts) == 3 && pathParts[0] == "repair" && request.Method == http.MethodPost {
		if err := server.Store.Repair(principal, pathParts[1], pathParts[2], requestID, correlationID); err != nil {
			writeAPIError(responseWriter, apiErr(err, correlationID))
			return
		}
		responseWriter.WriteHeader(http.StatusNoContent)
		return
	}
	writeAPIError(responseWriter, apiErr(ErrNotFound, correlationID))
}

func NewRequestID() string {
	var requestIDBytes [8]byte
	_, _ = rand.Read(requestIDBytes[:])
	return fmt.Sprintf("req_%x", requestIDBytes[:])
}
