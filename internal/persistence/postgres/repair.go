package postgres

import (
	"context"

	"ember.local/ember/internal/ember"
)

func (store *Store) Repair(principal ember.Principal, findingID, action, requestID, correlationID string) error {
	ctx, cancel := databaseContext(context.Background())
	defer cancel()
	if principal.Role != "owner" {
		return ember.ErrForbidden
	}
	if action != "quarantine" && action != "discard" {
		return ember.ErrInvalidRequest
	}
	var findingKind, findingReference, findingStatus string
	if err := store.db.QueryRowContext(ctx, `SELECT kind,reference,status FROM repair_findings WHERE id=$1`, findingID).Scan(&findingKind, &findingReference, &findingStatus); err != nil {
		return translateDatabaseError(err)
	}
	if findingStatus != "operator_action_required" {
		return ember.ErrConflict
	}
	if action == "quarantine" {
		if err := store.files.MoveToQuarantine(findingReference); err != nil {
			return err
		}
	}
	newStatus := "operator_" + action
	if _, err := store.db.ExecContext(ctx, `UPDATE repair_findings SET status=$1 WHERE id=$2`, newStatus, findingID); err != nil {
		return translateDatabaseError(err)
	}
	return store.recordAuditEvent(ctx, principal, "repair:"+action, "succeeded", findingID, "*", requestID, correlationID, "operator_action", "")
}

func (store *Store) Audit(principal ember.Principal, scope string) ([]ember.AuditEvent, error) {
	ctx, cancel := databaseContext(context.Background())
	defer cancel()
	if !ember.Allowed(principal, "read", scope) {
		return nil, ember.ErrForbidden
	}
	rows, err := store.db.QueryContext(ctx, `SELECT id,principal,action,outcome,COALESCE(target,''),COALESCE(key_hash,''),scope,request_id,correlation_id,COALESCE(reason,''),policy_version,at FROM audit_events WHERE ($1='' OR scope=$1 OR scope LIKE $1||'/%') ORDER BY at,id`, scope)
	if err != nil {
		return nil, translateDatabaseError(err)
	}
	defer rows.Close()
	var auditEvents []ember.AuditEvent
	for rows.Next() {
		var auditEvent ember.AuditEvent
		if err := rows.Scan(&auditEvent.ID, &auditEvent.Principal, &auditEvent.Action, &auditEvent.Outcome, &auditEvent.Target, &auditEvent.KeyHash, &auditEvent.Scope, &auditEvent.RequestID, &auditEvent.CorrelationID, &auditEvent.Reason, &auditEvent.PolicyVersion, &auditEvent.At); err != nil {
			return nil, translateDatabaseError(err)
		}
		auditEvents = append(auditEvents, auditEvent)
	}
	return auditEvents, translateDatabaseError(rows.Err())
}

func (store *Store) Findings(principal ember.Principal) ([]ember.Finding, error) {
	ctx, cancel := databaseContext(context.Background())
	defer cancel()
	if !ember.Allowed(principal, "read", principal.Scope) {
		return nil, ember.ErrForbidden
	}
	rows, err := store.db.QueryContext(ctx, `SELECT id,kind,reference,status,created_at FROM repair_findings ORDER BY created_at,id`)
	if err != nil {
		return nil, translateDatabaseError(err)
	}
	defer rows.Close()
	var findings []ember.Finding
	for rows.Next() {
		var finding ember.Finding
		if err := rows.Scan(&finding.ID, &finding.Kind, &finding.Reference, &finding.Status, &finding.CreatedAt); err != nil {
			return nil, translateDatabaseError(err)
		}
		findings = append(findings, finding)
	}
	return findings, translateDatabaseError(rows.Err())
}
