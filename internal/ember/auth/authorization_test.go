package auth

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/operationcontext"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

func TestAuthorizerAuthenticatesAndEnforcesInheritedScopedRoles(t *testing.T) {
	ctx := context.Background()
	auditPath := filepath.Join(t.TempDir(), "audit.json")
	audit, err := persistence.NewFileAuditStore(auditPath)
	if err != nil {
		t.Fatalf("NewFileAuditStore() error = %v", err)
	}
	authorizer, err := NewAuthorizer(audit)
	if err != nil {
		t.Fatalf("NewAuthorizer() error = %v", err)
	}
	if err := authorizer.RegisterScope(ctx, Scope{ID: "scope-root"}); err != nil {
		t.Fatalf("RegisterScope(root) error = %v", err)
	}
	if err := authorizer.RegisterScope(ctx, Scope{ID: "scope-child", ParentID: "scope-root"}); err != nil {
		t.Fatalf("RegisterScope(child) error = %v", err)
	}
	if err := authorizer.RegisterScope(ctx, Scope{ID: "scope-other"}); err != nil {
		t.Fatalf("RegisterScope(other) error = %v", err)
	}
	if err := authorizer.RegisterToken(ctx, "synthetic-reader-token", "operator-a"); err != nil {
		t.Fatalf("RegisterToken() error = %v", err)
	}
	if err := authorizer.Grant(ctx, "operator-a", "scope-root", RoleReader); err != nil {
		t.Fatalf("Grant() error = %v", err)
	}

	principal, err := authorizer.Authenticate(ctx, "synthetic-reader-token")
	if err != nil {
		t.Fatalf("Authenticate() error = %v", err)
	}
	identity := operationcontext.Identity{OperationID: "operation-1", RequestID: "request-1", CorrelationID: "correlation-1"}
	if err := authorizer.Authorize(ctx, principal, ActionRead, "scope-child", identity); err != nil {
		t.Fatalf("Authorize(inherited read) error = %v", err)
	}
	if err := authorizer.Authorize(ctx, principal, ActionWrite, "scope-child", identity); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("Authorize(write) error = %v, want ErrAccessDenied", err)
	}
	if err := authorizer.Authorize(ctx, principal, ActionRead, "scope-other", identity); !errors.Is(err, ErrAccessDenied) {
		t.Fatalf("Authorize(cross-scope read) error = %v, want ErrAccessDenied", err)
	}
	if err := authorizer.RegisterToken(ctx, "synthetic-contributor-token", "operator-b"); err != nil {
		t.Fatalf("RegisterToken(contributor) error = %v", err)
	}
	if err := authorizer.Grant(ctx, "operator-b", "scope-child", RoleContributor); err != nil {
		t.Fatalf("Grant(contributor) error = %v", err)
	}
	contributor, err := authorizer.Authenticate(ctx, "synthetic-contributor-token")
	if err != nil {
		t.Fatalf("Authenticate(contributor) error = %v", err)
	}
	privilegedIdentity := operationcontext.Identity{OperationID: "operation-2", RequestID: "request-2", CorrelationID: "correlation-2"}
	if err := authorizer.Authorize(ctx, contributor, ActionWrite, "scope-child", privilegedIdentity); err != nil {
		t.Fatalf("Authorize(privileged write) error = %v", err)
	}

	entries, err := audit.List(ctx, "scope-child", persistence.MaxAuditListLimit)
	if err != nil {
		t.Fatalf("List(child audit) error = %v", err)
	}
	denialFound := false
	allowedFound := false
	for _, entry := range entries {
		denialFound = denialFound || (entry.Action == "authorize:write" && entry.Outcome == "denied")
		allowedFound = allowedFound || (entry.Action == "authorize:write" && entry.Outcome == "allowed")
	}
	if len(entries) != 2 || !denialFound || !allowedFound {
		t.Fatalf("child access audit = %#v, want denied and allowed write records", entries)
	}
	entries, err = audit.List(ctx, "scope-other", persistence.MaxAuditListLimit)
	if err != nil {
		t.Fatalf("List(other audit) error = %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "authorize:read" || entries[0].Outcome != "denied" {
		t.Fatalf("cross-scope denial audit = %#v, want one redacted denial", entries)
	}

	reopened, err := persistence.NewFileAuditStore(auditPath)
	if err != nil {
		t.Fatalf("NewFileAuditStore(reopen) error = %v", err)
	}
	entries, err = reopened.List(ctx, "", persistence.MaxAuditListLimit)
	if err != nil {
		t.Fatalf("List(reopened audit) error = %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("reopened denial audit count = %d, want 3", len(entries))
	}
	auditData, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("ReadFile(audit) error = %v", err)
	}
	if bytes.Contains(auditData, []byte("synthetic-reader-token")) {
		t.Fatal("audit snapshot contains the credential token")
	}
}

func TestAuthorizerRejectsUnknownTokensAndInvalidIdentity(t *testing.T) {
	ctx := context.Background()
	authorizer, err := NewAuthorizer(nil)
	if err != nil {
		t.Fatalf("NewAuthorizer(nil) error = %v", err)
	}
	if _, err := authorizer.Authenticate(ctx, "unknown-token"); !errors.Is(err, ErrUnknownToken) {
		t.Fatalf("Authenticate(unknown) error = %v, want ErrUnknownToken", err)
	}
	if err := authorizer.RegisterToken(ctx, "synthetic-token", "operator-a"); err != nil {
		t.Fatalf("RegisterToken() error = %v", err)
	}
	if _, err := authorizer.Authenticate(ctx, ""); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("Authenticate(blank) error = %v, want ErrInvalidToken", err)
	}
	if err := authorizer.RegisterScope(ctx, Scope{ID: "scope-root"}); err != nil {
		t.Fatalf("RegisterScope() error = %v", err)
	}
	if err := authorizer.Grant(ctx, "operator-a", "scope-root", RoleReader); err != nil {
		t.Fatalf("Grant() error = %v", err)
	}
	principal, err := authorizer.Authenticate(ctx, "synthetic-token")
	if err != nil {
		t.Fatalf("Authenticate(valid) error = %v", err)
	}
	invalidIdentity := operationcontext.Identity{}
	if err := authorizer.Authorize(ctx, principal, ActionRead, "scope-root", invalidIdentity); !errors.Is(err, operationcontext.ErrInvalidIdentity) {
		t.Fatalf("Authorize(invalid identity) error = %v, want ErrInvalidIdentity", err)
	}
}
