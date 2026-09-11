package queue

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

const MaxRedriveRequestIDLength = 128

var (
	ErrInvalidRedriveRequest = errors.New("invalid redrive request")
	ErrRedriveConflict       = errors.New("redrive request identity conflict")
	ErrRedriveNotFound       = errors.New("dead-letter delivery not found")
	ErrRedriveFull           = errors.New("redrive history is full")
	ErrRedriveInProgress     = errors.New("redrive request is already in progress")
)

// RedriveOutcome is bounded metadata for one redrive request. It deliberately
// omits the delivery payload and consumer error details.
type RedriveOutcome struct {
	RequestID     string
	DeliveryID    string
	CorrelationID string
	Attempts      int
	RetryDelays   []time.Duration
	Status        DeliveryStatus
	Reason        string
}

type redriveDiskRecord struct {
	RequestID      string         `json:"request_id"`
	DeliveryID     string         `json:"delivery_id"`
	CorrelationID  string         `json:"correlation_id"`
	PayloadSHA256  string         `json:"payload_sha256"`
	MaxRetries     int            `json:"max_retries"`
	InitialBackoff int64          `json:"initial_backoff_ns"`
	MaxBackoff     int64          `json:"max_backoff_ns"`
	Attempts       int            `json:"attempts"`
	RetryDelays    []int64        `json:"retry_delays_ns"`
	Status         DeliveryStatus `json:"status"`
	Reason         string         `json:"reason,omitempty"`
}

func (record redriveDiskRecord) validate() error {
	if strings.TrimSpace(record.RequestID) == "" || len(record.RequestID) > MaxRedriveRequestIDLength ||
		strings.TrimSpace(record.DeliveryID) == "" || len(record.DeliveryID) > MaxDeliveryIDLength ||
		strings.TrimSpace(record.CorrelationID) == "" || len(record.CorrelationID) > MaxCorrelationIDLength ||
		len(record.PayloadSHA256) != sha256.Size*2 || !isLowerHex(record.PayloadSHA256) ||
		record.MaxRetries < 0 || record.MaxRetries > MaxRetryCount ||
		record.InitialBackoff < 0 || record.InitialBackoff > int64(MaxRetryBackoff) ||
		record.MaxBackoff < record.InitialBackoff || record.MaxBackoff > int64(MaxRetryBackoff) ||
		record.Attempts < 1 || record.Attempts > MaxRetryCount+1 ||
		len(record.RetryDelays) > MaxRetryCount || len(record.RetryDelays) != record.Attempts-1 ||
		record.Status != DeliveryStatusSucceeded && record.Status != DeliveryStatusFailed {
		return ErrInvalidRedriveRequest
	}
	for _, delay := range record.RetryDelays {
		if delay < 0 || delay > int64(MaxRetryBackoff) {
			return ErrInvalidRedriveRequest
		}
	}
	if record.Status == DeliveryStatusFailed && record.Reason != DeliveryFailureReason {
		return ErrInvalidRedriveRequest
	}
	if record.Status == DeliveryStatusSucceeded && record.Reason != "" {
		return ErrInvalidRedriveRequest
	}
	return nil
}

func (record redriveDiskRecord) outcome() RedriveOutcome {
	delays := make([]time.Duration, len(record.RetryDelays))
	for index, delay := range record.RetryDelays {
		delays[index] = time.Duration(delay)
	}
	return RedriveOutcome{
		RequestID:     record.RequestID,
		DeliveryID:    record.DeliveryID,
		CorrelationID: record.CorrelationID,
		Attempts:      record.Attempts,
		RetryDelays:   delays,
		Status:        record.Status,
		Reason:        record.Reason,
	}
}

func newRedriveDiskRecord(requestID string, delivery Delivery, policy RetryPolicy, outcome DeliveryOutcome) redriveDiskRecord {
	digest := sha256.Sum256(delivery.Payload)
	delays := make([]int64, len(outcome.RetryDelays))
	for index, delay := range outcome.RetryDelays {
		delays[index] = int64(delay)
	}
	return redriveDiskRecord{
		RequestID:      requestID,
		DeliveryID:     delivery.ID,
		CorrelationID:  delivery.CorrelationID,
		PayloadSHA256:  hex.EncodeToString(digest[:]),
		MaxRetries:     policy.MaxRetries,
		InitialBackoff: int64(policy.InitialBackoff),
		MaxBackoff:     int64(policy.MaxBackoff),
		Attempts:       outcome.Attempts,
		RetryDelays:    delays,
		Status:         outcome.Status,
		Reason:         outcome.Reason,
	}
}

func (record redriveDiskRecord) matches(requestID string, delivery Delivery, policy RetryPolicy) bool {
	digest := sha256.Sum256(delivery.Payload)
	return record.RequestID == requestID && record.DeliveryID == delivery.ID &&
		record.CorrelationID == delivery.CorrelationID && record.PayloadSHA256 == hex.EncodeToString(digest[:]) &&
		record.MaxRetries == policy.MaxRetries && record.InitialBackoff == int64(policy.InitialBackoff) &&
		record.MaxBackoff == int64(policy.MaxBackoff)
}

// RedriveStore is the existing dead-letter store with an idempotent recovery action.
type RedriveStore interface {
	DeadLetterStore
	Redrive(ctx context.Context, requestID string, delivery Delivery, policy RetryPolicy, consumer Consumer, wait Waiter) (RedriveOutcome, error)
}

// Redrive delivers a caller-supplied bounded payload for a recorded dead letter.
// Successful recovery removes the dead-letter record and retains only bounded
// request metadata so the same request can be replayed after a restart.
func (store *FileDeadLetterStore) Redrive(ctx context.Context, requestID string, delivery Delivery, policy RetryPolicy, consumer Consumer, wait Waiter) (RedriveOutcome, error) {
	if err := ctx.Err(); err != nil {
		return RedriveOutcome{}, err
	}
	if strings.TrimSpace(requestID) == "" || len(requestID) > MaxRedriveRequestIDLength {
		return RedriveOutcome{}, ErrInvalidRedriveRequest
	}
	if err := delivery.Validate(); err != nil {
		return RedriveOutcome{}, err
	}
	if err := policy.Validate(); err != nil {
		return RedriveOutcome{}, err
	}

	digest := sha256.Sum256(delivery.Payload)
	payloadDigest := hex.EncodeToString(digest[:])
	store.mu.Lock()
	for _, record := range store.redrives {
		if record.RequestID != requestID {
			continue
		}
		if !record.matches(requestID, delivery, policy) {
			store.mu.Unlock()
			return RedriveOutcome{}, ErrRedriveConflict
		}
		outcome := record.outcome()
		store.mu.Unlock()
		if outcome.Status == DeliveryStatusFailed {
			return outcome, ErrDeliveryFailed
		}
		return outcome, nil
	}
	if _, exists := store.inflight[requestID]; exists {
		store.mu.Unlock()
		return RedriveOutcome{}, ErrRedriveInProgress
	}
	deadLetterIndex := -1
	for index, record := range store.records {
		if record.DeliveryID != delivery.ID {
			continue
		}
		if record.CorrelationID != delivery.CorrelationID || record.PayloadSHA256 != payloadDigest {
			store.mu.Unlock()
			return RedriveOutcome{}, ErrRedriveConflict
		}
		deadLetterIndex = index
		break
	}
	if deadLetterIndex < 0 {
		store.mu.Unlock()
		return RedriveOutcome{}, ErrRedriveNotFound
	}
	if _, exists := store.inflightDeliveries[delivery.ID]; exists {
		store.mu.Unlock()
		return RedriveOutcome{}, ErrRedriveInProgress
	}
	if len(store.redrives) >= store.options.MaxRecords {
		store.mu.Unlock()
		return RedriveOutcome{}, ErrRedriveFull
	}
	if consumer == nil {
		store.mu.Unlock()
		return RedriveOutcome{}, ErrInvalidConsumer
	}
	store.inflight[requestID] = struct{}{}
	store.inflightDeliveries[delivery.ID] = struct{}{}
	store.mu.Unlock()

	outcome, deliverErr := Deliver(ctx, delivery, policy, consumer, wait)
	if deliverErr != nil && !errors.Is(deliverErr, ErrDeliveryFailed) {
		store.mu.Lock()
		delete(store.inflight, requestID)
		delete(store.inflightDeliveries, delivery.ID)
		store.mu.Unlock()
		return RedriveOutcome{}, deliverErr
	}
	redrive := newRedriveDiskRecord(requestID, delivery, policy, outcome)
	if err := redrive.validate(); err != nil {
		store.mu.Lock()
		delete(store.inflight, requestID)
		delete(store.inflightDeliveries, delivery.ID)
		store.mu.Unlock()
		return RedriveOutcome{}, ErrRedriveConflict
	}

	store.mu.Lock()
	defer store.mu.Unlock()
	delete(store.inflight, requestID)
	delete(store.inflightDeliveries, delivery.ID)
	oldRecords := append([]DeadLetterRecord(nil), store.records...)
	oldRedrives := append([]redriveDiskRecord(nil), store.redrives...)
	store.redrives = append(store.redrives, redrive)
	if outcome.Status == DeliveryStatusSucceeded {
		remaining := make([]DeadLetterRecord, 0, len(store.records)-1)
		remaining = append(remaining, store.records[:deadLetterIndex]...)
		remaining = append(remaining, store.records[deadLetterIndex+1:]...)
		store.records = remaining
	}
	if err := store.saveLocked(); err != nil {
		store.records = oldRecords
		store.redrives = oldRedrives
		return RedriveOutcome{}, err
	}
	result := redrive.outcome()
	if errors.Is(deliverErr, ErrDeliveryFailed) {
		return result, ErrDeliveryFailed
	}
	return result, nil
}

var _ RedriveStore = (*FileDeadLetterStore)(nil)
