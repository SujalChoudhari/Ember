package ember

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func TestTenantResourceReleaseGatePreservesIsolationAcrossRestoreAndWeb(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "state")
	operator, err := NewFileOperator(root, 64<<20)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}

	for _, tenant := range []models.Tenant{
		{ID: "alpha", DisplayName: "Alpha"},
		{ID: "beta", DisplayName: "Beta"},
	} {
		if _, err := operator.CreateTenant(ctx, OperatorPrincipal{}, tenant); err != nil {
			t.Fatalf("CreateTenant(%q) error = %v", tenant.ID, err)
		}
	}
	platform, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{
		Type: models.ResourceTypeGroup, Name: "platform-root",
	})
	if err != nil {
		t.Fatalf("CreateResource(platform) error = %v", err)
	}
	alphaResource, err := operator.CreateResource(ctx, OperatorPrincipal{TenantID: "alpha"}, models.ResourceSpec{
		Type: models.ResourceTypeGroup, Name: "shared",
	})
	if err != nil {
		t.Fatalf("CreateResource(alpha) error = %v", err)
	}
	betaResource, err := operator.CreateResource(ctx, OperatorPrincipal{TenantID: "beta"}, models.ResourceSpec{
		Type: models.ResourceTypeGroup, Name: "shared",
	})
	if err != nil {
		t.Fatalf("CreateResource(beta) error = %v", err)
	}
	if alphaResource.ID == betaResource.ID {
		t.Fatalf("tenant resource IDs = %q and %q, want isolated IDs", alphaResource.ID, betaResource.ID)
	}
	if _, err := operator.GetResource(ctx, OperatorPrincipal{TenantID: "beta"}, alphaResource.ID); !errors.Is(err, ErrOperatorScopeDenied) {
		t.Fatalf("cross-tenant GetResource() error = %v, want scope denial", err)
	}

	for _, path := range []string{
		filepath.Join(root, "platform.db"),
		filepath.Join(root, "tenants", "alpha", "tenant.db"),
		filepath.Join(root, "tenants", "beta", "tenant.db"),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("owned SQLite path %q stat error = %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "resources.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy resource snapshot stat error = %v, want os.ErrNotExist", err)
	}
	if err := operator.Close(); err != nil {
		t.Fatalf("Operator.Close() before backup error = %v", err)
	}

	restoredRoot := filepath.Join(t.TempDir(), "restored")
	if err := copyOwnedTree(root, restoredRoot); err != nil {
		t.Fatalf("copy closed state for backup/restore error = %v", err)
	}
	reopened, err := NewFileOperator(restoredRoot, 64<<20)
	if err != nil {
		t.Fatalf("NewFileOperator(restored) error = %v", err)
	}
	defer func() {
		if err := reopened.Close(); err != nil {
			t.Errorf("restored Operator.Close() error = %v", err)
		}
	}()
	if got, err := reopened.GetResource(ctx, OperatorPrincipal{}, platform.ID); err != nil || got.Spec.Name != "platform-root" {
		t.Fatalf("restored platform resource = %#v, %v", got, err)
	}
	if got, err := reopened.GetResource(ctx, OperatorPrincipal{TenantID: "alpha"}, alphaResource.ID); err != nil || got.Spec.Name != "shared" {
		t.Fatalf("restored alpha resource = %#v, %v", got, err)
	}
	if got, err := reopened.GetResource(ctx, OperatorPrincipal{TenantID: "beta"}, betaResource.ID); err != nil || got.Spec.Name != "shared" {
		t.Fatalf("restored beta resource = %#v, %v", got, err)
	}

	handler := NewWebHandler(reopened)
	platformPage := webRequest(t, handler, http.MethodGet, "/", nil)
	if platformPage.Code != http.StatusOK || !strings.Contains(platformPage.Body.String(), "Platform management") || !strings.Contains(platformPage.Body.String(), "platform-root") {
		t.Fatalf("restored platform page = %d %q, want platform resource management", platformPage.Code, platformPage.Body.String())
	}
	if !strings.Contains(platformPage.Body.String(), "name=\"viewport\"") {
		t.Fatalf("restored platform page missing mobile viewport: %q", platformPage.Body.String())
	}
	style := webRequest(t, handler, http.MethodGet, "/static/style.css", nil)
	if style.Code != http.StatusOK || !strings.Contains(style.Body.String(), "@media (max-width: 44rem)") || !strings.Contains(style.Body.String(), "@media (max-width: 24rem)") {
		t.Fatalf("mobile stylesheet = %d %q, want responsive breakpoints", style.Code, style.Body.String())
	}

	createdTenant := webForm(t, handler, http.MethodPost, "/tenants", url.Values{"id": {"gamma"}, "displayName": {"Gamma"}})
	if createdTenant.Code != http.StatusSeeOther || createdTenant.Header().Get("Location") != "/tenants/gamma" {
		t.Fatalf("POST /tenants = %d location %q, want gamma redirect", createdTenant.Code, createdTenant.Header().Get("Location"))
	}
	createdResource := webForm(t, handler, http.MethodPost, "/tenants/gamma/resources", url.Values{"type": {"group"}, "name": {"web-resource"}, "desiredState": {"ready"}})
	if createdResource.Code != http.StatusSeeOther || !strings.HasPrefix(createdResource.Header().Get("Location"), "/tenants/gamma/resources/") {
		t.Fatalf("POST gamma resource = %d location %q, want inspectable resource redirect", createdResource.Code, createdResource.Header().Get("Location"))
	}
	gammaPage := webRequest(t, handler, http.MethodGet, "/tenants/gamma", nil)
	if gammaPage.Code != http.StatusOK || !strings.Contains(gammaPage.Body.String(), "web-resource") || !strings.Contains(gammaPage.Body.String(), "isolated SQLite resource database") {
		t.Fatalf("gamma page = %d %q, want tenant-scoped resource management", gammaPage.Code, gammaPage.Body.String())
	}
	withoutConfirmation := webForm(t, handler, http.MethodPost, "/tenants/gamma/delete", nil)
	if withoutConfirmation.Code != http.StatusOK || !strings.Contains(withoutConfirmation.Body.String(), "Review before continuing") || !strings.Contains(withoutConfirmation.Body.String(), "Confirm tenant deletion") {
		t.Fatalf("unconfirmed tenant delete = %d %q, want confirmation review", withoutConfirmation.Code, withoutConfirmation.Body.String())
	}
	confirmed := webForm(t, handler, http.MethodPost, "/tenants/gamma/delete", url.Values{"confirm": {"true"}})
	if confirmed.Code != http.StatusSeeOther || confirmed.Header().Get("Location") != "/" {
		t.Fatalf("confirmed tenant delete = %d location %q, want platform redirect", confirmed.Code, confirmed.Header().Get("Location"))
	}
	if _, err := os.Stat(filepath.Join(restoredRoot, "tenants", "gamma")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted gamma tenant path stat error = %v, want os.ErrNotExist", err)
	}
}

func copyOwnedTree(source, destination string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("backup source contains an unexpected symlink")
		}
		sourcePath := filepath.Join(source, entry.Name())
		destinationPath := filepath.Join(destination, entry.Name())
		if entry.IsDir() {
			if err := copyOwnedTree(sourcePath, destinationPath); err != nil {
				return err
			}
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		data, err := os.ReadFile(sourcePath)
		if err != nil {
			return err
		}
		if err := os.WriteFile(destinationPath, data, info.Mode().Perm()); err != nil {
			return err
		}
	}
	return nil
}
