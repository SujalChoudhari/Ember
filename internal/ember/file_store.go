package ember

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type FileStore struct {
	root       string
	production bool
	used       int64
	mu         sync.Mutex
}

func NewFileStore(rootPath string, production bool) (*FileStore, error) {
	if rootPath == "" {
		return nil, fmt.Errorf("blob root is required")
	}
	absoluteRoot, err := filepath.Abs(rootPath)
	if err != nil {
		return nil, err
	}
	rootInfo, err := os.Lstat(absoluteRoot)
	if err != nil {
		return nil, err
	}
	if rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return nil, fmt.Errorf("blob root must be a real directory")
	}
	if production && !confinementAvailable(absoluteRoot) {
		return nil, ErrProvider
	}
	for _, providerDirectory := range []string{"staging", "objects", "quarantine"} {
		if err := os.MkdirAll(filepath.Join(absoluteRoot, providerDirectory), 0o750); err != nil {
			return nil, err
		}
	}
	return &FileStore{root: absoluteRoot, production: production}, nil
}

func (fileStore *FileStore) Root() string { return fileStore.root }

func generateRandomID(prefix string) string {
	var randomBytes [12]byte
	if _, err := rand.Read(randomBytes[:]); err != nil {
		return prefix + "_fallback"
	}
	return prefix + "_" + hex.EncodeToString(randomBytes[:])
}

func syncDirectory(directoryPath string) error {
	directory, err := os.Open(directoryPath)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}

func (fileStore *FileStore) Write(bucketID, objectKey string, body []byte, expectedLength int64, expectedSHA256 string) (ObjectVersion, error) {
	return fileStore.WriteReader(bucketID, objectKey, bytes.NewReader(body), expectedLength, expectedSHA256)
}

func (fileStore *FileStore) WriteReader(bucketID, objectKey string, body io.Reader, expectedLength int64, expectedSHA256 string) (ObjectVersion, error) {
	if err := validateBucketID(bucketID); err != nil {
		return ObjectVersion{}, err
	}
	if err := validateKey(objectKey); err != nil {
		return ObjectVersion{}, err
	}
	if expectedLength < 0 {
		return ObjectVersion{}, ErrInvalidRequest
	}
	if expectedLength > MaxObjectSize {
		return ObjectVersion{}, ErrPayloadTooLarge
	}
	if body == nil {
		return ObjectVersion{}, ErrInvalidRequest
	}
	fileStore.mu.Lock()
	defer fileStore.mu.Unlock()
	if fileStore.used+expectedLength > LogicalQuotaBytes {
		return ObjectVersion{}, ErrQuota
	}

	stagingRelativePath := filepath.Join("staging", generateRandomID("part")+".part")
	stagingPath := filepath.Join(fileStore.root, stagingRelativePath)
	stagingFile, err := providerOpen(fileStore.root, stagingRelativePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o640, fileStore.production)
	if err != nil {
		return ObjectVersion{}, ErrProvider
	}
	cleanupStagingFile := func() { _ = stagingFile.Close(); _ = os.Remove(stagingPath) }
	hasher := sha256.New()
	writtenLength, copyErr := io.CopyN(io.MultiWriter(stagingFile, hasher), body, expectedLength)
	if copyErr != nil || writtenLength != expectedLength {
		cleanupStagingFile()
		return ObjectVersion{}, ErrInvalidRequest
	}
	var extraByte [1]byte
	if bytesRead, readErr := body.Read(extraByte[:]); bytesRead > 0 || (readErr == nil && bytesRead == 0) {
		cleanupStagingFile()
		return ObjectVersion{}, ErrInvalidRequest
	}
	if err := stagingFile.Sync(); err != nil {
		cleanupStagingFile()
		return ObjectVersion{}, ErrProvider
	}
	if err := stagingFile.Close(); err != nil {
		_ = os.Remove(stagingPath)
		return ObjectVersion{}, ErrProvider
	}
	if err := syncDirectory(filepath.Join(fileStore.root, "staging")); err != nil {
		_ = os.Remove(stagingPath)
		return ObjectVersion{}, ErrProvider
	}
	actualSHA256 := hex.EncodeToString(hasher.Sum(nil))
	if expectedSHA256 == "" || !strings.EqualFold(expectedSHA256, actualSHA256) {
		_ = os.Remove(stagingPath)
		return ObjectVersion{}, ErrChecksum
	}

	versionID := generateRandomID("ver")
	shard := actualSHA256[:4]
	objectDirectoryRelativePath := filepath.Join("objects", bucketID, shard[:2], shard[2:])
	if err := mkdirAllWithinRoot(fileStore.root, objectDirectoryRelativePath, 0o750); err != nil {
		_ = os.Remove(stagingPath)
		return ObjectVersion{}, ErrProvider
	}
	objectRelativePath := filepath.Join(objectDirectoryRelativePath, versionID+".blob")
	if err := providerPathCheck(fileStore.root, objectRelativePath, fileStore.production); err == nil {
		_ = os.Remove(stagingPath)
		return ObjectVersion{}, ErrProvider
	} else if !os.IsNotExist(err) {
		_ = os.Remove(stagingPath)
		return ObjectVersion{}, ErrProvider
	}
	finalObjectPath := filepath.Join(fileStore.root, objectRelativePath)
	if err := os.Rename(stagingPath, finalObjectPath); err != nil {
		_ = os.Remove(stagingPath)
		return ObjectVersion{}, ErrProvider
	}
	if err := syncDirectory(filepath.Dir(finalObjectPath)); err != nil {
		return ObjectVersion{}, ErrProvider
	}
	if err := syncDirectory(filepath.Join(fileStore.root, "staging")); err != nil {
		return ObjectVersion{}, ErrProvider
	}
	fileStore.used += expectedLength
	return ObjectVersion{
		BucketID:  bucketID,
		Key:       objectKey,
		VersionID: versionID,
		SHA256:    actualSHA256,
		ETag:      "sha256:" + actualSHA256,
		Size:      expectedLength,
		Path:      filepath.ToSlash(objectRelativePath),
		Committed: time.Now().UTC(),
	}, nil
}

func (fileStore *FileStore) Read(objectVersion ObjectVersion) ([]byte, error) {
	if err := validateStoredPath(objectVersion.Path); err != nil {
		return nil, err
	}
	fileStore.mu.Lock()
	defer fileStore.mu.Unlock()
	objectFile, err := providerOpen(fileStore.root, objectVersion.Path, os.O_RDONLY, 0, fileStore.production)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrObjectUncommitted
		}
		return nil, ErrProvider
	}
	objectBytes, err := io.ReadAll(io.LimitReader(objectFile, MaxObjectSize+1))
	_ = objectFile.Close()
	if err != nil {
		return nil, ErrProvider
	}
	if int64(len(objectBytes)) > MaxObjectSize {
		return nil, ErrObjectCorrupt
	}
	checksum := sha256.Sum256(objectBytes)
	if hex.EncodeToString(checksum[:]) != objectVersion.SHA256 {
		return nil, ErrObjectCorrupt
	}
	return objectBytes, nil
}

func (fileStore *FileStore) Delete(objectVersion ObjectVersion) error {
	if err := validateStoredPath(objectVersion.Path); err != nil {
		return err
	}
	fileStore.mu.Lock()
	defer fileStore.mu.Unlock()
	if err := providerPathCheck(fileStore.root, objectVersion.Path, fileStore.production); err != nil && !os.IsNotExist(err) {
		return ErrProvider
	}
	if err := os.Remove(filepath.Join(fileStore.root, objectVersion.Path)); err != nil && !os.IsNotExist(err) {
		return ErrProvider
	}
	if err := syncDirectory(filepath.Dir(filepath.Join(fileStore.root, objectVersion.Path))); err != nil && !os.IsNotExist(err) {
		return ErrProvider
	}
	if fileStore.used >= objectVersion.Size {
		fileStore.used -= objectVersion.Size
	} else {
		fileStore.used = 0
	}
	return nil
}

// MoveToQuarantine moves an existing provider-owned staging or object file to
// a provider-generated quarantine name. The reference is always root-relative
// and may not name the quarantine tree itself.
func (fileStore *FileStore) MoveToQuarantine(storedReference string) error {
	if err := validateStoredPath(storedReference); err != nil {
		return err
	}
	storedReference = filepath.ToSlash(storedReference)
	if !strings.HasPrefix(storedReference, "staging/") && !strings.HasPrefix(storedReference, "objects/") {
		return ErrPathUnsafe
	}

	fileStore.mu.Lock()
	defer fileStore.mu.Unlock()

	sourcePath := filepath.Join(fileStore.root, filepath.FromSlash(storedReference))
	sourceInfo, err := os.Lstat(sourcePath)
	if err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return ErrProvider
	}
	if sourceInfo.Mode()&os.ModeSymlink != 0 || !sourceInfo.Mode().IsRegular() {
		return ErrPathUnsafe
	}
	if err := providerPathCheck(fileStore.root, storedReference, fileStore.production); err != nil {
		if os.IsNotExist(err) {
			return ErrNotFound
		}
		return ErrPathUnsafe
	}

	quarantineRelativePath := filepath.Join("quarantine", generateRandomID("orphan")+".quarantine")
	if err := validateStoredPath(quarantineRelativePath); err != nil {
		return err
	}
	if err := providerPathCheck(fileStore.root, filepath.Dir(quarantineRelativePath), fileStore.production); err != nil {
		return ErrProvider
	}
	if err := providerPathCheck(fileStore.root, quarantineRelativePath, fileStore.production); err == nil {
		return ErrProvider
	} else if !os.IsNotExist(err) {
		return ErrProvider
	}
	if err := os.Rename(sourcePath, filepath.Join(fileStore.root, filepath.FromSlash(quarantineRelativePath))); err != nil {
		return ErrProvider
	}
	if err := syncDirectory(filepath.Dir(sourcePath)); err != nil {
		return ErrProvider
	}
	if err := syncDirectory(filepath.Join(fileStore.root, "quarantine")); err != nil {
		return ErrProvider
	}
	return nil
}

func (fileStore *FileStore) Reset() error {
	fileStore.mu.Lock()
	defer fileStore.mu.Unlock()
	for _, providerDirectory := range []string{"staging", "objects", "quarantine"} {
		providerDirectoryPath := filepath.Join(fileStore.root, providerDirectory)
		if err := os.RemoveAll(providerDirectoryPath); err != nil {
			return err
		}
		if err := os.MkdirAll(providerDirectoryPath, 0o750); err != nil {
			return err
		}
		if err := syncDirectory(providerDirectoryPath); err != nil {
			return err
		}
	}
	fileStore.used = 0
	return syncDirectory(fileStore.root)
}
