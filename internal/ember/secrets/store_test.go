package secrets

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/auth"
	"github.com/SujalChoudhari/Ember/internal/ember/operationcontext"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

func TestStoreRequiresProtectedAccessAndKeepsAuditRedacted(t *testing.T) {
	ctx := context.Background()
	auditPath := filepath.Join(t.TempDir(), "audit.json")
	audit, err := persistence.NewFileAuditStore(auditPath)
	if err != nil {
		t.Fatalf("NewFileAuditStore() error = %v", err)
	}
	authorizer, err := auth.NewAuthorizer(audit)
	if err != nil {
		t.Fatalf("NewAuthorizer() error = %v", err)
	}
	if err := authorizer.RegisterScope(ctx, auth.Scope{ID: "scope-secrets"}); err != nil {
		t.Fatalf("RegisterScope() error = %v", err)
	}
	if err := authorizer.RegisterToken(ctx, "synthetic-owner-token", "owner"); err != nil {
		t.Fatalf("RegisterToken(owner) error = %v", err)
	}
	if err := authorizer.RegisterToken(ctx, "synthetic-reader-token", "reader"); err != nil {
		t.Fatalf("RegisterToken(reader) error = %v", err)
	}
	if err := authorizer.Grant(ctx, "owner", "scope-secrets", auth.RoleOwner); err != nil {
		t.Fatalf("Grant(owner) error = %v", err)
	}
	if err := authorizer.Grant(ctx, "reader", "scope-secrets", auth.RoleReader); err != nil {
		t.Fatalf("Grant(reader) error = %v", err)
	}
	owner, err := authorizer.Authenticate(ctx, "synthetic-owner-token")
	if err != nil {
		t.Fatalf("Authenticate(owner) error = %v", err)
	}
	reader, err := authorizer.Authenticate(ctx, "synthetic-reader-token")
	if err != nil {
		t.Fatalf("Authenticate(reader) error = %v", err)
	}
	store, err := NewStore(authorizer)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	identity := operationcontext.Identity{OperationID: "operation-secret", RequestID: "request-secret", CorrelationID: "correlation-secret"}
	secret := "synthetic-secret-" + t.Name()
	if err := store.Put(ctx, owner, "scope-secrets", "credential", secret, identity); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	got, err := store.Get(ctx, owner, "scope-secrets", "credential", identity)
	if err != nil || got != secret {
		t.Fatalf("Get(owner) = %q, %v; want protected value", got, err)
	}
	if _, err := store.Get(ctx, reader, "scope-secrets", "credential", identity); !errors.Is(err, auth.ErrAccessDenied) {
		t.Fatalf("Get(reader) error = %v, want auth.ErrAccessDenied", err)
	}
	if err := store.Delete(ctx, owner, "scope-secrets", "credential", identity); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := store.Get(ctx, owner, "scope-secrets", "credential", identity); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("Get(deleted) error = %v, want ErrSecretNotFound", err)
	}

	entries, err := audit.List(ctx, "scope-secrets", persistence.MaxAuditListLimit)
	if err != nil {
		t.Fatalf("List(audit) error = %v", err)
	}
	if len(entries) != 4 {
		t.Fatalf("audit entries = %d, want put/get/denied/delete evidence", len(entries))
	}
	var writeAllowed, readAllowed, readDenied, deleteAllowed bool
	for _, entry := range entries {
		switch {
		case entry.Action == "authorize:secret.write" && entry.Outcome == "allowed":
			writeAllowed = true
		case entry.Action == "authorize:secret.read" && entry.Outcome == "allowed":
			readAllowed = true
		case entry.Action == "authorize:secret.read" && entry.Outcome == "denied":
			readDenied = true
		case entry.Action == "authorize:secret.delete" && entry.Outcome == "allowed":
			deleteAllowed = true
		}
	}
	if !writeAllowed || !readAllowed || !readDenied || !deleteAllowed {
		t.Fatalf("secret access audit = %#v, want redacted allowed/denied evidence", entries)
	}
	auditData, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatalf("ReadFile(audit) error = %v", err)
	}
	if bytes.Contains(auditData, []byte(secret)) {
		t.Fatalf("audit snapshot contains secret value %q", secret)
	}
	if bytes.Contains([]byte(store.String()), []byte(secret)) {
		t.Fatalf("store diagnostics contain secret value: %s", store)
	}
}

func TestStoreBoundsAndInvalidInputs(t *testing.T) {
	authorizer, err := auth.NewAuthorizer(nil)
	if err != nil {
		t.Fatalf("NewAuthorizer() error = %v", err)
	}
	if _, err := NewStore(nil); !errors.Is(err, ErrInvalidStore) {
		t.Fatalf("NewStore(nil) error = %v, want ErrInvalidStore", err)
	}
	store, err := NewStore(authorizer)
	if err != nil {
		t.Fatalf("NewStore() error = %v", err)
	}
	if err := store.Put(context.Background(), auth.Principal{}, "", "credential", "value", operationcontext.Identity{}); !errors.Is(err, ErrInvalidSecretRequest) {
		t.Fatalf("Put(invalid) error = %v, want ErrInvalidSecretRequest", err)
	}
}
