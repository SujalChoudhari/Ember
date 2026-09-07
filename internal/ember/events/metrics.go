package events

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/queue"
)

// MetricsSnapshot is a bounded aggregate of event delivery outcomes. It keeps
// no event types, identifiers, payloads, or error text, so cardinality is
// constant and there is no retained per-event state.
type MetricsSnapshot struct {
	Cardinality             int
	DeliveryCount           uint64
	SuccessCount            uint64
	FailureCount            uint64
	RetryCount              uint64
	DeadLetterCount         uint64
	RecoveryCount           uint64
	RecoveryFailureCount    uint64
	DeliveryLatencyNanos    int64
	MaxDeliveryLatencyNanos int64
	RecoveryLatencyNanos    int64
	MaxRecoveryLatencyNanos int64
	RetryLagNanos           int64
}

// Metrics collects process-local, payload-free event delivery aggregates.
type Metrics struct {
	mu                      sync.RWMutex
	deliveryCount           uint64
	successCount            uint64
	failureCount            uint64
	retryCount              uint64
	deadLetterCount         uint64
	recoveryCount           uint64
	recoveryFailureCount    uint64
	deliveryLatencyNanos    int64
	maxDeliveryLatencyNanos int64
	recoveryLatencyNanos    int64
	maxRecoveryLatencyNanos int64
	retryLagNanos           int64
}

func NewMetrics() *Metrics {
	return &Metrics{}
}

func (metrics *Metrics) Snapshot() MetricsSnapshot {
	if metrics == nil {
		return MetricsSnapshot{}
	}
	metrics.mu.RLock()
	defer metrics.mu.RUnlock()
	return MetricsSnapshot{
		Cardinality:             1,
		DeliveryCount:           metrics.deliveryCount,
		SuccessCount:            metrics.successCount,
		FailureCount:            metrics.failureCount,
		RetryCount:              metrics.retryCount,
		DeadLetterCount:         metrics.deadLetterCount,
		RecoveryCount:           metrics.recoveryCount,
		RecoveryFailureCount:    metrics.recoveryFailureCount,
		DeliveryLatencyNanos:    metrics.deliveryLatencyNanos,
		MaxDeliveryLatencyNanos: metrics.maxDeliveryLatencyNanos,
		RecoveryLatencyNanos:    metrics.recoveryLatencyNanos,
		MaxRecoveryLatencyNanos: metrics.maxRecoveryLatencyNanos,
		RetryLagNanos:           metrics.retryLagNanos,
	}
}

// DeliverWithMetrics records the bounded outcome of an event delivery while
// preserving Deliver's existing validation, retry, and dead-letter behavior.
func DeliverWithMetrics(ctx context.Context, event Event, policy queue.RetryPolicy, consumer Consumer, wait Waiter, deadLetters queue.DeadLetterStore, metrics *Metrics) (queue.DeliveryOutcome, error) {
	started := time.Now()
	outcome, err := Deliver(ctx, event, policy, consumer, wait, deadLetters)
	if metrics != nil {
		metrics.observeDelivery(outcome, err, time.Since(started))
	}
	return outcome, err
}

// Recover performs an event redrive through the existing queue recovery store
// and records the bounded recovery outcome without duplicating persistence.
func Recover(ctx context.Context, store queue.RedriveStore, requestID string, event Event, policy queue.RetryPolicy, consumer Consumer, wait Waiter, metrics *Metrics) (queue.RedriveOutcome, error) {
	if err := event.Validate(); err != nil {
		return queue.RedriveOutcome{}, err
	}
	if consumer == nil {
		return queue.RedriveOutcome{}, queue.ErrInvalidConsumer
	}
	if store == nil {
		return queue.RedriveOutcome{}, queue.ErrInvalidDeadLetterStore
	}

	started := time.Now()
	outcome, err := store.Redrive(ctx, requestID, queue.Delivery{
		ID:            event.ID,
		CorrelationID: event.CorrelationID,
		Payload:       event.Payload,
	}, policy, func(ctx context.Context, delivery queue.Delivery) error {
		return consumer(ctx, Event{
			ID:            delivery.ID,
			CorrelationID: delivery.CorrelationID,
			Type:          event.Type,
			Payload:       delivery.Payload,
		})
	}, wait)
	if metrics != nil {
		metrics.observeRecovery(outcome, err, time.Since(started))
	}
	return outcome, err
}

func (metrics *Metrics) observeDelivery(outcome queue.DeliveryOutcome, err error, elapsed time.Duration) {
	if metrics == nil {
		return
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.deliveryCount = saturatingAdd(metrics.deliveryCount, 1)
	if outcome.Status == queue.DeliveryStatusSucceeded {
		metrics.successCount = saturatingAdd(metrics.successCount, 1)
	}
	if outcome.Status == queue.DeliveryStatusFailed {
		metrics.failureCount = saturatingAdd(metrics.failureCount, 1)
	}
	if outcome.Attempts > 1 {
		metrics.retryCount = saturatingAdd(metrics.retryCount, uint64(outcome.Attempts-1))
	}
	if errors.Is(err, queue.ErrDeliveryFailed) {
		metrics.deadLetterCount = saturatingAdd(metrics.deadLetterCount, 1)
	}
	metrics.deliveryLatencyNanos = saturatingAddInt64(metrics.deliveryLatencyNanos, elapsed.Nanoseconds())
	if elapsed.Nanoseconds() > metrics.maxDeliveryLatencyNanos {
		metrics.maxDeliveryLatencyNanos = elapsed.Nanoseconds()
	}
	for _, delay := range outcome.RetryDelays {
		metrics.retryLagNanos = saturatingAddInt64(metrics.retryLagNanos, delay.Nanoseconds())
	}
}

func (metrics *Metrics) observeRecovery(_ queue.RedriveOutcome, err error, elapsed time.Duration) {
	if metrics == nil {
		return
	}
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.recoveryCount = saturatingAdd(metrics.recoveryCount, 1)
	if err != nil {
		metrics.recoveryFailureCount = saturatingAdd(metrics.recoveryFailureCount, 1)
	}
	metrics.recoveryLatencyNanos = saturatingAddInt64(metrics.recoveryLatencyNanos, elapsed.Nanoseconds())
	if elapsed.Nanoseconds() > metrics.maxRecoveryLatencyNanos {
		metrics.maxRecoveryLatencyNanos = elapsed.Nanoseconds()
	}
}

func saturatingAdd(current, increment uint64) uint64 {
	if ^uint64(0)-current < increment {
		return ^uint64(0)
	}
	return current + increment
}

func saturatingAddInt64(current, increment int64) int64 {
	if increment > 0 && current > int64(^uint64(0)>>1)-increment {
		return int64(^uint64(0) >> 1)
	}
	return current + increment
}
