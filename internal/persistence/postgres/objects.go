package postgres

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"time"

	"ember.local/ember/internal/ember"
)

func hashObjectRequest(objectKey, expectedSHA256 string, expectedLength int64) string {
	return hashBytes([]byte(objectKey + ":" + expectedSHA256 + ":" + fmt.Sprint(expectedLength)))
}

func (store *Store) PutObject(principal ember.Principal, bucketID, objectKey string, body []byte, expectedLength int64, expectedSHA256, idempotencyKey, requestID, correlationID string) (*ember.ObjectVersion, *ember.Operation, error) {
	return store.PutObjectStream(principal, bucketID, objectKey, bytes.NewReader(body), expectedLength, expectedSHA256, idempotencyKey, requestID, correlationID)
}

func (store *Store) PutObjectStream(principal ember.Principal, bucketID, objectKey string, body io.Reader, expectedLength int64, expectedSHA256, idempotencyKey, requestID, correlationID string) (*ember.ObjectVersion, *ember.Operation, error) {
	ctx, cancel := databaseContext(context.Background())
	defer cancel()
	bucketResource, err := store.GetResource(principal, bucketID, requestID, correlationID)
	if err != nil {
		return nil, nil, err
	}
	if bucketResource.Type != "Ember.Blob/bucket" {
		return nil, nil, ember.ErrNotFound
	}
	if err := store.authorize(ctx, principal, "object:put", bucketResource.Scope, bucketID, requestID, correlationID); err != nil {
		return nil, nil, err
	}
	if idempotencyKey == "" {
		return nil, nil, ember.ErrInvalidRequest
	}
	if err := ember.ValidateObjectKey(objectKey); err != nil {
		return nil, nil, err
	}
	requestHash := hashObjectRequest(objectKey, expectedSHA256, expectedLength)
	var existingRequestHash, existingOperationID string
	idempotencyEndpoint := "object-put|" + bucketID + "|" + objectKey
	err = store.db.QueryRowContext(ctx, `SELECT request_hash,operation_id FROM idempotency_records WHERE principal=$1 AND endpoint=$2 AND idem_key=$3 AND expires_at>now()`, principal.Name, idempotencyEndpoint, idempotencyKey).Scan(&existingRequestHash, &existingOperationID)
	if err == nil {
		if existingRequestHash != requestHash {
			return nil, nil, ember.ErrIdempotency
		}
		objectVersion, objectErr := scanObject(store.db.QueryRowContext(ctx, objectSelect+` WHERE bucket_id=$1 AND object_key=$2`, bucketID, objectKey))
		operation, operationErr := scanOperation(store.db.QueryRowContext(ctx, operationSelect+` WHERE id=$1`, existingOperationID))
		if objectErr != nil || operationErr != nil {
			return nil, nil, ember.ErrProvider
		}
		return objectVersion, operation, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, translateDatabaseError(err)
	}
	existingObject, _ := scanObject(store.db.QueryRowContext(ctx, objectSelect+` WHERE bucket_id=$1 AND object_key=$2`, bucketID, objectKey))
	objectVersion, err := store.files.WriteReader(bucketID, objectKey, body, expectedLength, expectedSHA256)
	if err != nil {
		findingID := generateID("finding")
		_, _ = store.db.ExecContext(ctx, `INSERT INTO repair_findings(id,kind,reference,status) VALUES($1,'upload',$2,'operator_action_required')`, findingID, bucketID+":"+objectKey)
		_ = store.recordAuditEvent(ctx, principal, "object:put", "failed", bucketID, bucketResource.Scope, requestID, correlationID, "safe_failure", objectKey)
		return nil, nil, err
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	defer transaction.Rollback()
	createdAt := time.Now().UTC()
	operation := ember.Operation{
		ID:            generateID("op"),
		Action:        "object:put",
		Status:        "succeeded",
		ResourceID:    bucketID,
		Scope:         bucketResource.Scope,
		RequestID:     requestID,
		CorrelationID: correlationID,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO operations(id,action,status,resource_id,scope,request_id,correlation_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8)`, operation.ID, operation.Action, operation.Status, operation.ResourceID, operation.Scope, operation.RequestID, operation.CorrelationID, operation.CreatedAt); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO blob_objects(bucket_id,object_key,version_id,sha256,etag,size,opaque_path,committed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(bucket_id,object_key) DO UPDATE SET version_id=excluded.version_id,sha256=excluded.sha256,etag=excluded.etag,size=excluded.size,opaque_path=excluded.opaque_path,committed_at=excluded.committed_at`, objectVersion.BucketID, objectVersion.Key, objectVersion.VersionID, objectVersion.SHA256, objectVersion.ETag, objectVersion.Size, objectVersion.Path, objectVersion.Committed); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO idempotency_records(principal,endpoint,idem_key,request_hash,operation_id,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, principal.Name, idempotencyEndpoint, idempotencyKey, requestHash, operation.ID, createdAt.Add(24*time.Hour)); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO audit_events(id,principal,action,outcome,target,key_hash,scope,request_id,correlation_id,policy_version) VALUES($1,$2,'object:put','succeeded',$3,$4,$5,$6,$7,'phase1-v1')`, generateID("aud"), principal.Name, bucketID, hashString(objectKey), bucketResource.Scope, requestID, correlationID); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	if err := transaction.Commit(); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	if existingObject != nil && existingObject.Path != objectVersion.Path {
		_ = store.files.Delete(*existingObject)
	}
	return &objectVersion, &operation, nil
}

func (store *Store) GetObject(principal ember.Principal, bucketID, objectKey, requestID, correlationID string) (*ember.ObjectVersion, []byte, error) {
	ctx, cancel := databaseContext(context.Background())
	defer cancel()
	bucketResource, err := store.GetResource(principal, bucketID, requestID, correlationID)
	if err != nil {
		return nil, nil, err
	}
	if err := store.authorize(ctx, principal, "read", bucketResource.Scope, bucketID, requestID, correlationID); err != nil {
		return nil, nil, err
	}
	objectVersion, err := scanObject(store.db.QueryRowContext(ctx, objectSelect+` WHERE bucket_id=$1 AND object_key=$2`, bucketID, objectKey))
	if err != nil {
		return nil, nil, err
	}
	objectBytes, err := store.files.Read(*objectVersion)
	if err != nil {
		_ = store.recordAuditEvent(ctx, principal, "object:get", "failed", bucketID, bucketResource.Scope, requestID, correlationID, "integrity", objectKey)
		return nil, nil, err
	}
	_ = store.recordAuditEvent(ctx, principal, "object:get", "succeeded", bucketID, bucketResource.Scope, requestID, correlationID, "", objectKey)
	return objectVersion, objectBytes, nil
}

func (store *Store) DeleteObject(principal ember.Principal, bucketID, objectKey, idempotencyKey, requestID, correlationID string) error {
	ctx, cancel := databaseContext(context.Background())
	defer cancel()
	bucketResource, err := store.GetResource(principal, bucketID, requestID, correlationID)
	if err != nil {
		return err
	}
	if err := store.authorize(ctx, principal, "object:delete", bucketResource.Scope, bucketID, requestID, correlationID); err != nil {
		return err
	}
	if idempotencyKey == "" {
		return ember.ErrInvalidRequest
	}
	requestHash := hashString(objectKey)
	idempotencyEndpoint := "object-delete|" + bucketID + "|" + objectKey
	var existingRequestHash string
	if err := store.db.QueryRowContext(ctx, `SELECT request_hash FROM idempotency_records WHERE principal=$1 AND endpoint=$2 AND idem_key=$3 AND expires_at>now()`, principal.Name, idempotencyEndpoint, idempotencyKey).Scan(&existingRequestHash); err == nil {
		if existingRequestHash != requestHash {
			return ember.ErrIdempotency
		}
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return translateDatabaseError(err)
	}
	objectVersion, err := scanObject(store.db.QueryRowContext(ctx, objectSelect+` WHERE bucket_id=$1 AND object_key=$2`, bucketID, objectKey))
	if err != nil {
		return err
	}
	if err := store.files.Delete(*objectVersion); err != nil {
		return err
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return translateDatabaseError(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `DELETE FROM blob_objects WHERE bucket_id=$1 AND object_key=$2`, bucketID, objectKey); err != nil {
		return translateDatabaseError(err)
	}
	operationID := generateID("op")
	if _, err := transaction.ExecContext(ctx, `INSERT INTO operations(id,action,status,resource_id,scope,request_id,correlation_id) VALUES($1,'object:delete','succeeded',$2,$3,$4,$5)`, operationID, bucketID, bucketResource.Scope, requestID, correlationID); err != nil {
		return translateDatabaseError(err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO idempotency_records(principal,endpoint,idem_key,request_hash,operation_id,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, principal.Name, idempotencyEndpoint, idempotencyKey, requestHash, operationID, time.Now().UTC().Add(24*time.Hour)); err != nil {
		return translateDatabaseError(err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO audit_events(id,principal,action,outcome,target,key_hash,scope,request_id,correlation_id,policy_version) VALUES($1,$2,'object:delete','succeeded',$3,$4,$5,$6,$7,'phase1-v1')`, generateID("aud"), principal.Name, bucketID, hashString(objectKey), bucketResource.Scope, requestID, correlationID); err != nil {
		return translateDatabaseError(err)
	}
	return translateDatabaseError(transaction.Commit())
}
