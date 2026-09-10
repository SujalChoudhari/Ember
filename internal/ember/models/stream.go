package models

import (
	"errors"
	"strings"
	"time"
)

const (
	MaxStreamIDLength     = MaxResourceIDLength
	MaxStreamNameLength   = MaxResourceNameLength
	MaxStreamPartitions   = 64
	MaxStreamRecordBytes  = 1 << 20
	MaxStreamPayloadBytes = MaxStreamRecordBytes
	MaxStreamPartition    = MaxStreamPartitions - 1
)

type StreamSpec struct {
	Name           string
	PartitionCount int
}

type Stream struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	PartitionCount int       `json:"partition_count"`
	CreatedAt      time.Time `json:"created_at"`
}

type StreamRecord struct {
	StreamID   string    `json:"stream_id"`
	Partition  int       `json:"partition"`
	Offset     int64     `json:"offset"`
	Payload    []byte    `json:"payload"`
	AppendedAt time.Time `json:"appended_at"`
}

type StreamPartition struct {
	StreamID    string `json:"stream_id"`
	Partition   int    `json:"partition"`
	NextOffset  int64  `json:"next_offset"`
	RecordCount int    `json:"record_count"`
	Bytes       int64  `json:"bytes"`
}

type ConsumerGroupSpec struct {
	StreamID string `json:"stream_id"`
	Name     string `json:"name"`
	MemberID string `json:"member_id"`
}

type ConsumerGroup struct {
	ID        string    `json:"id"`
	StreamID  string    `json:"stream_id"`
	Name      string    `json:"name"`
	MemberID  string    `json:"member_id"`
	CreatedAt time.Time `json:"created_at"`
}

type ConsumerGroupPartition struct {
	GroupID         string    `json:"group_id"`
	StreamID        string    `json:"stream_id"`
	Partition       int       `json:"partition"`
	CommittedOffset int64     `json:"committed_offset"`
	NextOffset      int64     `json:"next_offset"`
	Lag             int64     `json:"lag"`
	LastError       string    `json:"last_error,omitempty"`
	UpdatedAt       time.Time `json:"updated_at"`
}

var (
	ErrInvalidStreamSpec         = errors.New("invalid stream spec")
	ErrInvalidStream             = errors.New("invalid stream")
	ErrInvalidStreamRecord       = errors.New("invalid stream record")
	ErrInvalidConsumerGroupSpec  = errors.New("invalid consumer group spec")
	ErrInvalidConsumerGroup      = errors.New("invalid consumer group")
	ErrInvalidConsumerGroupState = errors.New("invalid consumer group state")
)

func validStreamText(value string, maxLength int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maxLength && !strings.ContainsRune(value, 0)
}

func (spec StreamSpec) Validate() error {
	if !validStreamText(spec.Name, MaxStreamNameLength) ||
		spec.PartitionCount < 1 || spec.PartitionCount > MaxStreamPartitions {
		return ErrInvalidStreamSpec
	}
	return nil
}

func (stream Stream) Validate() error {
	if !validStreamText(stream.ID, MaxStreamIDLength) ||
		!validStreamText(stream.Name, MaxStreamNameLength) ||
		stream.PartitionCount < 1 || stream.PartitionCount > MaxStreamPartitions ||
		stream.CreatedAt.IsZero() {
		return ErrInvalidStream
	}
	return nil
}

func (spec ConsumerGroupSpec) Validate() error {
	if !validStreamText(spec.StreamID, MaxStreamIDLength) ||
		!validStreamText(spec.Name, MaxStreamNameLength) ||
		!validStreamText(spec.MemberID, MaxStreamIDLength) {
		return ErrInvalidConsumerGroupSpec
	}
	return nil
}

func (group ConsumerGroup) Validate() error {
	if !validStreamText(group.ID, MaxStreamIDLength) ||
		!validStreamText(group.StreamID, MaxStreamIDLength) ||
		!validStreamText(group.Name, MaxStreamNameLength) ||
		!validStreamText(group.MemberID, MaxStreamIDLength) ||
		group.CreatedAt.IsZero() {
		return ErrInvalidConsumerGroup
	}
	return nil
}

func (progress ConsumerGroupPartition) Validate() error {
	if !validStreamText(progress.GroupID, MaxStreamIDLength) ||
		!validStreamText(progress.StreamID, MaxStreamIDLength) ||
		progress.Partition < 0 || progress.Partition > MaxStreamPartition ||
		progress.CommittedOffset < 0 || progress.NextOffset < progress.CommittedOffset ||
		progress.Lag != progress.NextOffset-progress.CommittedOffset ||
		!validOptionalBoundedText(progress.LastError, MaxWorkloadReasonLength) ||
		progress.UpdatedAt.IsZero() {
		return ErrInvalidConsumerGroupState
	}
	return nil
}

func (record StreamRecord) Validate() error {
	if !validStreamText(record.StreamID, MaxStreamIDLength) ||
		record.Partition < 0 || record.Partition > MaxStreamPartition ||
		record.Offset < 0 || len(record.Payload) > MaxStreamRecordBytes ||
		record.AppendedAt.IsZero() {
		return ErrInvalidStreamRecord
	}
	return nil
}
