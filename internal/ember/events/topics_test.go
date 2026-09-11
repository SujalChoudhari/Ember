package events

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/queue"
)

func TestTopicBrokerRoutesMatchingEventsWithinScopeAndOwner(t *testing.T) {
	broker, err := NewTopicBroker(TopicBrokerOptions{MaxTopics: 4, MaxSubscriptions: 8})
	if err != nil {
		t.Fatalf("NewTopicBroker() error = %v", err)
	}
	ctx := context.Background()

	topic, err := broker.CreateTopic(ctx, "scope-a", "publisher", "resource-events")
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}
	if topic.ID != "topic-00000001" {
		t.Fatalf("topic ID = %q, want deterministic first ID", topic.ID)
	}
	if _, err := broker.CreateTopic(ctx, "scope-b", "publisher", "resource-events"); err != nil {
		t.Fatalf("CreateTopic(scope-b) error = %v", err)
	}

	matching, err := broker.CreateSubscription(ctx, "scope-a", "consumer-a", topic.ID, "updates", EventFilter{Type: "resource.updated", CorrelationID: "corr-1"})
	if err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	if matching.ID != "subscription-00000001" {
		t.Fatalf("subscription ID = %q, want deterministic first ID", matching.ID)
	}
	if _, err := broker.CreateSubscription(ctx, "scope-a", "consumer-a", topic.ID, "deletes", EventFilter{Type: "resource.deleted"}); err != nil {
		t.Fatalf("CreateSubscription(second) error = %v", err)
	}
	if _, err := broker.CreateSubscription(ctx, "scope-b", "consumer-a", topic.ID, "wrong-scope", EventFilter{}); !errors.Is(err, ErrTopicScopeDenied) {
		t.Fatalf("cross-scope CreateSubscription() error = %v, want ErrTopicScopeDenied", err)
	}

	var delivered []string
	count, err := broker.Deliver(ctx, "scope-a", "consumer-a", topic.ID, Event{ID: "event-1", CorrelationID: "corr-1", Type: "resource.updated"}, func(_ context.Context, subscription Subscription, event Event) error {
		delivered = append(delivered, subscription.ID+":"+event.ID)
		return nil
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if count != 1 || !reflect.DeepEqual(delivered, []string{"subscription-00000001:event-1"}) {
		t.Fatalf("Deliver() = %d/%v, want one matching owned subscription", count, delivered)
	}
	count, err = broker.Deliver(ctx, "scope-a", "consumer-b", topic.ID, Event{ID: "event-owner", CorrelationID: "corr-1", Type: "resource.updated"}, func(context.Context, Subscription, Event) error {
		t.Fatal("event was delivered to an unauthorized owner")
		return nil
	})
	if err != nil {
		t.Fatalf("Deliver(unauthorized owner) error = %v", err)
	}
	if count != 0 {
		t.Fatalf("Deliver(unauthorized owner) count = %d, want zero", count)
	}

	count, err = broker.Deliver(ctx, "scope-a", "consumer-a", topic.ID, Event{ID: "event-2", CorrelationID: "other", Type: "resource.updated"}, func(context.Context, Subscription, Event) error {
		t.Fatal("non-matching event was delivered")
		return nil
	})
	if err != nil {
		t.Fatalf("Deliver(non-match) error = %v", err)
	}
	if count != 0 {
		t.Fatalf("Deliver(non-match) count = %d, want zero", count)
	}
	if _, err := broker.GetTopic(ctx, "scope-b", topic.ID); !errors.Is(err, ErrTopicScopeDenied) {
		t.Fatalf("GetTopic(cross-scope) error = %v, want ErrTopicScopeDenied", err)
	}
}

func TestTopicBrokerLifecycleIsBoundedAndExplicit(t *testing.T) {
	broker, err := NewTopicBroker(TopicBrokerOptions{MaxTopics: 1, MaxSubscriptions: 1})
	if err != nil {
		t.Fatalf("NewTopicBroker() error = %v", err)
	}
	ctx := context.Background()
	topic, err := broker.CreateTopic(ctx, "scope-a", "publisher", "events")
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}
	subscription, err := broker.CreateSubscription(ctx, "scope-a", "consumer-a", topic.ID, "all", EventFilter{})
	if err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	if _, err := broker.CreateTopic(ctx, "scope-a", "publisher", "overflow"); !errors.Is(err, ErrTopicLimitExceeded) {
		t.Fatalf("CreateTopic(overflow) error = %v, want ErrTopicLimitExceeded", err)
	}
	if err := broker.DeleteTopic(ctx, "scope-a", topic.ID); !errors.Is(err, ErrTopicHasSubscriptions) {
		t.Fatalf("DeleteTopic(with subscription) error = %v, want ErrTopicHasSubscriptions", err)
	}
	if err := broker.DeleteSubscription(ctx, "scope-a", subscription.ID); err != nil {
		t.Fatalf("DeleteSubscription() error = %v", err)
	}
	if err := broker.DeleteTopic(ctx, "scope-a", topic.ID); err != nil {
		t.Fatalf("DeleteTopic() error = %v", err)
	}
	if _, err := broker.GetTopic(ctx, "scope-a", topic.ID); !errors.Is(err, ErrTopicNotFound) {
		t.Fatalf("GetTopic(deleted) error = %v, want ErrTopicNotFound", err)
	}
}

func TestTopicBrokerDeliverWithMetricsReportsAcknowledgementRetryDeadLetterAndIdentity(t *testing.T) {
	broker, err := NewTopicBroker(TopicBrokerOptions{MaxTopics: 1, MaxSubscriptions: 1})
	if err != nil {
		t.Fatalf("NewTopicBroker() error = %v", err)
	}
	ctx := context.Background()
	topic, err := broker.CreateTopic(ctx, "scope-a", "publisher", "events")
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}
	if _, err := broker.CreateSubscription(ctx, "scope-a", "consumer-a", topic.ID, "all", EventFilter{}); err != nil {
		t.Fatalf("CreateSubscription() error = %v", err)
	}
	deadLetters, err := queue.NewFileDeadLetterStore(filepath.Join(t.TempDir(), "dead-letters.json"), queue.DeadLetterStoreOptions{MaxRecords: 2})
	if err != nil {
		t.Fatalf("NewFileDeadLetterStore() error = %v", err)
	}
	metrics := NewMetrics()
	failed := Event{ID: "event-1", CorrelationID: "correlation-1", Type: "resource.updated", Payload: []byte("opaque")}
	var attempts int
	reports, err := broker.DeliverWithMetrics(ctx, "scope-a", "consumer-a", topic.ID, failed, queue.RetryPolicy{MaxRetries: 1}, func(context.Context, Subscription, Event) error {
		attempts++
		return errors.New("consumer failure")
	}, func(context.Context, time.Duration) error { return nil }, deadLetters, metrics)
	if !errors.Is(err, queue.ErrDeliveryFailed) || len(reports) != 1 || reports[0].Outcome.Status != queue.DeliveryStatusFailed || reports[0].Outcome.Attempts != 2 || attempts != 2 {
		t.Fatalf("DeliverWithMetrics(failure) = %#v, %v, attempts=%d; want bounded terminal failure", reports, err, attempts)
	}
	if reports[0].Outcome.DeliveryID == failed.ID || !strings.Contains(reports[0].Outcome.DeliveryID, failed.ID) {
		t.Fatalf("scoped delivery identity = %q, want original event identity plus subscription", reports[0].Outcome.DeliveryID)
	}

	succeeded := failed
	succeeded.ID = "event-2"
	reports, err = broker.DeliverWithMetrics(ctx, "scope-a", "consumer-a", topic.ID, succeeded, queue.RetryPolicy{}, func(context.Context, Subscription, Event) error {
		return nil
	}, nil, deadLetters, metrics)
	if err != nil || len(reports) != 1 || reports[0].Outcome.Status != queue.DeliveryStatusSucceeded || reports[0].Outcome.Attempts != 1 {
		t.Fatalf("DeliverWithMetrics(success) = %#v, %v; want acknowledged delivery", reports, err)
	}
	if snapshot := metrics.Snapshot(); snapshot.DeliveryCount != 2 || snapshot.SuccessCount != 1 || snapshot.FailureCount != 1 || snapshot.RetryCount != 1 || snapshot.DeadLetterCount != 1 {
		t.Fatalf("Metrics.Snapshot() = %#v, want bounded acknowledgement/retry/dead-letter counts", snapshot)
	}
}
