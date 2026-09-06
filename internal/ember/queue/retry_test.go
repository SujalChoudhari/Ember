package queue

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestDeliverRetriesWithBoundedBackoffAndReportsTerminalFailure(t *testing.T) {
	ctx := context.Background()
	policy := RetryPolicy{
		MaxRetries:     3,
		InitialBackoff: 10 * time.Millisecond,
		MaxBackoff:     25 * time.Millisecond,
	}
	delivery := Delivery{ID: "message-1", CorrelationID: "correlation-1", Payload: []byte("opaque")}
	var attempts int
	var delays []time.Duration

	outcome, err := Deliver(ctx, delivery, policy, func(context.Context, Delivery) error {
		attempts++
		return errors.New("consumer detail must not escape")
	}, func(_ context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return nil
	})
	if !errors.Is(err, ErrDeliveryFailed) {
		t.Fatalf("Deliver() error = %v, want ErrDeliveryFailed", err)
	}
	if outcome.Status != DeliveryStatusFailed {
		t.Fatalf("Deliver() status = %q, want failed", outcome.Status)
	}
	if outcome.DeliveryID != delivery.ID || outcome.CorrelationID != delivery.CorrelationID {
		t.Fatalf("Deliver() identity = %#v, want delivery identity", outcome)
	}
	if outcome.Attempts != 4 || attempts != 4 {
		t.Fatalf("Deliver() attempts = %d/%d, want four bounded attempts", outcome.Attempts, attempts)
	}
	wantDelays := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 25 * time.Millisecond}
	if !reflect.DeepEqual(delays, wantDelays) || !reflect.DeepEqual(outcome.RetryDelays, wantDelays) {
		t.Fatalf("Deliver() delays = %v/%v, want %v", delays, outcome.RetryDelays, wantDelays)
	}
	if len(outcome.RetryDelays) > MaxRetryCount {
		t.Fatalf("Deliver() retry metadata length = %d, exceeds %d", len(outcome.RetryDelays), MaxRetryCount)
	}
	if outcome.Reason != DeliveryFailureReason {
		t.Fatalf("Deliver() reason = %q, want stable terminal reason", outcome.Reason)
	}
}
