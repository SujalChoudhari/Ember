package ember

import (
	"context"
	"errors"
	"strings"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
	"github.com/SujalChoudhari/Ember/internal/ember/queue"
)

const workloadDataAction = "workload.data.write-read"

var (
	ErrInvalidWorkloadDataIntegration = errors.New("invalid workload data integration")
	ErrInvalidWorkloadDataRequest     = errors.New("invalid workload data request")
	ErrWorkloadDataScopeDenied        = errors.New("workload data scope denied")
	ErrWorkloadDataOperation          = errors.New("workload data operation failed")
)

// WorkloadDataQueue is the bounded queue surface used by the workload data
// integration. It intentionally excludes queue reset and administrative calls.
type WorkloadDataQueue interface {
	Enqueue(context.Context, string, []byte) (string, error)
	Receive(context.Context) (queue.ReceivedMessage, error)
	Acknowledge(context.Context, string) error
}

// WorkloadDataResult is the payload-free delivery identity plus the object
// returned by one successful workload data round trip.
type WorkloadDataResult struct {
	MessageID string
	Object    *models.BlobObject
	Content   []byte
}

// WorkloadDataIntegration binds a workload's scope to the existing blob,
// queue, and operation/audit boundaries. It does not create another store.
type WorkloadDataIntegration struct {
	resources  ResourceControlPlane
	blobs      persistence.BlobStore
	workQueue  WorkloadDataQueue
	operations ResourceOperationControlPlane
}

func NewWorkloadDataIntegration(resources ResourceControlPlane, blobs persistence.BlobStore, workQueue WorkloadDataQueue, operations ResourceOperationControlPlane) (*WorkloadDataIntegration, error) {
	if resources == nil || blobs == nil || workQueue == nil || operations == nil {
		return nil, ErrInvalidWorkloadDataIntegration
	}
	return &WorkloadDataIntegration{
		resources:  resources,
		blobs:      blobs,
		workQueue:  workQueue,
		operations: operations,
	}, nil
}

// WriteRead stores one bounded object, transfers its key through the approved
// queue, reads the object back, and acknowledges the delivery. The workload
// and bucket must both be direct children of scopeID.
func (integration *WorkloadDataIntegration) WriteRead(ctx context.Context, scopeID, workloadID, bucketID, objectKey string, content []byte, requestID, correlationID string) (*WorkloadDataResult, error) {
	if err := validateWorkloadDataRequest(ctx, scopeID, workloadID, bucketID, objectKey, requestID, correlationID); err != nil {
		return nil, err
	}
	workload, err := integration.resources.GetResource(ctx, scopeID, workloadID)
	if err != nil || workload.Spec.Type != models.ResourceTypeWorkload {
		return nil, ErrWorkloadDataScopeDenied
	}
	bucket, err := integration.resources.GetResource(ctx, scopeID, bucketID)
	if err != nil || bucket.Spec.Type != models.ResourceTypeBucket {
		return nil, integration.recordScopeDenial(ctx, bucketID, scopeID, requestID, correlationID)
	}

	request := ResourceOperationRequest{
		ResourceID:    bucketID,
		ScopeID:       scopeID,
		Action:        workloadDataAction,
		RequestID:     requestID,
		CorrelationID: correlationID,
	}
	var output *WorkloadDataResult
	operation, operationErr := integration.operations.Execute(ctx, request, func(effectContext context.Context) error {
		_, existingContent, lookupErr := integration.blobs.Get(effectContext, bucketID, objectKey)
		hadExistingObject := lookupErr == nil
		if lookupErr != nil && !errors.Is(lookupErr, persistence.ErrBlobObjectNotFound) {
			return lookupErr
		}
		putSucceeded := false
		committed := false
		defer func() {
			if committed || !putSucceeded {
				return
			}
			if hadExistingObject {
				_, _ = integration.blobs.Put(effectContext, bucketID, objectKey, existingContent)
				return
			}
			_ = integration.blobs.Delete(effectContext, bucketID, objectKey)
		}()

		object, err := integration.blobs.Put(effectContext, bucketID, objectKey, content)
		if err != nil {
			return err
		}
		if object == nil {
			return ErrWorkloadDataOperation
		}
		putSucceeded = true
		messageID, err := integration.workQueue.Enqueue(effectContext, correlationID, []byte(objectKey))
		if err != nil {
			return err
		}
		received, err := integration.workQueue.Receive(effectContext)
		if err != nil || received.ID == "" || received.CorrelationID != correlationID || string(received.Payload) != objectKey {
			return ErrWorkloadDataOperation
		}
		storedObject, storedContent, err := integration.blobs.Get(effectContext, bucketID, string(received.Payload))
		if err != nil {
			return err
		}
		if err := integration.workQueue.Acknowledge(effectContext, received.Receipt); err != nil {
			return err
		}
		output = &WorkloadDataResult{
			MessageID: messageID,
			Object:    storedObject,
			Content:   storedContent,
		}
		committed = true
		return nil
	})
	if operationErr != nil {
		return nil, ErrWorkloadDataOperation
	}
	if operation != nil && operation.Replayed {
		storedObject, storedContent, err := integration.blobs.Get(ctx, bucketID, objectKey)
		if err != nil {
			return nil, err
		}
		return &WorkloadDataResult{Object: storedObject, Content: storedContent}, nil
	}
	if output == nil {
		return nil, ErrWorkloadDataOperation
	}
	return output, nil
}

func validateWorkloadDataRequest(ctx context.Context, scopeID, workloadID, bucketID, objectKey, requestID, correlationID string) error {
	if ctx == nil || strings.TrimSpace(scopeID) == "" || len(scopeID) > models.MaxResourceIDLength ||
		strings.TrimSpace(workloadID) == "" || len(workloadID) > models.MaxResourceIDLength ||
		strings.TrimSpace(bucketID) == "" || len(bucketID) > models.MaxResourceIDLength ||
		strings.TrimSpace(requestID) == "" || len(requestID) > models.MaxOperationRequestIDLength ||
		strings.TrimSpace(correlationID) == "" || len(correlationID) > models.MaxOperationCorrelationIDLength {
		return ErrInvalidWorkloadDataRequest
	}
	if err := models.ValidateBlobObjectKey(objectKey); err != nil {
		return ErrInvalidWorkloadDataRequest
	}
	return nil
}

func (integration *WorkloadDataIntegration) recordScopeDenial(ctx context.Context, bucketID, scopeID, requestID, correlationID string) error {
	request := ResourceOperationRequest{
		ResourceID:    bucketID,
		ScopeID:       scopeID,
		Action:        workloadDataAction,
		RequestID:     requestID,
		CorrelationID: correlationID,
	}
	_, err := integration.operations.Execute(ctx, request, func(context.Context) error {
		return ErrWorkloadDataScopeDenied
	})
	if err != nil && !errors.Is(err, ErrResourceOperationFailed) {
		return err
	}
	return ErrWorkloadDataScopeDenied
}

var _ WorkloadDataQueue = (*queue.FileQueue)(nil)
