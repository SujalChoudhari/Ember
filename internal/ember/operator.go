package ember

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
)

var (
	ErrInvalidOperator          = errors.New("invalid operator")
	ErrInvalidOperatorPrincipal = errors.New("invalid operator principal")
	ErrOperatorScopeDenied      = errors.New("operator scope denied")
	ErrOperatorResetUnavailable = errors.New("operator reset unavailable")
	ErrOperatorBucketRequired   = errors.New("operator resource is not a bucket")
)

type OperatorPrincipal struct {
	ScopeID string
}

func (principal OperatorPrincipal) validate() error {
	if principal.ScopeID != "" && (strings.TrimSpace(principal.ScopeID) == "" || len(principal.ScopeID) > models.MaxResourceIDLength) {
		return ErrInvalidOperatorPrincipal
	}
	return nil
}

type Operator struct {
	resources  ResourceControlPlane
	blobs      persistence.BlobStore
	operations ResourceOperationControlPlane
	reset      func(context.Context) error
}

type OperatorResponse struct {
	Resource  *models.Resource    `json:"resource,omitempty"`
	Object    *models.BlobObject  `json:"object,omitempty"`
	Content   []byte              `json:"content,omitempty"`
	Operation *models.Operation   `json:"operation,omitempty"`
	Audit     []models.AuditEntry `json:"audit,omitempty"`
	Replayed  bool                `json:"replayed,omitempty"`
}

func NewOperator(resources ResourceControlPlane, blobs persistence.BlobStore, operations ResourceOperationControlPlane, reset func(context.Context) error) (*Operator, error) {
	if resources == nil || blobs == nil || operations == nil || reset == nil {
		return nil, ErrInvalidOperator
	}
	return &Operator{
		resources:  resources,
		blobs:      blobs,
		operations: operations,
		reset:      reset,
	}, nil
}

func NewFileOperator(root string, quota int64) (*Operator, error) {
	if strings.TrimSpace(root) == "" {
		return nil, ErrInvalidOperator
	}
	root = filepath.Clean(root)
	if info, err := os.Stat(root); err == nil && !info.IsDir() {
		return nil, ErrInvalidOperator
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, ErrInvalidOperator
	}

	resourceStore, err := persistence.NewFileResourceStore(filepath.Join(root, "resources.json"))
	if err != nil {
		return nil, err
	}
	resourceManager, err := NewResourceManager(resourceStore)
	if err != nil {
		return nil, err
	}
	blobStore, err := persistence.NewFileBlobStore(filepath.Join(root, "blobs"), quota)
	if err != nil {
		return nil, err
	}
	operationStore, err := persistence.NewFileOperationStore(filepath.Join(root, "operations.json"))
	if err != nil {
		return nil, err
	}
	auditStore, err := persistence.NewFileAuditStore(filepath.Join(root, "audit.json"))
	if err != nil {
		return nil, err
	}
	coordinator, err := NewResourceOperationCoordinator(operationStore, auditStore)
	if err != nil {
		return nil, err
	}
	reset := func(ctx context.Context) error {
		if err := blobStore.Reset(ctx); err != nil {
			return err
		}
		if err := resourceStore.Reset(ctx); err != nil {
			return err
		}
		if err := operationStore.Reset(ctx); err != nil {
			return err
		}
		return auditStore.Reset(ctx)
	}
	return NewOperator(resourceManager, blobStore, coordinator, reset)
}

func (operator *Operator) CreateResource(ctx context.Context, principal OperatorPrincipal, spec models.ResourceSpec) (*models.Resource, error) {
	if err := principal.validate(); err != nil {
		return nil, err
	}
	if spec.ParentID != principal.ScopeID {
		return nil, ErrOperatorScopeDenied
	}
	return operator.resources.CreateResource(ctx, spec)
}

func (operator *Operator) resourceLookupScope(principal OperatorPrincipal, resourceID string) string {
	if principal.ScopeID != "" && resourceID == principal.ScopeID {
		return ""
	}
	return principal.ScopeID
}

func (operator *Operator) GetResource(ctx context.Context, principal OperatorPrincipal, resourceID string) (*models.Resource, error) {
	resource, _, err := operator.authorizeResource(ctx, principal, resourceID)
	return resource, err
}

func (operator *Operator) authorizeResource(ctx context.Context, principal OperatorPrincipal, resourceID string) (*models.Resource, string, error) {
	if err := principal.validate(); err != nil {
		return nil, "", err
	}
	scopeID := operator.resourceLookupScope(principal, resourceID)
	resource, err := operator.resources.GetResource(ctx, scopeID, resourceID)
	if errors.Is(err, persistence.ErrResourceNotFound) && principal.ScopeID != "" {
		return nil, "", ErrOperatorScopeDenied
	}
	return resource, scopeID, err
}

func (operator *Operator) UpdateResourceTags(ctx context.Context, principal OperatorPrincipal, resourceID string, tags map[string]string, requestID, correlationID string) (*OperatorResponse, error) {
	_, scopeID, err := operator.authorizeResource(ctx, principal, resourceID)
	if err != nil {
		return nil, err
	}

	request := ResourceOperationRequest{
		ResourceID:    resourceID,
		ScopeID:       principal.ScopeID,
		Action:        "resource.update.tags",
		RequestID:     requestID,
		CorrelationID: correlationID,
	}
	var updated *models.Resource
	result, err := operator.operations.Execute(ctx, request, func(effectContext context.Context) error {
		updated, err = operator.resources.UpdateResourceTags(effectContext, scopeID, resourceID, tags)
		return err
	})
	if err != nil && result == nil {
		return nil, err
	}
	if result == nil {
		return nil, ErrOperatorResetUnavailable
	}
	if updated == nil {
		updated, err = operator.resources.GetResource(ctx, scopeID, resourceID)
		if err != nil {
			return nil, err
		}
	}
	response := &OperatorResponse{
		Resource:  updated,
		Operation: &result.Operation,
		Replayed:  result.Replayed,
	}
	return response, err
}

func (operator *Operator) GetOperation(ctx context.Context, principal OperatorPrincipal, operationID string) (*models.Operation, error) {
	operation, err := operator.operations.GetOperation(ctx, operationID)
	if err != nil {
		return nil, err
	}
	if _, _, err := operator.authorizeResource(ctx, principal, operation.ResourceID); err != nil {
		return nil, err
	}
	return operation, nil
}

func (operator *Operator) ListAuditHistory(ctx context.Context, principal OperatorPrincipal, resourceID string, limit int) ([]models.AuditEntry, error) {
	if _, _, err := operator.authorizeResource(ctx, principal, resourceID); err != nil {
		return nil, err
	}
	return operator.operations.ListAuditHistory(ctx, resourceID, limit)
}

func (operator *Operator) Reset(ctx context.Context, principal OperatorPrincipal) error {
	if err := principal.validate(); err != nil {
		return err
	}
	if principal.ScopeID != "" {
		return ErrOperatorScopeDenied
	}
	return operator.reset(ctx)
}

func (operator *Operator) authorizeBucket(ctx context.Context, principal OperatorPrincipal, bucketID string) error {
	resource, _, err := operator.authorizeResource(ctx, principal, bucketID)
	if err != nil {
		return err
	}
	if resource.Spec.Type != models.ResourceTypeBucket {
		return ErrOperatorBucketRequired
	}
	return nil
}

func (operator *Operator) PutBlob(ctx context.Context, principal OperatorPrincipal, bucketID, objectKey string, content []byte) (*OperatorResponse, error) {
	if err := operator.authorizeBucket(ctx, principal, bucketID); err != nil {
		return nil, err
	}
	object, err := operator.blobs.Put(ctx, bucketID, objectKey, content)
	if err != nil {
		return nil, err
	}
	return &OperatorResponse{Object: object}, nil
}

func (operator *Operator) GetBlob(ctx context.Context, principal OperatorPrincipal, bucketID, objectKey string) (*OperatorResponse, error) {
	if err := operator.authorizeBucket(ctx, principal, bucketID); err != nil {
		return nil, err
	}
	object, content, err := operator.blobs.Get(ctx, bucketID, objectKey)
	if err != nil {
		return nil, err
	}
	return &OperatorResponse{Object: object, Content: content}, nil
}

func (operator *Operator) ReadBlobRange(ctx context.Context, principal OperatorPrincipal, bucketID, objectKey string, start, end int64) (*OperatorResponse, error) {
	if err := operator.authorizeBucket(ctx, principal, bucketID); err != nil {
		return nil, err
	}
	content, err := operator.blobs.ReadRange(ctx, bucketID, objectKey, start, end)
	if err != nil {
		return nil, err
	}
	return &OperatorResponse{Content: content}, nil
}
