package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const (
	fileResourceStoreVersion  = 1
	MaxResourceStoreFileBytes = 1 << 20
)

var (
	ErrInvalidResourceStorePath = errors.New("invalid resource store path")
	ErrResourceStoreCorrupt     = errors.New("corrupt resource store")
	ErrResourceStoreTooLarge    = errors.New("resource store exceeds size limit")
	ErrResourceStoreIO          = errors.New("resource store I/O failure")
	ErrResourceStoreIDExhausted = errors.New("resource store resource IDs exhausted")
)

type resourceStoreDiskState struct {
	Version   int               `json:"version"`
	NextID    uint64            `json:"next_id"`
	Resources []models.Resource `json:"resources"`
}

// FileResourceStore persists bounded resource state in one private JSON snapshot.
// Lock state remains process-local; durable lock coordination is a separate concern.
type FileResourceStore struct {
	mu        sync.RWMutex
	path      string
	resources map[string]models.Resource
	locks     map[string]models.ResourceLock
	nextID    uint64
}

func NewFileResourceStore(path string) (*FileResourceStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrInvalidResourceStorePath
	}

	path = filepath.Clean(path)
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return nil, ErrInvalidResourceStorePath
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, ErrResourceStoreIO
	}

	store := &FileResourceStore{
		path:      path,
		resources: make(map[string]models.Resource),
		locks:     make(map[string]models.ResourceLock),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *FileResourceStore) load() error {
	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrResourceStoreIO
	}
	if len(data) > MaxResourceStoreFileBytes {
		return ErrResourceStoreTooLarge
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state resourceStoreDiskState
	if err := decoder.Decode(&state); err != nil {
		return ErrResourceStoreCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrResourceStoreCorrupt
	}
	if state.Version != fileResourceStoreVersion {
		return ErrResourceStoreCorrupt
	}

	resources := make(map[string]models.Resource, len(state.Resources))
	for _, resource := range state.Resources {
		if err := resource.Validate(); err != nil {
			return ErrResourceStoreCorrupt
		}
		if _, exists := resources[resource.ID]; exists {
			return ErrResourceStoreCorrupt
		}
		resources[resource.ID] = cloneStoredResource(resource)
	}
	for _, resource := range resources {
		if resource.Spec.ParentID != "" {
			if _, exists := resources[resource.Spec.ParentID]; !exists {
				return ErrResourceStoreCorrupt
			}
		}
	}

	store.resources = resources
	store.nextID = state.NextID
	return nil
}

func cloneStoredTags(tags map[string]string) map[string]string {
	if tags == nil {
		return nil
	}
	clone := make(map[string]string, len(tags))
	for key, value := range tags {
		clone[key] = value
	}
	return clone
}

func cloneStoredResource(resource models.Resource) models.Resource {
	resource.Spec.Tags = cloneStoredTags(resource.Spec.Tags)
	return resource
}

func validateFileResourceScope(scopeID string) error {
	if scopeID != "" && strings.TrimSpace(scopeID) == "" {
		return ErrInvalidScope
	}
	return nil
}

func (store *FileResourceStore) saveLocked() error {
	resources := make([]models.Resource, 0, len(store.resources))
	for _, resource := range store.resources {
		resources = append(resources, cloneStoredResource(resource))
	}
	sort.Slice(resources, func(i, j int) bool {
		return resources[i].ID < resources[j].ID
	})

	data, err := json.MarshalIndent(resourceStoreDiskState{
		Version:   fileResourceStoreVersion,
		NextID:    store.nextID,
		Resources: resources,
	}, "", "  ")
	if err != nil {
		return ErrResourceStoreIO
	}
	data = append(data, '\n')
	if len(data) > MaxResourceStoreFileBytes {
		return ErrResourceStoreTooLarge
	}

	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return ErrResourceStoreIO
	}
	temporary, err := os.CreateTemp(directory, ".ember-resources-*.tmp")
	if err != nil {
		return ErrResourceStoreIO
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if written, err := temporary.Write(data); err != nil || written != len(data) {
		_ = temporary.Close()
		return ErrResourceStoreIO
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return ErrResourceStoreIO
	}
	if err := temporary.Close(); err != nil {
		return ErrResourceStoreIO
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return ErrResourceStoreIO
	}

	// The rename is the commit point. Directory sync is best effort after the
	// atomically replaced snapshot has become the current state.
	if directoryFile, err := os.Open(directory); err == nil {
		_ = directoryFile.Sync()
		_ = directoryFile.Close()
	}
	return nil
}

func (store *FileResourceStore) nextResourceIDLocked() (string, error) {
	for {
		if store.nextID == ^uint64(0) {
			return "", ErrResourceStoreIDExhausted
		}
		store.nextID++
		id := fmt.Sprintf("resource-%08d", store.nextID)
		if _, exists := store.resources[id]; !exists {
			return id, nil
		}
	}
}

func (store *FileResourceStore) Create(ctx context.Context, spec models.ResourceSpec) (*models.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateFileResourceScope(spec.ParentID); err != nil {
		return nil, err
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	if spec.ParentID != "" {
		if _, exists := store.resources[spec.ParentID]; !exists {
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

	previousNextID := store.nextID
	id, err := store.nextResourceIDLocked()
	if err != nil {
		return nil, err
	}
	resource := models.Resource{
		ID: id,
		Spec: models.ResourceSpec{
			Type:              spec.Type,
			Name:              spec.Name,
			ParentID:          spec.ParentID,
			Tags:              cloneStoredTags(spec.Tags),
			Provider:          spec.Provider,
			DesiredState:      spec.DesiredState,
			WorkloadResources: spec.WorkloadResources,
			SecurityContext:   spec.SecurityContext,
		},
		ObservedState: models.ResourceStateUnknown,
	}
	if err := resource.Validate(); err != nil {
		store.nextID = previousNextID
		return nil, err
	}
	store.resources[resource.ID] = resource
	if err := store.saveLocked(); err != nil {
		delete(store.resources, resource.ID)
		store.nextID = previousNextID
		return nil, err
	}

	copy := cloneStoredResource(resource)
	return &copy, nil
}

func (store *FileResourceStore) Get(ctx context.Context, scopeID, resourceID string) (*models.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateFileResourceScope(scopeID); err != nil {
		return nil, err
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	resource, exists := store.resources[resourceID]
	if !exists || resource.Spec.ParentID != scopeID {
		return nil, ErrResourceNotFound
	}
	copy := cloneStoredResource(resource)
	return &copy, nil
}

func (store *FileResourceStore) List(ctx context.Context, scopeID string, limit int) ([]models.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateFileResourceScope(scopeID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxResourceListLimit {
		return nil, ErrInvalidResourceListLimit
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	if scopeID != "" {
		if _, exists := store.resources[scopeID]; !exists {
			return nil, ErrResourceNotFound
		}
	}
	resources := make([]models.Resource, 0, limit)
	for _, resource := range store.resources {
		if resource.Spec.ParentID == scopeID {
			resources = append(resources, cloneStoredResource(resource))
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

func (store *FileResourceStore) UpdateTags(ctx context.Context, scopeID, resourceID string, tags map[string]string) (*models.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateFileResourceScope(scopeID); err != nil {
		return nil, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	resource, exists := store.resources[resourceID]
	if !exists || resource.Spec.ParentID != scopeID {
		return nil, ErrResourceNotFound
	}
	if resourceHasReadOnlyLock(store.resources, store.locks, resourceID) {
		return nil, ErrResourceLocked
	}
	updated := resource
	updated.Spec.Tags = cloneStoredTags(tags)
	if err := updated.Spec.Validate(); err != nil {
		return nil, err
	}
	store.resources[resourceID] = updated
	if err := store.saveLocked(); err != nil {
		store.resources[resourceID] = resource
		return nil, err
	}
	copy := cloneStoredResource(updated)
	return &copy, nil
}

func (store *FileResourceStore) UpdateObservedState(ctx context.Context, scopeID, resourceID string, state models.ResourceState) (*models.Resource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateFileResourceScope(scopeID); err != nil {
		return nil, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	resource, exists := store.resources[resourceID]
	if !exists || resource.Spec.ParentID != scopeID {
		return nil, ErrResourceNotFound
	}
	updated := resource
	updated.ObservedState = state
	if err := updated.Validate(); err != nil {
		return nil, err
	}
	store.resources[resourceID] = updated
	if err := store.saveLocked(); err != nil {
		store.resources[resourceID] = resource
		return nil, err
	}
	copy := cloneStoredResource(updated)
	return &copy, nil
}

func (store *FileResourceStore) Delete(ctx context.Context, scopeID, resourceID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateFileResourceScope(scopeID); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	resource, exists := store.resources[resourceID]
	if !exists || resource.Spec.ParentID != scopeID {
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
	lock, hadLock := store.locks[resourceID]
	delete(store.resources, resourceID)
	delete(store.locks, resourceID)
	if err := store.saveLocked(); err != nil {
		store.resources[resourceID] = resource
		if hadLock {
			store.locks[resourceID] = lock
		}
		return err
	}
	return nil
}

func (store *FileResourceStore) AcquireLock(ctx context.Context, scopeID, resourceID string, lock models.ResourceLock) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateFileResourceScope(scopeID); err != nil {
		return err
	}
	if err := lock.Validate(); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	resource, exists := store.resources[resourceID]
	if !exists || resource.Spec.ParentID != scopeID {
		return ErrResourceNotFound
	}
	if current, held := store.locks[resourceID]; held {
		if current == lock {
			return nil
		}
		return ErrResourceLockConflict
	}
	store.locks[resourceID] = lock
	return nil
}

func (store *FileResourceStore) ReleaseLock(ctx context.Context, scopeID, resourceID string, lock models.ResourceLock) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateFileResourceScope(scopeID); err != nil {
		return err
	}
	if err := lock.Validate(); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	resource, exists := store.resources[resourceID]
	if !exists || resource.Spec.ParentID != scopeID {
		return ErrResourceNotFound
	}
	current, held := store.locks[resourceID]
	if !held {
		return ErrResourceLockNotHeld
	}
	if current != lock {
		return ErrResourceLockNotOwner
	}
	delete(store.locks, resourceID)
	return nil
}

func (store *FileResourceStore) InspectLock(ctx context.Context, scopeID, resourceID string) (*models.ResourceLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateFileResourceScope(scopeID); err != nil {
		return nil, err
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	resource, exists := store.resources[resourceID]
	if !exists || resource.Spec.ParentID != scopeID {
		return nil, ErrResourceNotFound
	}
	lock, held := store.locks[resourceID]
	if !held {
		return nil, nil
	}
	return &lock, nil
}

var _ ResourceStore = (*FileResourceStore)(nil)
