package ember

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

func TestResourceManagerExposesScopedResourceLifecycle(t *testing.T) {
	store, err := persistence.NewFileResourceStore(filepath.Join(t.TempDir(), "resources.json"))
	if err != nil {
		t.Fatalf("NewFileResourceStore() error = %v", err)
	}
	manager, err := NewResourceManager(store)
	if err != nil {
		t.Fatalf("NewResourceManager() error = %v", err)
	}
	var plane ResourceControlPlane = manager
	ctx := context.Background()
	resourceIDs := func(resources []models.Resource) []string {
		ids := make([]string, 0, len(resources))
		for _, resource := range resources {
			ids = append(ids, resource.ID)
		}
		return ids
	}
	provider := models.ProviderMetadata{Namespace: "Ember.Storage", Type: "buckets", Version: "v1"}

	rootA, err := plane.CreateResource(ctx, models.ResourceSpec{
		Type:         models.ResourceTypeGroup,
		Name:         "alpha",
		Provider:     provider,
		DesiredState: models.ResourceStateReady,
	})
	if err != nil {
		t.Fatalf("CreateResource(rootA) error = %v", err)
	}
	rootB, err := plane.CreateResource(ctx, models.ResourceSpec{
		Type:         models.ResourceTypeGroup,
		Name:         "beta",
		Provider:     provider,
		DesiredState: models.ResourceStateReady,
	})
	if err != nil {
		t.Fatalf("CreateResource(rootB) error = %v", err)
	}
	childA, err := plane.CreateResource(ctx, models.ResourceSpec{
		Type:         models.ResourceTypeBucket,
		Name:         "assets",
		ParentID:     rootA.ID,
		Provider:     provider,
		DesiredState: models.ResourceStateReady,
	})
	if err != nil {
		t.Fatalf("CreateResource(childA) error = %v", err)
	}
	childB, err := plane.CreateResource(ctx, models.ResourceSpec{
		Type:         models.ResourceTypeBucket,
		Name:         "assets",
		ParentID:     rootB.ID,
		Provider:     provider,
		DesiredState: models.ResourceStateReady,
	})
	if err != nil {
		t.Fatalf("CreateResource(childB) error = %v", err)
	}

	got, err := plane.GetResource(ctx, rootA.ID, childA.ID)
	if err != nil {
		t.Fatalf("GetResource(same scope) error = %v", err)
	}
	if got.ID != childA.ID || got.Spec.ParentID != rootA.ID || got.Spec.Provider != provider || got.ObservedState != models.ResourceStateUnknown {
		t.Fatalf("GetResource(same scope) = %#v, want child %#v", got, childA)
	}
	if _, err := plane.GetResource(ctx, rootB.ID, childA.ID); !errors.Is(err, persistence.ErrResourceNotFound) {
		t.Fatalf("GetResource(cross scope) error = %v, want ErrResourceNotFound", err)
	}

	gotChildren, err := plane.ListResources(ctx, rootA.ID, persistence.MaxResourceListLimit)
	if err != nil {
		t.Fatalf("ListResources(child scope) error = %v", err)
	}
	if gotIDs := resourceIDs(gotChildren); !reflect.DeepEqual(gotIDs, []string{childA.ID}) {
		t.Fatalf("ListResources(child scope) IDs = %v, want %v", gotIDs, []string{childA.ID})
	}
	gotRoots, err := plane.ListResources(ctx, "", persistence.MaxResourceListLimit)
	if err != nil {
		t.Fatalf("ListResources(root scope) error = %v", err)
	}
	if gotIDs := resourceIDs(gotRoots); !reflect.DeepEqual(gotIDs, []string{rootA.ID, rootB.ID}) {
		t.Fatalf("ListResources(root scope) IDs = %v, want %v", gotIDs, []string{rootA.ID, rootB.ID})
	}
	gotSibling, err := plane.ListResources(ctx, rootB.ID, persistence.MaxResourceListLimit)
	if err != nil {
		t.Fatalf("ListResources(sibling scope) error = %v", err)
	}
	if gotIDs := resourceIDs(gotSibling); !reflect.DeepEqual(gotIDs, []string{childB.ID}) {
		t.Fatalf("ListResources(sibling scope) IDs = %v, want %v", gotIDs, []string{childB.ID})
	}
}
