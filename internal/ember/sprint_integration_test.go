package ember

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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
