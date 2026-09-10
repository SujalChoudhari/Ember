package ember

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
	"github.com/SujalChoudhari/Ember/internal/ember/queue"
)

func TestWorkloadDataIntegrationWritesReadsAndAuditsScopeDenial(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	resourceStore, err := persistence.NewFileResourceStore(filepath.Join(root, "resources.json"))
	if err != nil {
		t.Fatalf("NewFileResourceStore() error = %v", err)
	}
	resources, err := NewResourceManager(resourceStore)
	if err != nil {
		t.Fatalf("NewResourceManager() error = %v", err)
	}
	provider := models.ProviderMetadata{Namespace: "Ember.Workload", Type: "local", Version: "v1"}
	groupA, err := resources.CreateResource(ctx, models.ResourceSpec{
		Type:     models.ResourceTypeGroup,
		Name:     "scope-a",
		Provider: provider,
	})
	if err != nil {
		t.Fatalf("CreateResource(groupA) error = %v", err)
	}
	groupB, err := resources.CreateResource(ctx, models.ResourceSpec{
		Type:     models.ResourceTypeGroup,
		Name:     "scope-b",
		Provider: provider,
	})
	if err != nil {
		t.Fatalf("CreateResource(groupB) error = %v", err)
	}
	workload, err := resources.CreateResource(ctx, models.ResourceSpec{
		Type:     models.ResourceTypeWorkload,
		Name:     "worker",
		ParentID: groupA.ID,
		Provider: provider,
	})
	if err != nil {
		t.Fatalf("CreateResource(workload) error = %v", err)
	}
	bucketA, err := resources.CreateResource(ctx, models.ResourceSpec{
		Type:     models.ResourceTypeBucket,
		Name:     "worker-data",
		ParentID: groupA.ID,
		Provider: provider,
	})
	if err != nil {
		t.Fatalf("CreateResource(bucketA) error = %v", err)
	}
	bucketB, err := resources.CreateResource(ctx, models.ResourceSpec{
		Type:     models.ResourceTypeBucket,
		Name:     "other-data",
		ParentID: groupB.ID,
		Provider: provider,
	})
	if err != nil {
		t.Fatalf("CreateResource(bucketB) error = %v", err)
	}

	blobs, err := persistence.NewFileBlobStore(filepath.Join(root, "blobs"), 1<<20)
	if err != nil {
		t.Fatalf("NewFileBlobStore() error = %v", err)
	}
	workQueue, err := queue.NewFileQueue(filepath.Join(root, "queue.json"), queue.QueueOptions{
		MaxMessages:       4,
		MaxBytes:          1024,
		VisibilityTimeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewFileQueue() error = %v", err)
	}
	operationStore, err := persistence.NewFileOperationStore(filepath.Join(root, "operations.json"))
	if err != nil {
		t.Fatalf("NewFileOperationStore() error = %v", err)
	}
	auditStore, err := persistence.NewFileAuditStore(filepath.Join(root, "audit.json"))
	if err != nil {
		t.Fatalf("NewFileAuditStore() error = %v", err)
	}
	operations, err := NewResourceOperationCoordinator(operationStore, auditStore)
	if err != nil {
		t.Fatalf("NewResourceOperationCoordinator() error = %v", err)
	}
	integration, err := NewWorkloadDataIntegration(resources, blobs, workQueue, operations)
	if err != nil {
		t.Fatalf("NewWorkloadDataIntegration() error = %v", err)
	}

	payload := []byte("bounded workload result")
	result, err := integration.WriteRead(ctx, groupA.ID, workload.ID, bucketA.ID, "results/output.txt", payload, "request-1", "correlation-1")
	if err != nil {
		t.Fatalf("WriteRead() error = %v", err)
	}
	if result.MessageID == "" || result.Object == nil || result.Object.BucketID != bucketA.ID || !bytes.Equal(result.Content, payload) {
		t.Fatalf("WriteRead() = %#v, want bounded object and payload", result)
	}
	if _, err := workQueue.Receive(ctx); !errors.Is(err, queue.ErrQueueEmpty) {
		t.Fatalf("Receive() after acknowledged integration error = %v, want ErrQueueEmpty", err)
	}
	storedObject, storedPayload, err := blobs.Get(ctx, bucketA.ID, "results/output.txt")
	if err != nil {
		t.Fatalf("Get() after integration error = %v", err)
	}
	if !reflect.DeepEqual(storedObject, result.Object) || !bytes.Equal(storedPayload, payload) {
		t.Fatalf("stored object = %#v, %q; want %#v, %q", storedObject, storedPayload, result.Object, payload)
	}
	failingIntegration, err := NewWorkloadDataIntegration(resources, blobs, failingWorkloadDataQueue{}, operations)
	if err != nil {
		t.Fatalf("NewWorkloadDataIntegration(failing queue) error = %v", err)
	}
	if _, err := failingIntegration.WriteRead(ctx, groupA.ID, workload.ID, bucketA.ID, "results/failure.txt", []byte("cleanup me"), "request-failure", "correlation-failure"); !errors.Is(err, ErrWorkloadDataOperation) {
		t.Fatalf("WriteRead(failing queue) error = %v, want ErrWorkloadDataOperation", err)
	}
	if _, _, err := blobs.Get(ctx, bucketA.ID, "results/failure.txt"); !errors.Is(err, persistence.ErrBlobObjectNotFound) {
		t.Fatalf("failed integration blob lookup error = %v, want no residue", err)
	}

	if _, err := integration.WriteRead(ctx, groupA.ID, workload.ID, bucketB.ID, "results/denied.txt", []byte("must not cross scope"), "request-2", "correlation-2"); !errors.Is(err, ErrWorkloadDataScopeDenied) {
		t.Fatalf("cross-scope WriteRead() error = %v, want ErrWorkloadDataScopeDenied", err)
	}
	entries, err := operations.ListAuditHistory(ctx, bucketB.ID, persistence.MaxAuditListLimit)
	if err != nil {
		t.Fatalf("ListAuditHistory() error = %v", err)
	}
	if len(entries) != 1 || entries[0].Action != "workload.data.write-read" || entries[0].Outcome != "failed" || entries[0].ScopeID != groupA.ID {
		t.Fatalf("denied audit entries = %#v, want one scoped failed entry", entries)
	}
	if _, _, err := blobs.Get(ctx, bucketB.ID, "results/denied.txt"); !errors.Is(err, persistence.ErrBlobObjectNotFound) {
		t.Fatalf("cross-scope blob lookup error = %v, want no residue", err)
	}
}

type failingWorkloadDataQueue struct{}

func (failingWorkloadDataQueue) Enqueue(context.Context, string, []byte) (string, error) {
	return "", errors.New("queue unavailable")
}

func (failingWorkloadDataQueue) Receive(context.Context) (queue.ReceivedMessage, error) {
	return queue.ReceivedMessage{}, queue.ErrQueueEmpty
}

func (failingWorkloadDataQueue) Acknowledge(context.Context, string) error {
	return nil
}
