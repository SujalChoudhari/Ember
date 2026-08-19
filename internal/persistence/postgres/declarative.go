package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ember.local/ember/internal/ember"
)

func (store *Store) ListDeclarativeResources(principal ember.Principal, scope string) ([]ember.DeclarativeResourceState, error) {
	ctx, cancel := databaseContext(context.Background())
	defer cancel()
	if !ember.Allowed(principal, "read", scope) {
		return nil, ember.ErrForbidden
	}
	rows, err := store.db.QueryContext(ctx, `SELECT logical_id,resource_id,api_version,type,scope,parent_id,spec_hash,spec_json,lifecycle FROM declarative_states WHERE scope=$1 ORDER BY logical_id`, scope)
	if err != nil {
		return nil, translateDatabaseError(err)
	}
	defer rows.Close()
	states := make([]ember.DeclarativeResourceState, 0)
	for rows.Next() {
		var state ember.DeclarativeResourceState
		var parentID sql.NullString
		var lifecycleJSON []byte
		if err := rows.Scan(&state.LogicalID, &state.ResourceID, &state.APIVersion, &state.Type, &state.Scope, &parentID, &state.SpecHash, &state.SpecJSON, &lifecycleJSON); err != nil {
			return nil, translateDatabaseError(err)
		}
		if parentID.Valid {
			state.ParentID = parentID.String
		}
		if len(lifecycleJSON) > 0 {
			if err := json.Unmarshal(lifecycleJSON, &state.Lifecycle); err != nil {
				return nil, fmt.Errorf("decode declarative lifecycle: %w", err)
			}
		}
		states = append(states, state)
	}
	if err := rows.Err(); err != nil {
		return nil, translateDatabaseError(err)
	}
	return states, nil
}

func (store *Store) ApplyDeclarativeResource(principal ember.Principal, spec ember.ResourceSpec, parentID, action, requestID, correlationID string) (*ember.Resource, *ember.Operation, error) {
	canonical, err := json.Marshal(spec)
	if err != nil {
		return nil, nil, fmt.Errorf("encode declarative resource: %w", err)
	}
	switch action {
	case string(ember.PlanCreate):
		idempotencyKey := "declarative-create-" + spec.Scope + ":" + spec.ID
		if spec.Type == "resourceGroup" {
			return store.CreateGroup(principal, spec.Name, spec.Scope, idempotencyKey, canonical, requestID, correlationID)
		}
		return store.CreateBucket(principal, parentID, spec.Name, spec.Scope, idempotencyKey, canonical, requestID, correlationID)
	case string(ember.PlanUpdate):
		return store.updateDeclarativeResource(principal, spec, parentID, requestID, correlationID)
	case string(ember.PlanDelete):
		return store.deleteDeclarativeResource(principal, spec, requestID, correlationID)
	default:
		return nil, nil, ember.ErrInvalidRequest
	}
}

func (store *Store) updateDeclarativeResource(principal ember.Principal, spec ember.ResourceSpec, parentID, requestID, correlationID string) (*ember.Resource, *ember.Operation, error) {
	ctx, cancel := databaseContext(context.Background())
	defer cancel()
	state, err := store.declarativeState(ctx, spec.ID, spec.Scope)
	if err != nil {
		return nil, nil, err
	}
	resource, err := scanResource(store.db.QueryRowContext(ctx, resourceSelect+` WHERE id=$1`, state.ResourceID))
	if err != nil {
		return nil, nil, err
	}
	if !ember.Allowed(principal, "resource:update", resource.Scope) {
		_ = store.recordAuditEvent(ctx, principal, "deploy:resource:update", "denied", resource.ID, resource.Scope, requestID, correlationID, "forbidden", "")
		return nil, nil, ember.ErrForbidden
	}
	tagsJSON, err := json.Marshal(spec.Tags)
	if err != nil {
		return nil, nil, fmt.Errorf("encode declarative tags: %w", err)
	}
	transaction, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	defer transaction.Rollback()
	if _, err := transaction.ExecContext(ctx, `UPDATE resources SET name=$1,type=$2,parent_id=NULLIF($3,''),scope=$4,tags=$5,updated_at=now() WHERE id=$6`, spec.Name, spec.Type, parentID, spec.Scope, tagsJSON, resource.ID); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	createdAt := time.Now().UTC()
	operation := ember.Operation{ID: generateID("op"), Action: "deploy:resource:update", Status: "succeeded", ResourceID: resource.ID, Scope: resource.Scope, RequestID: requestID, CorrelationID: correlationID, CreatedAt: createdAt, UpdatedAt: createdAt}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO operations(id,action,status,resource_id,scope,request_id,correlation_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8)`, operation.ID, operation.Action, operation.Status, operation.ResourceID, operation.Scope, operation.RequestID, operation.CorrelationID, createdAt); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	if _, err := transaction.ExecContext(ctx, `INSERT INTO audit_events(id,principal,action,outcome,target,scope,request_id,correlation_id,reason,policy_version) VALUES($1,$2,$3,'succeeded',$4,$5,$6,$7,'declarative_apply','phase2-v1')`, generateID("aud"), principal.Name, operation.Action, resource.ID, resource.Scope, requestID, correlationID); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	if err := transaction.Commit(); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	updated, err := scanResource(store.db.QueryRowContext(ctx, resourceSelect+` WHERE id=$1`, resource.ID))
	if err != nil {
		return nil, nil, err
	}
	return updated, &operation, nil
}

func (store *Store) deleteDeclarativeResource(principal ember.Principal, spec ember.ResourceSpec, requestID, correlationID string) (*ember.Resource, *ember.Operation, error) {
	ctx, cancel := databaseContext(context.Background())
	defer cancel()
	state, err := store.declarativeState(ctx, spec.ID, spec.Scope)
	if err != nil {
		return nil, nil, err
	}
	if err := store.DeleteResource(principal, state.ResourceID, requestID, correlationID); err != nil {
		return nil, nil, err
	}
	createdAt := time.Now().UTC()
	operation := &ember.Operation{ID: generateID("op"), Action: "deploy:resource:delete", Status: "succeeded", ResourceID: state.ResourceID, Scope: state.Scope, RequestID: requestID, CorrelationID: correlationID, CreatedAt: createdAt, UpdatedAt: createdAt}
	if _, err := store.db.ExecContext(ctx, `INSERT INTO operations(id,action,status,resource_id,scope,request_id,correlation_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8)`, operation.ID, operation.Action, operation.Status, operation.ResourceID, operation.Scope, operation.RequestID, operation.CorrelationID, createdAt); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	if err := store.recordAuditEvent(ctx, principal, operation.Action, "succeeded", state.ResourceID, state.Scope, requestID, correlationID, "declarative_apply", ""); err != nil {
		return nil, nil, translateDatabaseError(err)
	}
	return nil, operation, nil
}

func (store *Store) SaveDeclarativeState(principal ember.Principal, state ember.DeclarativeResourceState, requestID, correlationID string) error {
	ctx, cancel := databaseContext(context.Background())
	defer cancel()
	if !ember.Allowed(principal, "deployment:apply", state.Scope) {
		return ember.ErrForbidden
	}
	lifecycleJSON, err := json.Marshal(state.Lifecycle)
	if err != nil {
		return fmt.Errorf("encode declarative lifecycle: %w", err)
	}
	_, err = store.db.ExecContext(ctx, `INSERT INTO declarative_states(logical_id,resource_id,api_version,type,scope,parent_id,spec_hash,spec_json,lifecycle) VALUES($1,$2,$3,$4,$5,NULLIF($6,''),$7,$8,$9) ON CONFLICT(scope,logical_id) DO UPDATE SET resource_id=excluded.resource_id,api_version=excluded.api_version,type=excluded.type,parent_id=excluded.parent_id,spec_hash=excluded.spec_hash,spec_json=excluded.spec_json,lifecycle=excluded.lifecycle,updated_at=now()`, state.LogicalID, state.ResourceID, state.APIVersion, state.Type, state.Scope, state.ParentID, state.SpecHash, state.SpecJSON, lifecycleJSON)
	return translateDatabaseError(err)
}

func (store *Store) DeleteDeclarativeState(principal ember.Principal, scope, logicalID, requestID, correlationID string) error {
	ctx, cancel := databaseContext(context.Background())
	defer cancel()
	if err := store.db.QueryRowContext(ctx, `SELECT scope FROM declarative_states WHERE logical_id=$1 AND scope=$2`, logicalID, scope).Scan(&scope); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// Resource deletion cascades the declarative state row. The engine calls
			// this cleanup after a successful resource delete, so that is already
			// the desired durable result.
			return nil
		}
		return translateDatabaseError(err)
	}
	if !ember.Allowed(principal, "resource:update", scope) {
		return ember.ErrForbidden
	}
	_, err := store.db.ExecContext(ctx, `DELETE FROM declarative_states WHERE logical_id=$1 AND scope=$2`, logicalID, scope)
	return translateDatabaseError(err)
}

func (store *Store) declarativeState(ctx context.Context, logicalID, scope string) (ember.DeclarativeResourceState, error) {
	var state ember.DeclarativeResourceState
	var parentID sql.NullString
	var lifecycleJSON []byte
	err := store.db.QueryRowContext(ctx, `SELECT logical_id,resource_id,api_version,type,scope,parent_id,spec_hash,spec_json,lifecycle FROM declarative_states WHERE logical_id=$1 AND scope=$2`, logicalID, scope).Scan(&state.LogicalID, &state.ResourceID, &state.APIVersion, &state.Type, &state.Scope, &parentID, &state.SpecHash, &state.SpecJSON, &lifecycleJSON)
	if err != nil {
		return state, translateDatabaseError(err)
	}
	if parentID.Valid {
		state.ParentID = parentID.String
	}
	if len(lifecycleJSON) > 0 {
		if err := json.Unmarshal(lifecycleJSON, &state.Lifecycle); err != nil {
			return state, fmt.Errorf("decode declarative lifecycle: %w", err)
		}
	}
	return state, nil
}
