package ember

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

var (
	ErrInvalidResourceOperationCoordinator = errors.New("invalid resource operation coordinator")
	ErrInvalidResourceOperationRequest     = errors.New("invalid resource operation request")
	ErrInvalidResourceOperationEffect      = errors.New("invalid resource operation effect")
	ErrResourceOperationFailed             = errors.New("resource operation failed")
	ErrOperationAuditWrite                 = errors.New("operation audit write failed")
	ErrOperationIdentifier                 = errors.New("operation identifier generation failed")
)

// ResourceOperationRequest identifies one bounded state-changing request.
// RequestID is the idempotency key for the effect, while ScopeID is retained
// on the audit entry for scoped inspection.
type ResourceOperationRequest struct {
	ResourceID    string
	ScopeID       string
	Action        string
	RequestID     string
	CorrelationID string
}

// ResourceOperationEffect performs the state change after idempotency has
// been checked. It must not persist operation or audit records itself.
type ResourceOperationEffect func(context.Context) error

// ResourceOperationResult contains the durable operation and whether the
// effect was skipped because the request had already completed.
type ResourceOperationResult struct {
	Operation models.Operation
	Replayed  bool
}

// ResourceOperationControlPlane is the complete operation/audit inspection
// boundary. Execute coordinates one state-changing effect with its durable
// operation and audit records.
type ResourceOperationControlPlane interface {
	Execute(ctx context.Context, request ResourceOperationRequest, effect ResourceOperationEffect) (*ResourceOperationResult, error)
	GetOperation(ctx context.Context, operationID string) (*models.Operation, error)
	GetOperationByRequestID(ctx context.Context, requestID string) (*models.Operation, error)
	ListOperations(ctx context.Context, resourceID string, limit int) ([]models.Operation, error)
	ListAuditHistory(ctx context.Context, resourceID string, limit int) ([]models.AuditEntry, error)
}

// ResourceOperationCoordinator binds one state-changing effect to the
// approved operation and audit persistence boundaries. Its mutex makes the
// check/effect/record sequence single-flight for a coordinator instance;
// durable replay remains available after reopening the stores.
type ResourceOperationCoordinator struct {
	operations persistence.OperationStore
	audits     persistence.AuditStore
	mu         sync.Mutex
}

// NewResourceOperationCoordinator creates a coordinator over existing stores.
func NewResourceOperationCoordinator(operations persistence.OperationStore, audits persistence.AuditStore) (*ResourceOperationCoordinator, error) {
	if operations == nil || audits == nil {
		return nil, ErrInvalidResourceOperationCoordinator
	}
	return &ResourceOperationCoordinator{operations: operations, audits: audits}, nil
}

func newOperationIdentifier() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", ErrOperationIdentifier
	}
	return "operation-" + hex.EncodeToString(raw[:]), nil
}

func validOperationRequestText(value string, maxLength int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maxLength
}

func (request ResourceOperationRequest) validate() error {
	if !validOperationRequestText(request.ResourceID, models.MaxOperationResourceIDLength) ||
		!validOperationRequestText(request.Action, models.MaxAuditActionLength) ||
		!validOperationRequestText(request.RequestID, models.MaxOperationRequestIDLength) ||
		!validOperationRequestText(request.CorrelationID, models.MaxOperationCorrelationIDLength) {
		return ErrInvalidResourceOperationRequest
	}
	if request.ScopeID != "" && !validOperationRequestText(request.ScopeID, models.MaxAuditScopeIDLength) {
		return ErrInvalidResourceOperationRequest
	}
	return nil
}

func (coordinator *ResourceOperationCoordinator) Execute(ctx context.Context, request ResourceOperationRequest, effect ResourceOperationEffect) (*ResourceOperationResult, error) {
	if ctx == nil {
		return nil, ErrInvalidResourceOperationRequest
	}
	if err := request.validate(); err != nil {
		return nil, err
	}
	if effect == nil {
		return nil, ErrInvalidResourceOperationEffect
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()

	existing, err := coordinator.operations.GetByRequestID(ctx, request.RequestID)
	if err == nil {
		if existing.ResourceID != request.ResourceID {
			return nil, persistence.ErrOperationRequestConflict
		}
		result := &ResourceOperationResult{Operation: *existing, Replayed: true}
		if err := coordinator.ensureAudit(ctx, request, *existing); err != nil {
			return nil, err
		}
		if existing.Status == models.OperationStatusFailed {
			return result, ErrResourceOperationFailed
		}
		return result, nil
	}
	if !errors.Is(err, persistence.ErrOperationNotFound) {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	effectErr := effect(ctx)
	status := models.OperationStatusSucceeded
	outcome := "succeeded"
	if effectErr != nil {
		status = models.OperationStatusFailed
		outcome = "failed"
	}

	createdAt := time.Now().UTC()
	operationID, err := newOperationIdentifier()
	if err != nil {
		return nil, err
	}
	operation := models.Operation{
		ID:            operationID,
		ResourceID:    request.ResourceID,
		CorrelationID: request.CorrelationID,
		RequestID:     request.RequestID,
		Status:        status,
		CreatedAt:     createdAt,
		UpdatedAt:     createdAt,
		Outcome:       outcome,
	}
	stored, err := coordinator.operations.Create(ctx, operation)
	if err != nil {
		return nil, err
	}
	if stored.ID != operation.ID {
		if err := coordinator.ensureAudit(ctx, request, *stored); err != nil {
			return nil, err
		}
		return &ResourceOperationResult{Operation: *stored}, nil
	}

	if err := coordinator.ensureAudit(ctx, request, *stored); err != nil {
		return nil, err
	}
	result := &ResourceOperationResult{Operation: *stored}
	if effectErr != nil {
		return result, ErrResourceOperationFailed
	}
	return result, nil
}

func (coordinator *ResourceOperationCoordinator) ensureAudit(ctx context.Context, request ResourceOperationRequest, operation models.Operation) error {
	entry := models.AuditEntry{
		ID:            "audit-" + operation.ID,
		OperationID:   operation.ID,
		ResourceID:    operation.ResourceID,
		ScopeID:       request.ScopeID,
		CorrelationID: operation.CorrelationID,
		RequestID:     operation.RequestID,
		Action:        request.Action,
		Outcome:       operation.Outcome,
		CreatedAt:     operation.CreatedAt,
	}
	if err := coordinator.audits.Append(ctx, entry); err != nil && !errors.Is(err, persistence.ErrDuplicateAuditEntry) {
		return ErrOperationAuditWrite
	}
	return nil
}

func (coordinator *ResourceOperationCoordinator) ListAuditHistory(ctx context.Context, resourceID string, limit int) ([]models.AuditEntry, error) {
	return coordinator.audits.List(ctx, resourceID, limit)
}

func (coordinator *ResourceOperationCoordinator) GetOperation(ctx context.Context, operationID string) (*models.Operation, error) {
	return coordinator.operations.Get(ctx, operationID)
}

func (coordinator *ResourceOperationCoordinator) GetOperationByRequestID(ctx context.Context, requestID string) (*models.Operation, error) {
	return coordinator.operations.GetByRequestID(ctx, requestID)
}

func (coordinator *ResourceOperationCoordinator) ListOperations(ctx context.Context, resourceID string, limit int) ([]models.Operation, error) {
	return coordinator.operations.List(ctx, resourceID, limit)
}

var _ ResourceOperationControlPlane = (*ResourceOperationCoordinator)(nil)
