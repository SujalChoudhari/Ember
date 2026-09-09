package queue

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	queueStoreVersion      = 1
	DefaultQueueMessages   = 256
	DefaultQueueBytes      = 8 << 20
	MaxQueueMessages       = 4096
	MaxQueueBytes          = 64 << 20
	DefaultVisibility      = time.Minute
	MaxVisibilityTimeout   = time.Hour
	MaxQueueFileBytes      = 64 << 20
	MaxQueueCorrelationLen = MaxCorrelationIDLength
)

var (
	ErrInvalidQueue         = errors.New("invalid queue")
	ErrInvalidQueueOptions  = errors.New("invalid queue options")
	ErrQueueMessageTooLarge = errors.New("queue message exceeds size limit")
	ErrQueueFull            = errors.New("queue message limit exceeded")
	ErrQueueBytesExceeded   = errors.New("queue byte limit exceeded")
	ErrQueueEmpty           = errors.New("queue has no visible messages")
	ErrMessageNotFound      = errors.New("queue message not found")
	ErrStaleReceipt         = errors.New("queue receipt is stale")
	ErrQueueCorrupt         = errors.New("corrupt queue")
	ErrQueueTooLarge        = errors.New("queue exceeds size limit")
	ErrQueueIO              = errors.New("queue I/O failure")
)

// QueueOptions bounds retained messages and controls how long a received
// message remains invisible before it may be redelivered.
type QueueOptions struct {
	MaxMessages       int
	MaxBytes          int
	VisibilityTimeout time.Duration
}

func (options QueueOptions) normalized() (QueueOptions, error) {
	if options.MaxMessages == 0 {
		options.MaxMessages = DefaultQueueMessages
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = DefaultQueueBytes
	}
	if options.VisibilityTimeout == 0 {
		options.VisibilityTimeout = DefaultVisibility
	}
	if options.MaxMessages < 1 || options.MaxMessages > MaxQueueMessages ||
		options.MaxBytes < 1 || options.MaxBytes > MaxQueueBytes ||
		options.VisibilityTimeout <= 0 || options.VisibilityTimeout > MaxVisibilityTimeout {
		return QueueOptions{}, ErrInvalidQueueOptions
	}
	return options, nil
}

// ReceivedMessage is the bounded delivery view returned by Receive. The
// receipt is opaque to callers and is required to acknowledge this delivery.
type ReceivedMessage struct {
	Delivery
	Receipt       string
	DeliveryCount int
}

type queueDiskMessage struct {
	ID            string    `json:"id"`
	CorrelationID string    `json:"correlation_id"`
	Payload       []byte    `json:"payload"`
	EnqueuedAt    time.Time `json:"enqueued_at"`
	VisibleAt     time.Time `json:"visible_at"`
	DeliveryCount int       `json:"delivery_count"`
}

type queueDiskState struct {
	Version  int                `json:"version"`
	NextID   uint64             `json:"next_id"`
	Messages []queueDiskMessage `json:"messages"`
}

// FileQueue is a bounded, single-process queue with an atomic JSON snapshot.
// Payloads are delivered to consumers but never included in receipt or error
// metadata. Visibility and acknowledgement state survive process restart.
type FileQueue struct {
	mu       sync.Mutex
	path     string
	options  QueueOptions
	messages []queueDiskMessage
	nextID   uint64
	now      func() time.Time
}

func NewFileQueue(path string, options QueueOptions) (*FileQueue, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrInvalidQueue
	}
	normalized, err := options.normalized()
	if err != nil {
		return nil, err
	}
	path = filepath.Clean(path)
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return nil, ErrInvalidQueue
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, ErrQueueIO
	}

	queue := &FileQueue{
		path:    path,
		options: normalized,
		now:     time.Now,
	}
	if err := queue.load(); err != nil {
		return nil, err
	}
	return queue, nil
}

func (queue *FileQueue) load() error {
	data, err := os.ReadFile(queue.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrQueueIO
	}
	if len(data) > MaxQueueFileBytes {
		return ErrQueueTooLarge
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state queueDiskState
	if err := decoder.Decode(&state); err != nil {
		return ErrQueueCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrQueueCorrupt
	}
	if state.Version != queueStoreVersion || len(state.Messages) > queue.options.MaxMessages {
		return ErrQueueCorrupt
	}

	var bytesUsed int
	seen := make(map[string]struct{}, len(state.Messages))
	for _, message := range state.Messages {
		if err := validateDiskMessage(message); err != nil {
			return ErrQueueCorrupt
		}
		if _, exists := seen[message.ID]; exists {
			return ErrQueueCorrupt
		}
		seen[message.ID] = struct{}{}
		bytesUsed += len(message.Payload)
		if bytesUsed > queue.options.MaxBytes {
			return ErrQueueCorrupt
		}
	}
	queue.nextID = state.NextID
	queue.messages = append([]queueDiskMessage(nil), state.Messages...)
	return nil
}

func validateDiskMessage(message queueDiskMessage) error {
	if err := (Delivery{ID: message.ID, CorrelationID: message.CorrelationID, Payload: message.Payload}).Validate(); err != nil {
		return err
	}
	if message.EnqueuedAt.IsZero() || message.VisibleAt.IsZero() || message.DeliveryCount < 0 {
		return ErrInvalidDelivery
	}
	return nil
}

func (queue *FileQueue) saveLocked() error {
	data, err := json.MarshalIndent(queueDiskState{
		Version:  queueStoreVersion,
		NextID:   queue.nextID,
		Messages: queue.messages,
	}, "", "  ")
	if err != nil {
		return ErrQueueIO
	}
	data = append(data, '\n')
	if len(data) > MaxQueueFileBytes {
		return ErrQueueTooLarge
	}

	directory := filepath.Dir(queue.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return ErrQueueIO
	}
	temporary, err := os.CreateTemp(directory, ".ember-queue-*.tmp")
	if err != nil {
		return ErrQueueIO
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return ErrQueueIO
	}
	if written, err := temporary.Write(data); err != nil || written != len(data) {
		_ = temporary.Close()
		return ErrQueueIO
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return ErrQueueIO
	}
	if err := temporary.Close(); err != nil {
		return ErrQueueIO
	}
	if err := os.Rename(temporaryPath, queue.path); err != nil {
		return ErrQueueIO
	}
	if directoryFile, err := os.Open(directory); err == nil {
		_ = directoryFile.Sync()
		_ = directoryFile.Close()
	}
	return nil
}

func (queue *FileQueue) usedBytesLocked() int {
	used := 0
	for _, message := range queue.messages {
		used += len(message.Payload)
	}
	return used
}

func (queue *FileQueue) nextMessageIDLocked() (string, error) {
	if queue.nextID == ^uint64(0) {
		return "", ErrQueueFull
	}
	queue.nextID++
	return fmt.Sprintf("message-%08d", queue.nextID), nil
}

// Enqueue appends one bounded message and returns its deterministic identity.
func (queue *FileQueue) Enqueue(ctx context.Context, correlationID string, payload []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if strings.TrimSpace(correlationID) == "" || len(correlationID) > MaxQueueCorrelationLen {
		return "", ErrInvalidDelivery
	}
	if len(payload) > MaxDeliveryPayloadBytes {
		return "", ErrQueueMessageTooLarge
	}

	queue.mu.Lock()
	defer queue.mu.Unlock()
	if len(queue.messages) >= queue.options.MaxMessages {
		return "", ErrQueueFull
	}
	if len(payload) > queue.options.MaxBytes-queue.usedBytesLocked() {
		return "", ErrQueueBytesExceeded
	}
	id, err := queue.nextMessageIDLocked()
	if err != nil {
		return "", err
	}
	now := queue.now().UTC()
	message := queueDiskMessage{
		ID:            id,
		CorrelationID: correlationID,
		Payload:       append([]byte(nil), payload...),
		EnqueuedAt:    now,
		VisibleAt:     now,
	}
	queue.messages = append(queue.messages, message)
	if err := queue.saveLocked(); err != nil {
		queue.messages = queue.messages[:len(queue.messages)-1]
		queue.nextID--
		return "", err
	}
	return id, nil
}

// Receive returns the oldest visible message and starts its visibility window.
func (queue *FileQueue) Receive(ctx context.Context) (ReceivedMessage, error) {
	if err := ctx.Err(); err != nil {
		return ReceivedMessage{}, err
	}

	queue.mu.Lock()
	defer queue.mu.Unlock()
	now := queue.now().UTC()
	for index := range queue.messages {
		message := queue.messages[index]
		if now.Before(message.VisibleAt) {
			continue
		}
		previous := message
		message.DeliveryCount++
		message.VisibleAt = now.Add(queue.options.VisibilityTimeout)
		queue.messages[index] = message
		if err := queue.saveLocked(); err != nil {
			queue.messages[index] = previous
			return ReceivedMessage{}, err
		}
		return ReceivedMessage{
			Delivery: Delivery{
				ID:            message.ID,
				CorrelationID: message.CorrelationID,
				Payload:       append([]byte(nil), message.Payload...),
			},
			Receipt:       receiptFor(message),
			DeliveryCount: message.DeliveryCount,
		}, nil
	}
	return ReceivedMessage{}, ErrQueueEmpty
}

func receiptFor(message queueDiskMessage) string {
	return message.ID + ":" + strconv.Itoa(message.DeliveryCount)
}

// Acknowledge removes exactly the currently visible delivery identified by the
// receipt. An older receipt cannot acknowledge a redelivered message.
func (queue *FileQueue) Acknowledge(ctx context.Context, receipt string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	messageID, countText, ok := strings.Cut(receipt, ":")
	if !ok || messageID == "" {
		return ErrStaleReceipt
	}
	count, err := strconv.Atoi(countText)
	if err != nil || count < 1 {
		return ErrStaleReceipt
	}

	queue.mu.Lock()
	defer queue.mu.Unlock()
	for index, message := range queue.messages {
		if message.ID != messageID {
			continue
		}
		if receiptFor(message) != receipt {
			return ErrStaleReceipt
		}
		queue.messages = append(queue.messages[:index], queue.messages[index+1:]...)
		if err := queue.saveLocked(); err != nil {
			queue.messages = append(queue.messages, queueDiskMessage{})
			copy(queue.messages[index+1:], queue.messages[index:])
			queue.messages[index] = message
			return err
		}
		return nil
	}
	return ErrMessageNotFound
}

// Reset removes only the queue snapshot owned by this instance.
func (queue *FileQueue) Reset(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	queue.mu.Lock()
	defer queue.mu.Unlock()
	if err := os.Remove(queue.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return ErrQueueIO
	}
	queue.messages = nil
	queue.nextID = 0
	return nil
}

var _ interface {
	Enqueue(context.Context, string, []byte) (string, error)
	Receive(context.Context) (ReceivedMessage, error)
	Acknowledge(context.Context, string) error
	Reset(context.Context) error
} = (*FileQueue)(nil)
