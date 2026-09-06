package queue

import (
	"context"
	"errors"
	"strings"
	"time"
)

const (
	MaxRetryCount           = 8
	MaxDeliveryIDLength     = 128
	MaxCorrelationIDLength  = 128
	MaxDeliveryPayloadBytes = 1 << 20
	MaxRetryBackoff         = time.Hour
	DeliveryFailureReason   = "delivery attempts exhausted"
)

var (
	ErrInvalidDelivery    = errors.New("invalid delivery")
	ErrInvalidRetryPolicy = errors.New("invalid retry policy")
	ErrInvalidConsumer    = errors.New("invalid consumer")
	ErrDeliveryFailed     = errors.New("delivery failed")
)

// RetryPolicy bounds the number and delay of attempts. MaxRetries excludes the
// initial delivery attempt.
type RetryPolicy struct {
	MaxRetries     int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
}

func (policy RetryPolicy) Validate() error {
	if policy.MaxRetries < 0 || policy.MaxRetries > MaxRetryCount ||
		policy.InitialBackoff < 0 || policy.InitialBackoff > MaxRetryBackoff ||
		policy.MaxBackoff < policy.InitialBackoff || policy.MaxBackoff > MaxRetryBackoff {
		return ErrInvalidRetryPolicy
	}
	return nil
}

// Delivery contains the bounded identity passed to a consumer. Payload is
// supplied to the consumer but is never copied into DeliveryOutcome metadata.
type Delivery struct {
	ID            string
	CorrelationID string
	Payload       []byte
}

func (delivery Delivery) Validate() error {
	if strings.TrimSpace(delivery.ID) == "" || len(delivery.ID) > MaxDeliveryIDLength ||
		strings.TrimSpace(delivery.CorrelationID) == "" || len(delivery.CorrelationID) > MaxCorrelationIDLength ||
		len(delivery.Payload) > MaxDeliveryPayloadBytes {
		return ErrInvalidDelivery
	}
	return nil
}

type DeliveryStatus string

const (
	DeliveryStatusSucceeded DeliveryStatus = "succeeded"
	DeliveryStatusFailed    DeliveryStatus = "failed"
)

// DeliveryOutcome is bounded terminal metadata for one delivery attempt
// sequence. It deliberately omits the payload and consumer error text.
type DeliveryOutcome struct {
	DeliveryID    string
	CorrelationID string
	Attempts      int
	RetryDelays   []time.Duration
	Status        DeliveryStatus
	Reason        string
}

type Consumer func(context.Context, Delivery) error
type Waiter func(context.Context, time.Duration) error

// Deliver invokes consumer once and retries failures according to policy. A
// terminal consumer failure returns ErrDeliveryFailed with stable, bounded
// metadata; consumer error details are not returned or persisted.
func Deliver(ctx context.Context, delivery Delivery, policy RetryPolicy, consumer Consumer, wait Waiter) (DeliveryOutcome, error) {
	if err := ctx.Err(); err != nil {
		return DeliveryOutcome{}, err
	}
	if err := delivery.Validate(); err != nil {
		return DeliveryOutcome{}, err
	}
	if err := policy.Validate(); err != nil {
		return DeliveryOutcome{}, err
	}
	if consumer == nil {
		return DeliveryOutcome{}, ErrInvalidConsumer
	}
	if wait == nil {
		wait = waitFor
	}

	outcome := DeliveryOutcome{
		DeliveryID:    delivery.ID,
		CorrelationID: delivery.CorrelationID,
		RetryDelays:   make([]time.Duration, 0, policy.MaxRetries),
	}
	for attempt := 1; attempt <= policy.MaxRetries+1; attempt++ {
		outcome.Attempts = attempt
		if err := consumer(ctx, delivery); err == nil {
			outcome.Status = DeliveryStatusSucceeded
			return outcome, nil
		}
		if err := ctx.Err(); err != nil {
			return outcome, err
		}
		if attempt > policy.MaxRetries {
			outcome.Status = DeliveryStatusFailed
			outcome.Reason = DeliveryFailureReason
			return outcome, ErrDeliveryFailed
		}

		delay := retryDelay(policy, attempt)
		outcome.RetryDelays = append(outcome.RetryDelays, delay)
		if err := wait(ctx, delay); err != nil {
			return outcome, err
		}
	}

	// The loop is bounded by policy validation and cannot fall through.
	return outcome, ErrDeliveryFailed
}

func retryDelay(policy RetryPolicy, retryNumber int) time.Duration {
	delay := policy.InitialBackoff
	for i := 1; i < retryNumber; i++ {
		if delay >= policy.MaxBackoff {
			return policy.MaxBackoff
		}
		if delay > policy.MaxBackoff/2 {
			return policy.MaxBackoff
		}
		delay *= 2
	}
	if delay > policy.MaxBackoff {
		return policy.MaxBackoff
	}
	return delay
}

func waitFor(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
