package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"

	"ember.local/ember/internal/ember"
)

func (store *Store) recordAuditEvent(ctx context.Context, principal ember.Principal, action, outcome, target, scope, requestID, correlationID, reason, objectKey string) error {
	objectKeyHash := ""
	if objectKey != "" {
		checksum := sha256.Sum256([]byte(objectKey))
		objectKeyHash = hex.EncodeToString(checksum[:])
	}
	_, err := store.db.ExecContext(ctx, `INSERT INTO audit_events(id,principal,action,outcome,target,key_hash,scope,request_id,correlation_id,reason,policy_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, generateID("aud"), principal.Name, action, outcome, target, objectKeyHash, scope, requestID, correlationID, reason, "phase1-v1")
	return err
}

func (store *Store) authorize(ctx context.Context, principal ember.Principal, action, scope, target, requestID, correlationID string) error {
	if ember.Allowed(principal, action, scope) {
		return nil
	}
	_ = store.recordAuditEvent(ctx, principal, action, "denied", target, scope, requestID, correlationID, "forbidden", "")
	return ember.ErrForbidden
}
