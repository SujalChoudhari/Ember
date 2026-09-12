// Package secrets provides the explicit protected boundary for runtime secret
// values. Values are bounded and memory-only; ordinary Ember resource views do
// not depend on this package and cannot list or serialize its contents.
package secrets

import (
	"context"
	"errors"
	"strings"
	"sync"

	"github.com/SujalChoudhari/Ember/internal/ember/auth"
	"github.com/SujalChoudhari/Ember/internal/ember/operationcontext"
)

const (
	MaxSecrets         = 256
	MaxSecretNameSize  = 128
	MaxSecretValueSize = 4096
)

var (
	ErrInvalidStore         = errors.New("invalid secret store")
	ErrInvalidSecretRequest = errors.New("invalid secret request")
	ErrSecretNotFound       = errors.New("secret not found")
	ErrSecretBounds         = errors.New("secret store limit exceeded")
)

// Store is a bounded, non-listable in-memory secret store. The authorizer is
// required for every mutation and read, so callers must cross the explicit
// protected boundary before a value is returned.
type Store struct {
	mu         sync.RWMutex
	authorizer *auth.Authorizer
	values     map[string]string
}

func NewStore(authorizer *auth.Authorizer) (*Store, error) {
	if authorizer == nil {
		return nil, ErrInvalidStore
	}
	return &Store{authorizer: authorizer, values: make(map[string]string)}, nil
}

func validateRequest(ctx context.Context, scopeID, name string, identity operationcontext.Identity) error {
	if ctx == nil || strings.TrimSpace(scopeID) == "" || len(scopeID) > MaxSecretNameSize ||
		strings.TrimSpace(name) == "" || len(name) > MaxSecretNameSize {
		return ErrInvalidSecretRequest
	}
	if err := identity.Validate(); err != nil {
		return ErrInvalidSecretRequest
	}
	return nil
}

func secretKey(scopeID, name string) string {
	return scopeID + "\x00" + name
}

// Put stores or replaces one value after owner authorization. The value never
// enters an audit entry, snapshot, error, or diagnostic string.
func (store *Store) Put(ctx context.Context, principal auth.Principal, scopeID, name, value string, identity operationcontext.Identity) error {
	if err := validateRequest(ctx, scopeID, name, identity); err != nil || len(value) == 0 || len(value) > MaxSecretValueSize {
		return ErrInvalidSecretRequest
	}
	if err := store.authorizer.Authorize(ctx, principal, auth.ActionSecretWrite, scopeID, identity); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	key := secretKey(scopeID, name)
	if _, exists := store.values[key]; !exists && len(store.values) >= MaxSecrets {
		return ErrSecretBounds
	}
	store.values[key] = value
	return nil
}

// Get is the only value-returning boundary. Secret reads require the owner
// role and are audited by the authorizer without recording the returned value.
func (store *Store) Get(ctx context.Context, principal auth.Principal, scopeID, name string, identity operationcontext.Identity) (string, error) {
	if err := validateRequest(ctx, scopeID, name, identity); err != nil {
		return "", err
	}
	if err := store.authorizer.Authorize(ctx, principal, auth.ActionSecretRead, scopeID, identity); err != nil {
		return "", err
	}

	store.mu.RLock()
	value, exists := store.values[secretKey(scopeID, name)]
	store.mu.RUnlock()
	if !exists {
		return "", ErrSecretNotFound
	}
	return value, nil
}

// Delete removes one value after owner authorization. It is intentionally not
// exposed through ordinary resource, CLI, or HTTP list/get operations.
func (store *Store) Delete(ctx context.Context, principal auth.Principal, scopeID, name string, identity operationcontext.Identity) error {
	if err := validateRequest(ctx, scopeID, name, identity); err != nil {
		return err
	}
	if err := store.authorizer.Authorize(ctx, principal, auth.ActionSecretDelete, scopeID, identity); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	key := secretKey(scopeID, name)
	if _, exists := store.values[key]; !exists {
		return ErrSecretNotFound
	}
	delete(store.values, key)
	return nil
}

// String deliberately omits values so accidental diagnostics remain safe.
func (store *Store) String() string {
	return "secret store"
}
