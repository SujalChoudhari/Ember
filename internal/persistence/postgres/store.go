package postgres

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"ember.local/ember/internal/ember"
	"github.com/lib/pq"
)

type Store struct {
	db    *sql.DB
	files *ember.FileStore
}

func newStore(database *sql.DB, fileStore *ember.FileStore) (*Store, error) {
	if database == nil || fileStore == nil {
		return nil, fmt.Errorf("database and filesystem provider are required")
	}
	return &Store{db: database, files: fileStore}, nil
}

func (store *Store) DB() *sql.DB  { return store.db }
func (store *Store) Close() error { return store.db.Close() }

func databaseContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, 15*time.Second)
}

func generateID(prefix string) string {
	var randomBytes [12]byte
	if _, err := rand.Read(randomBytes[:]); err == nil {
		return prefix + "_" + hex.EncodeToString(randomBytes[:])
	}
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func translateDatabaseError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ember.ErrNotFound
	}
	var postgresError *pq.Error
	if errors.As(err, &postgresError) {
		switch string(postgresError.Code) {
		case "23505":
			return ember.ErrAlreadyExists
		case "23503":
			return ember.ErrDependencyExists
		}
		return fmt.Errorf("%w (postgres %s): %s", ember.ErrProvider, postgresError.Code, postgresError.Message)
	}
	return fmt.Errorf("%w: %v", ember.ErrProvider, err)
}

type rowScanner interface{ Scan(...any) error }

func scanResource(row rowScanner) (*ember.Resource, error) {
	var resource ember.Resource
	var parentID sql.NullString
	var tagsJSON []byte
	if err := row.Scan(&resource.ID, &resource.Name, &resource.Type, &parentID, &resource.Scope, &resource.DesiredState, &resource.ObservedState, &tagsJSON, &resource.CreatedAt, &resource.UpdatedAt); err != nil {
		return nil, translateDatabaseError(err)
	}
	if parentID.Valid {
		resource.ParentID = parentID.String
	}
	if len(tagsJSON) > 0 {
		_ = json.Unmarshal(tagsJSON, &resource.Tags)
	}
	return &resource, nil
}

func scanOperation(row rowScanner) (*ember.Operation, error) {
	var operation ember.Operation
	var resourceID sql.NullString
	var errorCode sql.NullString
	if err := row.Scan(&operation.ID, &operation.Action, &operation.Status, &resourceID, &operation.Scope, &operation.RequestID, &operation.CorrelationID, &errorCode, &operation.CreatedAt, &operation.UpdatedAt); err != nil {
		return nil, translateDatabaseError(err)
	}
	if resourceID.Valid {
		operation.ResourceID = resourceID.String
	}
	if errorCode.Valid {
		operation.ErrorCode = errorCode.String
	}
	return &operation, nil
}

func scanObject(row rowScanner) (*ember.ObjectVersion, error) {
	var objectVersion ember.ObjectVersion
	if err := row.Scan(&objectVersion.BucketID, &objectVersion.Key, &objectVersion.VersionID, &objectVersion.SHA256, &objectVersion.ETag, &objectVersion.Size, &objectVersion.Path, &objectVersion.Committed); err != nil {
		return nil, translateDatabaseError(err)
	}
	return &objectVersion, nil
}

const resourceSelect = `SELECT id,name,type,parent_id,scope,desired_state,observed_state,tags,created_at,updated_at FROM resources`
const operationSelect = `SELECT id,action,status,resource_id,scope,request_id,correlation_id,error_code,created_at,updated_at FROM operations`
const objectSelect = `SELECT bucket_id,object_key,version_id,sha256,etag,size,opaque_path,committed_at FROM blob_objects`

func hashBytes(payload []byte) string {
	checksum := sha256.Sum256(payload)
	return hex.EncodeToString(checksum[:])
}

func hashString(value string) string { return hashBytes([]byte(value)) }
