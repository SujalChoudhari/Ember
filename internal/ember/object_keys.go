package ember

import (
	"path/filepath"
	"strings"
	"unicode/utf8"
)

func validateKey(objectKey string) error {
	if objectKey == "" || len([]byte(objectKey)) > MaxObjectKeyBytes || !utf8.ValidString(objectKey) || strings.ContainsRune(objectKey, 0) || strings.Contains(objectKey, "\\") || filepath.IsAbs(objectKey) {
		return ErrPathUnsafe
	}
	for _, pathSegment := range strings.Split(objectKey, "/") {
		if pathSegment == "" || pathSegment == "." || pathSegment == ".." {
			return ErrPathUnsafe
		}
	}
	return nil
}

func ValidateObjectKey(objectKey string) error { return validateKey(objectKey) }

func validateStoredPath(storedPath string) error {
	if storedPath == "" || filepath.IsAbs(storedPath) || strings.ContainsRune(storedPath, 0) || strings.Contains(storedPath, "\\\\") {
		return ErrPathUnsafe
	}
	for _, pathSegment := range strings.Split(filepath.ToSlash(storedPath), "/") {
		if pathSegment == "" || pathSegment == "." || pathSegment == ".." {
			return ErrPathUnsafe
		}
	}
	return nil
}

func validateBucketID(bucketID string) error {
	if bucketID == "" || strings.ContainsAny(bucketID, "/\\\\") || bucketID == "." || bucketID == ".." {
		return ErrPathUnsafe
	}
	return nil
}
