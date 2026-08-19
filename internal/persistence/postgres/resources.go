package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"ember.local/ember/internal/ember"
)

func (store *Store) CreateGroup(principal ember.Principal, name, scope, idempotencyKey string, rawPayload []byte, requestID, correlationID string) (*ember.Resource, *ember.Operation, error) {
	ctx, cancel := databaseContext(context.Background())
	defer cancel()
	if err := store.authorize(ctx, principal, "group:create", scope, "resourceGroups", requestID, correlationID); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(name) == "" || scope == "" || idempotencyKey == "" {
		return nil, nil, ember.ErrInvalidRequest
	}
	requestHash := hashBytes(rawPayload)
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	defer transaction.Rollback()
	var existingRequestHash, existingOperationID, existingResourceID string
	err = transaction.QueryRowContext(ctx, `SELECT i.request_hash,i.operation_id,COALESCE(o.resource_id,'') FROM idempotency_records i JOIN operations o ON o.id=i.operation_id WHERE principal=$1 AND endpoint=$2 AND idem_key=$3 AND expires_at>now()`, principal.Name, "group-create", idempotencyKey).Scan(&existingRequestHash, &existingOperationID, &existingResourceID)
	if err == nil {
		if existingRequestHash != requestHash {
			return nil, nil, ember.ErrIdempotency
		}
		resource, resourceErr := scanResource(transaction.QueryRowContext(ctx, resourceSelect+` WHERE id=$1`, existingResourceID))
		operation, operationErr := scanOperation(transaction.QueryRowContext(ctx, operationSelect+` WHERE id=$1`, existingOperationID))
		if resourceErr != nil || operationErr != nil {
			return nil, nil, ember.ErrProvider
		}
		return resource, operation, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, translateDatabaseError(err)
	}
	var existingResourceIDForName string
	if err := transaction.QueryRowContext(ctx, `SELECT id FROM resources WHERE parent_id IS NULL AND type='resourceGroup' AND name=$1 AND scope=$2`, name, scope).Scan(&existingResourceIDForName); err == nil {
		return nil, nil, ember.ErrAlreadyExists
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, translateDatabaseError(err)
	}
	createdAt := time.Now().UTC()
	resourceID, operationID := generateID("rg"), generateID("op")
	tagsJSON := tagsFromRawPayload(rawPayload)
	if _, err := transaction.ExecContext(ctx, `INSERT INTO resources(id,name,type,scope,desired_state,observed_state,tags) VALUES($1,$2,'resourceGroup',$3,'created','created',$4)`, resourceID, name, scope, tagsJSON); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO operations(id,action,status,resource_id,scope,request_id,correlation_id,created_at,updated_at) VALUES($1,'group:create','succeeded',$2,$3,$4,$5,$6,$6)`, operationID, resourceID, scope, requestID, correlationID, createdAt); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO idempotency_records(principal,endpoint,idem_key,request_hash,operation_id,expires_at) VALUES($1,'group-create',$2,$3,$4,$5)`, principal.Name, idempotencyKey, requestHash, operationID, createdAt.Add(24*time.Hour)); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO audit_events(id,principal,action,outcome,target,scope,request_id,correlation_id,policy_version) VALUES($1,$2,'group:create','succeeded',$3,$4,$5,$6,'phase1-v1')`, generateID("aud"), principal.Name, resourceID, scope, requestID, correlationID); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	if err := transaction.Commit(); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	resource, _ := scanResource(store.db.QueryRowContext(ctx, resourceSelect+` WHERE id=$1`, resourceID))
	operation, _ := scanOperation(store.db.QueryRowContext(ctx, operationSelect+` WHERE id=$1`, operationID))
	return resource, operation, nil
}

func (store *Store) CreateBucket(principal ember.Principal, groupID, name, scope, idempotencyKey string, rawPayload []byte, requestID, correlationID string) (*ember.Resource, *ember.Operation, error) {
	ctx, cancel := databaseContext(context.Background())
	defer cancel()
	if err := store.authorize(ctx, principal, "bucket:create", scope, groupID, requestID, correlationID); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(name) == "" || idempotencyKey == "" {
		return nil, nil, ember.ErrInvalidRequest
	}
	requestHash := hashBytes(rawPayload)
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	defer transaction.Rollback()
	var existingRequestHash, existingOperationID, existingResourceID string
	err = transaction.QueryRowContext(ctx, `SELECT i.request_hash,i.operation_id,COALESCE(o.resource_id,'') FROM idempotency_records i JOIN operations o ON o.id=i.operation_id WHERE principal=$1 AND endpoint=$2 AND idem_key=$3 AND expires_at>now()`, principal.Name, "bucket-create", idempotencyKey).Scan(&existingRequestHash, &existingOperationID, &existingResourceID)
	if err == nil {
		if existingRequestHash != requestHash {
			return nil, nil, ember.ErrIdempotency
		}
		resource, resourceErr := scanResource(transaction.QueryRowContext(ctx, resourceSelect+` WHERE id=$1`, existingResourceID))
		operation, operationErr := scanOperation(transaction.QueryRowContext(ctx, operationSelect+` WHERE id=$1`, existingOperationID))
		if resourceErr != nil || operationErr != nil {
			return nil, nil, ember.ErrProvider
		}
		return resource, operation, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, translateDatabaseError(err)
	}
	var groupName string
	if err := transaction.QueryRowContext(ctx, `SELECT name FROM resources WHERE id=$1 AND type='resourceGroup'`, groupID).Scan(&groupName); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	var existingResourceIDForName string
	if err := transaction.QueryRowContext(ctx, `SELECT id FROM resources WHERE parent_id=$1 AND type='Ember.Blob/bucket' AND name=$2`, groupID, name).Scan(&existingResourceIDForName); err == nil {
		return nil, nil, ember.ErrAlreadyExists
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, translateDatabaseError(err)
	}
	createdAt := time.Now().UTC()
	resourceID, operationID := generateID("res"), generateID("op")
	tagsJSON := tagsFromRawPayload(rawPayload)
	if _, err := transaction.ExecContext(ctx, `INSERT INTO resources(id,name,type,parent_id,scope,desired_state,observed_state,tags) VALUES($1,$2,'Ember.Blob/bucket',$3,$4,'created','created',$5)`, resourceID, name, groupID, scope, tagsJSON); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO operations(id,action,status,resource_id,scope,request_id,correlation_id,created_at,updated_at) VALUES($1,'bucket:create','succeeded',$2,$3,$4,$5,$6,$6)`, operationID, resourceID, scope, requestID, correlationID, createdAt); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO idempotency_records(principal,endpoint,idem_key,request_hash,operation_id,expires_at) VALUES($1,'bucket-create',$2,$3,$4,$5)`, principal.Name, idempotencyKey, requestHash, operationID, createdAt.Add(24*time.Hour)); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO audit_events(id,principal,action,outcome,target,scope,request_id,correlation_id,policy_version) VALUES($1,$2,'bucket:create','succeeded',$3,$4,$5,$6,'phase1-v1')`, generateID("aud"), principal.Name, resourceID, scope, requestID, correlationID); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	if err := transaction.Commit(); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	resource, _ := scanResource(store.db.QueryRowContext(ctx, resourceSelect+` WHERE id=$1`, resourceID))
	operation, _ := scanOperation(store.db.QueryRowContext(ctx, operationSelect+` WHERE id=$1`, operationID))
	return resource, operation, nil
}

func tagsFromRawPayload(rawPayload []byte) []byte {
	var payload struct {
		Tags map[string]string `json:"tags"`
	}
	if err := json.Unmarshal(rawPayload, &payload); err != nil || payload.Tags == nil {
		return []byte(`{}`)
	}
	tags, err := json.Marshal(payload.Tags)
	if err != nil {
		return []byte(`{}`)
	}
	return tags
}

func (store *Store) GetResource(principal ember.Principal, resourceID, requestID, correlationID string) (*ember.Resource, error) {
	ctx, cancel := databaseContext(context.Background())
	defer cancel()
	resource, err := scanResource(store.db.QueryRowContext(ctx, resourceSelect+` WHERE id=$1`, resourceID))
	if err != nil {
		return nil, err
	}
	if !ember.Allowed(principal, "read", resource.Scope) {
		_ = store.recordAuditEvent(ctx, principal, "resource:read", "denied", resourceID, resource.Scope, requestID, correlationID, "forbidden", "")
		return nil, ember.ErrForbidden
	}
	return resource, nil
}

func (store *Store) GetOperation(principal ember.Principal, operationID, requestID, correlationID string) (*ember.Operation, error) {
	ctx, cancel := databaseContext(context.Background())
	defer cancel()
	operation, err := scanOperation(store.db.QueryRowContext(ctx, operationSelect+` WHERE id=$1`, operationID))
	if err != nil {
		return nil, err
	}
	if !ember.Allowed(principal, "read", operation.Scope) {
		_ = store.recordAuditEvent(ctx, principal, "operation:read", "denied", operationID, operation.Scope, requestID, correlationID, "forbidden", "")
		return nil, ember.ErrForbidden
	}
	return operation, nil
}

func (store *Store) DeleteResource(principal ember.Principal, resourceID, requestID, correlationID string) error {
	ctx, cancel := databaseContext(context.Background())
	defer cancel()
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return translateDatabaseError(err)
	}
	defer transaction.Rollback()
	resource, err := scanResource(transaction.QueryRowContext(ctx, resourceSelect+` WHERE id=$1`, resourceID))
	if err != nil {
		return err
	}
	if err := store.authorize(ctx, principal, "bucket:delete", resource.Scope, resourceID, requestID, correlationID); err != nil {
		return err
	}
	var lockCount int
	if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM locks WHERE resource_id=$1`, resourceID).Scan(&lockCount); err != nil {
		return translateDatabaseError(err)
	}
	if lockCount > 0 {
		_ = store.recordAuditEvent(ctx, principal, "resource:delete", "denied", resourceID, resource.Scope, requestID, correlationID, "locked", "")
		return ember.ErrResourceLocked
	}
	var childResourceCount int
	if err := transaction.QueryRowContext(ctx, `SELECT count(*) FROM resources WHERE parent_id=$1`, resourceID).Scan(&childResourceCount); err != nil {
		return translateDatabaseError(err)
	}
	if childResourceCount > 0 {
		return ember.ErrDependencyExists
	}
	if resource.Type == "Ember.Blob/bucket" {
		rows, queryErr := transaction.QueryContext(ctx, objectSelect+` WHERE bucket_id=$1`, resourceID)
		if queryErr != nil {
			return translateDatabaseError(queryErr)
		}
		for rows.Next() {
			objectVersion, scanErr := scanObject(rows)
			if scanErr != nil {
				rows.Close()
				return scanErr
			}
			if deleteErr := store.files.Delete(*objectVersion); deleteErr != nil {
				rows.Close()
				return deleteErr
			}
		}
		rows.Close()
		if _, deleteErr := transaction.ExecContext(ctx, `DELETE FROM blob_objects WHERE bucket_id=$1`, resourceID); deleteErr != nil {
			return translateDatabaseError(deleteErr)
		}
	}
	if _, err := transaction.ExecContext(ctx, `DELETE FROM resources WHERE id=$1`, resourceID); err != nil {
		return translateDatabaseError(err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO audit_events(id,principal,action,outcome,target,scope,request_id,correlation_id,policy_version) VALUES($1,$2,'resource:delete','succeeded',$3,$4,$5,$6,'phase1-v1')`, generateID("aud"), principal.Name, resourceID, resource.Scope, requestID, correlationID); err != nil {
		return translateDatabaseError(err)
	}
	return translateDatabaseError(transaction.Commit())
}
