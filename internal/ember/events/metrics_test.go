package events

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/queue"
)

func TestMetricsTracksBoundedDeliveryAndRecoveryOutcomes(t *testing.T) {
	metrics := NewMetrics()
	store, err := queue.NewFileDeadLetterStore(filepath.Join(t.TempDir(), "dead-letters.json"), queue.DeadLetterStoreOptions{MaxRecords: 2})
	if err != nil {
		t.Fatalf("NewFileDeadLetterStore() error = %v", err)
	}

	event := Event{ID: "event-1", CorrelationID: "correlation-1", Type: "resource.updated", Payload: []byte("opaque-payload")}
	var attempts int
	outcome, err := DeliverWithMetrics(
		context.Background(), event, queue.RetryPolicy{MaxRetries: 1, InitialBackoff: 5 * time.Millisecond, MaxBackoff: 5 * time.Millisecond},
		func(context.Context, Event) error {
			attempts++
			if attempts == 1 {
				return errors.New("transient consumer failure")
			}
			return nil
		},
		func(context.Context, time.Duration) error { return nil }, store, metrics,
	)
	if err != nil || outcome.Status != queue.DeliveryStatusSucceeded || outcome.Attempts != 2 {
		t.Fatalf("DeliverWithMetrics() = %#v, %v; want success after one retry", outcome, err)
	}

	failedEvent := event
	failedEvent.ID = "event-2"
	failedEvent.CorrelationID = "correlation-2"
	outcome, err = DeliverWithMetrics(
		context.Background(), failedEvent, queue.RetryPolicy{},
		func(context.Context, Event) error { return errors.New("terminal consumer failure") },
		func(context.Context, time.Duration) error { return nil }, store, metrics,
	)
	if !errors.Is(err, queue.ErrDeliveryFailed) || outcome.Status != queue.DeliveryStatusFailed {
		t.Fatalf("DeliverWithMetrics() failure = %#v, %v; want terminal failure", outcome, err)
	}

	recovered, err := Recover(
		context.Background(), store, "request-1", failedEvent, queue.RetryPolicy{},
		func(context.Context, Event) error { return nil },
		func(context.Context, time.Duration) error { return nil }, metrics,
	)
	if err != nil || recovered.Status != queue.DeliveryStatusSucceeded {
		t.Fatalf("Recover() = %#v, %v; want successful recovery", recovered, err)
	}

	snapshot := metrics.Snapshot()
	if snapshot.DeliveryCount != 2 || snapshot.SuccessCount != 1 || snapshot.FailureCount != 1 ||
		snapshot.RetryCount != 1 || snapshot.DeadLetterCount != 1 || snapshot.RecoveryCount != 1 ||
		snapshot.RecoveryFailureCount != 0 || snapshot.DeliveryLatencyNanos == 0 ||
		snapshot.MaxDeliveryLatencyNanos == 0 || snapshot.RecoveryLatencyNanos == 0 ||
		snapshot.MaxRecoveryLatencyNanos == 0 || snapshot.RetryLagNanos != int64(5*time.Millisecond) {
		t.Fatalf("Metrics.Snapshot() = %#v, want bounded delivery and recovery counters", snapshot)
	}
}

func TestMetricsUsesConstantCardinalityAndSaturatesCounters(t *testing.T) {
	metrics := NewMetrics()
	metrics.observeDelivery(queue.DeliveryOutcome{Status: queue.DeliveryStatusSucceeded, Attempts: queue.MaxRetryCount + 1}, nil, 1)
	metrics.observeDelivery(queue.DeliveryOutcome{Status: queue.DeliveryStatusFailed, Attempts: queue.MaxRetryCount + 1, RetryDelays: make([]time.Duration, queue.MaxRetryCount)}, queue.ErrDeliveryFailed, 1)

	snapshot := metrics.Snapshot()
	if snapshot.Cardinality != 1 || snapshot.DeliveryCount != 2 || snapshot.RetryCount != queue.MaxRetryCount*2 {
		t.Fatalf("Metrics.Snapshot() = %#v, want one bounded aggregate", snapshot)
	}
}

func TestMetricsTracksRecoveryFailure(t *testing.T) {
	metrics := NewMetrics()
	store, err := queue.NewFileDeadLetterStore(filepath.Join(t.TempDir(), "dead-letters.json"), queue.DeadLetterStoreOptions{MaxRecords: 1})
	if err != nil {
		t.Fatalf("NewFileDeadLetterStore() error = %v", err)
	}

	_, err = Recover(
		context.Background(), store, "missing-request",
		Event{ID: "missing-event", CorrelationID: "missing-correlation", Type: "resource.updated"},
		queue.RetryPolicy{}, func(context.Context, Event) error { return nil }, nil, metrics,
	)
	if !errors.Is(err, queue.ErrRedriveNotFound) {
		t.Fatalf("Recover() error = %v, want queue.ErrRedriveNotFound", err)
	}

	snapshot := metrics.Snapshot()
	if snapshot.RecoveryCount != 1 || snapshot.RecoveryFailureCount != 1 {
		t.Fatalf("Metrics.Snapshot() = %#v, want one failed recovery", snapshot)
	}
}
