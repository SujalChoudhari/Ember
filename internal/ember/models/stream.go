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

var (
	ErrInvalidStreamSpec   = errors.New("invalid stream spec")
	ErrInvalidStream       = errors.New("invalid stream")
	ErrInvalidStreamRecord = errors.New("invalid stream record")
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

func (record StreamRecord) Validate() error {
	if !validStreamText(record.StreamID, MaxStreamIDLength) ||
		record.Partition < 0 || record.Partition > MaxStreamPartition ||
		record.Offset < 0 || len(record.Payload) > MaxStreamRecordBytes ||
		record.AppendedAt.IsZero() {
		return ErrInvalidStreamRecord
	}
	return nil
}
