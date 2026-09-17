package persistence

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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
	locks     map[string]models.ResourceLock
	nextID    uint64
}

func newMemoryResourceStore() *memoryResourceStore {
	return &memoryResourceStore{
		resources: make(map[string]models.Resource),
		locks:     make(map[string]models.ResourceLock),
	}
}

func cloneTags(tags map[string]string) map[string]string {
	if tags == nil {
		return nil
	}
	clone := make(map[string]string, len(tags))
	for key, value := range tags {
		clone[key] = value
	}
	return clone
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
	if resourceHasReadOnlyLock(store.resources, store.locks, spec.ParentID) {
		return nil, ErrResourceLocked
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
	resource.Spec.Tags = cloneTags(spec.Tags)
	if err := resource.Validate(); err != nil {
		return nil, err
	}
	store.resources[resource.ID] = *resource
	resource.Spec.Tags = cloneTags(resource.Spec.Tags)
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
	copy.Spec.Tags = cloneTags(resource.Spec.Tags)
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
			resource.Spec.Tags = cloneTags(resource.Spec.Tags)
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

func (store *memoryResourceStore) UpdateTags(ctx context.Context, scopeID, resourceID string, tags map[string]string) (*models.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateMemoryScope(scopeID); err != nil {
		return nil, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	resource, ok := store.resources[resourceID]
	if !ok || resource.Spec.ParentID != scopeID {
		return nil, ErrResourceNotFound
	}
	if resourceHasReadOnlyLock(store.resources, store.locks, resourceID) {
		return nil, ErrResourceLocked
	}
	updatedSpec := resource.Spec
	updatedSpec.Tags = cloneTags(tags)
	if err := updatedSpec.Validate(); err != nil {
		return nil, err
	}
	updated := resource
	updated.Spec = updatedSpec
	store.resources[resourceID] = updated
	updated.Spec.Tags = cloneTags(updated.Spec.Tags)
	return &updated, nil
}

func (store *memoryResourceStore) UpdateObservedState(ctx context.Context, scopeID, resourceID string, state models.ResourceState) (*models.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateMemoryScope(scopeID); err != nil {
		return nil, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	resource, ok := store.resources[resourceID]
	if !ok || resource.Spec.ParentID != scopeID {
		return nil, ErrResourceNotFound
	}
	updated := resource
	updated.ObservedState = state
	if err := updated.Validate(); err != nil {
		return nil, err
	}
	store.resources[resourceID] = updated
	updated.Spec.Tags = cloneTags(updated.Spec.Tags)
	return &updated, nil
}

func (store *memoryResourceStore) Delete(ctx context.Context, scopeID, resourceID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateMemoryScope(scopeID); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	resource, ok := store.resources[resourceID]
	if !ok || resource.Spec.ParentID != scopeID {
		return ErrResourceNotFound
	}
	if resourceHasReadOnlyLock(store.resources, store.locks, resourceID) {
		return ErrResourceLocked
	}
	for _, child := range store.resources {
		if child.Spec.ParentID == resourceID {
			return ErrResourceHasDependents
		}
	}
	delete(store.resources, resourceID)
	delete(store.locks, resourceID)
	return nil
}

func (store *memoryResourceStore) AcquireLock(ctx context.Context, scopeID, resourceID string, lock models.ResourceLock) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateMemoryScope(scopeID); err != nil {
		return err
	}
	if err := lock.Validate(); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	resource, ok := store.resources[resourceID]
	if !ok || resource.Spec.ParentID != scopeID {
		return ErrResourceNotFound
	}
	if current, ok := store.locks[resourceID]; ok {
		if current == lock {
			return nil
		}
		return ErrResourceLockConflict
	}
	store.locks[resourceID] = lock
	return nil
}

func (store *memoryResourceStore) ReleaseLock(ctx context.Context, scopeID, resourceID string, lock models.ResourceLock) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateMemoryScope(scopeID); err != nil {
		return err
	}
	if err := lock.Validate(); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	resource, ok := store.resources[resourceID]
	if !ok || resource.Spec.ParentID != scopeID {
		return ErrResourceNotFound
	}
	current, ok := store.locks[resourceID]
	if !ok {
		return ErrResourceLockNotHeld
	}
	if current != lock {
		return ErrResourceLockNotOwner
	}
	delete(store.locks, resourceID)
	return nil
}

func (store *memoryResourceStore) InspectLock(ctx context.Context, scopeID, resourceID string) (*models.ResourceLock, error) {
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
	lock, ok := store.locks[resourceID]
	if !ok {
		return nil, nil
	}
	return &lock, nil
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

func TestResourceStoreTagsUpdateAndSafeDeleteContract(t *testing.T) {
	store := newMemoryResourceStore()
	ctx := context.Background()
	inputTags := map[string]string{"environment": "test"}
	parent := createResource(t, store, models.ResourceSpec{
		Type:         models.ResourceTypeGroup,
		Name:         "parent",
		Tags:         inputTags,
		Provider:     providerMetadata(),
		DesiredState: models.ResourceStateReady,
	})
	parent.Spec.Tags["environment"] = "returned-create-mutation"
	inputTags["environment"] = "caller-mutated"

	stored, err := store.Get(ctx, "", parent.ID)
	if err != nil {
		t.Fatalf("Get() after Create() error = %v", err)
	}
	if got := stored.Spec.Tags["environment"]; got != "test" {
		t.Fatalf("stored tag = %q, want %q", got, "test")
	}
	stored.Spec.Tags["environment"] = "returned-get-mutation"
	stored, err = store.Get(ctx, "", parent.ID)
	if err != nil {
		t.Fatalf("second Get() after returned-map mutation error = %v", err)
	}
	if got := stored.Spec.Tags["environment"]; got != "test" {
		t.Fatalf("stored tag after returned Get() mutation = %q, want %q", got, "test")
	}

	sibling := createResource(t, store, models.ResourceSpec{
		Type:         models.ResourceTypeGroup,
		Name:         "sibling",
		Provider:     providerMetadata(),
		DesiredState: models.ResourceStateReady,
	})
	childSpec := resourceSpec(parent.ID, "child")
	childSpec.Tags = map[string]string{"tier": "child"}
	child := createResource(t, store, childSpec)

	rootResources, err := store.List(ctx, "", MaxResourceListLimit)
	if err != nil {
		t.Fatalf("root List() error = %v", err)
	}
	foundParent := false
	for index := range rootResources {
		if rootResources[index].ID == parent.ID {
			rootResources[index].Spec.Tags["environment"] = "returned-list-mutation"
			foundParent = true
		}
	}
	if !foundParent {
		t.Fatalf("root List() did not return parent %q", parent.ID)
	}
	stored, err = store.Get(ctx, "", parent.ID)
	if err != nil {
		t.Fatalf("Get() after returned List() mutation error = %v", err)
	}
	if got := stored.Spec.Tags["environment"]; got != "test" {
		t.Fatalf("stored tag after returned List() mutation = %q, want %q", got, "test")
	}
	childResources, err := store.List(ctx, parent.ID, MaxResourceListLimit)
	if err != nil {
		t.Fatalf("child List() error = %v", err)
	}
	if len(childResources) != 1 {
		t.Fatalf("child List() length = %d, want 1", len(childResources))
	}
	childResources[0].Spec.Tags["tier"] = "returned-list-mutation"
	storedChild, err := store.Get(ctx, parent.ID, child.ID)
	if err != nil {
		t.Fatalf("Get() child after returned List() mutation error = %v", err)
	}
	if got := storedChild.Spec.Tags["tier"]; got != "child" {
		t.Fatalf("stored child tag after returned List() mutation = %q, want %q", got, "child")
	}

	if _, err := store.UpdateTags(ctx, sibling.ID, child.ID, map[string]string{"owner": "platform"}); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("sibling-scope UpdateTags() error = %v, want ErrResourceNotFound", err)
	}
	if err := store.Delete(ctx, sibling.ID, child.ID); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("sibling-scope Delete() error = %v, want ErrResourceNotFound", err)
	}

	updatedTags := map[string]string{"environment": "production", "owner": "platform"}
	updated, err := store.UpdateTags(ctx, "", parent.ID, updatedTags)
	if err != nil {
		t.Fatalf("UpdateTags() error = %v", err)
	}
	if updated.ID != parent.ID {
		t.Fatalf("UpdateTags() ID = %q, want immutable ID %q", updated.ID, parent.ID)
	}
	if updated.Spec.Type != parent.Spec.Type || updated.Spec.Name != parent.Spec.Name || updated.Spec.ParentID != parent.Spec.ParentID ||
		updated.Spec.Provider != parent.Spec.Provider || updated.Spec.DesiredState != parent.Spec.DesiredState || updated.ObservedState != parent.ObservedState {
		t.Fatalf("UpdateTags() changed immutable resource fields: %#v", updated)
	}
	if !reflect.DeepEqual(updated.Spec.Tags, updatedTags) {
		t.Fatalf("UpdateTags() tags = %#v, want %#v", updated.Spec.Tags, updatedTags)
	}
	updated.Spec.Tags["environment"] = "returned-update-mutation"
	updatedTags["environment"] = "caller-mutated"
	stored, err = store.Get(ctx, "", parent.ID)
	if err != nil {
		t.Fatalf("Get() after UpdateTags() returned-map mutation error = %v", err)
	}
	if got := stored.Spec.Tags["environment"]; got != "production" {
		t.Fatalf("stored tag after UpdateTags() caller/returned mutation = %q, want %q", got, "production")
	}

	if err := store.Delete(ctx, "", parent.ID); !errors.Is(err, ErrResourceHasDependents) {
		t.Fatalf("Delete() with child error = %v, want ErrResourceHasDependents", err)
	}
	if _, err := store.Get(ctx, "", parent.ID); err != nil {
		t.Fatalf("parent after refused Delete() error = %v", err)
	}
	if _, err := store.Get(ctx, parent.ID, child.ID); err != nil {
		t.Fatalf("child after refused parent Delete() error = %v", err)
	}

	if err := store.Delete(ctx, parent.ID, child.ID); err != nil {
		t.Fatalf("Delete() leaf error = %v", err)
	}
	if _, err := store.Get(ctx, parent.ID, child.ID); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("deleted child Get() error = %v, want ErrResourceNotFound", err)
	}
	if err := store.Delete(ctx, "", parent.ID); err != nil {
		t.Fatalf("Delete() parent after child error = %v", err)
	}
	if _, err := store.Get(ctx, "", parent.ID); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("deleted parent Get() error = %v, want ErrResourceNotFound", err)
	}
	if err := store.Delete(ctx, "", sibling.ID); err != nil {
		t.Fatalf("Delete() sibling after parent/child cleanup error = %v", err)
	}
	rootResources, err = store.List(ctx, "", MaxResourceListLimit)
	if err != nil {
		t.Fatalf("root List() after deletion error = %v", err)
	}
	if len(rootResources) != 0 {
		t.Fatalf("root List() after deletion = %#v, want no residue", rootResources)
	}
}

func TestResourceStoreMutationValidationContract(t *testing.T) {
	store := newMemoryResourceStore()
	ctx := context.Background()
	spec := resourceSpec("", "mutable")
	spec.Tags = map[string]string{"owner": "platform"}
	resource := createResource(t, store, spec)

	if _, err := store.UpdateTags(ctx, "", resource.ID, map[string]string{strings.Repeat("x", models.MaxResourceTagKeyLength+1): "value"}); !errors.Is(err, models.ErrInvalidResourceSpec) {
		t.Fatalf("overlong UpdateTags() error = %v, want ErrInvalidResourceSpec", err)
	}
	stored, err := store.Get(ctx, "", resource.ID)
	if err != nil {
		t.Fatalf("Get() after invalid UpdateTags() error = %v", err)
	}
	if !reflect.DeepEqual(stored.Spec.Tags, spec.Tags) {
		t.Fatalf("stored tags after invalid UpdateTags() = %#v, want %#v", stored.Spec.Tags, spec.Tags)
	}
	if _, err := store.UpdateTags(ctx, " 	", resource.ID, map[string]string{"owner": "platform"}); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("blank-scope UpdateTags() error = %v, want ErrInvalidScope", err)
	}
	if _, err := store.UpdateTags(ctx, "missing-scope", resource.ID, map[string]string{"owner": "platform"}); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("cross-scope UpdateTags() error = %v, want ErrResourceNotFound", err)
	}
	if _, err := store.UpdateTags(ctx, "", "missing-resource", map[string]string{"owner": "platform"}); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("missing UpdateTags() error = %v, want ErrResourceNotFound", err)
	}
	if err := store.Delete(ctx, " 	", resource.ID); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("blank-scope Delete() error = %v, want ErrInvalidScope", err)
	}
	if err := store.Delete(ctx, "missing-scope", resource.ID); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("cross-scope Delete() error = %v, want ErrResourceNotFound", err)
	}
}

func TestResourceModelTagBoundsContract(t *testing.T) {
	tooLong := func(length int) string { return strings.Repeat("x", length+1) }
	boundaryTags := make(map[string]string, models.MaxResourceTagCount)
	for index := 0; index < models.MaxResourceTagCount; index++ {
		prefix := fmt.Sprintf("tag-%02d", index)
		boundaryTags[prefix+strings.Repeat("k", models.MaxResourceTagKeyLength-len(prefix))] = strings.Repeat("v", models.MaxResourceTagValueLength)
	}
	if err := (models.ResourceSpec{
		Type: models.ResourceTypeBucket,
		Name: "bounded",
		Tags: boundaryTags,
	}).Validate(); err != nil {
		t.Fatalf("exact-boundary tags Validate() error = %v, want nil", err)
	}
	if err := (models.ResourceSpec{
		Type: models.ResourceTypeBucket,
		Name: "bounded",
		Tags: map[string]string{" 	": "value"},
	}).Validate(); !errors.Is(err, models.ErrInvalidResourceSpec) {
		t.Fatalf("blank tag key Validate() error = %v, want ErrInvalidResourceSpec", err)
	}

	tooMany := make(map[string]string, models.MaxResourceTagCount+1)
	for index := 0; index <= models.MaxResourceTagCount; index++ {
		tooMany[fmt.Sprintf("tag-%d", index)] = "value"
	}

	tests := []struct {
		name string
		tags map[string]string
	}{
		{
			name: "overlong key",
			tags: map[string]string{tooLong(models.MaxResourceTagKeyLength): "value"},
		},
		{
			name: "overlong value",
			tags: map[string]string{"key": tooLong(models.MaxResourceTagValueLength)},
		},
		{
			name: "too many tags",
			tags: tooMany,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := models.ResourceSpec{
				Type: models.ResourceTypeBucket,
				Name: "bounded",
				Tags: tt.tags,
			}
			if err := spec.Validate(); !errors.Is(err, models.ErrInvalidResourceSpec) {
				t.Fatalf("Validate() error = %v, want ErrInvalidResourceSpec", err)
			}
		})
	}
}

func TestResourceStoreLockLifecycleContract(t *testing.T) {
	store := newMemoryResourceStore()
	ctx := context.Background()
	parent := createResource(t, store, models.ResourceSpec{
		Type:         models.ResourceTypeGroup,
		Name:         "parent",
		Provider:     providerMetadata(),
		DesiredState: models.ResourceStateReady,
	})
	child := createResource(t, store, resourceSpec(parent.ID, "child"))
	lock := models.ResourceLock{Owner: "controller-a", Token: "token-a"}
	conflict := models.ResourceLock{Owner: "controller-b", Token: "token-b"}
	wrongOwner := models.ResourceLock{Owner: "controller-b", Token: lock.Token}

	unlocked, err := store.InspectLock(ctx, "", parent.ID)
	if err != nil {
		t.Fatalf("InspectLock() before acquisition error = %v", err)
	}
	if unlocked != nil {
		t.Fatalf("InspectLock() before acquisition = %#v, want nil", unlocked)
	}

	if err := store.AcquireLock(ctx, "", parent.ID, lock); err != nil {
		t.Fatalf("AcquireLock() error = %v", err)
	}
	inspected, err := store.InspectLock(ctx, "", parent.ID)
	if err != nil {
		t.Fatalf("InspectLock() after acquisition error = %v", err)
	}
	if inspected == nil || *inspected != lock {
		t.Fatalf("InspectLock() after acquisition = %#v, want %#v", inspected, lock)
	}

	if err := store.AcquireLock(ctx, "", parent.ID, conflict); !errors.Is(err, ErrResourceLockConflict) {
		t.Fatalf("conflicting AcquireLock() error = %v, want ErrResourceLockConflict", err)
	}
	inspected, err = store.InspectLock(ctx, "", parent.ID)
	if err != nil {
		t.Fatalf("InspectLock() after conflict error = %v", err)
	}
	if inspected == nil || *inspected != lock {
		t.Fatalf("lock after conflict = %#v, want unchanged %#v", inspected, lock)
	}
	if err := store.AcquireLock(ctx, "", parent.ID, lock); err != nil {
		t.Fatalf("idempotent AcquireLock() error = %v", err)
	}

	if err := store.ReleaseLock(ctx, "", parent.ID, wrongOwner); !errors.Is(err, ErrResourceLockNotOwner) {
		t.Fatalf("non-owner ReleaseLock() error = %v, want ErrResourceLockNotOwner", err)
	}
	inspected, err = store.InspectLock(ctx, "", parent.ID)
	if err != nil {
		t.Fatalf("InspectLock() after refused release error = %v", err)
	}
	if inspected == nil || *inspected != lock {
		t.Fatalf("lock after refused release = %#v, want unchanged %#v", inspected, lock)
	}

	if err := store.ReleaseLock(ctx, "", parent.ID, lock); err != nil {
		t.Fatalf("ReleaseLock() error = %v", err)
	}
	unlocked, err = store.InspectLock(ctx, "", parent.ID)
	if err != nil {
		t.Fatalf("InspectLock() after release error = %v", err)
	}
	if unlocked != nil {
		t.Fatalf("InspectLock() after release = %#v, want nil", unlocked)
	}
	if err := store.ReleaseLock(ctx, "", parent.ID, lock); !errors.Is(err, ErrResourceLockNotHeld) {
		t.Fatalf("release without held lock error = %v, want ErrResourceLockNotHeld", err)
	}
	if err := store.AcquireLock(ctx, "", "missing-resource", lock); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("missing-resource AcquireLock() error = %v, want ErrResourceNotFound", err)
	}
	if err := store.ReleaseLock(ctx, "", "missing-resource", lock); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("missing-resource ReleaseLock() error = %v, want ErrResourceNotFound", err)
	}
	if _, err := store.InspectLock(ctx, "", "missing-resource"); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("missing-resource InspectLock() error = %v, want ErrResourceNotFound", err)
	}

	if err := store.AcquireLock(ctx, parent.ID, child.ID, lock); err != nil {
		t.Fatalf("child AcquireLock() error = %v", err)
	}
	if _, err := store.InspectLock(ctx, "", child.ID); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("cross-scope InspectLock() error = %v, want ErrResourceNotFound", err)
	}
	if err := store.ReleaseLock(ctx, "", child.ID, lock); !errors.Is(err, ErrResourceNotFound) {
		t.Fatalf("cross-scope ReleaseLock() error = %v, want ErrResourceNotFound", err)
	}
	if err := store.AcquireLock(ctx, " 	", parent.ID, lock); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("blank-scope AcquireLock() error = %v, want ErrInvalidScope", err)
	}
	if _, err := store.InspectLock(ctx, " 	", parent.ID); !errors.Is(err, ErrInvalidScope) {
		t.Fatalf("blank-scope InspectLock() error = %v, want ErrInvalidScope", err)
	}
}

func TestResourceStoreLockInputValidationContract(t *testing.T) {
	store := newMemoryResourceStore()
	resource := createResource(t, store, resourceSpec("", "locked"))
	invalidLocks := []models.ResourceLock{
		{Owner: " 	", Token: "token"},
		{Owner: "owner", Token: " "},
		{Owner: strings.Repeat("o", models.MaxResourceLockOwnerLength+1), Token: "token"},
		{Owner: "owner", Token: strings.Repeat("t", models.MaxResourceLockTokenLength+1)},
	}
	for index, lock := range invalidLocks {
		if err := store.AcquireLock(context.Background(), "", resource.ID, lock); !errors.Is(err, models.ErrInvalidResourceLock) {
			t.Errorf("invalid lock %d AcquireLock() error = %v, want models.ErrInvalidResourceLock", index, err)
		}
	}
	boundary := models.ResourceLock{
		Owner: strings.Repeat("o", models.MaxResourceLockOwnerLength),
		Token: strings.Repeat("t", models.MaxResourceLockTokenLength),
	}
	if err := store.AcquireLock(context.Background(), "", resource.ID, boundary); err != nil {
		t.Fatalf("exact-boundary AcquireLock() error = %v, want nil", err)
	}
	inspected, err := store.InspectLock(context.Background(), "", resource.ID)
	if err != nil {
		t.Fatalf("InspectLock() at exact boundary error = %v", err)
	}
	if inspected == nil || *inspected != boundary {
		t.Fatalf("exact-boundary InspectLock() = %#v, want %#v", inspected, boundary)
	}
	if err := store.ReleaseLock(context.Background(), "", resource.ID, boundary); err != nil {
		t.Fatalf("exact-boundary ReleaseLock() error = %v", err)
	}
	unlocked, err := store.InspectLock(context.Background(), "", resource.ID)
	if err != nil {
		t.Fatalf("InspectLock() after invalid input error = %v", err)
	}
	if unlocked != nil {
		t.Fatalf("invalid acquisition mutated lock state: %#v", unlocked)
	}
}

func TestResourceReadOnlyLockAncestryCycleFailsClosed(t *testing.T) {
	resources := map[string]models.Resource{
		"resource-a": {
			ID: "resource-a",
			Spec: models.ResourceSpec{
				Type:     models.ResourceTypeGroup,
				Name:     "a",
				ParentID: "resource-b",
			},
		},
		"resource-b": {
			ID: "resource-b",
			Spec: models.ResourceSpec{
				Type:     models.ResourceTypeGroup,
				Name:     "b",
				ParentID: "resource-a",
			},
		},
	}
	if !resourceHasReadOnlyLock(resources, map[string]models.ResourceLock{}, "resource-a") {
		t.Fatal("resourceHasReadOnlyLock() returned false for a cyclic ancestry")
	}
}

func TestReadOnlyLockBlocksResourceMutations(t *testing.T) {
	tests := []struct {
		name     string
		newStore func(t *testing.T) ResourceStore
	}{
		{
			name: "memory",
			newStore: func(*testing.T) ResourceStore {
				return newMemoryResourceStore()
			},
		},
		{
			name: "file",
			newStore: func(t *testing.T) ResourceStore {
				store, err := NewFileResourceStore(filepath.Join(t.TempDir(), "resources.json"))
				if err != nil {
					t.Fatalf("NewFileResourceStore() error = %v", err)
				}
				return store
			},
		},
		{
			name: "sqlite",
			newStore: func(t *testing.T) ResourceStore {
				store, err := NewSQLiteResourceStore(filepath.Join(t.TempDir(), "platform.db"))
				if err != nil {
					t.Fatalf("NewSQLiteResourceStore() error = %v", err)
				}
				return store
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store := tt.newStore(t)
			parent := createResource(t, store, resourceSpec("", "parent"))
			lock := models.ResourceLock{Owner: "controller-a", Token: "token-a"}
			if err := store.AcquireLock(ctx, "", parent.ID, lock); err != nil {
				t.Fatalf("AcquireLock(parent) error = %v", err)
			}

			if _, err := store.Create(ctx, resourceSpec(parent.ID, "child")); !errors.Is(err, ErrResourceLocked) {
				t.Fatalf("Create(child under locked parent) error = %v, want ErrResourceLocked", err)
			}
			if err := store.ReleaseLock(ctx, "", parent.ID, lock); err != nil {
				t.Fatalf("ReleaseLock(parent) error = %v", err)
			}
			child := createResource(t, store, resourceSpec(parent.ID, "child"))
			if child.ID != "resource-00000002" {
				t.Fatalf("child ID after blocked Create() = %q, want resource-00000002", child.ID)
			}

			if err := store.AcquireLock(ctx, "", parent.ID, lock); err != nil {
				t.Fatalf("AcquireLock(parent, inherited) error = %v", err)
			}
			if _, err := store.UpdateTags(ctx, parent.ID, child.ID, map[string]string{"environment": "blocked"}); !errors.Is(err, ErrResourceLocked) {
				t.Fatalf("inherited UpdateTags() error = %v, want ErrResourceLocked", err)
			}
			if err := store.Delete(ctx, parent.ID, child.ID); !errors.Is(err, ErrResourceLocked) {
				t.Fatalf("inherited Delete() error = %v, want ErrResourceLocked", err)
			}
			if err := store.ReleaseLock(ctx, "", parent.ID, lock); err != nil {
				t.Fatalf("ReleaseLock(parent, inherited) error = %v", err)
			}

			if err := store.AcquireLock(ctx, parent.ID, child.ID, lock); err != nil {
				t.Fatalf("AcquireLock(child) error = %v", err)
			}
			if _, err := store.UpdateTags(ctx, parent.ID, child.ID, map[string]string{"environment": "blocked"}); !errors.Is(err, ErrResourceLocked) {
				t.Fatalf("direct UpdateTags() error = %v, want ErrResourceLocked", err)
			}
			got, err := store.Get(ctx, parent.ID, child.ID)
			if err != nil {
				t.Fatalf("Get() after blocked UpdateTags() error = %v", err)
			}
			if len(got.Spec.Tags) != 0 {
				t.Fatalf("tags after blocked UpdateTags() = %#v, want unchanged", got.Spec.Tags)
			}

			if err := store.Delete(ctx, parent.ID, child.ID); !errors.Is(err, ErrResourceLocked) {
				t.Fatalf("direct Delete() error = %v, want ErrResourceLocked", err)
			}
			if _, err := store.Get(ctx, parent.ID, child.ID); err != nil {
				t.Fatalf("Get() after blocked Delete() error = %v", err)
			}

			if err := store.ReleaseLock(ctx, parent.ID, child.ID, lock); err != nil {
				t.Fatalf("ReleaseLock(child) error = %v", err)
			}
			if _, err := store.UpdateTags(ctx, parent.ID, child.ID, map[string]string{"environment": "unlocked"}); err != nil {
				t.Fatalf("UpdateTags() after ReleaseLock() error = %v", err)
			}
			if err := store.Delete(ctx, parent.ID, child.ID); err != nil {
				t.Fatalf("Delete() after ReleaseLock() error = %v", err)
			}
			if err := store.Delete(ctx, "", parent.ID); err != nil {
				t.Fatalf("Delete(parent) after child cleanup error = %v", err)
			}
		})
	}
}

var _ ResourceStore = (*memoryResourceStore)(nil)
