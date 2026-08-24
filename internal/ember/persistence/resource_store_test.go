package persistence

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const testListLimit = 2

type memoryResourceStore struct {
	mu        sync.RWMutex
	resources map[string]models.Resource
	nextID    uint64
}

func newMemoryResourceStore() *memoryResourceStore {
	return &memoryResourceStore{resources: make(map[string]models.Resource)}
}

func (store *memoryResourceStore) Create(ctx context.Context, spec models.ResourceSpec) (*models.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateMemoryScope(spec.ParentID); err != nil {
		return nil, err
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	if spec.ParentID != "" {
		if _, ok := store.resources[spec.ParentID]; !ok {
			return nil, ErrResourceNotFound
		}
	}
	for _, resource := range store.resources {
		if resource.Spec.ParentID == spec.ParentID &&
			resource.Spec.Type == spec.Type &&
			resource.Spec.Name == spec.Name {
			return nil, ErrDuplicateResource
		}
	}

	store.nextID++
	resource := &models.Resource{
		ID:            fmt.Sprintf("resource-%08d", store.nextID),
		Spec:          spec,
		ObservedState: models.ResourceStateUnknown,
	}
	if err := resource.Validate(); err != nil {
		return nil, err
	}
	store.resources[resource.ID] = *resource
	return resource, nil
}

func (store *memoryResourceStore) Get(ctx context.Context, scopeID, resourceID string) (*models.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateMemoryScope(scopeID); err != nil {
		return nil, err
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	resource, ok := store.resources[resourceID]
	if !ok || resource.Spec.ParentID != scopeID {
		return nil, ErrResourceNotFound
	}
	copy := resource
	return &copy, nil
}

func (store *memoryResourceStore) List(ctx context.Context, scopeID string, limit int) ([]models.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateMemoryScope(scopeID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxResourceListLimit {
		return nil, ErrInvalidResourceListLimit
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	if scopeID != "" {
		if _, ok := store.resources[scopeID]; !ok {
			return nil, ErrResourceNotFound
		}
	}
	resources := make([]models.Resource, 0, limit)
	for _, resource := range store.resources {
		if resource.Spec.ParentID == scopeID {
			resources = append(resources, resource)
		}
	}
	sort.Slice(resources, func(i, j int) bool {
		return resources[i].ID < resources[j].ID
	})
	if len(resources) > limit {
		resources = resources[:limit]
	}
	return resources, nil
}

func validateMemoryScope(scopeID string) error {
	if scopeID != "" && strings.TrimSpace(scopeID) == "" {
		return ErrInvalidScope
	}
	return nil
}

func providerMetadata() models.ProviderMetadata {
	return models.ProviderMetadata{
		Namespace: "Ember.Storage",
		Type:      "buckets",
		Version:   "v1",
	}
}

func resourceSpec(parentID, name string) models.ResourceSpec {
	return models.ResourceSpec{
		Type:         models.ResourceTypeBucket,
		Name:         name,
		ParentID:     parentID,
		Provider:     providerMetadata(),
		DesiredState: models.ResourceStateReady,
	}
}

func createResource(t *testing.T, store ResourceStore, spec models.ResourceSpec) *models.Resource {
	t.Helper()
	resource, err := store.Create(context.Background(), spec)
	if err != nil {
		t.Fatalf("Create(%q) error = %v", spec.Name, err)
	}
	return resource
}

func resourceIDs(resources []models.Resource) []string {
	ids := make([]string, 0, len(resources))
	for _, resource := range resources {
		ids = append(ids, resource.ID)
	}
	return ids
}

func TestResourceStoreScopesAndStateContract(t *testing.T) {
	store := newMemoryResourceStore()
	ctx := context.Background()

	rootA := createResource(t, store, models.ResourceSpec{
		Type:         models.ResourceTypeGroup,
		Name:         "alpha",
		Provider:     providerMetadata(),
		DesiredState: models.ResourceStateReady,
	})
	rootB := createResource(t, store, models.ResourceSpec{
		Type:         models.ResourceTypeGroup,
		Name:         "beta",
		Provider:     providerMetadata(),
		DesiredState: models.ResourceStateReady,
	})
	childA := createResource(t, store, resourceSpec(rootA.ID, "shared-name"))
	childB := createResource(t, store, resourceSpec(rootB.ID, "shared-name"))

	if rootA.ID == rootB.ID || childA.ID == childB.ID {
		t.Fatal("resource IDs must be unique")
	}
	if childA.ID == childA.Spec.Name || strings.Contains(childA.ID, "/") || strings.Contains(childA.ID, childA.Spec.ParentID) {
		t.Fatalf("resource ID %q is not opaque to its name or parent scope", childA.ID)
	}

	got, err := store.Get(ctx, rootA.ID, childA.ID)
	if err != nil {
		t.Fatalf("same-scope Get() error = %v", err)
	}
	if got.ID != childA.ID || got.Spec.Name != childA.Spec.Name {
		t.Fatalf("same-scope Get() = %#v, want %#v", got, childA)
	}
	if got.Spec.Provider != providerMetadata() {
		t.Fatalf("provider metadata = %#v, want %#v", got.Spec.Provider, providerMetadata())
	}
	if got.Spec.DesiredState != models.ResourceStateReady {
		t.Fatalf("desired state = %q, want %q", got.Spec.DesiredState, models.ResourceStateReady)
	}
	if got.ObservedState != models.ResourceStateUnknown {
		t.Fatalf("observed state = %q, want %q", got.ObservedState, models.ResourceStateUnknown)
	}
	if got.Spec.DesiredState == got.ObservedState {
		t.Fatal("desired and observed state must remain distinct")
	}

	stable, err := store.Get(ctx, rootA.ID, childA.ID)
	if err != nil {
		t.Fatalf("second same-scope Get() error = %v", err)
	}
	if stable.ID != got.ID {
		t.Fatalf("resource ID changed across Get(): first %q, second %q", got.ID, stable.ID)
	}

	if _, err := store.Get(ctx, rootB.ID, childA.ID); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("cross-scope Get() error = %v, want ErrResourceNotFound", err)
	}
	if _, err := store.Get(ctx, rootA.ID, "missing-resource"); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("missing Get() error = %v, want ErrResourceNotFound", err)
	}
	if _, err := store.Get(ctx, "missing-scope", childA.ID); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("unknown-scope Get() error = %v, want ErrResourceNotFound", err)
	}

	rootResources, err := store.List(ctx, "", MaxResourceListLimit)
	if err != nil {
		t.Fatalf("root List() error = %v", err)
	}
	if got := resourceIDs(rootResources); !reflect.DeepEqual(got, []string{rootA.ID, rootB.ID}) {
		t.Fatalf("root List() IDs = %v, want %v", got, []string{rootA.ID, rootB.ID})
	}
	alphaResources, err := store.List(ctx, rootA.ID, MaxResourceListLimit)
	if err != nil {
		t.Fatalf("alpha List() error = %v", err)
	}
	if got := resourceIDs(alphaResources); !reflect.DeepEqual(got, []string{childA.ID}) {
		t.Fatalf("alpha List() IDs = %v, want %v", got, []string{childA.ID})
	}
	betaResources, err := store.List(ctx, rootB.ID, MaxResourceListLimit)
	if err != nil {
		t.Fatalf("beta List() error = %v", err)
	}
	if got := resourceIDs(betaResources); !reflect.DeepEqual(got, []string{childB.ID}) {
		t.Fatalf("beta List() IDs = %v, want %v", got, []string{childB.ID})
	}
	if _, err := store.List(ctx, "missing-scope", MaxResourceListLimit); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("unknown-scope List() error = %v, want ErrResourceNotFound", err)
	}
}

func TestResourceStoreDeterministicBoundedListContract(t *testing.T) {
	store := newMemoryResourceStore()
	parent := createResource(t, store, models.ResourceSpec{
		Type:         models.ResourceTypeGroup,
		Name:         "parent",
		Provider:     providerMetadata(),
		DesiredState: models.ResourceStateReady,
	})
	for _, name := range []string{"zeta", "alpha", "middle"} {
		createResource(t, store, resourceSpec(parent.ID, name))
	}

	first, err := store.List(context.Background(), parent.ID, testListLimit)
	if err != nil {
		t.Fatalf("bounded List() error = %v", err)
	}
	second, err := store.List(context.Background(), parent.ID, testListLimit)
	if err != nil {
		t.Fatalf("repeat bounded List() error = %v", err)
	}
	if len(first) != testListLimit || len(second) != testListLimit {
		t.Fatalf("bounded List() lengths = %d and %d, want %d", len(first), len(second), testListLimit)
	}
	if got, want := resourceIDs(first), resourceIDs(second); !reflect.DeepEqual(got, want) {
		t.Fatalf("repeated List() IDs = %v and %v, want deterministic results", got, want)
	}
	if !sort.SliceIsSorted(first, func(i, j int) bool { return first[i].ID < first[j].ID }) {
		t.Fatalf("List() IDs = %v, want deterministic ID order", resourceIDs(first))
	}
	if _, err := store.List(context.Background(), parent.ID, 0); !errors.Is(err, ErrInvalidResourceListLimit) {
		t.Fatalf("zero-limit List() error = %v, want ErrInvalidResourceListLimit", err)
	}
	if _, err := store.List(context.Background(), parent.ID, MaxResourceListLimit+1); !errors.Is(err, ErrInvalidResourceListLimit) {
		t.Fatalf("over-limit List() error = %v, want ErrInvalidResourceListLimit", err)
	}
}

func TestResourceStoreValidationErrorsContract(t *testing.T) {
	store := newMemoryResourceStore()
	ctx := context.Background()
	valid := resourceSpec("", "unique")

	if _, err := store.Create(ctx, valid); err != nil {
		t.Fatalf("initial Create() error = %v", err)
	}
	if _, err := store.Create(ctx, valid); !errors.Is(err, ErrDuplicateResource) {
		t.Fatalf("duplicate Create() error = %v, want ErrDuplicateResource", err)
	}
	if _, err := store.Create(ctx, models.ResourceSpec{
		Type:     models.ResourceTypeBucket,
		Name:     " ",
		Provider: providerMetadata(),
	}); !errors.Is(err, models.ErrInvalidResourceSpec) {
		t.Fatalf("invalid Create() error = %v, want models.ErrInvalidResourceSpec", err)
	}
	if _, err := store.Create(ctx, models.ResourceSpec{
		Type:     models.ResourceTypeBucket,
		Name:     "invalid-scope",
		ParentID: " \t",
	}); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("blank-scope Create() error = %v, want ErrInvalidScope", err)
	}
	if _, err := store.Get(ctx, " \t", "resource-1"); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("blank-scope Get() error = %v, want ErrInvalidScope", err)
	}
	if _, err := store.List(ctx, " \t", MaxResourceListLimit); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("blank-scope List() error = %v, want ErrInvalidScope", err)
	}
	if _, err := store.Create(ctx, resourceSpec("missing-parent", "child")); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("missing-parent Create() error = %v, want ErrResourceNotFound", err)
	}
}

func TestResourceModelBoundsContract(t *testing.T) {
	tooLong := func(length int) string { return strings.Repeat("x", length+1) }

	if err := (models.ResourceSpec{
		Type: models.ResourceTypeBucket,
		Name: tooLong(models.MaxResourceNameLength),
	}).Validate(); !errors.Is(err, models.ErrInvalidResourceSpec) {
		t.Fatalf("overlong name Validate() error = %v, want ErrInvalidResourceSpec", err)
	}
	if err := (models.ResourceSpec{
		Type: models.ResourceTypeBucket,
		Name: "bounded",
		Provider: models.ProviderMetadata{
			Namespace: tooLong(models.MaxProviderNamespaceLength),
		},
	}).Validate(); !errors.Is(err, models.ErrInvalidResourceSpec) {
		t.Fatalf("overlong provider namespace Validate() error = %v, want ErrInvalidResourceSpec", err)
	}
	if err := (models.ResourceSpec{
		Type:         models.ResourceTypeBucket,
		Name:         "bounded",
		DesiredState: models.ResourceState(tooLong(models.MaxResourceStateLength)),
	}).Validate(); !errors.Is(err, models.ErrInvalidResourceSpec) {
		t.Fatalf("overlong desired state Validate() error = %v, want ErrInvalidResourceSpec", err)
	}
	if err := (models.Resource{
		ID: "resource-1",
		Spec: models.ResourceSpec{
			Type: models.ResourceTypeBucket,
			Name: "bounded",
		},
		ObservedState: models.ResourceState(tooLong(models.MaxResourceStateLength)),
	}).Validate(); !errors.Is(err, models.ErrInvalidResource) {
		t.Fatalf("overlong observed state Validate() error = %v, want ErrInvalidResource", err)
	}
}

var _ ResourceStore = (*memoryResourceStore)(nil)
