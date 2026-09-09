// Package auth provides bounded operator authentication and scope-aware RBAC.
package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/operationcontext"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

const (
	MaxSubjectLength       = 128
	MaxTokenLength         = 256
	MaxScopeIDLength       = 128
	MaxRegisteredTokens    = 256
	MaxRegisteredScopes    = 1024
	MaxRoleBindings        = 2048
	MaxAuthorizationAction = 32
)

var (
	ErrInvalidContext      = errors.New("invalid authorization context")
	ErrInvalidSubject      = errors.New("invalid operator subject")
	ErrInvalidToken        = errors.New("invalid operator token")
	ErrUnknownToken        = errors.New("unknown operator token")
	ErrUnauthenticated     = errors.New("operator is not authenticated")
	ErrInvalidScope        = errors.New("invalid authorization scope")
	ErrUnknownScope        = errors.New("unknown authorization scope")
	ErrDuplicateScope      = errors.New("duplicate authorization scope")
	ErrDuplicateToken      = errors.New("duplicate operator token")
	ErrInvalidRole         = errors.New("invalid authorization role")
	ErrUnknownSubject      = errors.New("unknown operator subject")
	ErrDuplicateBinding    = errors.New("duplicate role binding")
	ErrInvalidAction       = errors.New("invalid authorization action")
	ErrAccessDenied        = errors.New("authorization denied")
	ErrAuditUnavailable    = errors.New("authorization audit unavailable")
	ErrAuthorizationBounds = errors.New("authorization state limit exceeded")
)

type Role string

const (
	RoleReader      Role = "Reader"
	RoleContributor Role = "Contributor"
	RoleOwner       Role = "Owner"
)

type Action string

const (
	ActionRead   Action = "read"
	ActionWrite  Action = "write"
	ActionDelete Action = "delete"
	ActionAdmin  Action = "admin"
)

type Scope struct {
	ID       string
	ParentID string
}

type Principal struct {
	Subject string

	tokenDigest string
}

type roleBinding struct {
	Subject string
	ScopeID string
	Role    Role
}

type Authorizer struct {
	mu       sync.RWMutex
	audit    persistence.AuditStore
	tokens   map[string]string
	scopes   map[string]Scope
	bindings map[string]roleBinding
	now      func() time.Time
}

func NewAuthorizer(audit persistence.AuditStore) (*Authorizer, error) {
	return &Authorizer{
		audit:    audit,
		tokens:   make(map[string]string),
		scopes:   make(map[string]Scope),
		bindings: make(map[string]roleBinding),
		now:      time.Now,
	}, nil
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidContext
	}
	return ctx.Err()
}

func validateText(value string, max int, invalid error) error {
	if strings.TrimSpace(value) == "" || len([]byte(value)) > max {
		return invalid
	}
	return nil
}

func validateScope(scope Scope) error {
	if err := validateText(scope.ID, MaxScopeIDLength, ErrInvalidScope); err != nil {
		return err
	}
	if scope.ParentID != "" {
		if err := validateText(scope.ParentID, MaxScopeIDLength, ErrInvalidScope); err != nil {
			return err
		}
		if scope.ParentID == scope.ID {
			return ErrInvalidScope
		}
	}
	return nil
}

func validateRole(role Role) error {
	switch role {
	case RoleReader, RoleContributor, RoleOwner:
		return nil
	default:
		return ErrInvalidRole
	}
}

func validateAction(action Action) error {
	if len([]byte(action)) > MaxAuthorizationAction {
		return ErrInvalidAction
	}
	switch action {
	case ActionRead, ActionWrite, ActionDelete, ActionAdmin:
		return nil
	default:
		return ErrInvalidAction
	}
}

func tokenDigest(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func (authorizer *Authorizer) RegisterScope(ctx context.Context, scope Scope) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := validateScope(scope); err != nil {
		return err
	}

	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	if _, exists := authorizer.scopes[scope.ID]; exists {
		return ErrDuplicateScope
	}
	if len(authorizer.scopes) >= MaxRegisteredScopes {
		return ErrAuthorizationBounds
	}
	if scope.ParentID != "" {
		if _, exists := authorizer.scopes[scope.ParentID]; !exists {
			return ErrUnknownScope
		}
	}
	authorizer.scopes[scope.ID] = scope
	return nil
}

func (authorizer *Authorizer) RegisterToken(ctx context.Context, token, subject string) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := validateText(token, MaxTokenLength, ErrInvalidToken); err != nil {
		return err
	}
	if err := validateText(subject, MaxSubjectLength, ErrInvalidSubject); err != nil {
		return err
	}

	digest := tokenDigest(token)
	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	if current, exists := authorizer.tokens[digest]; exists {
		if current == subject {
			return nil
		}
		return ErrDuplicateToken
	}
	if len(authorizer.tokens) >= MaxRegisteredTokens {
		return ErrAuthorizationBounds
	}
	authorizer.tokens[digest] = subject
	return nil
}

func (authorizer *Authorizer) Grant(ctx context.Context, subject, scopeID string, role Role) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := validateText(subject, MaxSubjectLength, ErrInvalidSubject); err != nil {
		return err
	}
	if err := validateText(scopeID, MaxScopeIDLength, ErrInvalidScope); err != nil {
		return err
	}
	if err := validateRole(role); err != nil {
		return err
	}

	authorizer.mu.Lock()
	defer authorizer.mu.Unlock()
	knownSubject := false
	for _, registeredSubject := range authorizer.tokens {
		if registeredSubject == subject {
			knownSubject = true
			break
		}
	}
	if !knownSubject {
		return ErrUnknownSubject
	}
	if _, exists := authorizer.scopes[scopeID]; !exists {
		return ErrUnknownScope
	}
	key := bindingKey(subject, scopeID)
	if current, exists := authorizer.bindings[key]; exists {
		if current.Role == role {
			return nil
		}
		return ErrDuplicateBinding
	}
	if len(authorizer.bindings) >= MaxRoleBindings {
		return ErrAuthorizationBounds
	}
	authorizer.bindings[key] = roleBinding{Subject: subject, ScopeID: scopeID, Role: role}
	return nil
}

func (authorizer *Authorizer) Authenticate(ctx context.Context, token string) (Principal, error) {
	if err := contextError(ctx); err != nil {
		return Principal{}, err
	}
	if err := validateText(token, MaxTokenLength, ErrInvalidToken); err != nil {
		return Principal{}, err
	}
	digest := tokenDigest(token)

	authorizer.mu.RLock()
	subject, exists := authorizer.tokens[digest]
	authorizer.mu.RUnlock()
	if !exists {
		return Principal{}, ErrUnknownToken
	}
	return Principal{Subject: subject, tokenDigest: digest}, nil
}

func bindingKey(subject, scopeID string) string {
	return subject + "\x00" + scopeID
}

func roleAllows(role Role, action Action) bool {
	switch role {
	case RoleReader:
		return action == ActionRead
	case RoleContributor:
		return action == ActionRead || action == ActionWrite
	case RoleOwner:
		return true
	default:
		return false
	}
}

func (authorizer *Authorizer) allowsLocked(principal Principal, action Action, scopeID string) bool {
	visited := make(map[string]struct{})
	for currentScope := scopeID; currentScope != ""; {
		if _, seen := visited[currentScope]; seen {
			return false
		}
		visited[currentScope] = struct{}{}
		if binding, exists := authorizer.bindings[bindingKey(principal.Subject, currentScope)]; exists && roleAllows(binding.Role, action) {
			return true
		}
		scope, exists := authorizer.scopes[currentScope]
		if !exists {
			return false
		}
		currentScope = scope.ParentID
	}
	return false
}

func accessAuditID(principal Principal, action Action, scopeID, outcome string, identity operationcontext.Identity) string {
	value := principal.Subject + "\x00" + string(action) + "\x00" + scopeID + "\x00" + outcome + "\x00" + identity.OperationID + "\x00" + identity.RequestID
	digest := sha256.Sum256([]byte(value))
	return "authz-" + hex.EncodeToString(digest[:])
}

func (authorizer *Authorizer) auditAccess(ctx context.Context, principal Principal, action Action, scopeID, outcome string, identity operationcontext.Identity) error {
	if authorizer.audit == nil {
		return nil
	}
	entry := models.AuditEntry{
		ID:            accessAuditID(principal, action, scopeID, outcome, identity),
		OperationID:   identity.OperationID,
		ResourceID:    scopeID,
		ScopeID:       scopeID,
		CorrelationID: identity.CorrelationID,
		RequestID:     identity.RequestID,
		Action:        "authorize:" + string(action),
		Outcome:       outcome,
		CreatedAt:     authorizer.now(),
	}
	// Keep the audit construction attribution-only and avoid exposing the
	// credential or any provider payload in the denial record.
	return authorizer.audit.Append(ctx, entry)
}

func (authorizer *Authorizer) Authorize(ctx context.Context, principal Principal, action Action, scopeID string, identity operationcontext.Identity) error {
	if err := contextError(ctx); err != nil {
		return err
	}
	if err := validateAction(action); err != nil {
		return err
	}
	if err := validateText(scopeID, MaxScopeIDLength, ErrInvalidScope); err != nil {
		return err
	}
	if err := identity.Validate(); err != nil {
		return err
	}

	authorizer.mu.RLock()
	subject, authenticated := authorizer.tokens[principal.tokenDigest]
	allowed := authenticated && subject == principal.Subject && authorizer.allowsLocked(principal, action, scopeID)
	authorizer.mu.RUnlock()
	if allowed {
		if action != ActionRead {
			if err := authorizer.auditAccess(ctx, principal, action, scopeID, "allowed", identity); err != nil && !errors.Is(err, persistence.ErrDuplicateAuditEntry) {
				return errors.Join(ErrAuditUnavailable, fmt.Errorf("privileged authorization audit: %w", err))
			}
		}
		return nil
	}
	if !authenticated || subject != principal.Subject {
		return ErrUnauthenticated
	}
	if err := authorizer.auditAccess(ctx, principal, action, scopeID, "denied", identity); err != nil && !errors.Is(err, persistence.ErrDuplicateAuditEntry) {
		return errors.Join(ErrAccessDenied, fmt.Errorf("%w: %v", ErrAuditUnavailable, err))
	}
	return ErrAccessDenied
}
