package ember

import "time"

type Resource struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Type          string            `json:"type"`
	ParentID      string            `json:"parentId,omitempty"`
	Scope         string            `json:"scope"`
	DesiredState  string            `json:"desiredState"`
	ObservedState string            `json:"observedState"`
	Tags          map[string]string `json:"tags,omitempty"`
	CreatedAt     time.Time         `json:"createdAt"`
	UpdatedAt     time.Time         `json:"updatedAt"`
}

type Operation struct {
	ID            string    `json:"id"`
	Action        string    `json:"action"`
	Status        string    `json:"status"`
	ResourceID    string    `json:"resourceId,omitempty"`
	Scope         string    `json:"scope"`
	RequestID     string    `json:"requestId"`
	CorrelationID string    `json:"correlationId"`
	ErrorCode     string    `json:"errorCode,omitempty"`
	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
}

type Lock struct {
	Kind      string    `json:"kind"`
	Note      string    `json:"note,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type ObjectVersion struct {
	BucketID  string    `json:"bucketId"`
	Key       string    `json:"key"`
	VersionID string    `json:"versionId"`
	SHA256    string    `json:"sha256"`
	ETag      string    `json:"etag"`
	Size      int64     `json:"size"`
	Path      string    `json:"-"`
	Committed time.Time `json:"committedAt"`
}

type AuditEvent struct {
	ID            string    `json:"id"`
	Principal     string    `json:"principal"`
	Action        string    `json:"action"`
	Outcome       string    `json:"outcome"`
	Target        string    `json:"target,omitempty"`
	KeyHash       string    `json:"keyHash,omitempty"`
	Scope         string    `json:"scope"`
	RequestID     string    `json:"requestId"`
	CorrelationID string    `json:"correlationId"`
	Reason        string    `json:"reason,omitempty"`
	PolicyVersion string    `json:"policyVersion"`
	At            time.Time `json:"at"`
}

type Finding struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Reference string    `json:"reference"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

type idempotencyRecord struct {
	requestHash string
	operationID string
	resourceID  string
	expiresAt   time.Time
}
