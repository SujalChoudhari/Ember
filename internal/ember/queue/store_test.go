package queue

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFileQueueEnqueueReceiveAcknowledgeAndPersist(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.json")
	clock := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	queue, err := NewFileQueue(path, QueueOptions{MaxMessages: 4, MaxBytes: 64, VisibilityTimeout: time.Minute})
	if err != nil {
		t.Fatalf("NewFileQueue() error = %v", err)
	}
	queue.now = func() time.Time { return clock }

	messageID, err := queue.Enqueue(ctx, "corr-1", []byte("payload"))
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if messageID != "message-00000001" {
		t.Fatalf("Enqueue() ID = %q, want deterministic first ID", messageID)
	}

	received, err := queue.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive() error = %v", err)
	}
	if received.ID != messageID || received.CorrelationID != "corr-1" || string(received.Payload) != "payload" || received.DeliveryCount != 1 {
		t.Fatalf("Receive() = %#v, want first delivery", received)
	}
	if err := queue.Acknowledge(ctx, received.Receipt); err != nil {
		t.Fatalf("Acknowledge() error = %v", err)
	}
	if _, err := queue.Receive(ctx); !errors.Is(err, ErrQueueEmpty) {
		t.Fatalf("Receive(after acknowledge) error = %v, want ErrQueueEmpty", err)
	}

	reopened, err := NewFileQueue(path, QueueOptions{MaxMessages: 4, MaxBytes: 64, VisibilityTimeout: time.Minute})
	if err != nil {
		t.Fatalf("NewFileQueue(reopen) error = %v", err)
	}
	reopened.now = func() time.Time { return clock }
	if _, err := reopened.Receive(ctx); !errors.Is(err, ErrQueueEmpty) {
		t.Fatalf("Receive(after reopen) error = %v, want ErrQueueEmpty", err)
	}
}

func TestFileQueueRedeliversAfterVisibilityAndRejectsStaleAcknowledgement(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.json")
	clock := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	queue, err := NewFileQueue(path, QueueOptions{MaxMessages: 4, MaxBytes: 64, VisibilityTimeout: time.Minute})
	if err != nil {
		t.Fatalf("NewFileQueue() error = %v", err)
	}
	queue.now = func() time.Time { return clock }
	if _, err := queue.Enqueue(ctx, "corr-1", []byte("payload")); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	first, err := queue.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive(first) error = %v", err)
	}
	clock = clock.Add(time.Minute)
	second, err := queue.Receive(ctx)
	if err != nil {
		t.Fatalf("Receive(redelivery) error = %v", err)
	}
	if second.ID != first.ID || second.DeliveryCount != 2 || !reflect.DeepEqual(second.Payload, first.Payload) || second.Receipt == first.Receipt {
		t.Fatalf("redelivery = %#v, want same message with a fresh receipt", second)
	}
	if err := queue.Acknowledge(ctx, first.Receipt); !errors.Is(err, ErrStaleReceipt) {
		t.Fatalf("Acknowledge(stale) error = %v, want ErrStaleReceipt", err)
	}
	if err := queue.Acknowledge(ctx, second.Receipt); err != nil {
		t.Fatalf("Acknowledge(current) error = %v", err)
	}
}

func TestFileQueueEnforcesConfiguredBoundsWithoutGrowingState(t *testing.T) {
	ctx := context.Background()
	queue, err := NewFileQueue(filepath.Join(t.TempDir(), "queue.json"), QueueOptions{MaxMessages: 1, MaxBytes: 5, VisibilityTimeout: time.Minute})
	if err != nil {
		t.Fatalf("NewFileQueue() error = %v", err)
	}
	if _, err := queue.Enqueue(ctx, "corr-1", []byte("12345")); err != nil {
		t.Fatalf("Enqueue(first) error = %v", err)
	}
	if _, err := queue.Enqueue(ctx, "corr-2", []byte("x")); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("Enqueue(message bound) error = %v, want ErrQueueFull", err)
	}
	if err := queue.Reset(ctx); err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if _, err := queue.Enqueue(ctx, "corr-2", []byte("123456")); !errors.Is(err, ErrQueueBytesExceeded) {
		t.Fatalf("Enqueue(byte bound) error = %v, want ErrQueueBytesExceeded", err)
	}
}
