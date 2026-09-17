package ember

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

func TestOperatorTenantLifecycleAndScopeIsolation(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	operator, err := NewFileOperator(root, 64)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}

	alpha, err := operator.CreateTenant(ctx, OperatorPrincipal{}, models.Tenant{ID: "alpha", DisplayName: "Alpha"})
	if err != nil {
		t.Fatalf("CreateTenant(alpha) error = %v", err)
	}
	beta, err := operator.CreateTenant(ctx, OperatorPrincipal{}, models.Tenant{ID: "beta", DisplayName: "Beta"})
	if err != nil {
		t.Fatalf("CreateTenant(beta) error = %v", err)
	}
	if _, err := operator.GetTenant(ctx, OperatorPrincipal{ScopeID: alpha.ID}, beta.ID); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("GetTenant(cross-scope) error = %v, want ErrOperatorScopeDenied", err)
	}
	if _, err := operator.ListTenants(ctx, OperatorPrincipal{ScopeID: alpha.ID}); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("ListTenants(scoped) error = %v, want ErrOperatorScopeDenied", err)
	}
	if _, err := operator.OpenTenant(ctx, OperatorPrincipal{ScopeID: alpha.ID}, beta.ID); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("OpenTenant(cross-scope) error = %v, want ErrOperatorScopeDenied", err)
	}

	opened, err := operator.OpenTenant(ctx, OperatorPrincipal{ScopeID: alpha.ID}, alpha.ID)
	if err != nil {
		t.Fatalf("OpenTenant(own scope) error = %v", err)
	}
	if opened.ID() != alpha.ID {
		t.Fatalf("OpenTenant(own scope) ID = %q, want %q", opened.ID(), alpha.ID)
	}
	if err := opened.Close(); err != nil {
		t.Fatalf("TenantDatabase.Close() error = %v", err)
	}

	if err := operator.DeleteTenant(ctx, OperatorPrincipal{}, alpha.ID, false); !errors.Is(err, ErrDestructiveConfirmationRequired) {
		t.Fatalf("DeleteTenant(without confirmation) error = %v, want confirmation error", err)
	}
	if err := operator.DeleteTenant(ctx, OperatorPrincipal{}, beta.ID, true); err != nil {
		t.Fatalf("DeleteTenant(beta) error = %v", err)
	}
	if _, err := operator.GetTenant(ctx, OperatorPrincipal{}, beta.ID); !errors.Is(err, persistence.ErrTenantNotFound) {
		t.Fatalf("GetTenant(deleted beta) error = %v, want ErrTenantNotFound", err)
	}

	if _, err := NewFileOperator(root, 64); err != nil {
		t.Fatalf("restart NewFileOperator() error = %v", err)
	}
	persisted, err := operator.GetTenant(ctx, OperatorPrincipal{}, alpha.ID)
	if err != nil {
		t.Fatalf("GetTenant(persisted alpha) error = %v", err)
	}
	if persisted.ID != alpha.ID || persisted.DisplayName != alpha.DisplayName || !persisted.CreatedAt.Equal(alpha.CreatedAt) {
		t.Fatalf("persisted alpha = %#v, want %#v", persisted, alpha)
	}
	if err := operator.Reset(ctx, OperatorPrincipal{}); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if _, err := operator.GetTenant(ctx, OperatorPrincipal{}, alpha.ID); !errors.Is(err, persistence.ErrTenantNotFound) {
		t.Fatalf("GetTenant(after reset) error = %v, want ErrTenantNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(root, "tenants", alpha.ID, "tenant.db")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("tenant database after reset error = %v, want os.ErrNotExist", err)
	}
}

func TestRunCLISupportsTenantLifecycleAndConfirmation(t *testing.T) {
	operator, err := NewFileOperator(filepath.Join(t.TempDir(), "state"), 64)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}
	ctx := context.Background()
	var output bytes.Buffer
	if err := RunCLI(ctx, operator, []string{"tenant", "create", "--id", "cli-alpha", "--name", "CLI Alpha"}, &output); err != nil {
		t.Fatalf("tenant create error = %v", err)
	}
	var created OperatorResponse
	if err := json.Unmarshal(output.Bytes(), &created); err != nil {
		t.Fatalf("decode tenant create output %q error = %v", output.String(), err)
	}
	if created.Tenant == nil || created.Tenant.ID != "cli-alpha" {
		t.Fatalf("tenant create response = %#v, want cli-alpha", created)
	}

	output.Reset()
	if err := RunCLI(ctx, operator, []string{"tenant", "list"}, &output); err != nil {
		t.Fatalf("tenant list error = %v", err)
	}
	var listed OperatorResponse
	if err := json.Unmarshal(output.Bytes(), &listed); err != nil {
		t.Fatalf("decode tenant list output %q error = %v", output.String(), err)
	}
	if len(listed.Tenants) != 1 || listed.Tenants[0].ID != "cli-alpha" {
		t.Fatalf("tenant list response = %#v, want cli-alpha", listed)
	}

	output.Reset()
	if err := RunCLI(ctx, operator, []string{"tenant", "delete", "--id", "cli-alpha"}, &output); !errors.Is(err, ErrDestructiveConfirmationRequired) {
		t.Fatalf("tenant delete without confirmation error = %v, want confirmation error", err)
	}
	if err := RunCLI(ctx, operator, []string{"tenant", "delete", "--id", "cli-alpha", "--confirm"}, &output); err != nil {
		t.Fatalf("tenant delete with confirmation error = %v", err)
	}
}
