package ember

import (
	"errors"
)

const (
	MaxObjectSize     int64 = 10 * 1024 * 1024
	MaxObjectKeyBytes       = 1024
	LogicalQuotaBytes int64 = 1 << 30
)

var (
	ErrInvalidRequest    = errors.New("invalid request")
	ErrUnauthorized      = errors.New("unauthorized")
	ErrForbidden         = errors.New("forbidden")
	ErrNotFound          = errors.New("not found")
	ErrAlreadyExists     = errors.New("already exists")
	ErrConflict          = errors.New("conflict")
	ErrResourceLocked    = errors.New("resource locked")
	ErrDependencyExists  = errors.New("dependency exists")
	ErrIdempotency       = errors.New("idempotency conflict")
	ErrQuota             = errors.New("quota exceeded")
	ErrPayloadTooLarge   = errors.New("payload too large")
	ErrPathUnsafe        = errors.New("unsafe object key")
	ErrChecksum          = errors.New("checksum mismatch")
	ErrObjectUncommitted = errors.New("object not committed")
	ErrObjectCorrupt     = errors.New("object corrupt")
	ErrProvider          = errors.New("provider unavailable")
	ErrRecovery          = errors.New("recovery required")
)

type APIError struct {
	Code          string `json:"code"`
	Message       string `json:"message"`
	Target        string `json:"target,omitempty"`
	Retryable     bool   `json:"retryable"`
	OperationID   string `json:"operationId,omitempty"`
	CorrelationID string `json:"correlationId,omitempty"`
	Status        int    `json:"-"`
}

func apiErr(err error, correlationID string) *APIError {
	statusCode, errorCode, message, retryable := 500, "InternalError", "internal error", false
	switch {
	case errors.Is(err, ErrInvalidRequest):
		statusCode, errorCode, message = 400, "InvalidRequest", "request is invalid"
	case errors.Is(err, ErrUnauthorized):
		statusCode, errorCode, message = 401, "Unauthorized", "authentication required"
	case errors.Is(err, ErrForbidden):
		statusCode, errorCode, message = 403, "Forbidden", "operation is not permitted"
	case errors.Is(err, ErrNotFound):
		statusCode, errorCode, message = 404, "NotFound", "resource was not found"
	case errors.Is(err, ErrAlreadyExists):
		statusCode, errorCode, message = 409, "AlreadyExists", "resource already exists"
	case errors.Is(err, ErrConflict):
		statusCode, errorCode, message = 409, "Conflict", "request conflicts with current state"
	case errors.Is(err, ErrResourceLocked):
		statusCode, errorCode, message = 409, "ResourceLocked", "resource is locked"
	case errors.Is(err, ErrDependencyExists):
		statusCode, errorCode, message = 409, "DependencyExists", "dependent resources exist"
	case errors.Is(err, ErrIdempotency):
		statusCode, errorCode, message = 409, "IdempotencyConflict", "idempotency key was used for another request"
	case errors.Is(err, ErrQuota):
		statusCode, errorCode, message = 413, "QuotaExceeded", "storage quota exceeded"
	case errors.Is(err, ErrPayloadTooLarge):
		statusCode, errorCode, message = 413, "PayloadTooLarge", "payload exceeds the synchronous limit"
	case errors.Is(err, ErrPathUnsafe):
		statusCode, errorCode, message = 422, "PathUnsafe", "object key is not allowed"
	case errors.Is(err, ErrChecksum):
		statusCode, errorCode, message = 422, "ChecksumMismatch", "content checksum does not match"
	case errors.Is(err, ErrObjectUncommitted):
		statusCode, errorCode, message = 422, "ObjectNotCommitted", "object is not committed"
	case errors.Is(err, ErrObjectCorrupt):
		statusCode, errorCode, message = 500, "ObjectCorrupt", "committed object bytes failed verification"
	case errors.Is(err, ErrRecovery):
		statusCode, errorCode, message = 409, "RecoveryRequired", "operator recovery is required"
	case errors.Is(err, ErrProvider):
		statusCode, errorCode, message, retryable = 503, "ProviderUnavailable", "provider is unavailable", true
	}
	return &APIError{Code: errorCode, Message: message, Retryable: retryable, Status: statusCode, CorrelationID: correlationID}
}
