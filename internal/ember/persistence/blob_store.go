package persistence

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const (
	blobStoreVersion                = 1
	MaxBlobListLimit                = 100
	MaxBlobStoreMetadataBytes       = 1 << 20
	MaxBlobStoreQuota         int64 = 1 << 30
)

var (
	ErrInvalidBlobStorePath = errors.New("invalid blob store path")
	ErrInvalidBlobQuota     = errors.New("invalid blob store quota")
	ErrBlobObjectNotFound   = errors.New("blob object not found")
	ErrBlobObjectCorrupt    = errors.New("corrupt blob object")
	ErrBlobObjectTooLarge   = errors.New("blob object exceeds size limit")
	ErrBlobQuotaExceeded    = errors.New("blob store quota exceeded")
	ErrInvalidBlobRange     = errors.New("invalid blob range")
	ErrInvalidBlobListLimit = errors.New("invalid blob list limit")
	ErrBlobStoreCorrupt     = errors.New("corrupt blob store")
	ErrBlobStoreTooLarge    = errors.New("blob store metadata exceeds size limit")
	ErrBlobStoreIO          = errors.New("blob store I/O failure")
)

type BlobStore interface {
	Put(ctx context.Context, bucketID, objectKey string, content []byte) (*models.BlobObject, error)
	Get(ctx context.Context, bucketID, objectKey string) (*models.BlobObject, []byte, error)
	ReadRange(ctx context.Context, bucketID, objectKey string, start, end int64) ([]byte, error)
	List(ctx context.Context, bucketID string, limit int) ([]models.BlobObject, error)
	Delete(ctx context.Context, bucketID, objectKey string) error
	Reset(ctx context.Context) error
}

type blobStoreDiskState struct {
	Version int                 `json:"version"`
	Objects []models.BlobObject `json:"objects"`
}

// FileBlobStore persists bounded object metadata and payloads below one private root.
// Bucket resources and their lifecycle remain owned by ResourceStore.
type FileBlobStore struct {
	mu      sync.RWMutex
	root    string
	quota   int64
	objects map[string]models.BlobObject
}

func NewFileBlobStore(root string, quota int64) (*FileBlobStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, ErrInvalidBlobStorePath
	}
	if quota <= 0 || quota > MaxBlobStoreQuota {
		return nil, ErrInvalidBlobQuota
	}

	absoluteRoot, err := filepath.Abs(filepath.Clean(root))
	if err != nil {
		return nil, ErrInvalidBlobStorePath
	}
	if info, err := os.Stat(absoluteRoot); err == nil {
		if !info.IsDir() {
			return nil, ErrInvalidBlobStorePath
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, ErrBlobStoreIO
	}

	store := &FileBlobStore{
		root:    absoluteRoot,
		quota:   quota,
		objects: make(map[string]models.BlobObject),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func (store *FileBlobStore) load() error {
	data, err := os.ReadFile(store.metadataPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrBlobStoreIO
	}
	if len(data) > MaxBlobStoreMetadataBytes {
		return ErrBlobStoreTooLarge
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state blobStoreDiskState
	if err := decoder.Decode(&state); err != nil {
		return ErrBlobStoreCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrBlobStoreCorrupt
	}
	if state.Version != blobStoreVersion {
		return ErrBlobStoreCorrupt
	}

	objects := make(map[string]models.BlobObject, len(state.Objects))
	var usedBytes int64
	for _, object := range state.Objects {
		if err := object.Validate(); err != nil {
			return ErrBlobStoreCorrupt
		}
		key := blobObjectMapKey(object.BucketID, object.Key)
		if _, exists := objects[key]; exists {
			return ErrBlobStoreCorrupt
		}
		if object.Size > store.quota-usedBytes {
			return ErrBlobQuotaExceeded
		}
		info, err := os.Lstat(store.objectPath(object.BucketID, object.Key))
		if err != nil || !info.Mode().IsRegular() || info.Size() != object.Size {
			return ErrBlobStoreCorrupt
		}
		objects[key] = object
		usedBytes += object.Size
	}

	store.objects = objects
	return nil
}

func (store *FileBlobStore) metadataPath() string {
	return filepath.Join(store.root, "metadata.json")
}

func (store *FileBlobStore) objectsRoot() string {
	return filepath.Join(store.root, "objects")
}

func (store *FileBlobStore) objectPath(bucketID, objectKey string) string {
	return filepath.Join(store.objectsRoot(), bucketID, filepath.FromSlash(objectKey))
}

func blobObjectMapKey(bucketID, objectKey string) string {
	return bucketID + "\x00" + objectKey
}

func validateBlobInputs(bucketID, objectKey string) error {
	if err := models.ValidateBlobBucketID(bucketID); err != nil {
		return err
	}
	return models.ValidateBlobObjectKey(objectKey)
}

func (store *FileBlobStore) ensureRoot() error {
	if err := os.MkdirAll(store.root, 0o700); err != nil {
		return ErrBlobStoreIO
	}
	return nil
}

func (store *FileBlobStore) metadataBytesLocked() ([]byte, error) {
	objects := make([]models.BlobObject, 0, len(store.objects))
	for _, object := range store.objects {
		if err := object.Validate(); err != nil {
			return nil, ErrBlobStoreCorrupt
		}
		objects = append(objects, object)
	}
	sort.Slice(objects, func(i, j int) bool {
		if objects[i].BucketID == objects[j].BucketID {
			return objects[i].Key < objects[j].Key
		}
		return objects[i].BucketID < objects[j].BucketID
	})

	data, err := json.MarshalIndent(blobStoreDiskState{Version: blobStoreVersion, Objects: objects}, "", "  ")
	if err != nil {
		return nil, ErrBlobStoreIO
	}
	data = append(data, '\n')
	if len(data) > MaxBlobStoreMetadataBytes {
		return nil, ErrBlobStoreTooLarge
	}
	return data, nil
}

func (store *FileBlobStore) saveLocked() error {
	data, err := store.metadataBytesLocked()
	if err != nil {
		return err
	}
	if err := store.ensureRoot(); err != nil {
		return err
	}

	temporary, err := os.CreateTemp(store.root, ".ember-blob-metadata-*.tmp")
	if err != nil {
		return ErrBlobStoreIO
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return ErrBlobStoreIO
	}
	if written, err := temporary.Write(data); err != nil || written != len(data) {
		_ = temporary.Close()
		return ErrBlobStoreIO
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return ErrBlobStoreIO
	}
	if err := temporary.Close(); err != nil {
		return ErrBlobStoreIO
	}
	if err := os.Rename(temporaryPath, store.metadataPath()); err != nil {
		return ErrBlobStoreIO
	}
	if directory, err := os.Open(store.root); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func (store *FileBlobStore) usedBytesLocked() int64 {
	var used int64
	for _, object := range store.objects {
		used += object.Size
	}
	return used
}

func writeBlobPayload(path string, content []byte) (string, error) {
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return "", ErrBlobStoreIO
	}
	temporary, err := os.CreateTemp(directory, ".ember-blob-payload-*.tmp")
	if err != nil {
		return "", ErrBlobStoreIO
	}
	temporaryPath := temporary.Name()
	cleanup := func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}
	if err := temporary.Chmod(0o600); err != nil {
		cleanup()
		return "", ErrBlobStoreIO
	}
	if written, err := temporary.Write(content); err != nil || written != len(content) {
		cleanup()
		return "", ErrBlobStoreIO
	}
	if err := temporary.Sync(); err != nil {
		cleanup()
		return "", ErrBlobStoreIO
	}
	if err := temporary.Close(); err != nil {
		_ = os.Remove(temporaryPath)
		return "", ErrBlobStoreIO
	}
	return temporaryPath, nil
}

func moveBlobToBackup(path string) (string, error) {
	directory := filepath.Dir(path)
	placeholder, err := os.CreateTemp(directory, ".ember-blob-backup-*.tmp")
	if err != nil {
		return "", ErrBlobStoreIO
	}
	backupPath := placeholder.Name()
	if err := placeholder.Close(); err != nil {
		_ = os.Remove(backupPath)
		return "", ErrBlobStoreIO
	}
	if err := os.Remove(backupPath); err != nil {
		return "", ErrBlobStoreIO
	}
	if err := os.Rename(path, backupPath); err != nil {
		return "", ErrBlobStoreIO
	}
	return backupPath, nil
}

func rollbackBlobPayload(path, backupPath string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrBlobStoreIO
	}
	if backupPath != "" {
		if err := os.Rename(backupPath, path); err != nil {
			return ErrBlobStoreIO
		}
	}
	return nil
}

func (store *FileBlobStore) readBlobPayloadLocked(object models.BlobObject) ([]byte, error) {
	path := store.objectPath(object.BucketID, object.Key)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && !info.Mode().IsRegular()) {
		return nil, ErrBlobObjectCorrupt
	}
	if err != nil {
		return nil, ErrBlobStoreIO
	}
	if info.Size() != object.Size {
		return nil, ErrBlobObjectCorrupt
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrBlobObjectCorrupt
	}
	if err != nil {
		return nil, ErrBlobStoreIO
	}
	if int64(len(data)) != object.Size {
		return nil, ErrBlobObjectCorrupt
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != object.SHA256 {
		return nil, ErrBlobObjectCorrupt
	}
	return data, nil
}

func (store *FileBlobStore) Put(ctx context.Context, bucketID, objectKey string, content []byte) (*models.BlobObject, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateBlobInputs(bucketID, objectKey); err != nil {
		return nil, err
	}
	if int64(len(content)) > models.MaxBlobObjectSize {
		return nil, ErrBlobObjectTooLarge
	}

	digest := sha256.Sum256(content)
	digestText := hex.EncodeToString(digest[:])
	object := models.BlobObject{
		BucketID:  bucketID,
		Key:       objectKey,
		VersionID: digestText,
		SHA256:    digestText,
		ETag:      digestText,
		Size:      int64(len(content)),
	}
	if err := object.Validate(); err != nil {
		return nil, err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	mapKey := blobObjectMapKey(bucketID, objectKey)
	previous, hadPrevious := store.objects[mapKey]
	used := store.usedBytesLocked()
	if hadPrevious {
		used -= previous.Size
	}
	if object.Size > store.quota-used {
		return nil, ErrBlobQuotaExceeded
	}

	path := store.objectPath(bucketID, objectKey)
	temporaryPath, err := writeBlobPayload(path, content)
	if err != nil {
		return nil, err
	}
	backupPath := ""
	if info, statErr := os.Lstat(path); statErr == nil {
		if !info.Mode().IsRegular() {
			_ = os.Remove(temporaryPath)
			return nil, ErrBlobObjectCorrupt
		}
		backupPath, err = moveBlobToBackup(path)
		if err != nil {
			_ = os.Remove(temporaryPath)
			return nil, err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		_ = os.Remove(temporaryPath)
		return nil, ErrBlobStoreIO
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		_ = rollbackBlobPayload(path, backupPath)
		return nil, ErrBlobStoreIO
	}

	store.objects[mapKey] = object
	if err := store.saveLocked(); err != nil {
		delete(store.objects, mapKey)
		if hadPrevious {
			store.objects[mapKey] = previous
		}
		if rollbackErr := rollbackBlobPayload(path, backupPath); rollbackErr != nil {
			return nil, rollbackErr
		}
		return nil, err
	}
	if backupPath != "" {
		_ = os.Remove(backupPath)
	}
	copy := object
	return &copy, nil
}

func (store *FileBlobStore) Get(ctx context.Context, bucketID, objectKey string) (*models.BlobObject, []byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if err := validateBlobInputs(bucketID, objectKey); err != nil {
		return nil, nil, err
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	object, exists := store.objects[blobObjectMapKey(bucketID, objectKey)]
	if !exists {
		return nil, nil, ErrBlobObjectNotFound
	}
	content, err := store.readBlobPayloadLocked(object)
	if err != nil {
		return nil, nil, err
	}
	copy := object
	return &copy, content, nil
}

func (store *FileBlobStore) ReadRange(ctx context.Context, bucketID, objectKey string, start, end int64) ([]byte, error) {
	object, content, err := store.Get(ctx, bucketID, objectKey)
	if err != nil {
		return nil, err
	}
	if start < 0 || end < start || end > object.Size {
		return nil, ErrInvalidBlobRange
	}
	return append([]byte(nil), content[int(start):int(end)]...), nil
}

func (store *FileBlobStore) List(ctx context.Context, bucketID string, limit int) ([]models.BlobObject, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := models.ValidateBlobBucketID(bucketID); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxBlobListLimit {
		return nil, ErrInvalidBlobListLimit
	}

	store.mu.RLock()
	defer store.mu.RUnlock()

	objects := make([]models.BlobObject, 0, limit)
	for _, object := range store.objects {
		if object.BucketID == bucketID {
			objects = append(objects, object)
		}
	}
	sort.Slice(objects, func(i, j int) bool {
		return objects[i].Key < objects[j].Key
	})
	if len(objects) > limit {
		objects = objects[:limit]
	}
	return objects, nil
}

func (store *FileBlobStore) Delete(ctx context.Context, bucketID, objectKey string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateBlobInputs(bucketID, objectKey); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	mapKey := blobObjectMapKey(bucketID, objectKey)
	object, exists := store.objects[mapKey]
	if !exists {
		return ErrBlobObjectNotFound
	}
	path := store.objectPath(object.BucketID, object.Key)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return ErrBlobObjectCorrupt
	}
	if err != nil {
		return ErrBlobStoreIO
	}
	if !info.Mode().IsRegular() {
		return ErrBlobObjectCorrupt
	}
	backupPath, err := moveBlobToBackup(path)
	if err != nil {
		return err
	}
	delete(store.objects, mapKey)
	if err := store.saveLocked(); err != nil {
		store.objects[mapKey] = object
		if rollbackErr := rollbackBlobPayload(path, backupPath); rollbackErr != nil {
			return rollbackErr
		}
		return err
	}
	_ = os.Remove(backupPath)
	removeEmptyBlobDirectories(filepath.Dir(path), store.objectsRoot())
	return nil
}

func removeEmptyBlobDirectories(directory, stop string) {
	for directory != stop && strings.HasPrefix(directory, stop+string(os.PathSeparator)) {
		if err := os.Remove(directory); err != nil {
			return
		}
		directory = filepath.Dir(directory)
	}
}

func (store *FileBlobStore) Reset(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	if err := os.RemoveAll(store.objectsRoot()); err != nil {
		return ErrBlobStoreIO
	}
	if err := os.Remove(store.metadataPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrBlobStoreIO
	}
	store.objects = make(map[string]models.BlobObject)
	return nil
}

var _ BlobStore = (*FileBlobStore)(nil)
