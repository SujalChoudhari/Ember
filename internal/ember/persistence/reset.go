package persistence

import (
	"context"
	"errors"
	"os"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func removeOwnedSnapshot(ctx context.Context, path string, failure error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return failure
	}
	return nil
}

func (store *FileResourceStore) Reset(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := removeOwnedSnapshot(ctx, store.path, ErrResourceStoreIO); err != nil {
		return err
	}
	store.resources = make(map[string]models.Resource)
	store.locks = make(map[string]models.ResourceLock)
	store.nextID = 0
	return nil
}

func (store *FileOperationStore) Reset(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := removeOwnedSnapshot(ctx, store.path, ErrOperationStoreIO); err != nil {
		return err
	}
	store.operations = make(map[string]models.Operation)
	store.requestIDs = make(map[string]string)
	return nil
}

func (store *FileAuditStore) Reset(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := removeOwnedSnapshot(ctx, store.path, ErrAuditStoreIO); err != nil {
		return err
	}
	store.entries = make(map[string]models.AuditEntry)
	return nil
}

var _ interface{ Reset(context.Context) error } = (*FileResourceStore)(nil)
var _ interface{ Reset(context.Context) error } = (*FileOperationStore)(nil)
var _ interface{ Reset(context.Context) error } = (*FileAuditStore)(nil)
