package ember

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	MaxObjectSize     int64 = 10 * 1024 * 1024
	MaxObjectKeyBytes       = 1024
	LogicalQuotaBytes int64 = 1 << 30
)

var (
	ErrInvalidRequest    = errors.New("invalid request")
	ErrUnauthorized      = errors.New("unauthorized")
	ErrForbidden         = errors.New("forbidden")
	ErrNotFound          = errors.New("not found")
	ErrAlreadyExists     = errors.New("already exists")
	ErrConflict          = errors.New("conflict")
	ErrResourceLocked    = errors.New("resource locked")
	ErrDependencyExists  = errors.New("dependency exists")
	ErrIdempotency       = errors.New("idempotency conflict")
	ErrQuota             = errors.New("quota exceeded")
	ErrPayloadTooLarge   = errors.New("payload too large")
	ErrPathUnsafe        = errors.New("unsafe object key")
	ErrChecksum          = errors.New("checksum mismatch")
	ErrObjectUncommitted = errors.New("object not committed")
	ErrObjectCorrupt     = errors.New("object corrupt")
	ErrProvider          = errors.New("provider unavailable")
	ErrRecovery          = errors.New("recovery required")
)

type APIError struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	Target        string `json:"target,omitempty"`
	Retryable     bool   `json:"retryable"`
	OperationID   string `json:"operationId,omitempty"`
	CorrelationID string `json:"correlationId,omitempty"`
	Status        int    `json:"-"`
}

func apiErr(err error, correlation string) *APIError {
	status, code, message, retryable := 500, "InternalError", "internal error", false
	switch {
	case errors.Is(err, ErrInvalidRequest):
		status, code, message = 400, "InvalidRequest", "request is invalid"
	case errors.Is(err, ErrUnauthorized):
		status, code, message = 401, "Unauthorized", "authentication required"
	case errors.Is(err, ErrForbidden):
		status, code, message = 403, "Forbidden", "operation is not permitted"
	case errors.Is(err, ErrNotFound):
		status, code, message = 404, "NotFound", "resource was not found"
	case errors.Is(err, ErrAlreadyExists):
		status, code, message = 409, "AlreadyExists", "resource already exists"
	case errors.Is(err, ErrConflict):
		status, code, message = 409, "Conflict", "request conflicts with current state"
	case errors.Is(err, ErrResourceLocked):
		status, code, message = 409, "ResourceLocked", "resource is locked"
	case errors.Is(err, ErrDependencyExists):
		status, code, message = 409, "DependencyExists", "dependent resources exist"
	case errors.Is(err, ErrIdempotency):
		status, code, message = 409, "IdempotencyConflict", "idempotency key was used for another request"
	case errors.Is(err, ErrQuota):
		status, code, message = 413, "QuotaExceeded", "storage quota exceeded"
	case errors.Is(err, ErrPayloadTooLarge):
		status, code, message = 413, "PayloadTooLarge", "payload exceeds the synchronous limit"
	case errors.Is(err, ErrPathUnsafe):
		status, code, message = 422, "PathUnsafe", "object key is not allowed"
	case errors.Is(err, ErrChecksum):
		status, code, message = 422, "ChecksumMismatch", "content checksum does not match"
	case errors.Is(err, ErrObjectUncommitted):
		status, code, message = 422, "ObjectNotCommitted", "object is not committed"
	case errors.Is(err, ErrObjectCorrupt):
		status, code, message = 500, "ObjectCorrupt", "committed object bytes failed verification"
	case errors.Is(err, ErrRecovery):
		status, code, message = 409, "RecoveryRequired", "operator recovery is required"
	case errors.Is(err, ErrProvider):
		status, code, message, retryable = 503, "ProviderUnavailable", "provider is unavailable", true
	}
	return &APIError{Code: code, Message: message, Retryable: retryable, Status: status, CorrelationID: correlation}
}

type Principal struct {
	Token string
	Name  string
	Role  string
	Scope string
}

type Auth struct {
	byToken map[string]Principal
}

func NewAuth(fixtures map[string]Principal) Auth { return Auth{byToken: fixtures} }

func DefaultTestAuth() Auth {
	return NewAuth(map[string]Principal{
		"owner-test-token":  {Token: "owner-test-token", Name: "local-owner", Role: "owner", Scope: "*"},
		"editor-test-token": {Token: "editor-test-token", Name: "local-editor", Role: "editor", Scope: "*"},
		"reader-test-token": {Token: "reader-test-token", Name: "local-reader", Role: "reader", Scope: "*"},
	})
}

func LoadAuthFile(path string) (Auth, error) {
	info, err := os.Stat(path)
	if err != nil {
		return Auth{}, err
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Auth{}, fmt.Errorf("auth file must be mode 0600")
	}
	var raw map[string]struct {
		Principal string `json:"principal"`
		Role      string `json:"role"`
		Scope     string `json:"scope"`
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Auth{}, err
	}
	if err := json.Unmarshal(b, &raw); err != nil {
		return Auth{}, err
	}
	out := make(map[string]Principal, len(raw))
	for token, v := range raw {
		if token == "" || v.Principal == "" || v.Scope == "" {
			return Auth{}, fmt.Errorf("invalid auth fixture")
		}
		if v.Role != "owner" && v.Role != "editor" && v.Role != "reader" {
			return Auth{}, fmt.Errorf("invalid auth role")
		}
		out[token] = Principal{Token: token, Name: v.Principal, Role: v.Role, Scope: v.Scope}
	}
	return NewAuth(out), nil
}

func (a Auth) Authenticate(header string) (Principal, error) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return Principal{}, ErrUnauthorized
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	if token == "" {
		return Principal{}, ErrUnauthorized
	}
	p, ok := a.byToken[token]
	if !ok {
		return Principal{}, ErrUnauthorized
	}
	return p, nil
}

func allowed(role, action string) bool {
	if role == "owner" {
		return true
	}
	if role == "editor" {
		switch action {
		case "read", "group:create", "group:delete", "bucket:create", "bucket:delete", "object:put", "object:delete":
			return true
		}
	}
	if role == "reader" && action == "read" {
		return true
	}
	return false
}

func inScope(p Principal, scope string) bool {
	return p.Scope == "*" || p.Scope == scope || strings.HasPrefix(scope, p.Scope+"/")
}

func scopeString(instance, tenant, subscription, group string) string {
	return strings.Join([]string{instance, tenant, subscription, group}, "/")
}

type Resource struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Type          string            `json:"type"`
	ParentID      string            `json:"parentId,omitempty"`
	Scope         string            `json:"scope"`
	DesiredState  string            `json:"desiredState"`
	ObservedState string            `json:"observedState"`
	Tags          map[string]string `json:"tags,omitempty"`
	CreatedAt     time.Time         `json:"createdAt"`
	UpdatedAt     time.Time         `json:"updatedAt"`
}

type Operation struct {
	ID            string    `json:"id"`
	Action        string    `json:"action"`
	Status        string    `json:"status"`
	ResourceID    string    `json:"resourceId,omitempty"`
	Scope         string    `json:"scope"`
	RequestID     string    `json:"requestId"`
	CorrelationID string    `json:"correlationId"`
	ErrorCode     string    `json:"errorCode,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type Lock struct {
	Kind      string    `json:"kind"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type ObjectVersion struct {
	BucketID  string    `json:"bucketId"`
	Key       string    `json:"key"`
	VersionID string    `json:"versionId"`
	SHA256    string    `json:"sha256"`
	ETag      string    `json:"etag"`
	Size      int64     `json:"size"`
	Path      string    `json:"-"`
	Committed time.Time `json:"committedAt"`
}

type AuditEvent struct {
	ID            string    `json:"id"`
	Principal     string    `json:"principal"`
	Action        string    `json:"action"`
	Outcome       string    `json:"outcome"`
	Target        string    `json:"target,omitempty"`
	KeyHash       string    `json:"keyHash,omitempty"`
	Scope         string    `json:"scope"`
	RequestID     string    `json:"requestId"`
	CorrelationID string    `json:"correlationId"`
	Reason        string    `json:"reason,omitempty"`
	PolicyVersion string    `json:"policyVersion"`
	At            time.Time `json:"at"`
}

type Finding struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Reference string    `json:"reference"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

type idempotencyRecord struct {
	Hash, OperationID, ResourceID string
	ExpiresAt                     time.Time
}

type FileStore struct {
	root       string
	production bool
	used       int64
	mu         sync.Mutex
}

func NewFileStore(root string, production bool) (*FileStore, error) {
	if root == "" {
		return nil, fmt.Errorf("blob root is required")
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("blob root must be a real directory")
	}
	if production && !confinementAvailable(abs) {
		return nil, ErrProvider
	}
	for _, name := range []string{"staging", "objects", "quarantine"} {
		if err := os.MkdirAll(filepath.Join(abs, name), 0o750); err != nil {
			return nil, err
		}
	}
	return &FileStore{root: abs, production: production}, nil
}

func (f *FileStore) Root() string { return f.root }

func validateKey(key string) error {
	if key == "" || len([]byte(key)) > MaxObjectKeyBytes || !utf8.ValidString(key) || strings.ContainsRune(key, 0) || strings.Contains(key, "\\") || filepath.IsAbs(key) {
		return ErrPathUnsafe
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return ErrPathUnsafe
		}
	}
	return nil
}

func randomID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		return prefix + "_fallback"
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}

func (f *FileStore) Write(bucket, key string, body []byte, expectedLength int64, expectedSHA string) (ObjectVersion, error) {
	return f.WriteReader(bucket, key, bytes.NewReader(body), expectedLength, expectedSHA)
}

func validateStoredPath(rel string) error {
	if rel == "" || filepath.IsAbs(rel) || strings.ContainsRune(rel, 0) || strings.Contains(rel, "\\\\") {
		return ErrPathUnsafe
	}
	for _, segment := range strings.Split(filepath.ToSlash(rel), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return ErrPathUnsafe
		}
	}
	return nil
}

func validateBucketID(bucket string) error {
	if bucket == "" || strings.ContainsAny(bucket, "/\\\\") || bucket == "." || bucket == ".." {
		return ErrPathUnsafe
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (f *FileStore) WriteReader(bucket, key string, body io.Reader, expectedLength int64, expectedSHA string) (ObjectVersion, error) {
	if err := validateBucketID(bucket); err != nil {
		return ObjectVersion{}, err
	}
	if err := validateKey(key); err != nil {
		return ObjectVersion{}, err
	}
	if expectedLength < 0 {
		return ObjectVersion{}, ErrInvalidRequest
	}
	if expectedLength > MaxObjectSize {
		return ObjectVersion{}, ErrPayloadTooLarge
	}
	if body == nil {
		return ObjectVersion{}, ErrInvalidRequest
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.used+expectedLength > LogicalQuotaBytes {
		return ObjectVersion{}, ErrQuota
	}

	partRel := filepath.Join("staging", randomID("part")+".part")
	partPath := filepath.Join(f.root, partRel)
	part, err := providerOpen(f.root, partRel, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640, f.production)
	if err != nil {
		return ObjectVersion{}, ErrProvider
	}
	cleanup := func() { _ = part.Close(); _ = os.Remove(partPath) }
	hasher := sha256.New()
	written, copyErr := io.CopyN(io.MultiWriter(part, hasher), body, expectedLength)
	if copyErr != nil || written != expectedLength {
		cleanup()
		return ObjectVersion{}, ErrInvalidRequest
	}
	var extra [1]byte
	if n, err := body.Read(extra[:]); n > 0 || (err == nil && n == 0) {
		cleanup()
		return ObjectVersion{}, ErrInvalidRequest
	}
	if err := part.Sync(); err != nil {
		cleanup()
		return ObjectVersion{}, ErrProvider
	}
	if err := part.Close(); err != nil {
		_ = os.Remove(partPath)
		return ObjectVersion{}, ErrProvider
	}
	if err := syncDirectory(filepath.Join(f.root, "staging")); err != nil {
		_ = os.Remove(partPath)
		return ObjectVersion{}, ErrProvider
	}
	actualHex := hex.EncodeToString(hasher.Sum(nil))
	if expectedSHA == "" || !strings.EqualFold(expectedSHA, actualHex) {
		_ = os.Remove(partPath)
		return ObjectVersion{}, ErrChecksum
	}

	version := randomID("ver")
	shard := actualHex[:4]
	dirRel := filepath.Join("objects", bucket, shard[:2], shard[2:])
	if err := mkdirAllWithinRoot(f.root, dirRel, 0o750); err != nil {
		_ = os.Remove(partPath)
		return ObjectVersion{}, ErrProvider
	}
	rel := filepath.Join(dirRel, version+".blob")
	if err := providerPathCheck(f.root, rel, f.production); err == nil {
		_ = os.Remove(partPath)
		return ObjectVersion{}, ErrProvider
	} else if !os.IsNotExist(err) {
		_ = os.Remove(partPath)
		return ObjectVersion{}, ErrProvider
	}
	final := filepath.Join(f.root, rel)
	if err := os.Rename(partPath, final); err != nil {
		_ = os.Remove(partPath)
		return ObjectVersion{}, ErrProvider
	}
	if err := syncDirectory(filepath.Dir(final)); err != nil {
		return ObjectVersion{}, ErrProvider
	}
	if err := syncDirectory(filepath.Join(f.root, "staging")); err != nil {
		return ObjectVersion{}, ErrProvider
	}
	f.used += expectedLength
	return ObjectVersion{BucketID: bucket, Key: key, VersionID: version, SHA256: actualHex, ETag: "sha256:" + actualHex, Size: expectedLength, Path: filepath.ToSlash(rel), Committed: time.Now().UTC()}, nil
}

func (f *FileStore) Read(obj ObjectVersion) ([]byte, error) {
	if err := validateStoredPath(obj.Path); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	file, err := providerOpen(f.root, obj.Path, os.O_RDONLY, 0, f.production)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrObjectUncommitted
		}
		return nil, ErrProvider
	}
	b, err := io.ReadAll(io.LimitReader(file, MaxObjectSize+1))
	_ = file.Close()
	if err != nil {
		return nil, ErrProvider
	}
	if int64(len(b)) > MaxObjectSize {
		return nil, ErrObjectCorrupt
	}
	h := sha256.Sum256(b)
	if hex.EncodeToString(h[:]) != obj.SHA256 {
		return nil, ErrObjectCorrupt
	}
	return b, nil
}

func (f *FileStore) Delete(obj ObjectVersion) error {
	if err := validateStoredPath(obj.Path); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := providerPathCheck(f.root, obj.Path, f.production); err != nil && !os.IsNotExist(err) {
		return ErrProvider
	}
	if err := os.Remove(filepath.Join(f.root, obj.Path)); err != nil && !os.IsNotExist(err) {
		return ErrProvider
	}
	if err := syncDirectory(filepath.Dir(filepath.Join(f.root, obj.Path))); err != nil && !os.IsNotExist(err) {
		return ErrProvider
	}
	if f.used >= obj.Size {
		f.used -= obj.Size
	} else {
		f.used = 0
	}
	return nil
}

// MoveToQuarantine moves an existing provider-owned staging or object file to
// a provider-generated quarantine name. The reference is always root-relative
// and may not name the quarantine tree itself.
func (f *FileStore) MoveToQuarantine(reference string) error {
	if err := validateStoredPath(reference); err != nil {
		return err
	}
	reference = filepath.ToSlash(reference)
	if !strings.HasPrefix(reference, "staging/") && !strings.HasPrefix(reference, "objects/") {
		return ErrPathUnsafe
	}

	f.mu.Lock()
	defer f.mu.Unlock()

	source := filepath.Join(f.root, filepath.FromSlash(reference))
	info, err := os.Lstat(source)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return ErrProvider
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return ErrPathUnsafe
	}
	if err := providerPathCheck(f.root, reference, f.production); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return ErrPathUnsafe
	}

	quarantine := filepath.Join("quarantine", randomID("orphan")+".quarantine")
	if err := validateStoredPath(quarantine); err != nil {
		return err
	}
	if err := providerPathCheck(f.root, filepath.Dir(quarantine), f.production); err != nil {
		return ErrProvider
	}
	if err := providerPathCheck(f.root, quarantine, f.production); err == nil {
		return ErrProvider
	} else if !os.IsNotExist(err) {
		return ErrProvider
	}
	if err := os.Rename(source, filepath.Join(f.root, filepath.FromSlash(quarantine))); err != nil {
		return ErrProvider
	}
	if err := syncDirectory(filepath.Dir(source)); err != nil {
		return ErrProvider
	}
	if err := syncDirectory(filepath.Join(f.root, "quarantine")); err != nil {
		return ErrProvider
	}
	return nil
}

func (f *FileStore) Reset() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, name := range []string{"staging", "objects", "quarantine"} {
		path := filepath.Join(f.root, name)
		if err := os.RemoveAll(path); err != nil {
			return err
		}
		if err := os.MkdirAll(path, 0o750); err != nil {
			return err
		}
		if err := syncDirectory(path); err != nil {
			return err
		}
	}
	f.used = 0
	return syncDirectory(f.root)
}

type Store struct {
	mu          sync.RWMutex
	files       *FileStore
	resources   map[string]*Resource
	operations  map[string]*Operation
	buckets     map[string]struct{}
	objects     map[string]ObjectVersion
	locks       map[string]map[string]Lock
	idempotency map[string]idempotencyRecord
	audit       []AuditEvent
	findings    map[string]Finding
	sequence    uint64
}

func NewStore(files *FileStore) *Store {
	return &Store{files: files, resources: map[string]*Resource{}, operations: map[string]*Operation{}, buckets: map[string]struct{}{}, objects: map[string]ObjectVersion{}, locks: map[string]map[string]Lock{}, idempotency: map[string]idempotencyRecord{}, findings: map[string]Finding{}}
}

type ControlPlane interface {
	CreateGroup(Principal, string, string, string, []byte, string, string) (*Resource, *Operation, error)
	CreateBucket(Principal, string, string, string, string, []byte, string, string) (*Resource, *Operation, error)
	GetResource(Principal, string, string, string) (*Resource, error)
	GetOperation(Principal, string, string, string) (*Operation, error)
	DeleteResource(Principal, string, string, string) error
	AddLock(Principal, string, string, string, string, string) error
	RemoveLock(Principal, string, string, string, string) error
	PutObjectStream(Principal, string, string, io.Reader, int64, string, string, string, string) (*ObjectVersion, *Operation, error)
	GetObject(Principal, string, string, string, string) (*ObjectVersion, []byte, error)
	DeleteObject(Principal, string, string, string, string, string) error
	Repair(Principal, string, string, string, string) error
	Audit(Principal, string) ([]AuditEvent, error)
	Findings(Principal) ([]Finding, error)
}

func Allowed(p Principal, action, scope string) bool {
	return inScope(p, scope) && allowed(p.Role, action)
}
func ValidateObjectKey(key string) error { return validateKey(key) }

func (s *Store) nextID(prefix string) string {
	s.sequence++
	return fmt.Sprintf("%s_%08d", prefix, s.sequence)
}
func hashRequest(raw []byte) string          { h := sha256.Sum256(raw); return hex.EncodeToString(h[:]) }
func objectMapKey(bucket, key string) string { return bucket + "\x00" + key }
func (s *Store) auditLocked(p Principal, action, outcome, target, scope, requestID, correlation, reason, key string) {
	keyHash := ""
	if key != "" {
		keyHash = hashRequest([]byte(key))
	}
	s.audit = append(s.audit, AuditEvent{ID: s.nextID("aud"), Principal: p.Name, Action: action, Outcome: outcome, Target: target, KeyHash: keyHash, Scope: scope, RequestID: requestID, CorrelationID: correlation, Reason: reason, PolicyVersion: "phase1-v1", At: time.Now().UTC()})
}
func (s *Store) authorizeLocked(p Principal, action, scope, target, req, corr string) error {
	if !inScope(p, scope) || !allowed(p.Role, action) {
		s.auditLocked(p, action, "denied", target, scope, req, corr, "forbidden", "")
		return ErrForbidden
	}
	return nil
}
func (s *Store) newOpLocked(p Principal, action, resourceID, scope, req, corr string) *Operation {
	now := time.Now().UTC()
	op := &Operation{ID: s.nextID("op"), Action: action, Status: "succeeded", ResourceID: resourceID, Scope: scope, RequestID: req, CorrelationID: corr, CreatedAt: now, UpdatedAt: now}
	s.operations[op.ID] = op
	return op
}

func (s *Store) CreateGroup(p Principal, name, scope, idemKey string, raw []byte, req, corr string) (*Resource, *Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.authorizeLocked(p, "group:create", scope, "resourceGroups", req, corr); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(name) == "" || scope == "" || idemKey == "" {
		return nil, nil, ErrInvalidRequest
	}
	ik := p.Name + "|group-create|" + idemKey
	h := hashRequest(raw)
	if old, ok := s.idempotency[ik]; ok && time.Now().Before(old.ExpiresAt) {
		if old.Hash != h {
			return nil, nil, ErrIdempotency
		}
		return s.resources[old.ResourceID], s.operations[old.OperationID], nil
	}
	for _, r := range s.resources {
		if r.Type == "resourceGroup" && r.Name == name && r.Scope == scope {
			return nil, nil, ErrAlreadyExists
		}
	}
	now := time.Now().UTC()
	r := &Resource{ID: s.nextID("rg"), Name: name, Type: "resourceGroup", Scope: scope, DesiredState: "created", ObservedState: "created", CreatedAt: now, UpdatedAt: now}
	op := s.newOpLocked(p, "group:create", r.ID, scope, req, corr)
	s.resources[r.ID] = r
	s.idempotency[ik] = idempotencyRecord{Hash: h, OperationID: op.ID, ResourceID: r.ID, ExpiresAt: now.Add(24 * time.Hour)}
	s.auditLocked(p, "group:create", "succeeded", r.ID, scope, req, corr, "", "")
	return r, op, nil
}

func (s *Store) CreateBucket(p Principal, groupID, name, scope, idemKey string, raw []byte, req, corr string) (*Resource, *Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.authorizeLocked(p, "bucket:create", scope, groupID, req, corr); err != nil {
		return nil, nil, err
	}
	group, ok := s.resources[groupID]
	if !ok || group.Type != "resourceGroup" {
		return nil, nil, ErrNotFound
	}
	if idemKey == "" || strings.TrimSpace(name) == "" {
		return nil, nil, ErrInvalidRequest
	}
	ik := p.Name + "|bucket-create|" + idemKey
	h := hashRequest(raw)
	if old, ok := s.idempotency[ik]; ok && time.Now().Before(old.ExpiresAt) {
		if old.Hash != h {
			return nil, nil, ErrIdempotency
		}
		return s.resources[old.ResourceID], s.operations[old.OperationID], nil
	}
	for _, r := range s.resources {
		if r.Type == "Ember.Blob/bucket" && r.Name == name && r.ParentID == groupID {
			return nil, nil, ErrAlreadyExists
		}
	}
	now := time.Now().UTC()
	r := &Resource{ID: s.nextID("res"), Name: name, Type: "Ember.Blob/bucket", ParentID: groupID, Scope: scope, DesiredState: "created", ObservedState: "created", CreatedAt: now, UpdatedAt: now}
	op := s.newOpLocked(p, "bucket:create", r.ID, scope, req, corr)
	s.resources[r.ID] = r
	s.buckets[r.ID] = struct{}{}
	s.idempotency[ik] = idempotencyRecord{Hash: h, OperationID: op.ID, ResourceID: r.ID, ExpiresAt: now.Add(24 * time.Hour)}
	s.auditLocked(p, "bucket:create", "succeeded", r.ID, scope, req, corr, "", "")
	return r, op, nil
}

func (s *Store) GetResource(p Principal, id, req, corr string) (*Resource, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.resources[id]
	if !ok {
		return nil, ErrNotFound
	}
	if !inScope(p, r.Scope) || !allowed(p.Role, "read") {
		s.auditLocked(p, "resource:read", "denied", id, r.Scope, req, corr, "forbidden", "")
		return nil, ErrForbidden
	}
	return r, nil
}
func (s *Store) GetOperation(p Principal, id, req, corr string) (*Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.operations[id]
	if !ok {
		return nil, ErrNotFound
	}
	if !inScope(p, op.Scope) || !allowed(p.Role, "read") {
		s.auditLocked(p, "operation:read", "denied", id, op.Scope, req, corr, "forbidden", "")
		return nil, ErrForbidden
	}
	return op, nil
}

func (s *Store) DeleteResource(p Principal, id, req, corr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.resources[id]
	if !ok {
		return ErrNotFound
	}
	if err := s.authorizeLocked(p, "bucket:delete", r.Scope, id, req, corr); err != nil {
		return err
	}
	if ls := s.locks[id]; len(ls) > 0 {
		s.auditLocked(p, "resource:delete", "denied", id, r.Scope, req, corr, "locked", "")
		return ErrResourceLocked
	}
	for _, child := range s.resources {
		if child.ParentID == id {
			s.auditLocked(p, "resource:delete", "denied", id, r.Scope, req, corr, "dependency", "")
			return ErrDependencyExists
		}
	}
	if r.Type == "Ember.Blob/bucket" {
		for k, obj := range s.objects {
			if strings.HasPrefix(k, id+"\x00") {
				if err := s.files.Delete(obj); err != nil {
					return err
				}
				delete(s.objects, k)
			}
		}
	}
	delete(s.resources, id)
	delete(s.buckets, id)
	s.auditLocked(p, "resource:delete", "succeeded", id, r.Scope, req, corr, "", "")
	return nil
}

func (s *Store) AddLock(p Principal, id, kind, note, req, corr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.resources[id]
	if !ok {
		return ErrNotFound
	}
	if err := s.authorizeLocked(p, "lock:write", r.Scope, id, req, corr); err != nil {
		return err
	}
	if s.locks[id] == nil {
		s.locks[id] = map[string]Lock{}
	}
	s.locks[id][kind] = Lock{Kind: kind, Note: note, CreatedAt: time.Now().UTC()}
	s.auditLocked(p, "lock:write", "succeeded", id, r.Scope, req, corr, "", "")
	return nil
}
func (s *Store) RemoveLock(p Principal, id, kind, req, corr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.resources[id]
	if !ok {
		return ErrNotFound
	}
	if err := s.authorizeLocked(p, "lock:write", r.Scope, id, req, corr); err != nil {
		return err
	}
	if s.locks[id] == nil {
		return ErrNotFound
	}
	delete(s.locks[id], kind)
	s.auditLocked(p, "lock:delete", "succeeded", id, r.Scope, req, corr, "", "")
	return nil
}

func (s *Store) PutObject(p Principal, bucketID, key string, body []byte, expectedLength int64, expectedSHA, idemKey, req, corr string) (*ObjectVersion, *Operation, error) {
	return s.PutObjectStream(p, bucketID, key, bytes.NewReader(body), expectedLength, expectedSHA, idemKey, req, corr)
}

func (s *Store) PutObjectStream(p Principal, bucketID, key string, body io.Reader, expectedLength int64, expectedSHA, idemKey, req, corr string) (*ObjectVersion, *Operation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.resources[bucketID]
	if !ok || r.Type != "Ember.Blob/bucket" {
		return nil, nil, ErrNotFound
	}
	if err := s.authorizeLocked(p, "object:put", r.Scope, bucketID, req, corr); err != nil {
		return nil, nil, err
	}
	if idemKey == "" {
		return nil, nil, ErrInvalidRequest
	}
	if err := validateKey(key); err != nil {
		return nil, nil, err
	}
	ik := p.Name + "|object-put|" + bucketID + "|" + key + "|" + idemKey
	h := hashRequest([]byte(key + ":" + expectedSHA + ":" + fmt.Sprint(expectedLength)))
	if old, ok := s.idempotency[ik]; ok && time.Now().Before(old.ExpiresAt) {
		if old.Hash != h {
			return nil, nil, ErrIdempotency
		}
		op := s.operations[old.OperationID]
		obj := s.objects[objectMapKey(bucketID, key)]
		return &obj, op, nil
	}
	obj, err := s.files.WriteReader(bucketID, key, body, expectedLength, expectedSHA)
	if err != nil {
		f := Finding{ID: s.nextID("finding"), Kind: "upload", Reference: bucketID + ":" + hashRequest([]byte(key)), Status: "operator_action_required", CreatedAt: time.Now().UTC()}
		s.findings[f.ID] = f
		s.auditLocked(p, "object:put", "failed", bucketID, r.Scope, req, corr, "safe_failure", key)
		return nil, nil, err
	}
	op := s.newOpLocked(p, "object:put", bucketID, r.Scope, req, corr)
	s.objects[objectMapKey(bucketID, key)] = obj
	s.idempotency[ik] = idempotencyRecord{Hash: h, OperationID: op.ID, ResourceID: bucketID, ExpiresAt: time.Now().UTC().Add(24 * time.Hour)}
	s.auditLocked(p, "object:put", "succeeded", bucketID, r.Scope, req, corr, "", key)
	return &obj, op, nil
}

func (s *Store) GetObject(p Principal, bucketID, key, req, corr string) (*ObjectVersion, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.resources[bucketID]
	if !ok || r.Type != "Ember.Blob/bucket" {
		return nil, nil, ErrNotFound
	}
	if err := s.authorizeLocked(p, "read", r.Scope, bucketID, req, corr); err != nil {
		return nil, nil, err
	}
	obj, ok := s.objects[objectMapKey(bucketID, key)]
	if !ok {
		return nil, nil, ErrNotFound
	}
	b, err := s.files.Read(obj)
	if err != nil {
		s.auditLocked(p, "object:get", "failed", bucketID, r.Scope, req, corr, "integrity", key)
		return nil, nil, err
	}
	s.auditLocked(p, "object:get", "succeeded", bucketID, r.Scope, req, corr, "", key)
	return &obj, b, nil
}
func (s *Store) DeleteObject(p Principal, bucketID, key, idemKey, req, corr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.resources[bucketID]
	if !ok || r.Type != "Ember.Blob/bucket" {
		return ErrNotFound
	}
	if err := s.authorizeLocked(p, "object:delete", r.Scope, bucketID, req, corr); err != nil {
		return err
	}
	if idemKey == "" {
		return ErrInvalidRequest
	}
	ik := p.Name + "|object-delete|" + bucketID + "|" + key + "|" + idemKey
	h := hashRequest([]byte(key))
	if old, ok := s.idempotency[ik]; ok && time.Now().Before(old.ExpiresAt) {
		if old.Hash != h {
			return ErrIdempotency
		}
		return nil
	}
	obj, ok := s.objects[objectMapKey(bucketID, key)]
	if !ok {
		return ErrNotFound
	}
	if err := s.files.Delete(obj); err != nil {
		return err
	}
	delete(s.objects, objectMapKey(bucketID, key))
	op := s.newOpLocked(p, "object:delete", bucketID, r.Scope, req, corr)
	s.idempotency[ik] = idempotencyRecord{Hash: h, OperationID: op.ID, ResourceID: bucketID, ExpiresAt: time.Now().UTC().Add(24 * time.Hour)}
	s.auditLocked(p, "object:delete", "succeeded", bucketID, r.Scope, req, corr, "", key)
	return nil
}

func (s *Store) Repair(p Principal, id, action, req, corr string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p.Role != "owner" {
		s.auditLocked(p, "repair:"+action, "denied", id, "*", req, corr, "forbidden", "")
		return ErrForbidden
	}
	f, ok := s.findings[id]
	if !ok {
		return ErrNotFound
	}
	if action != "quarantine" && action != "discard" {
		return ErrInvalidRequest
	}
	f.Status = "operator_" + action
	s.findings[id] = f
	s.auditLocked(p, "repair:"+action, "succeeded", id, "*", req, corr, "operator_action", "")
	return nil
}

func (s *Store) Audit(p Principal, scope string) ([]AuditEvent, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !inScope(p, scope) || !allowed(p.Role, "read") {
		return nil, ErrForbidden
	}
	out := make([]AuditEvent, 0)
	for _, e := range s.audit {
		if inScope(p, e.Scope) && (scope == "" || strings.HasPrefix(e.Scope, scope)) {
			out = append(out, e)
		}
	}
	return out, nil
}
func (s *Store) Findings(p Principal) ([]Finding, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !allowed(p.Role, "read") {
		return nil, ErrForbidden
	}
	out := make([]Finding, 0, len(s.findings))
	for _, f := range s.findings {
		out = append(out, f)
	}
	return out, nil
}
func (s *Store) Reset() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.files.Reset(); err != nil {
		return err
	}
	s.resources = map[string]*Resource{}
	s.operations = map[string]*Operation{}
	s.buckets = map[string]struct{}{}
	s.objects = map[string]ObjectVersion{}
	s.locks = map[string]map[string]Lock{}
	s.idempotency = map[string]idempotencyRecord{}
	s.audit = nil
	s.findings = map[string]Finding{}
	s.sequence = 0
	return nil
}

func readBounded(r io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, MaxObjectSize+1))
	if err != nil {
		return nil, ErrProvider
	}
	if int64(len(b)) > MaxObjectSize {
		return nil, ErrPayloadTooLarge
	}
	return b, nil
}
