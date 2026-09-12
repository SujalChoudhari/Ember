package ember

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/deployment"
	"github.com/SujalChoudhari/Ember/internal/ember/events"
	"github.com/SujalChoudhari/Ember/internal/ember/models"
	"github.com/SujalChoudhari/Ember/internal/ember/persistence"
	"github.com/SujalChoudhari/Ember/internal/ember/queue"
)

func TestSprintOneSurfacesShareBoundedCleanState(t *testing.T) {
	ctx := context.Background()
	state := filepath.Join(t.TempDir(), "state")
	operator, err := NewFileOperator(state, 64)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}

	applied, _, err := operator.applyDeployment(ctx, OperatorPrincipal{}, []byte(`{"version":"v1","resources":[{"type":"group","name":"platform","desiredState":"ready"}]}`), nil, deployment.ApplyOptions{
		RequestID: "request-sprint-one", CorrelationID: "correlation-sprint-one",
	})
	if err != nil || applied == nil || len(applied.Operations) != 1 {
		t.Fatalf("applyDeployment() = %#v, %v; want one portal operation", applied, err)
	}
	group, err := operator.GetResource(ctx, OperatorPrincipal{}, "resource-00000001")
	if err != nil {
		t.Fatalf("GetResource(applied group) error = %v", err)
	}
	operation, err := operator.GetOperation(ctx, OperatorPrincipal{}, applied.Operations[0].ID)
	if err != nil || operation.CorrelationID != "correlation-sprint-one" {
		t.Fatalf("GetOperation() = %#v, %v; want correlated portal operation", operation, err)
	}

	workload, err := operator.CreateWorkload(ctx, OperatorPrincipal{ScopeID: group.ID}, models.ResourceSpec{
		Type:         models.ResourceTypeWorkload,
		Name:         "api",
		ParentID:     group.ID,
		Provider:     models.ProviderMetadata{Namespace: "Ember.Compute", Type: "workloads", Version: "v1"},
		DesiredState: models.ResourceStateReady,
	})
	if err != nil {
		t.Fatalf("CreateWorkload() error = %v", err)
	}
	observability, err := operator.InspectWorkload(ctx, OperatorPrincipal{ScopeID: group.ID}, workload.Resource.ID, 10)
	if err != nil || observability.Status.Health != models.WorkloadHealthHealthy || observability.Status.Readiness != models.WorkloadReadinessReady {
		t.Fatalf("InspectWorkload() = %#v, %v; want healthy ready compute view", observability, err)
	}

	network, err := operator.CreateNetwork(ctx, OperatorPrincipal{ScopeID: group.ID}, "frontend")
	if err != nil {
		t.Fatalf("CreateNetwork() error = %v", err)
	}
	port, err := operator.AllocateNetworkPort(ctx, OperatorPrincipal{ScopeID: group.ID}, network.ID, workload.Resource.ID, 8080, models.NetworkProtocolTCP)
	if err != nil {
		t.Fatalf("AllocateNetworkPort() error = %v", err)
	}
	endpoint, err := operator.PublishNetworkEndpoint(ctx, OperatorPrincipal{ScopeID: group.ID}, network.ID, port.ID, "api")
	if err != nil {
		t.Fatalf("PublishNetworkEndpoint() error = %v", err)
	}
	if _, err := operator.GetNetworkEndpoint(ctx, OperatorPrincipal{ScopeID: "foreign"}, endpoint.ID); !errors.Is(err, persistence.ErrNetworkEndpointNotFound) {
		t.Fatalf("GetNetworkEndpoint(cross scope) error = %v, want not found", err)
	}

	fileQueue, err := queue.NewFileQueue(filepath.Join(state, "queue.json"), queue.QueueOptions{
		MaxMessages: 2, MaxBytes: 64, VisibilityTimeout: time.Minute,
	})
	if err != nil {
		t.Fatalf("NewFileQueue() error = %v", err)
	}
	messageID, err := fileQueue.Enqueue(ctx, "correlation-queue", []byte("bounded-payload"))
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	received, err := fileQueue.Receive(ctx)
	if err != nil || received.ID != messageID || received.CorrelationID != "correlation-queue" {
		t.Fatalf("Receive() = %#v, %v; want correlated queue delivery", received, err)
	}
	if err := fileQueue.Acknowledge(ctx, received.Receipt); err != nil {
		t.Fatalf("Acknowledge() error = %v", err)
	}

	broker, err := events.NewTopicBroker(events.TopicBrokerOptions{MaxTopics: 1, MaxSubscriptions: 1})
	if err != nil {
		t.Fatalf("NewTopicBroker() error = %v", err)
	}
	topic, err := broker.CreateTopic(ctx, group.ID, "publisher", "workload-events")
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}
	if _, err := broker.CreateSubscription(ctx, group.ID, "consumer", topic.ID, "updates", events.EventFilter{Type: "workload.updated"}); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	metrics := events.NewMetrics()
	deadLetters, err := queue.NewFileDeadLetterStore(filepath.Join(state, "dead-letters.json"), queue.DeadLetterStoreOptions{MaxRecords: 2})
	if err != nil {
		t.Fatalf("NewFileDeadLetterStore() error = %v", err)
	}
	reports, err := broker.DeliverWithMetrics(ctx, group.ID, "consumer", topic.ID, events.Event{
		ID: "event-1", CorrelationID: "correlation-event", Type: "workload.updated", Payload: []byte("bounded-event"),
	}, queue.RetryPolicy{}, func(context.Context, events.Subscription, events.Event) error { return nil }, nil, deadLetters, metrics)
	if err != nil || len(reports) != 1 || reports[0].Outcome.Status != queue.DeliveryStatusSucceeded || reports[0].Outcome.CorrelationID != "correlation-event" {
		t.Fatalf("DeliverWithMetrics() = %#v, %v; want correlated acknowledged event", reports, err)
	}
	if snapshot := metrics.Snapshot(); snapshot.DeliveryCount != 1 || snapshot.SuccessCount != 1 || snapshot.Cardinality != 1 {
		t.Fatalf("Metrics.Snapshot() = %#v; want bounded event evidence", snapshot)
	}

	if err := operator.Reset(ctx, OperatorPrincipal{}); err != nil {
		t.Fatalf("operator.Reset() error = %v", err)
	}
	if err := fileQueue.Reset(ctx); err != nil {
		t.Fatalf("queue.Reset() error = %v", err)
	}
	for _, path := range []string{
		filepath.Join(state, "resources.json"),
		filepath.Join(state, "networks.json"),
		filepath.Join(state, "operations.json"),
		filepath.Join(state, "apply-progress.json"),
		filepath.Join(state, "queue.json"),
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned path %q after clean reset: error = %v, want os.ErrNotExist", path, err)
		}
	}
}

func TestSprintTwoResourceBlobSurfacesShareBoundedCleanState(t *testing.T) {
	ctx := context.Background()
	state := filepath.Join(t.TempDir(), "state")
	operator, err := NewFileOperator(state, 64)
	if err != nil {
		t.Fatalf("NewFileOperator() error = %v", err)
	}

	group, err := operator.CreateResource(ctx, OperatorPrincipal{}, models.ResourceSpec{Type: models.ResourceTypeGroup, Name: "platform"})
	if err != nil {
		t.Fatalf("CreateResource(group) error = %v", err)
	}
	bucket, err := operator.CreateResource(ctx, OperatorPrincipal{ScopeID: group.ID}, models.ResourceSpec{Type: models.ResourceTypeBucket, Name: "assets", ParentID: group.ID})
	if err != nil {
		t.Fatalf("CreateResource(bucket) error = %v", err)
	}

	lock := models.ResourceLock{Owner: "sprint-two", Token: "lock-token"}
	if err := operator.AcquireResourceLock(ctx, OperatorPrincipal{}, group.ID, lock); err != nil {
		t.Fatalf("AcquireResourceLock() error = %v", err)
	}
	if _, err := operator.UpdateResourceTags(ctx, OperatorPrincipal{ScopeID: group.ID}, bucket.ID, map[string]string{"tier": "blocked"}, "request-locked", "correlation-locked"); !errors.Is(err, persistence.ErrResourceLocked) {
		t.Fatalf("UpdateResourceTags(locked) error = %v, want resource lock", err)
	}
	if err := operator.ReleaseResourceLock(ctx, OperatorPrincipal{}, group.ID, lock); err != nil {
		t.Fatalf("ReleaseResourceLock() error = %v", err)
	}

	trusted := []byte("hello world")
	put, err := operator.PutBlob(ctx, OperatorPrincipal{ScopeID: group.ID}, bucket.ID, "nested/file.txt", trusted)
	if err != nil || put == nil || put.Object == nil {
		t.Fatalf("PutBlob() = (%#v, %v), want bounded object", put, err)
	}
	rangeResult, err := operator.ReadBlobRange(ctx, OperatorPrincipal{ScopeID: group.ID}, bucket.ID, "nested/file.txt", 6, 11)
	if err != nil || string(rangeResult.Content) != "world" {
		t.Fatalf("ReadBlobRange() = (%#v, %v), want world", rangeResult, err)
	}

	payloadPath := filepath.Join(state, "blobs", "objects", bucket.ID, "nested", "file.txt")
	if err := os.WriteFile(payloadPath, []byte("payloAd"), 0o600); err != nil {
		t.Fatalf("WriteFile(corrupt payload) error = %v", err)
	}
	integrity, verifyErr := operator.VerifyBlob(ctx, OperatorPrincipal{ScopeID: group.ID}, bucket.ID, "nested/file.txt")
	if !errors.Is(verifyErr, persistence.ErrBlobObjectCorrupt) || integrity == nil || integrity.Integrity == nil || integrity.Integrity.Status != models.BlobIntegrityCorrupt {
		t.Fatalf("VerifyBlob(corrupt) = (%#v, %v), want bounded corruption evidence", integrity, verifyErr)
	}
	if _, err := operator.RecoverBlob(ctx, OperatorPrincipal{ScopeID: group.ID}, bucket.ID, "nested/file.txt", put.Object.SHA256, trusted, "request-recover", "correlation-recover"); err != nil {
		t.Fatalf("RecoverBlob() error = %v", err)
	}
	if recovered, err := operator.GetBlob(ctx, OperatorPrincipal{ScopeID: group.ID}, bucket.ID, "nested/file.txt"); err != nil || string(recovered.Content) != string(trusted) {
		t.Fatalf("GetBlob(after recovery) = (%#v, %v), want trusted payload", recovered, err)
	}

	if err := operator.Reset(ctx, OperatorPrincipal{}); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	for _, path := range []string{
		filepath.Join(state, "resources.json"),
		filepath.Join(state, "blobs", "metadata.json"),
		filepath.Join(state, "blobs", "objects"),
	} {
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned path %q after reset: error = %v, want os.ErrNotExist", path, err)
		}
	}
}

func TestSprintTwoQueueEventSurfacesShareBoundedRecoveryEvidence(t *testing.T) {
	ctx := context.Background()
	state := filepath.Join(t.TempDir(), "state")
	deadLetters, err := queue.NewFileDeadLetterStore(filepath.Join(state, "dead-letters.json"), queue.DeadLetterStoreOptions{MaxRecords: 2})
	if err != nil {
		t.Fatalf("NewFileDeadLetterStore() error = %v", err)
	}

	delivery := queue.Delivery{ID: "message-1", CorrelationID: "correlation-queue", Payload: []byte("bounded-queue-payload")}
	queueAttempts := 0
	failed, err := queue.DeliverWithDeadLetter(ctx, delivery, queue.RetryPolicy{MaxRetries: 1}, func(context.Context, queue.Delivery) error {
		queueAttempts++
		return errors.New("consumer failure")
	}, func(context.Context, time.Duration) error { return nil }, deadLetters)
	if !errors.Is(err, queue.ErrDeliveryFailed) || failed.Status != queue.DeliveryStatusFailed || failed.Attempts != 2 || queueAttempts != 2 {
		t.Fatalf("DeliverWithDeadLetter() = (%#v, %v), want bounded terminal failure", failed, err)
	}
	redriveCalls := 0
	redriven, err := deadLetters.Redrive(ctx, "request-queue-recovery", delivery, queue.RetryPolicy{}, func(context.Context, queue.Delivery) error {
		redriveCalls++
		return nil
	}, nil)
	if err != nil || redriven.Status != queue.DeliveryStatusSucceeded || redriveCalls != 1 {
		t.Fatalf("Redrive() = (%#v, %v), want one successful recovery", redriven, err)
	}
	replayed, err := deadLetters.Redrive(ctx, "request-queue-recovery", delivery, queue.RetryPolicy{}, func(context.Context, queue.Delivery) error {
		redriveCalls++
		return errors.New("must not run on replay")
	}, nil)
	if err != nil || !reflect.DeepEqual(replayed, redriven) || redriveCalls != 1 {
		t.Fatalf("Redrive(replay) = (%#v, %v), calls=%d; want idempotent replay", replayed, err, redriveCalls)
	}

	fileQueue, err := queue.NewFileQueue(filepath.Join(state, "queue.json"), queue.QueueOptions{MaxMessages: 1, MaxBytes: 64, VisibilityTimeout: time.Minute})
	if err != nil {
		t.Fatalf("NewFileQueue() error = %v", err)
	}
	messageID, err := fileQueue.Enqueue(ctx, "correlation-progress", []byte("progress"))
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	received, err := fileQueue.Receive(ctx)
	if err != nil || received.ID != messageID || received.CorrelationID != "correlation-progress" {
		t.Fatalf("Receive() = (%#v, %v), want correlated progress", received, err)
	}
	if err := fileQueue.Acknowledge(ctx, received.Receipt); err != nil {
		t.Fatalf("Acknowledge() error = %v", err)
	}

	metrics := events.NewMetrics()
	event := events.Event{ID: "event-1", CorrelationID: "correlation-event", Type: "resource.updated", Payload: []byte("bounded-event")}
	eventAttempts := 0
	delivered, err := events.DeliverWithMetrics(ctx, event, queue.RetryPolicy{MaxRetries: 1}, func(context.Context, events.Event) error {
		eventAttempts++
		if eventAttempts == 1 {
			return errors.New("transient event failure")
		}
		return nil
	}, func(context.Context, time.Duration) error { return nil }, deadLetters, metrics)
	if err != nil || delivered.Status != queue.DeliveryStatusSucceeded || delivered.Attempts != 2 {
		t.Fatalf("DeliverWithMetrics(success) = (%#v, %v), want retry then success", delivered, err)
	}
	failedEvent := events.Event{ID: "event-2", CorrelationID: "correlation-failure", Type: "resource.updated", Payload: []byte("bounded-failure")}
	terminal, err := events.DeliverWithMetrics(ctx, failedEvent, queue.RetryPolicy{}, func(context.Context, events.Event) error {
		return errors.New("terminal event failure")
	}, func(context.Context, time.Duration) error { return nil }, deadLetters, metrics)
	if !errors.Is(err, queue.ErrDeliveryFailed) || terminal.Status != queue.DeliveryStatusFailed {
		t.Fatalf("DeliverWithMetrics(failure) = (%#v, %v), want terminal failure", terminal, err)
	}
	recovered, err := events.Recover(ctx, deadLetters, "request-event-recovery", failedEvent, queue.RetryPolicy{}, func(context.Context, events.Event) error { return nil }, nil, metrics)
	if err != nil || recovered.Status != queue.DeliveryStatusSucceeded {
		t.Fatalf("Recover() = (%#v, %v), want successful recovery", recovered, err)
	}
	snapshot := metrics.Snapshot()
	if snapshot.DeliveryCount != 2 || snapshot.SuccessCount != 1 || snapshot.FailureCount != 1 || snapshot.RetryCount != 1 || snapshot.DeadLetterCount != 1 || snapshot.RecoveryCount != 1 || snapshot.RecoveryFailureCount != 0 {
		t.Fatalf("Metrics.Snapshot() = %#v, want bounded delivery/recovery counters", snapshot)
	}
	if records, err := deadLetters.List(ctx, 2); err != nil || len(records) != 0 {
		t.Fatalf("dead-letter records after recovery = (%#v, %v), want empty", records, err)
	}
}
