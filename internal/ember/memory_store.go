package ember

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"
)

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
	declarative map[string]DeclarativeResourceState
	sequence    uint64
}

func NewStore(fileStore *FileStore) *Store {
	return &Store{
		files:       fileStore,
		resources:   map[string]*Resource{},
		operations:  map[string]*Operation{},
		buckets:     map[string]struct{}{},
		objects:     map[string]ObjectVersion{},
		locks:       map[string]map[string]Lock{},
		idempotency: map[string]idempotencyRecord{},
		findings:    map[string]Finding{},
		declarative: map[string]DeclarativeResourceState{},
	}
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

func (store *Store) nextIdentifier(prefix string) string {
	store.sequence++
	return fmt.Sprintf("%s_%08d", prefix, store.sequence)
}

func hashBytes(payload []byte) string {
	checksum := sha256.Sum256(payload)
	return hex.EncodeToString(checksum[:])
}

func objectMapKey(bucketID, objectKey string) string { return bucketID + "\x00" + objectKey }

func (store *Store) recordAuditEventLocked(principal Principal, action, outcome, target, scope, requestID, correlationID, reason, objectKey string) {
	objectKeyHash := ""
	if objectKey != "" {
		objectKeyHash = hashBytes([]byte(objectKey))
	}
	store.audit = append(store.audit, AuditEvent{
		ID:            store.nextIdentifier("aud"),
		Principal:     principal.Name,
		Action:        action,
		Outcome:       outcome,
		Target:        target,
		KeyHash:       objectKeyHash,
		Scope:         scope,
		RequestID:     requestID,
		CorrelationID: correlationID,
		Reason:        reason,
		PolicyVersion: "phase1-v1",
		At:            time.Now().UTC(),
	})
}

func (store *Store) authorizeLocked(principal Principal, action, scope, target, requestID, correlationID string) error {
	if !inScope(principal, scope) || !allowed(principal.Role, action) {
		store.recordAuditEventLocked(principal, action, "denied", target, scope, requestID, correlationID, "forbidden", "")
		return ErrForbidden
	}
	return nil
}

func (store *Store) createOperationLocked(principal Principal, action, resourceID, scope, requestID, correlationID string) *Operation {
	createdAt := time.Now().UTC()
	operation := &Operation{
		ID:            store.nextIdentifier("op"),
		Action:        action,
		Status:        "succeeded",
		ResourceID:    resourceID,
		Scope:         scope,
		RequestID:     requestID,
		CorrelationID: correlationID,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}
	store.operations[operation.ID] = operation
	return operation
}

func (store *Store) CreateGroup(principal Principal, name, scope, idempotencyKey string, rawPayload []byte, requestID, correlationID string) (*Resource, *Operation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.authorizeLocked(principal, "group:create", scope, "resourceGroups", requestID, correlationID); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(name) == "" || scope == "" || idempotencyKey == "" {
		return nil, nil, ErrInvalidRequest
	}
	idempotencyRecordKey := principal.Name + "|group-create|" + idempotencyKey
	requestHash := hashBytes(rawPayload)
	if existingRecord, found := store.idempotency[idempotencyRecordKey]; found && time.Now().Before(existingRecord.expiresAt) {
		if existingRecord.requestHash != requestHash {
			return nil, nil, ErrIdempotency
		}
		return store.resources[existingRecord.resourceID], store.operations[existingRecord.operationID], nil
	}
	for _, existingResource := range store.resources {
		if existingResource.Type == "resourceGroup" && existingResource.Name == name && existingResource.Scope == scope {
			return nil, nil, ErrAlreadyExists
		}
	}
	createdAt := time.Now().UTC()
	resource := &Resource{
		ID:            store.nextIdentifier("rg"),
		Name:          name,
		Type:          "resourceGroup",
		Scope:         scope,
		DesiredState:  "created",
		ObservedState: "created",
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}
	operation := store.createOperationLocked(principal, "group:create", resource.ID, scope, requestID, correlationID)
	store.resources[resource.ID] = resource
	store.idempotency[idempotencyRecordKey] = idempotencyRecord{
		requestHash: requestHash,
		operationID: operation.ID,
		resourceID:  resource.ID,
		expiresAt:   createdAt.Add(24 * time.Hour),
	}
	store.recordAuditEventLocked(principal, "group:create", "succeeded", resource.ID, scope, requestID, correlationID, "", "")
	return resource, operation, nil
}

func (store *Store) CreateBucket(principal Principal, groupID, name, scope, idempotencyKey string, rawPayload []byte, requestID, correlationID string) (*Resource, *Operation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.authorizeLocked(principal, "bucket:create", scope, groupID, requestID, correlationID); err != nil {
		return nil, nil, err
	}
	groupResource, found := store.resources[groupID]
	if !found || groupResource.Type != "resourceGroup" {
		return nil, nil, ErrNotFound
	}
	if idempotencyKey == "" || strings.TrimSpace(name) == "" {
		return nil, nil, ErrInvalidRequest
	}
	idempotencyRecordKey := principal.Name + "|bucket-create|" + idempotencyKey
	requestHash := hashBytes(rawPayload)
	if existingRecord, found := store.idempotency[idempotencyRecordKey]; found && time.Now().Before(existingRecord.expiresAt) {
		if existingRecord.requestHash != requestHash {
			return nil, nil, ErrIdempotency
		}
		return store.resources[existingRecord.resourceID], store.operations[existingRecord.operationID], nil
	}
	for _, existingResource := range store.resources {
		if existingResource.Type == "Ember.Blob/bucket" && existingResource.Name == name && existingResource.ParentID == groupID {
			return nil, nil, ErrAlreadyExists
		}
	}
	createdAt := time.Now().UTC()
	resource := &Resource{
		ID:            store.nextIdentifier("res"),
		Name:          name,
		Type:          "Ember.Blob/bucket",
		ParentID:      groupID,
		Scope:         scope,
		DesiredState:  "created",
		ObservedState: "created",
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}
	operation := store.createOperationLocked(principal, "bucket:create", resource.ID, scope, requestID, correlationID)
	store.resources[resource.ID] = resource
	store.buckets[resource.ID] = struct{}{}
	store.idempotency[idempotencyRecordKey] = idempotencyRecord{
		requestHash: requestHash,
		operationID: operation.ID,
		resourceID:  resource.ID,
		expiresAt:   createdAt.Add(24 * time.Hour),
	}
	store.recordAuditEventLocked(principal, "bucket:create", "succeeded", resource.ID, scope, requestID, correlationID, "", "")
	return resource, operation, nil
}

func (store *Store) GetResource(principal Principal, resourceID, requestID, correlationID string) (*Resource, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	resource, found := store.resources[resourceID]
	if !found {
		return nil, ErrNotFound
	}
	if !inScope(principal, resource.Scope) || !allowed(principal.Role, "read") {
		store.recordAuditEventLocked(principal, "resource:read", "denied", resourceID, resource.Scope, requestID, correlationID, "forbidden", "")
		return nil, ErrForbidden
	}
	return resource, nil
}

func (store *Store) ListResources(principal Principal, scope, requestID, correlationID string) ([]*Resource, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	if !allowed(principal.Role, "read") {
		return nil, ErrForbidden
	}
	resources := make([]*Resource, 0)
	for _, resource := range store.resources {
		if scope != "" && !strings.HasPrefix(resource.Scope, scope) {
			continue
		}
		if !inScope(principal, resource.Scope) {
			continue
		}
		copyOfResource := *resource
		copyOfResource.Tags = cloneResourceTags(resource.Tags)
		resources = append(resources, &copyOfResource)
	}
	sort.Slice(resources, func(left, right int) bool { return resources[left].ID < resources[right].ID })
	return resources, nil
}

func (store *Store) UpdateResource(principal Principal, resourceID, desiredState string, tags map[string]string, requestID, correlationID string) (*Resource, *Operation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	resource, found := store.resources[resourceID]
	if !found {
		return nil, nil, ErrNotFound
	}
	if err := store.authorizeLocked(principal, "resource:update", resource.Scope, resourceID, requestID, correlationID); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(desiredState) == "" {
		return nil, nil, ErrInvalidRequest
	}
	resource.DesiredState = desiredState
	resource.Tags = cloneResourceTags(tags)
	resource.UpdatedAt = time.Now().UTC()
	operation := store.createOperationLocked(principal, "resource:update", resource.ID, resource.Scope, requestID, correlationID)
	store.recordAuditEventLocked(principal, "resource:update", "succeeded", resource.ID, resource.Scope, requestID, correlationID, "", "")
	copyOfResource := *resource
	copyOfResource.Tags = cloneResourceTags(resource.Tags)
	return &copyOfResource, operation, nil
}

func (store *Store) RecordOperation(principal Principal, action, status, resourceID, scope, requestID, correlationID, errorCode, reason string) (*Operation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !inScope(principal, scope) || !allowed(principal.Role, "deployment:apply") {
		store.recordAuditEventLocked(principal, action, "denied", resourceID, scope, requestID, correlationID, "forbidden", "")
		return nil, ErrForbidden
	}
	if status == "" {
		return nil, ErrInvalidRequest
	}
	createdAt := time.Now().UTC()
	operation := &Operation{ID: store.nextIdentifier("op"), Action: action, Status: status, ResourceID: resourceID, Scope: scope, RequestID: requestID, CorrelationID: correlationID, ErrorCode: errorCode, CreatedAt: createdAt, UpdatedAt: createdAt}
	store.operations[operation.ID] = operation
	outcome := status
	if outcome != "succeeded" && outcome != "failed" {
		outcome = "failed"
	}
	store.recordAuditEventLocked(principal, action, outcome, resourceID, scope, requestID, correlationID, reason, "")
	return operation, nil
}

func cloneResourceTags(tags map[string]string) map[string]string {
	if len(tags) == 0 {
		return map[string]string{}
	}
	copyOfTags := make(map[string]string, len(tags))
	for key, value := range tags {
		copyOfTags[key] = value
	}
	return copyOfTags
}

func (store *Store) GetOperation(principal Principal, operationID, requestID, correlationID string) (*Operation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	operation, found := store.operations[operationID]
	if !found {
		return nil, ErrNotFound
	}
	if !inScope(principal, operation.Scope) || !allowed(principal.Role, "read") {
		store.recordAuditEventLocked(principal, "operation:read", "denied", operationID, operation.Scope, requestID, correlationID, "forbidden", "")
		return nil, ErrForbidden
	}
	return operation, nil
}

func (store *Store) DeleteResource(principal Principal, resourceID, requestID, correlationID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	resource, found := store.resources[resourceID]
	if !found {
		return ErrNotFound
	}
	if err := store.authorizeLocked(principal, "bucket:delete", resource.Scope, resourceID, requestID, correlationID); err != nil {
		return err
	}
	if locksForResource := store.locks[resourceID]; len(locksForResource) > 0 {
		store.recordAuditEventLocked(principal, "resource:delete", "denied", resourceID, resource.Scope, requestID, correlationID, "locked", "")
		return ErrResourceLocked
	}
	for _, childResource := range store.resources {
		if childResource.ParentID == resourceID {
			store.recordAuditEventLocked(principal, "resource:delete", "denied", resourceID, resource.Scope, requestID, correlationID, "dependency", "")
			return ErrDependencyExists
		}
	}
	if resource.Type == "Ember.Blob/bucket" {
		for storageKey, objectVersion := range store.objects {
			if strings.HasPrefix(storageKey, resourceID+"\x00") {
				if err := store.files.Delete(objectVersion); err != nil {
					return err
				}
				delete(store.objects, storageKey)
			}
		}
	}
	delete(store.resources, resourceID)
	delete(store.buckets, resourceID)
	store.recordAuditEventLocked(principal, "resource:delete", "succeeded", resourceID, resource.Scope, requestID, correlationID, "", "")
	return nil
}

func (store *Store) AddLock(principal Principal, resourceID, lockKind, note, requestID, correlationID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	resource, found := store.resources[resourceID]
	if !found {
		return ErrNotFound
	}
	if err := store.authorizeLocked(principal, "lock:write", resource.Scope, resourceID, requestID, correlationID); err != nil {
		return err
	}
	if store.locks[resourceID] == nil {
		store.locks[resourceID] = map[string]Lock{}
	}
	store.locks[resourceID][lockKind] = Lock{Kind: lockKind, Note: note, CreatedAt: time.Now().UTC()}
	store.recordAuditEventLocked(principal, "lock:write", "succeeded", resourceID, resource.Scope, requestID, correlationID, "", "")
	return nil
}

func (store *Store) RemoveLock(principal Principal, resourceID, lockKind, requestID, correlationID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	resource, found := store.resources[resourceID]
	if !found {
		return ErrNotFound
	}
	if err := store.authorizeLocked(principal, "lock:write", resource.Scope, resourceID, requestID, correlationID); err != nil {
		return err
	}
	if store.locks[resourceID] == nil {
		return ErrNotFound
	}
	delete(store.locks[resourceID], lockKind)
	store.recordAuditEventLocked(principal, "lock:delete", "succeeded", resourceID, resource.Scope, requestID, correlationID, "", "")
	return nil
}

func (store *Store) PutObject(principal Principal, bucketID, objectKey string, body []byte, expectedLength int64, expectedSHA256, idempotencyKey, requestID, correlationID string) (*ObjectVersion, *Operation, error) {
	return store.PutObjectStream(principal, bucketID, objectKey, bytes.NewReader(body), expectedLength, expectedSHA256, idempotencyKey, requestID, correlationID)
}

func (store *Store) PutObjectStream(principal Principal, bucketID, objectKey string, body io.Reader, expectedLength int64, expectedSHA256, idempotencyKey, requestID, correlationID string) (*ObjectVersion, *Operation, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	bucketResource, found := store.resources[bucketID]
	if !found || bucketResource.Type != "Ember.Blob/bucket" {
		return nil, nil, ErrNotFound
	}
	if err := store.authorizeLocked(principal, "object:put", bucketResource.Scope, bucketID, requestID, correlationID); err != nil {
		return nil, nil, err
	}
	if idempotencyKey == "" {
		return nil, nil, ErrInvalidRequest
	}
	if err := validateKey(objectKey); err != nil {
		return nil, nil, err
	}
	idempotencyRecordKey := principal.Name + "|object-put|" + bucketID + "|" + objectKey + "|" + idempotencyKey
	requestHash := hashBytes([]byte(objectKey + ":" + expectedSHA256 + ":" + fmt.Sprint(expectedLength)))
	if existingRecord, found := store.idempotency[idempotencyRecordKey]; found && time.Now().Before(existingRecord.expiresAt) {
		if existingRecord.requestHash != requestHash {
			return nil, nil, ErrIdempotency
		}
		existingOperation := store.operations[existingRecord.operationID]
		existingObject := store.objects[objectMapKey(bucketID, objectKey)]
		return &existingObject, existingOperation, nil
	}
	objectVersion, err := store.files.WriteReader(bucketID, objectKey, body, expectedLength, expectedSHA256)
	if err != nil {
		finding := Finding{
			ID:        store.nextIdentifier("finding"),
			Kind:      "upload",
			Reference: bucketID + ":" + hashBytes([]byte(objectKey)),
			Status:    "operator_action_required",
			CreatedAt: time.Now().UTC(),
		}
		store.findings[finding.ID] = finding
		store.recordAuditEventLocked(principal, "object:put", "failed", bucketID, bucketResource.Scope, requestID, correlationID, "safe_failure", objectKey)
		return nil, nil, err
	}
	operation := store.createOperationLocked(principal, "object:put", bucketID, bucketResource.Scope, requestID, correlationID)
	store.objects[objectMapKey(bucketID, objectKey)] = objectVersion
	store.idempotency[idempotencyRecordKey] = idempotencyRecord{
		requestHash: requestHash,
		operationID: operation.ID,
		resourceID:  bucketID,
		expiresAt:   time.Now().UTC().Add(24 * time.Hour),
	}
	store.recordAuditEventLocked(principal, "object:put", "succeeded", bucketID, bucketResource.Scope, requestID, correlationID, "", objectKey)
	return &objectVersion, operation, nil
}

func (store *Store) GetObject(principal Principal, bucketID, objectKey, requestID, correlationID string) (*ObjectVersion, []byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	bucketResource, found := store.resources[bucketID]
	if !found || bucketResource.Type != "Ember.Blob/bucket" {
		return nil, nil, ErrNotFound
	}
	if err := store.authorizeLocked(principal, "read", bucketResource.Scope, bucketID, requestID, correlationID); err != nil {
		return nil, nil, err
	}
	objectVersion, found := store.objects[objectMapKey(bucketID, objectKey)]
	if !found {
		return nil, nil, ErrNotFound
	}
	objectBytes, err := store.files.Read(objectVersion)
	if err != nil {
		store.recordAuditEventLocked(principal, "object:get", "failed", bucketID, bucketResource.Scope, requestID, correlationID, "integrity", objectKey)
		return nil, nil, err
	}
	store.recordAuditEventLocked(principal, "object:get", "succeeded", bucketID, bucketResource.Scope, requestID, correlationID, "", objectKey)
	return &objectVersion, objectBytes, nil
}

func (store *Store) DeleteObject(principal Principal, bucketID, objectKey, idempotencyKey, requestID, correlationID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	bucketResource, found := store.resources[bucketID]
	if !found || bucketResource.Type != "Ember.Blob/bucket" {
		return ErrNotFound
	}
	if err := store.authorizeLocked(principal, "object:delete", bucketResource.Scope, bucketID, requestID, correlationID); err != nil {
		return err
	}
	if idempotencyKey == "" {
		return ErrInvalidRequest
	}
	idempotencyRecordKey := principal.Name + "|object-delete|" + bucketID + "|" + objectKey + "|" + idempotencyKey
	requestHash := hashBytes([]byte(objectKey))
	if existingRecord, found := store.idempotency[idempotencyRecordKey]; found && time.Now().Before(existingRecord.expiresAt) {
		if existingRecord.requestHash != requestHash {
			return ErrIdempotency
		}
		return nil
	}
	objectVersion, found := store.objects[objectMapKey(bucketID, objectKey)]
	if !found {
		return ErrNotFound
	}
	if err := store.files.Delete(objectVersion); err != nil {
		return err
	}
	delete(store.objects, objectMapKey(bucketID, objectKey))
	operation := store.createOperationLocked(principal, "object:delete", bucketID, bucketResource.Scope, requestID, correlationID)
	store.idempotency[idempotencyRecordKey] = idempotencyRecord{
		requestHash: requestHash,
		operationID: operation.ID,
		resourceID:  bucketID,
		expiresAt:   time.Now().UTC().Add(24 * time.Hour),
	}
	store.recordAuditEventLocked(principal, "object:delete", "succeeded", bucketID, bucketResource.Scope, requestID, correlationID, "", objectKey)
	return nil
}

func (store *Store) Repair(principal Principal, findingID, action, requestID, correlationID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if principal.Role != "owner" {
		store.recordAuditEventLocked(principal, "repair:"+action, "denied", findingID, "*", requestID, correlationID, "forbidden", "")
		return ErrForbidden
	}
	finding, found := store.findings[findingID]
	if !found {
		return ErrNotFound
	}
	if action != "quarantine" && action != "discard" {
		return ErrInvalidRequest
	}
	finding.Status = "operator_" + action
	store.findings[findingID] = finding
	store.recordAuditEventLocked(principal, "repair:"+action, "succeeded", findingID, "*", requestID, correlationID, "operator_action", "")
	return nil
}

func (store *Store) Audit(principal Principal, scope string) ([]AuditEvent, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	if !inScope(principal, scope) || !allowed(principal.Role, "read") {
		return nil, ErrForbidden
	}
	auditEvents := make([]AuditEvent, 0)
	for _, auditEvent := range store.audit {
		if inScope(principal, auditEvent.Scope) && (scope == "" || strings.HasPrefix(auditEvent.Scope, scope)) {
			auditEvents = append(auditEvents, auditEvent)
		}
	}
	return auditEvents, nil
}

func (store *Store) Findings(principal Principal) ([]Finding, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	if !allowed(principal.Role, "read") {
		return nil, ErrForbidden
	}
	findings := make([]Finding, 0, len(store.findings))
	for _, finding := range store.findings {
		findings = append(findings, finding)
	}
	return findings, nil
}

func (store *Store) Reset() error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.files.Reset(); err != nil {
		return err
	}
	store.resources = map[string]*Resource{}
	store.operations = map[string]*Operation{}
	store.buckets = map[string]struct{}{}
	store.objects = map[string]ObjectVersion{}
	store.locks = map[string]map[string]Lock{}
	store.idempotency = map[string]idempotencyRecord{}
	store.audit = nil
	store.findings = map[string]Finding{}
	store.declarative = map[string]DeclarativeResourceState{}
	store.sequence = 0
	return nil
}

func readBounded(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, MaxObjectSize+1))
	if err != nil {
		return nil, ErrProvider
	}
	if int64(len(body)) > MaxObjectSize {
		return nil, ErrPayloadTooLarge
	}
	return body, nil
}
