package events

import (
	"context"
	"errors"
	"strings"

	"github.com/SujalChoudhari/Ember/internal/ember/queue"
)

const MaxEventTypeLength = 128

var ErrInvalidEvent = errors.New("invalid event")

type Event struct {
	ID            string
	CorrelationID string
	Type          string
	Payload       []byte
}

func (event Event) Validate() error {
	if strings.TrimSpace(event.Type) == "" || len(event.Type) > MaxEventTypeLength {
		return ErrInvalidEvent
	}
	delivery := queue.Delivery{
		ID:            event.ID,
		CorrelationID: event.CorrelationID,
		Payload:       event.Payload,
	}
	if err := delivery.Validate(); err != nil {
		return ErrInvalidEvent
	}
	return nil
}

type Consumer func(context.Context, Event) error
type Waiter = queue.Waiter

// Deliver invokes the consumer once and retries failed acknowledgements within
// the supplied policy. An exhausted event is recorded through the existing
// bounded dead-letter store; successful consumer return is the acknowledgement.
func Deliver(ctx context.Context, event Event, policy queue.RetryPolicy, consumer Consumer, wait Waiter, deadLetters queue.DeadLetterStore) (queue.DeliveryOutcome, error) {
	if err := event.Validate(); err != nil {
		return queue.DeliveryOutcome{}, err
	}
	if consumer == nil {
		return queue.DeliveryOutcome{}, queue.ErrInvalidConsumer
	}
	delivery := queue.Delivery{
		ID:            event.ID,
		CorrelationID: event.CorrelationID,
		Payload:       event.Payload,
	}
	return queue.DeliverWithDeadLetter(
		ctx,
		delivery,
		policy,
		func(ctx context.Context, _ queue.Delivery) error {
			return consumer(ctx, event)
		},
		wait,
		deadLetters,
	)
}
