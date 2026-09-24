package ember

import (
	"context"
	"encoding/json"
	"errors"
	"os"

	"github.com/SujalChoudhari/Ember/internal/ember/events"
	"github.com/SujalChoudhari/Ember/internal/ember/queue"
)

var ErrOperatorEventsUnavailable = errors.New("operator event runtime unavailable")

func (operator *Operator) eventScope(principal OperatorPrincipal) (string, error) {
	if operator == nil || operator.eventBroker == nil || operator.eventDeadLetters == nil {
		return "", ErrOperatorEventsUnavailable
	}
	if err := principal.validate(); err != nil {
		return "", err
	}
	if principal.ScopeID == "" {
		return "", ErrOperatorScopeDenied
	}
	return principal.ScopeID, nil
}

func (operator *Operator) ListEventTopics(ctx context.Context, principal OperatorPrincipal, limit int) ([]events.Topic, error) {
	scopeID, err := operator.eventScope(principal)
	if err != nil {
		return nil, err
	}
	return operator.eventBroker.ListTopics(ctx, scopeID, limit)
}

func (operator *Operator) CreateEventTopic(ctx context.Context, principal OperatorPrincipal, owner, name string) (*events.Topic, error) {
	scopeID, err := operator.eventScope(principal)
	if err != nil {
		return nil, err
	}
	topic, err := operator.eventBroker.CreateTopic(ctx, scopeID, owner, name)
	if err != nil {
		return nil, err
	}
	if err := operator.persistEventTopology(); err != nil {
		return nil, err
	}
	return topic, nil
}

func (operator *Operator) DeleteEventTopic(ctx context.Context, principal OperatorPrincipal, topicID string) error {
	scopeID, err := operator.eventScope(principal)
	if err != nil {
		return err
	}
	if err := operator.eventBroker.DeleteTopic(ctx, scopeID, topicID); err != nil {
		return err
	}
	return operator.persistEventTopology()
}

func (operator *Operator) ListEventSubscriptions(ctx context.Context, principal OperatorPrincipal, limit int) ([]events.Subscription, error) {
	scopeID, err := operator.eventScope(principal)
	if err != nil {
		return nil, err
	}
	return operator.eventBroker.ListSubscriptions(ctx, scopeID, limit)
}

func (operator *Operator) CreateEventSubscription(ctx context.Context, principal OperatorPrincipal, owner, topicID, name, eventType, correlationID string) (*events.Subscription, error) {
	scopeID, err := operator.eventScope(principal)
	if err != nil {
		return nil, err
	}
	subscription, err := operator.eventBroker.CreateSubscription(ctx, scopeID, owner, topicID, name, events.EventFilter{Type: eventType, CorrelationID: correlationID})
	if err != nil {
		return nil, err
	}
	if err := operator.persistEventTopology(); err != nil {
		return nil, err
	}
	return subscription, nil
}

func (operator *Operator) DeleteEventSubscription(ctx context.Context, principal OperatorPrincipal, subscriptionID string) error {
	scopeID, err := operator.eventScope(principal)
	if err != nil {
		return err
	}
	if err := operator.eventBroker.DeleteSubscription(ctx, scopeID, subscriptionID); err != nil {
		return err
	}
	return operator.persistEventTopology()
}

// PublishEvent is an operator probe. It routes the event through the real
// scoped topic broker and treats the console consumer as an acknowledgement;
// it does not claim to be a replacement for an application consumer.
func (operator *Operator) PublishEvent(ctx context.Context, principal OperatorPrincipal, owner, topicID string, event events.Event, retries int) ([]events.TopicDeliveryReport, error) {
	scopeID, err := operator.eventScope(principal)
	if err != nil {
		return nil, err
	}
	if retries < 0 || retries > queue.MaxRetryCount {
		return nil, queue.ErrInvalidRetryPolicy
	}
	event.TenantID = principal.TenantID
	event.ScopeID = scopeID
	return operator.eventBroker.DeliverWithMetrics(ctx, scopeID, owner, topicID, event, queue.RetryPolicy{MaxRetries: retries}, func(context.Context, events.Subscription, events.Event) error {
		return nil
	}, nil, operator.eventDeadLetters, operator.eventMetrics)
}

func (operator *Operator) ListEventDeadLetters(ctx context.Context, principal OperatorPrincipal, limit int) ([]queue.DeadLetterRecord, error) {
	if _, err := operator.eventScope(principal); err != nil {
		return nil, err
	}
	owned, ok := operator.eventDeadLetters.(interface {
		ListOwned(context.Context, string, string, int) ([]queue.DeadLetterRecord, error)
	})
	if !ok {
		return nil, ErrOperatorEventsUnavailable
	}
	return owned.ListOwned(ctx, principal.TenantID, principal.ScopeID, limit)
}

func (operator *Operator) EventMetrics() (events.MetricsSnapshot, error) {
	if operator == nil || operator.eventMetrics == nil {
		return events.MetricsSnapshot{}, ErrOperatorEventsUnavailable
	}
	return operator.eventMetrics.Snapshot(), nil
}

func (operator *Operator) EventRuntimeStatus() string {
	if operator == nil || operator.eventBroker == nil || operator.eventDeadLetters == nil {
		return "unavailable"
	}
	return "local-persistent"
}

func (operator *Operator) loadEventTopology() error {
	if operator == nil || operator.eventBroker == nil || operator.eventTopologyPath == "" {
		return ErrOperatorEventsUnavailable
	}
	data, err := os.ReadFile(operator.eventTopologyPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var state events.TopicBrokerState
	if err := json.Unmarshal(data, &state); err != nil {
		return err
	}
	return operator.eventBroker.Restore(state)
}

func (operator *Operator) persistEventTopology() error {
	if operator == nil || operator.eventBroker == nil || operator.eventTopologyPath == "" {
		return ErrOperatorEventsUnavailable
	}
	operator.eventPersistMu.Lock()
	defer operator.eventPersistMu.Unlock()
	data, err := json.Marshal(operator.eventBroker.Snapshot())
	if err != nil {
		return err
	}
	temporary := operator.eventTopologyPath + ".tmp"
	if err := os.WriteFile(temporary, data, 0o600); err != nil {
		return err
	}
	return os.Rename(temporary, operator.eventTopologyPath)
}
