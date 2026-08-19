package ember

import (
	"sort"
	"time"
)

func declarativeStateKey(scope, logicalID string) string {
	return scope + "\x00" + logicalID
}

func (store *Store) ListDeclarativeResources(principal Principal, scope string) ([]DeclarativeResourceState, error) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	if !inScope(principal, scope) || !allowed(principal.Role, "read") {
		return nil, ErrForbidden
	}
	states := make([]DeclarativeResourceState, 0)
	for _, state := range store.declarative {
		if scope == "" || state.Scope == scope {
			state.SpecJSON = append([]byte(nil), state.SpecJSON...)
			states = append(states, state)
		}
	}
	sort.Slice(states, func(left, right int) bool { return states[left].LogicalID < states[right].LogicalID })
	return states, nil
}

func (store *Store) ApplyDeclarativeResource(principal Principal, spec ResourceSpec, parentID, action, requestID, correlationID string) (*Resource, *Operation, error) {
	canonical, err := resourceCanonicalJSON(spec)
	if err != nil {
		return nil, nil, err
	}
	switch action {
	case string(PlanCreate):
		idempotencyKey := "declarative-create-" + spec.Scope + ":" + spec.ID
		var resource *Resource
		var operation *Operation
		if spec.Type == "resourceGroup" {
			resource, operation, err = store.CreateGroup(principal, spec.Name, spec.Scope, idempotencyKey, canonical, requestID, correlationID)
		} else {
			resource, operation, err = store.CreateBucket(principal, parentID, spec.Name, spec.Scope, idempotencyKey, canonical, requestID, correlationID)
		}
		if err != nil {
			return nil, nil, err
		}
		store.mu.Lock()
		if existing := store.resources[resource.ID]; existing != nil {
			existing.Tags = cloneResourceTags(spec.Tags)
			existing.UpdatedAt = time.Now().UTC()
			copyOfResource := *existing
			copyOfResource.Tags = cloneResourceTags(existing.Tags)
			resource = &copyOfResource
		}
		store.mu.Unlock()
		return resource, operation, nil
	case string(PlanUpdate):
		store.mu.Lock()
		defer store.mu.Unlock()
		state, exists := store.declarative[declarativeStateKey(spec.Scope, spec.ID)]
		if !exists {
			return nil, nil, ErrNotFound
		}
		resource, exists := store.resources[state.ResourceID]
		if !exists {
			return nil, nil, ErrNotFound
		}
		if err := store.authorizeLocked(principal, "resource:update", resource.Scope, resource.ID, requestID, correlationID); err != nil {
			return nil, nil, err
		}
		resource.Name = spec.Name
		resource.Type = spec.Type
		resource.ParentID = parentID
		resource.Scope = spec.Scope
		resource.Tags = cloneResourceTags(spec.Tags)
		resource.UpdatedAt = time.Now().UTC()
		operation := store.createOperationLocked(principal, "deploy:resource:update", resource.ID, resource.Scope, requestID, correlationID)
		store.recordAuditEventLocked(principal, "deploy:resource:update", "succeeded", resource.ID, resource.Scope, requestID, correlationID, "declarative_apply", "")
		copyOfResource := *resource
		copyOfResource.Tags = cloneResourceTags(resource.Tags)
		return &copyOfResource, operation, nil
	case string(PlanDelete):
		store.mu.RLock()
		state, exists := store.declarative[declarativeStateKey(spec.Scope, spec.ID)]
		store.mu.RUnlock()
		if !exists {
			return nil, nil, ErrNotFound
		}
		if err := store.DeleteResource(principal, state.ResourceID, requestID, correlationID); err != nil {
			return nil, nil, err
		}
		store.mu.Lock()
		operation := store.createOperationLocked(principal, "deploy:resource:delete", state.ResourceID, state.Scope, requestID, correlationID)
		store.recordAuditEventLocked(principal, "deploy:resource:delete", "succeeded", state.ResourceID, state.Scope, requestID, correlationID, "declarative_apply", "")
		store.mu.Unlock()
		return nil, operation, nil
	default:
		return nil, nil, ErrInvalidRequest
	}
}

func (store *Store) SaveDeclarativeState(principal Principal, state DeclarativeResourceState, requestID, correlationID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if !inScope(principal, state.Scope) || !allowed(principal.Role, "deployment:apply") {
		return ErrForbidden
	}
	state.SpecJSON = append([]byte(nil), state.SpecJSON...)
	store.declarative[declarativeStateKey(state.Scope, state.LogicalID)] = state
	return nil
}

func (store *Store) DeleteDeclarativeState(principal Principal, scope, logicalID, requestID, correlationID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	state, exists := store.declarative[declarativeStateKey(scope, logicalID)]
	if !exists {
		return ErrNotFound
	}
	if !inScope(principal, state.Scope) || !allowed(principal.Role, "deployment:apply") {
		return ErrForbidden
	}
	delete(store.declarative, declarativeStateKey(scope, logicalID))
	return nil
}
