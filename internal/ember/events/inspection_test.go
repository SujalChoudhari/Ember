package events

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/queue"
)

func TestInspectDeadLettersAndRecoverEventIdempotently(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "dead-letters.json")
	store, err := queue.NewFileDeadLetterStore(path, queue.DeadLetterStoreOptions{MaxRecords: 2})
	if err != nil {
		t.Fatalf("NewFileDeadLetterStore() error = %v", err)
	}
	event := Event{
		ID:            "event-1",
		CorrelationID: "correlation-1",
		Type:          "resource.updated",
		Payload:       []byte("opaque-secret-payload"),
	}
	if _, err := Deliver(ctx, event, queue.RetryPolicy{}, func(context.Context, Event) error {
		return errors.New("consumer detail")
	}, nil, store); !errors.Is(err, queue.ErrDeliveryFailed) {
		t.Fatalf("Deliver() error = %v, want queue.ErrDeliveryFailed", err)
	}

	records, err := InspectDeadLetters(ctx, store, 1)
	if err != nil {
		t.Fatalf("InspectDeadLetters() error = %v", err)
	}
	if len(records) != 1 || records[0].DeliveryID != event.ID || records[0].CorrelationID != event.CorrelationID ||
		records[0].PayloadBytes != len(event.Payload) || records[0].PayloadSHA256 == "" {
		t.Fatalf("InspectDeadLetters() = %#v, want one redacted event record", records)
	}

	calls := 0
	recovered, err := Recover(ctx, store, "request-1", event, queue.RetryPolicy{}, func(context.Context, Event) error {
		calls++
		return nil
	}, nil, nil)
	if err != nil {
		t.Fatalf("Recover() error = %v", err)
	}
	if recovered.Status != queue.DeliveryStatusSucceeded || calls != 1 {
		t.Fatalf("Recover() = %#v/calls=%d, want successful single delivery", recovered, calls)
	}

	replay, err := Recover(ctx, store, "request-1", event, queue.RetryPolicy{}, func(context.Context, Event) error {
		calls++
		return errors.New("must not run on replay")
	}, nil, nil)
	if err != nil {
		t.Fatalf("Recover() replay error = %v", err)
	}
	if !reflect.DeepEqual(replay, recovered) || calls != 1 {
		t.Fatalf("Recover() replay = %#v/calls=%d, want %#v/calls=1", replay, calls, recovered)
	}

	if _, err := Recover(ctx, store, "request-1", Event{
		ID:            event.ID,
		CorrelationID: event.CorrelationID,
		Type:          event.Type,
		Payload:       []byte("different-payload"),
	}, queue.RetryPolicy{}, func(context.Context, Event) error { return nil }, nil, nil); !errors.Is(err, queue.ErrRedriveConflict) {
		t.Fatalf("Recover() conflicting replay error = %v, want queue.ErrRedriveConflict", err)
	}

	records, err = InspectDeadLetters(ctx, store, 1)
	if err != nil {
		t.Fatalf("InspectDeadLetters() after recovery error = %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("InspectDeadLetters() after recovery = %#v, want no recovered record", records)
	}
}
