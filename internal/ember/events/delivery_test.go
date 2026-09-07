package events

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/queue"
)

func TestDeliverRecordsBoundedEventFailureAndPreservesIdentity(t *testing.T) {
	store, err := queue.NewFileDeadLetterStore(filepath.Join(t.TempDir(), "dead-letters.json"), queue.DeadLetterStoreOptions{MaxRecords: 2})
	if err != nil {
		t.Fatalf("NewFileDeadLetterStore() error = %v", err)
	}
	event := Event{
		ID:            "event-1",
		CorrelationID: "correlation-1",
		Type:          "resource.updated",
		Payload:       []byte("opaque-payload"),
	}
	policy := queue.RetryPolicy{MaxRetries: 2, InitialBackoff: 10 * time.Millisecond, MaxBackoff: 15 * time.Millisecond}
	var attempts int
	var received []Event
	var delays []time.Duration

	outcome, err := Deliver(
		context.Background(), event, policy,
		func(_ context.Context, got Event) error {
			attempts++
			received = append(received, got)
			return errors.New("consumer detail must not escape")
		},
		func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		}, store,
	)
	if !errors.Is(err, queue.ErrDeliveryFailed) {
		t.Fatalf("Deliver() error = %v, want queue.ErrDeliveryFailed", err)
	}
	if outcome.Status != queue.DeliveryStatusFailed || outcome.Attempts != 3 {
		t.Fatalf("Deliver() outcome = %#v, want bounded terminal failure", outcome)
	}
	if outcome.DeliveryID != event.ID || outcome.CorrelationID != event.CorrelationID || outcome.Reason != queue.DeliveryFailureReason {
		t.Fatalf("Deliver() identity/reason = %#v, want event identity and stable reason", outcome)
	}
	wantDelays := []time.Duration{10 * time.Millisecond, 15 * time.Millisecond}
	if !reflect.DeepEqual(delays, wantDelays) || !reflect.DeepEqual(outcome.RetryDelays, wantDelays) {
		t.Fatalf("Deliver() delays = %v/%v, want %v", delays, outcome.RetryDelays, wantDelays)
	}
	if attempts != 3 || len(received) != attempts {
		t.Fatalf("consumer attempts = %d/%d, want three bounded attempts", attempts, len(received))
	}
	for _, got := range received {
		if !reflect.DeepEqual(got, event) {
			t.Fatalf("consumer event = %#v, want %#v", got, event)
		}
	}
	records, err := store.List(context.Background(), 2)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 1 || records[0].DeliveryID != event.ID || records[0].CorrelationID != event.CorrelationID || records[0].Attempts != outcome.Attempts {
		t.Fatalf("dead-letter records = %#v, want one correlated terminal record", records)
	}
}
