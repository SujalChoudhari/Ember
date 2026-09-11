package events

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"github.com/SujalChoudhari/Ember/internal/ember/queue"
)

const (
	MaxTopicNameLength        = 128
	MaxSubscriptionNameLength = 128
	MaxTopicScopeLength       = 128
	MaxTopicOwnerLength       = 128
	MaxTopicCount             = 100
	MaxSubscriptionCount      = 1_000
)

var (
	ErrInvalidTopicBrokerOptions = errors.New("invalid topic broker options")
	ErrInvalidTopic              = errors.New("invalid topic")
	ErrInvalidSubscription       = errors.New("invalid subscription")
	ErrTopicNotFound             = errors.New("topic not found")
	ErrSubscriptionNotFound      = errors.New("subscription not found")
	ErrTopicScopeDenied          = errors.New("topic scope denied")
	ErrSubscriptionScopeDenied   = errors.New("subscription scope denied")
	ErrTopicLimitExceeded        = errors.New("topic limit exceeded")
	ErrSubscriptionLimitExceeded = errors.New("subscription limit exceeded")
	ErrTopicHasSubscriptions     = errors.New("topic has subscriptions")
	ErrDuplicateTopic            = errors.New("duplicate topic")
	ErrDuplicateSubscription     = errors.New("duplicate subscription")
	ErrInvalidEventConsumer      = errors.New("invalid event consumer")
)

type TopicBrokerOptions struct {
	MaxTopics        int
	MaxSubscriptions int
}

type Topic struct {
	ID      string
	ScopeID string
	Owner   string
	Name    string
}

type Subscription struct {
	ID      string
	TopicID string
	ScopeID string
	Owner   string
	Name    string
	Filter  EventFilter
}

// TopicDeliveryReport exposes one bounded delivery outcome per matching
// subscription. Delivery IDs are namespaced by subscription so separate
// subscribers cannot collide in the shared dead-letter store.
type TopicDeliveryReport struct {
	Subscription Subscription
	Outcome      queue.DeliveryOutcome
}

type EventFilter struct {
	Type          string
	CorrelationID string
}

func (filter EventFilter) Validate() error {
	if len(filter.Type) > MaxEventTypeLength || len(filter.CorrelationID) > MaxTopicOwnerLength {
		return ErrInvalidSubscription
	}
	return nil
}

func (filter EventFilter) matches(event Event) bool {
	return (filter.Type == "" || filter.Type == event.Type) &&
		(filter.CorrelationID == "" || filter.CorrelationID == event.CorrelationID)
}

type TopicBroker struct {
	mu                 sync.RWMutex
	maxTopics          int
	maxSubscriptions   int
	nextTopicID        uint64
	nextSubscriptionID uint64
	topics             map[string]Topic
	subscriptions      map[string]Subscription
}

func NewTopicBroker(options TopicBrokerOptions) (*TopicBroker, error) {
	if options.MaxTopics <= 0 || options.MaxTopics > MaxTopicCount ||
		options.MaxSubscriptions <= 0 || options.MaxSubscriptions > MaxSubscriptionCount {
		return nil, ErrInvalidTopicBrokerOptions
	}
	return &TopicBroker{
		maxTopics:        options.MaxTopics,
		maxSubscriptions: options.MaxSubscriptions,
		topics:           make(map[string]Topic),
		subscriptions:    make(map[string]Subscription),
	}, nil
}

func validateTopicScope(scopeID string) error {
	if len(scopeID) > MaxTopicScopeLength || (scopeID != "" && strings.TrimSpace(scopeID) == "") {
		return ErrInvalidTopic
	}
	return nil
}

func validateTopicIdentity(scopeID, owner, name string) error {
	if err := validateTopicScope(scopeID); err != nil {
		return err
	}
	if strings.TrimSpace(owner) == "" || len(owner) > MaxTopicOwnerLength ||
		strings.TrimSpace(name) == "" || len(name) > MaxTopicNameLength {
		return ErrInvalidTopic
	}
	return nil
}

func validateSubscriptionIdentity(scopeID, owner, name string) error {
	if err := validateTopicScope(scopeID); err != nil {
		return ErrInvalidSubscription
	}
	if strings.TrimSpace(owner) == "" || len(owner) > MaxTopicOwnerLength ||
		strings.TrimSpace(name) == "" || len(name) > MaxSubscriptionNameLength {
		return ErrInvalidSubscription
	}
	return nil
}

func (broker *TopicBroker) CreateTopic(ctx context.Context, scopeID, owner, name string) (*Topic, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateTopicIdentity(scopeID, owner, name); err != nil {
		return nil, err
	}

	broker.mu.Lock()
	defer broker.mu.Unlock()
	if len(broker.topics) >= broker.maxTopics {
		return nil, ErrTopicLimitExceeded
	}
	for _, topic := range broker.topics {
		if topic.ScopeID == scopeID && topic.Name == name {
			return nil, ErrDuplicateTopic
		}
	}
	broker.nextTopicID++
	topic := Topic{
		ID:      formatTopicID(broker.nextTopicID),
		ScopeID: scopeID,
		Owner:   owner,
		Name:    name,
	}
	broker.topics[topic.ID] = topic
	return &topic, nil
}

func (broker *TopicBroker) GetTopic(ctx context.Context, scopeID, topicID string) (*Topic, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateTopicScope(scopeID); err != nil {
		return nil, err
	}
	broker.mu.RLock()
	defer broker.mu.RUnlock()
	topic, exists := broker.topics[topicID]
	if !exists {
		return nil, ErrTopicNotFound
	}
	if topic.ScopeID != scopeID {
		return nil, ErrTopicScopeDenied
	}
	return &topic, nil
}

func (broker *TopicBroker) DeleteTopic(ctx context.Context, scopeID, topicID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateTopicScope(scopeID); err != nil {
		return err
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	topic, exists := broker.topics[topicID]
	if !exists {
		return ErrTopicNotFound
	}
	if topic.ScopeID != scopeID {
		return ErrTopicScopeDenied
	}
	for _, subscription := range broker.subscriptions {
		if subscription.TopicID == topicID {
			return ErrTopicHasSubscriptions
		}
	}
	delete(broker.topics, topicID)
	return nil
}

func (broker *TopicBroker) CreateSubscription(ctx context.Context, scopeID, owner, topicID, name string, filter EventFilter) (*Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateSubscriptionIdentity(scopeID, owner, name); err != nil {
		return nil, err
	}
	if err := filter.Validate(); err != nil {
		return nil, err
	}

	broker.mu.Lock()
	defer broker.mu.Unlock()
	topic, exists := broker.topics[topicID]
	if !exists {
		return nil, ErrTopicNotFound
	}
	if topic.ScopeID != scopeID {
		return nil, ErrTopicScopeDenied
	}
	if len(broker.subscriptions) >= broker.maxSubscriptions {
		return nil, ErrSubscriptionLimitExceeded
	}
	for _, subscription := range broker.subscriptions {
		if subscription.TopicID == topicID && subscription.Name == name {
			return nil, ErrDuplicateSubscription
		}
	}
	broker.nextSubscriptionID++
	subscription := Subscription{
		ID:      formatSubscriptionID(broker.nextSubscriptionID),
		TopicID: topicID,
		ScopeID: scopeID,
		Owner:   owner,
		Name:    name,
		Filter:  filter,
	}
	broker.subscriptions[subscription.ID] = subscription
	return &subscription, nil
}

func (broker *TopicBroker) GetSubscription(ctx context.Context, scopeID, subscriptionID string) (*Subscription, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateTopicScope(scopeID); err != nil {
		return nil, ErrInvalidSubscription
	}
	broker.mu.RLock()
	defer broker.mu.RUnlock()
	subscription, exists := broker.subscriptions[subscriptionID]
	if !exists {
		return nil, ErrSubscriptionNotFound
	}
	if subscription.ScopeID != scopeID {
		return nil, ErrSubscriptionScopeDenied
	}
	return &subscription, nil
}

func (broker *TopicBroker) DeleteSubscription(ctx context.Context, scopeID, subscriptionID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := validateTopicScope(scopeID); err != nil {
		return ErrInvalidSubscription
	}
	broker.mu.Lock()
	defer broker.mu.Unlock()
	subscription, exists := broker.subscriptions[subscriptionID]
	if !exists {
		return ErrSubscriptionNotFound
	}
	if subscription.ScopeID != scopeID {
		return ErrSubscriptionScopeDenied
	}
	delete(broker.subscriptions, subscriptionID)
	return nil
}

func (broker *TopicBroker) Deliver(ctx context.Context, scopeID, owner, topicID string, event Event, consumer func(context.Context, Subscription, Event) error) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := validateTopicScope(scopeID); err != nil {
		return 0, err
	}
	if strings.TrimSpace(owner) == "" || len(owner) > MaxTopicOwnerLength {
		return 0, ErrInvalidEventConsumer
	}
	if consumer == nil {
		return 0, ErrInvalidEventConsumer
	}
	if err := event.Validate(); err != nil {
		return 0, err
	}

	broker.mu.RLock()
	topic, exists := broker.topics[topicID]
	if !exists {
		broker.mu.RUnlock()
		return 0, ErrTopicNotFound
	}
	if topic.ScopeID != scopeID {
		broker.mu.RUnlock()
		return 0, ErrTopicScopeDenied
	}
	matching := make([]Subscription, 0)
	for _, subscription := range broker.subscriptions {
		if subscription.TopicID == topicID && subscription.ScopeID == scopeID &&
			subscription.Owner == owner && subscription.Filter.matches(event) {
			matching = append(matching, subscription)
		}
	}
	broker.mu.RUnlock()
	sort.Slice(matching, func(i, j int) bool { return matching[i].ID < matching[j].ID })

	for index, subscription := range matching {
		if err := consumer(ctx, subscription, event); err != nil {
			return index, err
		}
	}
	return len(matching), nil
}

// DeliverWithMetrics routes matching subscriptions with bounded retry,
// dead-letter, recovery-visible outcome, and aggregate metric semantics.
func (broker *TopicBroker) DeliverWithMetrics(ctx context.Context, scopeID, owner, topicID string, event Event, policy queue.RetryPolicy, consumer func(context.Context, Subscription, Event) error, wait Waiter, deadLetters queue.DeadLetterStore, metrics *Metrics) ([]TopicDeliveryReport, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateTopicScope(scopeID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(owner) == "" || len(owner) > MaxTopicOwnerLength || consumer == nil {
		return nil, ErrInvalidEventConsumer
	}
	if err := event.Validate(); err != nil {
		return nil, err
	}

	broker.mu.RLock()
	topic, exists := broker.topics[topicID]
	if !exists {
		broker.mu.RUnlock()
		return nil, ErrTopicNotFound
	}
	if topic.ScopeID != scopeID {
		broker.mu.RUnlock()
		return nil, ErrTopicScopeDenied
	}
	matching := make([]Subscription, 0)
	for _, subscription := range broker.subscriptions {
		if subscription.TopicID == topicID && subscription.ScopeID == scopeID && subscription.Owner == owner && subscription.Filter.matches(event) {
			matching = append(matching, subscription)
		}
	}
	broker.mu.RUnlock()
	sort.Slice(matching, func(i, j int) bool { return matching[i].ID < matching[j].ID })

	reports := make([]TopicDeliveryReport, 0, len(matching))
	for _, subscription := range matching {
		scopedEvent := event
		scopedEvent.ID = event.ID + ":" + subscription.ID
		if len(scopedEvent.ID) > queue.MaxDeliveryIDLength {
			return reports, ErrInvalidEvent
		}
		outcome, err := DeliverWithMetrics(ctx, scopedEvent, policy, func(deliveryContext context.Context, delivered Event) error {
			delivered.ID = event.ID
			return consumer(deliveryContext, subscription, delivered)
		}, wait, deadLetters, metrics)
		reports = append(reports, TopicDeliveryReport{Subscription: subscription, Outcome: outcome})
		if err != nil {
			return reports, err
		}
	}
	return reports, nil
}

func formatTopicID(id uint64) string {
	return formatSequenceID("topic", id)
}

func formatSubscriptionID(id uint64) string {
	return formatSequenceID("subscription", id)
}

func formatSequenceID(prefix string, id uint64) string {
	return prefix + "-" + zeroPadSequence(id)
}

func zeroPadSequence(id uint64) string {
	const width = 8
	value := ""
	for id > 0 {
		value = string(rune('0'+id%10)) + value
		id /= 10
	}
	for len(value) < width {
		value = "0" + value
	}
	return value
}
