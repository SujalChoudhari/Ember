package queue

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFileDeadLetterStoreRedrivesIdempotentlyAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dead-letters.json")
	store, err := NewFileDeadLetterStore(path, DeadLetterStoreOptions{MaxRecords: 2})
	if err != nil {
		t.Fatalf("NewFileDeadLetterStore() error = %v", err)
	}
	delivery := Delivery{ID: "message-1", CorrelationID: "correlation-1", Payload: []byte("s3cr3t-value")}
	_, err = DeliverWithDeadLetter(
		context.Background(), delivery, RetryPolicy{},
		func(context.Context, Delivery) error { return errors.New("consumer detail") },
		func(context.Context, time.Duration) error { return nil }, store,
	)
	if !errors.Is(err, ErrDeliveryFailed) {
		t.Fatalf("DeliverWithDeadLetter() error = %v, want ErrDeliveryFailed", err)
	}

	var calls int
	outcome, err := store.Redrive(
		context.Background(), "request-1", delivery, RetryPolicy{},
		func(context.Context, Delivery) error {
			calls++
			return nil
		}, nil,
	)
	if err != nil {
		t.Fatalf("Redrive() error = %v", err)
	}
	if outcome.RequestID != "request-1" || outcome.DeliveryID != delivery.ID ||
		outcome.Status != DeliveryStatusSucceeded || outcome.Attempts != 1 {
		t.Fatalf("Redrive() outcome = %#v, want successful bounded outcome", outcome)
	}
	if calls != 1 {
		t.Fatalf("Redrive() consumer calls = %d, want one", calls)
	}
	if records, err := store.List(context.Background(), 2); err != nil {
		t.Fatalf("List() after redrive error = %v", err)
	} else if len(records) != 0 {
		t.Fatalf("List() after redrive length = %d, want dead letter removed", len(records))
	}

	replay, err := store.Redrive(
		context.Background(), "request-1", delivery, RetryPolicy{},
		func(context.Context, Delivery) error {
			calls++
			return errors.New("must not run on replay")
		}, nil,
	)
	if err != nil {
		t.Fatalf("Redrive() replay error = %v", err)
	}
	if !reflect.DeepEqual(replay, outcome) || calls != 1 {
		t.Fatalf("Redrive() replay = %#v/calls=%d, want %#v/calls=1", replay, calls, outcome)
	}

	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if bytes.Contains(persisted, delivery.Payload) || bytes.Contains(persisted, []byte("consumer detail")) {
		t.Fatalf("redrive snapshot contains payload or consumer error detail")
	}

	restarted, err := NewFileDeadLetterStore(path, DeadLetterStoreOptions{MaxRecords: 2})
	if err != nil {
		t.Fatalf("NewFileDeadLetterStore(restart) error = %v", err)
	}
	restartedReplay, err := restarted.Redrive(
		context.Background(), "request-1", delivery, RetryPolicy{},
		func(context.Context, Delivery) error {
			calls++
			return errors.New("must not run after restart")
		}, nil,
	)
	if err != nil {
		t.Fatalf("Redrive() restart replay error = %v", err)
	}
	if !reflect.DeepEqual(restartedReplay, outcome) || calls != 1 {
		t.Fatalf("Redrive() restart replay = %#v/calls=%d, want %#v/calls=1", restartedReplay, calls, outcome)
	}

	_, err = restarted.Redrive(
		context.Background(), "request-1", Delivery{ID: delivery.ID, CorrelationID: delivery.CorrelationID, Payload: []byte("other")}, RetryPolicy{},
		func(context.Context, Delivery) error { return nil }, nil,
	)
	if !errors.Is(err, ErrRedriveConflict) {
		t.Fatalf("Redrive() conflicting replay error = %v, want ErrRedriveConflict", err)
	}
}
