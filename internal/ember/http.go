package ember

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

type Server struct { Store *Store; Auth Auth }

func NewServer(store *Store, auth Auth) *Server { return &Server{Store: store, Auth: auth} }

func (s *Server) Handler() http.Handler { return http.HandlerFunc(s.serveHTTP) }

func requestIDs(r *http.Request) (string, string) {
	req := r.Header.Get("X-Request-ID")
	if req == "" { req = randomID("req") }
	corr := r.Header.Get("X-Correlation-ID")
	if corr == "" { corr = req }
	return req, corr
}

func (s *Server) principal(w http.ResponseWriter, r *http.Request, req, corr string) (Principal, bool) {
	p, err := s.Auth.Authenticate(r.Header.Get("Authorization"))
	if err != nil { writeAPIError(w, apiErr(err, corr)); return Principal{}, false }
	return p, true
}

func writeJSON(w http.ResponseWriter, status int, v any) { w.Header().Set("Content-Type", "application/json"); w.WriteHeader(status); _ = json.NewEncoder(w).Encode(v) }
func writeAPIError(w http.ResponseWriter, e *APIError) { if e.Status == 0 { e.Status = 500 }; writeJSON(w, e.Status, map[string]any{"error": e}) }
func operationResponse(op *Operation, status int) map[string]any { return map[string]any{"operation": op, "statusUrl": "/api/v1/operations/" + op.ID, "requestId": op.RequestID, "correlationId": op.CorrelationID, "status": status} }

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	req, corr := requestIDs(r); w.Header().Set("X-Request-ID", req); w.Header().Set("X-Correlation-ID", corr)
	if r.URL.Path == "/healthz" { writeJSON(w, 200, map[string]string{"status": "ok"}); return }
	if !strings.HasPrefix(r.URL.Path, "/api/v1/") { writeAPIError(w, apiErr(ErrNotFound, corr)); return }
	p, ok := s.principal(w, r, req, corr); if !ok { return }
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/")
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) >= 2 && parts[0] == "operations" && r.Method == http.MethodGet { op, err := s.Store.GetOperation(p, parts[1], req, corr); if err != nil { writeAPIError(w, apiErr(err, corr)); return }; writeJSON(w, 200, op); return }
	if len(parts) >= 2 && parts[0] == "resources" {
		if len(parts) == 2 && r.Method == http.MethodGet { obj, err := s.Store.GetResource(p, parts[1], req, corr); if err != nil { writeAPIError(w, apiErr(err, corr)); return }; writeJSON(w, 200, obj); return }
		if len(parts) == 3 && parts[2] == "locks" && r.Method == http.MethodPost { var in struct{ Kind, Note string }; if json.NewDecoder(r.Body).Decode(&in) != nil { writeAPIError(w, apiErr(ErrInvalidRequest, corr)); return }; err := s.Store.AddLock(p, parts[1], in.Kind, in.Note, req, corr); if err != nil { writeAPIError(w, apiErr(err, corr)); return }; writeJSON(w, 201, map[string]string{"status": "created"}); return }
		if len(parts) == 4 && parts[2] == "locks" && r.Method == http.MethodDelete { err := s.Store.RemoveLock(p, parts[1], parts[3], req, corr); if err != nil { writeAPIError(w, apiErr(err, corr)); return }; w.WriteHeader(http.StatusNoContent); return }
		if len(parts) == 2 && r.Method == http.MethodDelete { err := s.Store.DeleteResource(p, parts[1], req, corr); if err != nil { writeAPIError(w, apiErr(err, corr)); return }; w.WriteHeader(http.StatusNoContent); return }
	}
	if len(parts) == 7 && parts[0] == "instances" && parts[2] == "tenants" && parts[4] == "subscriptions" && parts[6] == "resourceGroups" && r.Method == http.MethodPost { var in struct{Name string `json:"name"`}; if json.NewDecoder(r.Body).Decode(&in) != nil { writeAPIError(w, apiErr(ErrInvalidRequest, corr)); return }; scope := scopeString(parts[1], parts[3], parts[5], in.Name); rsrc, op, err := s.Store.CreateGroup(p, in.Name, scope, r.Header.Get("Idempotency-Key"), []byte(in.Name+scope), req, corr); if err != nil { writeAPIError(w, apiErr(err, corr)); return }; writeJSON(w, 201, map[string]any{"resource": rsrc, "operation": operationResponse(op, 201)}); return }
	if len(parts) == 5 && parts[0] == "resourceGroups" && parts[2] == "providers" && parts[3] == "Ember.Blob" && parts[4] == "buckets" && r.Method == http.MethodPost { var in struct{Name string `json:"name"`}; if json.NewDecoder(r.Body).Decode(&in) != nil { writeAPIError(w, apiErr(ErrInvalidRequest, corr)); return }; group, err := s.Store.GetResource(p, parts[1], req, corr); if err != nil { writeAPIError(w, apiErr(err, corr)); return }; rsrc, op, err := s.Store.CreateBucket(p, group.ID, in.Name, group.Scope, r.Header.Get("Idempotency-Key"), []byte(in.Name+group.ID), req, corr); if err != nil { writeAPIError(w, apiErr(err, corr)); return }; writeJSON(w, 201, map[string]any{"resource": rsrc, "operation": operationResponse(op, 201)}); return }
	if len(parts) >= 5 && parts[0] == "data" && parts[1] == "buckets" && parts[3] == "objects" {
		bucket, key := parts[2], strings.Join(parts[4:], "/")
		switch r.Method {
		case http.MethodPut:
			body, err := readBounded(r.Body); if err != nil { writeAPIError(w, apiErr(err, corr)); return }; expected := r.Header.Get("Content-Digest"); expected = strings.TrimPrefix(expected, "sha-256="); expected = strings.Trim(expected, "\""); length := r.ContentLength; obj, op, err := s.Store.PutObject(p, bucket, key, body, length, expected, r.Header.Get("Idempotency-Key"), req, corr); if err != nil { writeAPIError(w, apiErr(err, corr)); return }; w.Header().Set("ETag", obj.ETag); w.Header().Set("X-Ember-Version-ID", obj.VersionID); writeJSON(w, 201, map[string]any{"object": obj, "operation": operationResponse(op, 201)}); return
		case http.MethodGet, http.MethodHead:
			obj, body, err := s.Store.GetObject(p, bucket, key, req, corr); if err != nil { writeAPIError(w, apiErr(err, corr)); return }; w.Header().Set("ETag", obj.ETag); w.Header().Set("X-Ember-Version-ID", obj.VersionID); w.Header().Set("Content-Length", fmt.Sprintf("%d", obj.Size)); w.Header().Set("Content-Digest", "sha-256=\""+obj.SHA256+"\""); if r.Method == http.MethodHead { w.WriteHeader(200); return }; w.WriteHeader(200); _, _ = w.Write(body); return
		case http.MethodDelete:
			err := s.Store.DeleteObject(p, bucket, key, r.Header.Get("Idempotency-Key"), req, corr); if err != nil { writeAPIError(w, apiErr(err, corr)); return }; w.WriteHeader(http.StatusNoContent); return
		}
	}
	if len(parts) == 1 && parts[0] == "audit" && r.Method == http.MethodGet { events, err := s.Store.Audit(p, r.URL.Query().Get("scope")); if err != nil { writeAPIError(w, apiErr(err, corr)); return }; writeJSON(w, 200, events); return }
	if len(parts) == 2 && parts[0] == "repair" && parts[1] == "findings" && r.Method == http.MethodGet { findings, err := s.Store.Findings(p); if err != nil { writeAPIError(w, apiErr(err, corr)); return }; writeJSON(w, 200, findings); return }
	if len(parts) == 3 && parts[0] == "repair" && r.Method == http.MethodPost { if err := s.Store.Repair(p, parts[1], parts[2], req, corr); err != nil { writeAPIError(w, apiErr(err, corr)); return }; w.WriteHeader(http.StatusNoContent); return }
	writeAPIError(w, apiErr(ErrNotFound, corr))
}

func NewRequestID() string { var b [8]byte; _, _ = rand.Read(b[:]); return fmt.Sprintf("req_%x", b[:]) }
var _ = time.Second
