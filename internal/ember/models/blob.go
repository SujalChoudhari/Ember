package models

import (
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

const (
	MaxBlobBucketIDLength        = MaxResourceIDLength
	MaxBlobObjectKeyBytes        = 1024
	MaxBlobObjectSize      int64 = 10 * 1024 * 1024
	MaxBlobVersionIDLength       = 128
	MaxBlobSHA256Length          = 64
	MaxBlobETagLength            = 128
)

type BlobObject struct {
	BucketID  string
	Key       string
	VersionID string
	SHA256    string
	ETag      string
	Size      int64
}

var (
	ErrInvalidBlobObject    = errors.New("invalid blob object")
	ErrInvalidBlobBucketID  = errors.New("invalid blob bucket ID")
	ErrInvalidBlobObjectKey = errors.New("invalid blob object key")
)

func ValidateBlobBucketID(bucketID string) error {
	if !validBlobText(bucketID, MaxBlobBucketIDLength) ||
		bucketID == "." || bucketID == ".." || strings.ContainsAny(bucketID, "/\\\x00") {
		return ErrInvalidBlobBucketID
	}
	return nil
}

func ValidateBlobObjectKey(objectKey string) error {
	if !validBlobText(objectKey, MaxBlobObjectKeyBytes) ||
		strings.ContainsRune(objectKey, 0) || strings.ContainsRune(objectKey, '\\') || filepath.IsAbs(objectKey) {
		return ErrInvalidBlobObjectKey
	}
	for _, segment := range strings.Split(filepath.ToSlash(objectKey), "/") {
		if segment == "" || segment == "." || segment == ".." {
			return ErrInvalidBlobObjectKey
		}
	}
	return nil
}

func (object BlobObject) Validate() error {
	if ValidateBlobBucketID(object.BucketID) != nil ||
		ValidateBlobObjectKey(object.Key) != nil ||
		!validBlobText(object.VersionID, MaxBlobVersionIDLength) ||
		!validBlobText(object.ETag, MaxBlobETagLength) ||
		object.Size < 0 || object.Size > MaxBlobObjectSize ||
		len(object.SHA256) != MaxBlobSHA256Length {
		return ErrInvalidBlobObject
	}
	if _, err := hex.DecodeString(object.SHA256); err != nil {
		return ErrInvalidBlobObject
	}
	return nil
}

func validBlobText(value string, maxLength int) bool {
	return strings.TrimSpace(value) != "" && len([]byte(value)) <= maxLength && utf8.ValidString(value)
}
