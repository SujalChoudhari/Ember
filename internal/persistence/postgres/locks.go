package postgres

import (
	"context"

	"ember.local/ember/internal/ember"
)

func (store *Store) AddLock(principal ember.Principal, resourceID, lockKind, note, requestID, correlationID string) error {
	return store.mutateLock(principal, resourceID, lockKind, note, requestID, correlationID, true)
}

func (store *Store) RemoveLock(principal ember.Principal, resourceID, lockKind, requestID, correlationID string) error {
	return store.mutateLock(principal, resourceID, lockKind, "", requestID, correlationID, false)
}

func (store *Store) mutateLock(principal ember.Principal, resourceID, lockKind, note, requestID, correlationID string, shouldAddLock bool) error {
	ctx, cancel := databaseContext(context.Background())
	defer cancel()
	resource, err := store.GetResource(principal, resourceID, requestID, correlationID)
	if err != nil {
		return err
	}
	if lockKind == "" {
		return ember.ErrInvalidRequest
	}
	if err := store.authorize(ctx, principal, "lock:write", resource.Scope, resourceID, requestID, correlationID); err != nil {
		return err
	}
	var query string
	var queryArguments []any
	if shouldAddLock {
		query = `INSERT INTO locks(resource_id,kind,note) VALUES($1,$2,$3) ON CONFLICT(resource_id,kind) DO UPDATE SET note=excluded.note,created_at=now()`
		queryArguments = []any{resourceID, lockKind, note}
	} else {
		query = `DELETE FROM locks WHERE resource_id=$1 AND kind=$2`
		queryArguments = []any{resourceID, lockKind}
	}
	if _, err := store.db.ExecContext(ctx, query, queryArguments...); err != nil {
		return translateDatabaseError(err)
	}
	return store.recordAuditEvent(ctx, principal, "lock:write", "succeeded", resourceID, resource.Scope, requestID, correlationID, "", "")
}
