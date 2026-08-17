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
	"io"
	"strings"
	"time"

	"ember.local/ember/internal/ember"
	"github.com/lib/pq"
)

type Store struct {
	db    *sql.DB
	files *ember.FileStore
}

func newStore(db *sql.DB, files *ember.FileStore) (*Store, error) {
	if db == nil || files == nil {
		return nil, fmt.Errorf("database and filesystem provider are required")
	}
	return &Store{db: db, files: files}, nil
}

func (s *Store) DB() *sql.DB  { return s.db }
func (s *Store) Close() error { return s.db.Close() }

func dbContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 15*time.Second)
}

func newID(prefix string) string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err == nil {
		return prefix + "_" + hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
}

func requestHash(key, digest string, length int64) string {
	h := sha256.Sum256([]byte(key + ":" + digest + ":" + fmt.Sprint(length)))
	return hex.EncodeToString(h[:])
}

func translateDBError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		return ember.ErrNotFound
	}
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		switch string(pqErr.Code) {
		case "23505":
			return ember.ErrAlreadyExists
		case "23503":
			return ember.ErrDependencyExists
		}
		return fmt.Errorf("%w (postgres %s): %s", ember.ErrProvider, pqErr.Code, pqErr.Message)
	}
	return fmt.Errorf("%w: %v", ember.ErrProvider, err)
}

type rowScanner interface{ Scan(...any) error }

func scanResource(row rowScanner) (*ember.Resource, error) {
	var r ember.Resource
	var parent sql.NullString
	var tags []byte
	if err := row.Scan(&r.ID, &r.Name, &r.Type, &parent, &r.Scope, &r.DesiredState, &r.ObservedState, &tags, &r.CreatedAt, &r.UpdatedAt); err != nil {
		return nil, translateDBError(err)
	}
	if parent.Valid {
		r.ParentID = parent.String
	}
	if len(tags) > 0 {
		_ = json.Unmarshal(tags, &r.Tags)
	}
	return &r, nil
}

func scanOperation(row rowScanner) (*ember.Operation, error) {
	var op ember.Operation
	var resource sql.NullString
	var errorCode sql.NullString
	if err := row.Scan(&op.ID, &op.Action, &op.Status, &resource, &op.Scope, &op.RequestID, &op.CorrelationID, &errorCode, &op.CreatedAt, &op.UpdatedAt); err != nil {
		return nil, translateDBError(err)
	}
	if resource.Valid {
		op.ResourceID = resource.String
	}
	if errorCode.Valid {
		op.ErrorCode = errorCode.String
	}
	return &op, nil
}

func scanObject(row rowScanner) (*ember.ObjectVersion, error) {
	var o ember.ObjectVersion
	if err := row.Scan(&o.BucketID, &o.Key, &o.VersionID, &o.SHA256, &o.ETag, &o.Size, &o.Path, &o.Committed); err != nil {
		return nil, translateDBError(err)
	}
	return &o, nil
}

func (s *Store) audit(ctx context.Context, p ember.Principal, action, outcome, target, scope, requestID, correlation, reason, key string) error {
	keyHash := ""
	if key != "" {
		h := sha256.Sum256([]byte(key))
		keyHash = hex.EncodeToString(h[:])
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO audit_events(id,principal,action,outcome,target,key_hash,scope,request_id,correlation_id,reason,policy_version) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, newID("aud"), p.Name, action, outcome, target, keyHash, scope, requestID, correlation, reason, "phase1-v1")
	return err
}

func (s *Store) authorize(ctx context.Context, p ember.Principal, action, scope, target, requestID, correlation string) error {
	if ember.Allowed(p, action, scope) {
		return nil
	}
	_ = s.audit(ctx, p, action, "denied", target, scope, requestID, correlation, "forbidden", "")
	return ember.ErrForbidden
}

const resourceSelect = `SELECT id,name,type,parent_id,scope,desired_state,observed_state,tags,created_at,updated_at FROM resources`
const operationSelect = `SELECT id,action,status,resource_id,scope,request_id,correlation_id,error_code,created_at,updated_at FROM operations`
const objectSelect = `SELECT bucket_id,object_key,version_id,sha256,etag,size,opaque_path,committed_at FROM blob_objects`

func (s *Store) CreateGroup(p ember.Principal, name, scope, idemKey string, raw []byte, requestID, correlation string) (*ember.Resource, *ember.Operation, error) {
	ctx, cancel := dbContext(context.Background())
	defer cancel()
	if err := s.authorize(ctx, p, "group:create", scope, "resourceGroups", requestID, correlation); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(name) == "" || scope == "" || idemKey == "" {
		return nil, nil, ember.ErrInvalidRequest
	}
	h := hashBytes(raw)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, translateDBError(err)
	}
	defer tx.Rollback()
	var oldHash, oldOp, oldResource string
	err = tx.QueryRowContext(ctx, `SELECT i.request_hash,i.operation_id,COALESCE(o.resource_id,'') FROM idempotency_records i JOIN operations o ON o.id=i.operation_id WHERE principal=$1 AND endpoint=$2 AND idem_key=$3 AND expires_at>now()`, p.Name, "group-create", idemKey).Scan(&oldHash, &oldOp, &oldResource)
	if err == nil {
		if oldHash != h {
			return nil, nil, ember.ErrIdempotency
		}
		r, e1 := scanResource(tx.QueryRowContext(ctx, resourceSelect+` WHERE id=$1`, oldResource))
		op, e2 := scanOperation(tx.QueryRowContext(ctx, operationSelect+` WHERE id=$1`, oldOp))
		if e1 != nil || e2 != nil {
			return nil, nil, ember.ErrProvider
		}
		return r, op, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, translateDBError(err)
	}
	var exists string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM resources WHERE parent_id IS NULL AND type='resourceGroup' AND name=$1 AND scope=$2`, name, scope).Scan(&exists); err == nil {
		return nil, nil, ember.ErrAlreadyExists
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, translateDBError(err)
	}
	now := time.Now().UTC()
	rid, oid := newID("rg"), newID("op")
	if _, err := tx.ExecContext(ctx, `INSERT INTO resources(id,name,type,scope,desired_state,observed_state) VALUES($1,$2,'resourceGroup',$3,'created','created')`, rid, name, scope); err != nil {
		return nil, nil, translateDBError(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO operations(id,action,status,resource_id,scope,request_id,correlation_id,created_at,updated_at) VALUES($1,'group:create','succeeded',$2,$3,$4,$5,$6,$6)`, oid, rid, scope, requestID, correlation, now); err != nil {
		return nil, nil, translateDBError(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_records(principal,endpoint,idem_key,request_hash,operation_id,expires_at) VALUES($1,'group-create',$2,$3,$4,$5)`, p.Name, idemKey, h, oid, now.Add(24*time.Hour)); err != nil {
		return nil, nil, translateDBError(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,principal,action,outcome,target,scope,request_id,correlation_id,policy_version) VALUES($1,$2,'group:create','succeeded',$3,$4,$5,$6,'phase1-v1')`, newID("aud"), p.Name, rid, scope, requestID, correlation); err != nil {
		return nil, nil, translateDBError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, translateDBError(err)
	}
	r, _ := scanResource(s.db.QueryRowContext(ctx, resourceSelect+` WHERE id=$1`, rid))
	op, _ := scanOperation(s.db.QueryRowContext(ctx, operationSelect+` WHERE id=$1`, oid))
	return r, op, nil
}

func (s *Store) CreateBucket(p ember.Principal, groupID, name, scope, idemKey string, raw []byte, requestID, correlation string) (*ember.Resource, *ember.Operation, error) {
	ctx, cancel := dbContext(context.Background())
	defer cancel()
	if err := s.authorize(ctx, p, "bucket:create", scope, groupID, requestID, correlation); err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(name) == "" || idemKey == "" {
		return nil, nil, ember.ErrInvalidRequest
	}
	h := hashBytes(raw)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, translateDBError(err)
	}
	defer tx.Rollback()
	var oldHash, oldOp, oldResource string
	err = tx.QueryRowContext(ctx, `SELECT i.request_hash,i.operation_id,COALESCE(o.resource_id,'') FROM idempotency_records i JOIN operations o ON o.id=i.operation_id WHERE principal=$1 AND endpoint=$2 AND idem_key=$3 AND expires_at>now()`, p.Name, "bucket-create", idemKey).Scan(&oldHash, &oldOp, &oldResource)
	if err == nil {
		if oldHash != h {
			return nil, nil, ember.ErrIdempotency
		}
		r, e1 := scanResource(tx.QueryRowContext(ctx, resourceSelect+` WHERE id=$1`, oldResource))
		op, e2 := scanOperation(tx.QueryRowContext(ctx, operationSelect+` WHERE id=$1`, oldOp))
		if e1 != nil || e2 != nil {
			return nil, nil, ember.ErrProvider
		}
		return r, op, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, translateDBError(err)
	}
	var groupName string
	if err := tx.QueryRowContext(ctx, `SELECT name FROM resources WHERE id=$1 AND type='resourceGroup'`, groupID).Scan(&groupName); err != nil {
		return nil, nil, translateDBError(err)
	}
	var exists string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM resources WHERE parent_id=$1 AND type='Ember.Blob/bucket' AND name=$2`, groupID, name).Scan(&exists); err == nil {
		return nil, nil, ember.ErrAlreadyExists
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, translateDBError(err)
	}
	now := time.Now().UTC()
	rid, oid := newID("res"), newID("op")
	if _, err := tx.ExecContext(ctx, `INSERT INTO resources(id,name,type,parent_id,scope,desired_state,observed_state) VALUES($1,$2,'Ember.Blob/bucket',$3,$4,'created','created')`, rid, name, groupID, scope); err != nil {
		return nil, nil, translateDBError(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO operations(id,action,status,resource_id,scope,request_id,correlation_id,created_at,updated_at) VALUES($1,'bucket:create','succeeded',$2,$3,$4,$5,$6,$6)`, oid, rid, scope, requestID, correlation, now); err != nil {
		return nil, nil, translateDBError(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_records(principal,endpoint,idem_key,request_hash,operation_id,expires_at) VALUES($1,'bucket-create',$2,$3,$4,$5)`, p.Name, idemKey, h, oid, now.Add(24*time.Hour)); err != nil {
		return nil, nil, translateDBError(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,principal,action,outcome,target,scope,request_id,correlation_id,policy_version) VALUES($1,$2,'bucket:create','succeeded',$3,$4,$5,$6,'phase1-v1')`, newID("aud"), p.Name, rid, scope, requestID, correlation); err != nil {
		return nil, nil, translateDBError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, translateDBError(err)
	}
	r, _ := scanResource(s.db.QueryRowContext(ctx, resourceSelect+` WHERE id=$1`, rid))
	op, _ := scanOperation(s.db.QueryRowContext(ctx, operationSelect+` WHERE id=$1`, oid))
	_ = groupName
	return r, op, nil
}

func (s *Store) GetResource(p ember.Principal, id, requestID, correlation string) (*ember.Resource, error) {
	ctx, cancel := dbContext(context.Background())
	defer cancel()
	r, err := scanResource(s.db.QueryRowContext(ctx, resourceSelect+` WHERE id=$1`, id))
	if err != nil {
		return nil, err
	}
	if !ember.Allowed(p, "read", r.Scope) {
		_ = s.audit(ctx, p, "resource:read", "denied", id, r.Scope, requestID, correlation, "forbidden", "")
		return nil, ember.ErrForbidden
	}
	return r, nil
}
func (s *Store) GetOperation(p ember.Principal, id, requestID, correlation string) (*ember.Operation, error) {
	ctx, cancel := dbContext(context.Background())
	defer cancel()
	op, err := scanOperation(s.db.QueryRowContext(ctx, operationSelect+` WHERE id=$1`, id))
	if err != nil {
		return nil, err
	}
	if !ember.Allowed(p, "read", op.Scope) {
		_ = s.audit(ctx, p, "operation:read", "denied", id, op.Scope, requestID, correlation, "forbidden", "")
		return nil, ember.ErrForbidden
	}
	return op, nil
}

func (s *Store) DeleteResource(p ember.Principal, id, requestID, correlation string) error {
	ctx, cancel := dbContext(context.Background())
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return translateDBError(err)
	}
	defer tx.Rollback()
	r, err := scanResource(tx.QueryRowContext(ctx, resourceSelect+` WHERE id=$1`, id))
	if err != nil {
		return err
	}
	if err := s.authorize(ctx, p, "bucket:delete", r.Scope, id, requestID, correlation); err != nil {
		return err
	}
	var locked int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM locks WHERE resource_id=$1`, id).Scan(&locked); err != nil {
		return translateDBError(err)
	}
	if locked > 0 {
		_ = s.audit(ctx, p, "resource:delete", "denied", id, r.Scope, requestID, correlation, "locked", "")
		return ember.ErrResourceLocked
	}
	var children int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM resources WHERE parent_id=$1`, id).Scan(&children); err != nil {
		return translateDBError(err)
	}
	if children > 0 {
		return ember.ErrDependencyExists
	}
	if r.Type == "Ember.Blob/bucket" {
		rows, e := tx.QueryContext(ctx, objectSelect+` WHERE bucket_id=$1`, id)
		if e != nil {
			return translateDBError(e)
		}
		for rows.Next() {
			o, e := scanObject(rows)
			if e != nil {
				rows.Close()
				return e
			}
			if e := s.files.Delete(*o); e != nil {
				rows.Close()
				return e
			}
		}
		rows.Close()
		if _, e := tx.ExecContext(ctx, `DELETE FROM blob_objects WHERE bucket_id=$1`, id); e != nil {
			return translateDBError(e)
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM resources WHERE id=$1`, id); err != nil {
		return translateDBError(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,principal,action,outcome,target,scope,request_id,correlation_id,policy_version) VALUES($1,$2,'resource:delete','succeeded',$3,$4,$5,$6,'phase1-v1')`, newID("aud"), p.Name, id, r.Scope, requestID, correlation); err != nil {
		return translateDBError(err)
	}
	return translateDBError(tx.Commit())
}

func (s *Store) AddLock(p ember.Principal, id, kind, note, requestID, correlation string) error {
	return s.lockMutation(p, id, kind, note, requestID, correlation, true)
}
func (s *Store) RemoveLock(p ember.Principal, id, kind, requestID, correlation string) error {
	return s.lockMutation(p, id, kind, "", requestID, correlation, false)
}
func (s *Store) lockMutation(p ember.Principal, id, kind, note, requestID, correlation string, add bool) error {
	ctx, cancel := dbContext(context.Background())
	defer cancel()
	r, err := s.GetResource(p, id, requestID, correlation)
	if err != nil {
		return err
	}
	if kind == "" {
		return ember.ErrInvalidRequest
	}
	if err := s.authorize(ctx, p, "lock:write", r.Scope, id, requestID, correlation); err != nil {
		return err
	}
	var q string
	var args []any
	if add {
		q = `INSERT INTO locks(resource_id,kind,note) VALUES($1,$2,$3) ON CONFLICT(resource_id,kind) DO UPDATE SET note=excluded.note,created_at=now()`
		args = []any{id, kind, note}
	} else {
		q = `DELETE FROM locks WHERE resource_id=$1 AND kind=$2`
		args = []any{id, kind}
	}
	if _, err := s.db.ExecContext(ctx, q, args...); err != nil {
		return translateDBError(err)
	}
	return s.audit(ctx, p, "lock:write", "succeeded", id, r.Scope, requestID, correlation, "", "")
}

func (s *Store) PutObject(p ember.Principal, bucketID, key string, body []byte, expectedLength int64, expectedSHA, idemKey, requestID, correlation string) (*ember.ObjectVersion, *ember.Operation, error) {
	return s.PutObjectStream(p, bucketID, key, strings.NewReader(string(body)), expectedLength, expectedSHA, idemKey, requestID, correlation)
}
func (s *Store) PutObjectStream(p ember.Principal, bucketID, key string, body io.Reader, expectedLength int64, expectedSHA, idemKey, requestID, correlation string) (*ember.ObjectVersion, *ember.Operation, error) {
	ctx, cancel := dbContext(context.Background())
	defer cancel()
	r, err := s.GetResource(p, bucketID, requestID, correlation)
	if err != nil {
		return nil, nil, err
	}
	if r.Type != "Ember.Blob/bucket" {
		return nil, nil, ember.ErrNotFound
	}
	if err := s.authorize(ctx, p, "object:put", r.Scope, bucketID, requestID, correlation); err != nil {
		return nil, nil, err
	}
	if idemKey == "" {
		return nil, nil, ember.ErrInvalidRequest
	}
	if err := ember.ValidateObjectKey(key); err != nil {
		return nil, nil, err
	}
	h := requestHash(key, expectedSHA, expectedLength)
	var oldHash, oldOp string
	err = s.db.QueryRowContext(ctx, `SELECT request_hash,operation_id FROM idempotency_records WHERE principal=$1 AND endpoint=$2 AND idem_key=$3 AND expires_at>now()`, p.Name, "object-put|"+bucketID+"|"+key, idemKey).Scan(&oldHash, &oldOp)
	if err == nil {
		if oldHash != h {
			return nil, nil, ember.ErrIdempotency
		}
		o, e1 := scanObject(s.db.QueryRowContext(ctx, objectSelect+` WHERE bucket_id=$1 AND object_key=$2`, bucketID, key))
		op, e2 := scanOperation(s.db.QueryRowContext(ctx, operationSelect+` WHERE id=$1`, oldOp))
		if e1 != nil || e2 != nil {
			return nil, nil, ember.ErrProvider
		}
		return o, op, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, nil, translateDBError(err)
	}
	oldObj, _ := scanObject(s.db.QueryRowContext(ctx, objectSelect+` WHERE bucket_id=$1 AND object_key=$2`, bucketID, key))
	obj, err := s.files.WriteReader(bucketID, key, body, expectedLength, expectedSHA)
	if err != nil {
		fid := newID("finding")
		_, _ = s.db.ExecContext(ctx, `INSERT INTO repair_findings(id,kind,reference,status) VALUES($1,'upload',$2,'operator_action_required')`, fid, bucketID+":"+key)
		_ = s.audit(ctx, p, "object:put", "failed", bucketID, r.Scope, requestID, correlation, "safe_failure", key)
		return nil, nil, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, nil, translateDBError(err)
	}
	defer tx.Rollback()
	op := ember.Operation{ID: newID("op"), Action: "object:put", Status: "succeeded", ResourceID: bucketID, Scope: r.Scope, RequestID: requestID, CorrelationID: correlation, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if _, err := tx.ExecContext(ctx, `INSERT INTO operations(id,action,status,resource_id,scope,request_id,correlation_id,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$8)`, op.ID, op.Action, op.Status, op.ResourceID, op.Scope, op.RequestID, op.CorrelationID, op.CreatedAt); err != nil {
		return nil, nil, translateDBError(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO blob_objects(bucket_id,object_key,version_id,sha256,etag,size,opaque_path,committed_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(bucket_id,object_key) DO UPDATE SET version_id=excluded.version_id,sha256=excluded.sha256,etag=excluded.etag,size=excluded.size,opaque_path=excluded.opaque_path,committed_at=excluded.committed_at`, obj.BucketID, obj.Key, obj.VersionID, obj.SHA256, obj.ETag, obj.Size, obj.Path, obj.Committed); err != nil {
		return nil, nil, translateDBError(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_records(principal,endpoint,idem_key,request_hash,operation_id,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, p.Name, "object-put|"+bucketID+"|"+key, idemKey, h, op.ID, time.Now().UTC().Add(24*time.Hour)); err != nil {
		return nil, nil, translateDBError(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,principal,action,outcome,target,key_hash,scope,request_id,correlation_id,policy_version) VALUES($1,$2,'object:put','succeeded',$3,$4,$5,$6,$7,'phase1-v1')`, newID("aud"), p.Name, bucketID, hashString(key), r.Scope, requestID, correlation); err != nil {
		return nil, nil, translateDBError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, nil, translateDBError(err)
	}
	if oldObj != nil && oldObj.Path != obj.Path {
		_ = s.files.Delete(*oldObj)
	}
	return &obj, &op, nil
}

func (s *Store) GetObject(p ember.Principal, bucketID, key, requestID, correlation string) (*ember.ObjectVersion, []byte, error) {
	ctx, cancel := dbContext(context.Background())
	defer cancel()
	r, err := s.GetResource(p, bucketID, requestID, correlation)
	if err != nil {
		return nil, nil, err
	}
	if err := s.authorize(ctx, p, "read", r.Scope, bucketID, requestID, correlation); err != nil {
		return nil, nil, err
	}
	o, err := scanObject(s.db.QueryRowContext(ctx, objectSelect+` WHERE bucket_id=$1 AND object_key=$2`, bucketID, key))
	if err != nil {
		return nil, nil, err
	}
	b, err := s.files.Read(*o)
	if err != nil {
		_ = s.audit(ctx, p, "object:get", "failed", bucketID, r.Scope, requestID, correlation, "integrity", key)
		return nil, nil, err
	}
	_ = s.audit(ctx, p, "object:get", "succeeded", bucketID, r.Scope, requestID, correlation, "", key)
	return o, b, nil
}
func (s *Store) DeleteObject(p ember.Principal, bucketID, key, idemKey, requestID, correlation string) error {
	ctx, cancel := dbContext(context.Background())
	defer cancel()
	r, err := s.GetResource(p, bucketID, requestID, correlation)
	if err != nil {
		return err
	}
	if err := s.authorize(ctx, p, "object:delete", r.Scope, bucketID, requestID, correlation); err != nil {
		return err
	}
	if idemKey == "" {
		return ember.ErrInvalidRequest
	}
	h := hashString(key)
	var oldHash string
	if err := s.db.QueryRowContext(ctx, `SELECT request_hash FROM idempotency_records WHERE principal=$1 AND endpoint=$2 AND idem_key=$3 AND expires_at>now()`, p.Name, "object-delete|"+bucketID+"|"+key, idemKey).Scan(&oldHash); err == nil {
		if oldHash != h {
			return ember.ErrIdempotency
		}
		return nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return translateDBError(err)
	}
	o, err := scanObject(s.db.QueryRowContext(ctx, objectSelect+` WHERE bucket_id=$1 AND object_key=$2`, bucketID, key))
	if err != nil {
		return err
	}
	if err := s.files.Delete(*o); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return translateDBError(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM blob_objects WHERE bucket_id=$1 AND object_key=$2`, bucketID, key); err != nil {
		return translateDBError(err)
	}
	op := newID("op")
	if _, err := tx.ExecContext(ctx, `INSERT INTO operations(id,action,status,resource_id,scope,request_id,correlation_id) VALUES($1,'object:delete','succeeded',$2,$3,$4,$5)`, op, bucketID, r.Scope, requestID, correlation); err != nil {
		return translateDBError(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO idempotency_records(principal,endpoint,idem_key,request_hash,operation_id,expires_at) VALUES($1,$2,$3,$4,$5,$6)`, p.Name, "object-delete|"+bucketID+"|"+key, idemKey, h, op, time.Now().UTC().Add(24*time.Hour)); err != nil {
		return translateDBError(err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO audit_events(id,principal,action,outcome,target,key_hash,scope,request_id,correlation_id,policy_version) VALUES($1,$2,'object:delete','succeeded',$3,$4,$5,$6,$7,'phase1-v1')`, newID("aud"), p.Name, bucketID, hashString(key), r.Scope, requestID, correlation); err != nil {
		return translateDBError(err)
	}
	return translateDBError(tx.Commit())
}

func (s *Store) Repair(p ember.Principal, id, action, requestID, correlation string) error {
	ctx, cancel := dbContext(context.Background())
	defer cancel()
	if p.Role != "owner" {
		return ember.ErrForbidden
	}
	if action != "quarantine" && action != "discard" {
		return ember.ErrInvalidRequest
	}
	var kind, ref, status string
	if err := s.db.QueryRowContext(ctx, `SELECT kind,reference,status FROM repair_findings WHERE id=$1`, id).Scan(&kind, &ref, &status); err != nil {
		return translateDBError(err)
	}
	if status != "operator_action_required" {
		return ember.ErrConflict
	}
	if action == "quarantine" {
		if err := s.files.MoveToQuarantine(ref); err != nil {
			return err
		}
	}
	newStatus := "operator_" + action
	if _, err := s.db.ExecContext(ctx, `UPDATE repair_findings SET status=$1 WHERE id=$2`, newStatus, id); err != nil {
		return translateDBError(err)
	}
	return s.audit(ctx, p, "repair:"+action, "succeeded", id, "*", requestID, correlation, "operator_action", "")
}
func (s *Store) Audit(p ember.Principal, scope string) ([]ember.AuditEvent, error) {
	ctx, cancel := dbContext(context.Background())
	defer cancel()
	if !ember.Allowed(p, "read", scope) {
		return nil, ember.ErrForbidden
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,principal,action,outcome,COALESCE(target,''),COALESCE(key_hash,''),scope,request_id,correlation_id,COALESCE(reason,''),policy_version,at FROM audit_events WHERE ($1='' OR scope=$1 OR scope LIKE $1||'/%') ORDER BY at,id`, scope)
	if err != nil {
		return nil, translateDBError(err)
	}
	defer rows.Close()
	var out []ember.AuditEvent
	for rows.Next() {
		var e ember.AuditEvent
		if err := rows.Scan(&e.ID, &e.Principal, &e.Action, &e.Outcome, &e.Target, &e.KeyHash, &e.Scope, &e.RequestID, &e.CorrelationID, &e.Reason, &e.PolicyVersion, &e.At); err != nil {
			return nil, translateDBError(err)
		}
		out = append(out, e)
	}
	return out, translateDBError(rows.Err())
}
func (s *Store) Findings(p ember.Principal) ([]ember.Finding, error) {
	ctx, cancel := dbContext(context.Background())
	defer cancel()
	if !ember.Allowed(p, "read", p.Scope) {
		return nil, ember.ErrForbidden
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,kind,reference,status,created_at FROM repair_findings ORDER BY created_at,id`)
	if err != nil {
		return nil, translateDBError(err)
	}
	defer rows.Close()
	var out []ember.Finding
	for rows.Next() {
		var f ember.Finding
		if err := rows.Scan(&f.ID, &f.Kind, &f.Reference, &f.Status, &f.CreatedAt); err != nil {
			return nil, translateDBError(err)
		}
		out = append(out, f)
	}
	return out, translateDBError(rows.Err())
}

func hashBytes(b []byte) string  { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func hashString(s string) string { return hashBytes([]byte(s)) }
