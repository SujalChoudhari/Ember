package persistence

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func TestFileStreamStorePersistsPartitionedRecordsAndAppliesRetention(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store, err := NewFileStreamStore(filepath.Join(root, "streams.json"), StreamOptions{
		MaxStreams:             4,
		MaxPartitions:          4,
		MaxRecordsPerPartition: 3,
		MaxBytes:               64,
		Retention:              time.Hour,
	})
	if err != nil {
		t.Fatalf("NewFileStreamStore() error = %v", err)
	}
	start := time.Date(2026, time.January, 1, 12, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return start }

	stream, err := store.Create(ctx, models.StreamSpec{Name: "orders", PartitionCount: 2})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if stream.ID != "stream-00000001" || stream.PartitionCount != 2 {
		t.Fatalf("Create() = %#v, want deterministic stream", stream)
	}
	first, err := store.Append(ctx, stream.ID, 0, []byte("one"))
	if err != nil {
		t.Fatalf("Append(first) error = %v", err)
	}
	second, err := store.Append(ctx, stream.ID, 0, []byte("two"))
	if err != nil {
		t.Fatalf("Append(second) error = %v", err)
	}
	other, err := store.Append(ctx, stream.ID, 1, []byte("other"))
	if err != nil {
		t.Fatalf("Append(other partition) error = %v", err)
	}
	if first.Offset != 0 || second.Offset != 1 || other.Offset != 0 {
		t.Fatalf("offsets = %d, %d, %d; want 0, 1, 0", first.Offset, second.Offset, other.Offset)
	}

	got, err := store.Read(ctx, stream.ID, 0, 0, 10)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	if len(got) != 2 || !bytes.Equal(got[0].Payload, []byte("one")) || !bytes.Equal(got[1].Payload, []byte("two")) {
		t.Fatalf("Read() = %#v, want ordered records", got)
	}
	partition, err := store.InspectPartition(ctx, stream.ID, 0)
	if err != nil {
		t.Fatalf("InspectPartition() error = %v", err)
	}
	if partition.NextOffset != 2 || partition.RecordCount != 2 || partition.Bytes != 6 {
		t.Fatalf("InspectPartition() = %#v, want bounded partition state", partition)
	}

	reopened, err := NewFileStreamStore(filepath.Join(root, "streams.json"), StreamOptions{
		MaxStreams: 4, MaxPartitions: 4, MaxRecordsPerPartition: 3, MaxBytes: 64, Retention: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewFileStreamStore(reopen) error = %v", err)
	}
	reopened.now = func() time.Time { return start.Add(2 * time.Hour) }
	if got, err := reopened.Read(ctx, stream.ID, 0, 0, 10); err != nil || len(got) != 0 {
		t.Fatalf("Read(after retention) = %#v, %v; want empty", got, err)
	}
	partition, err = reopened.InspectPartition(ctx, stream.ID, 0)
	if err != nil {
		t.Fatalf("InspectPartition(after retention) error = %v", err)
	}
	if partition.NextOffset != 2 || partition.RecordCount != 0 {
		t.Fatalf("InspectPartition(after retention) = %#v, want retained offset and cleaned records", partition)
	}

	if _, err := reopened.Read(ctx, stream.ID, 2, 0, 1); !errors.Is(err, ErrInvalidStreamPartition) {
		t.Fatalf("Read(invalid partition) error = %v, want ErrInvalidStreamPartition", err)
	}
	if got, err := reopened.List(ctx, 4); err != nil || !reflect.DeepEqual(got, []models.Stream{*stream}) {
		t.Fatalf("List() = %#v, %v; want stream identity", got, err)
	}
}

func TestFileStreamStoreConsumerGroupProgressReplayAndRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "streams.json")
	store, err := NewFileStreamStore(path, StreamOptions{MaxStreams: 1, MaxPartitions: 1, MaxRecordsPerPartition: 4, MaxBytes: 64})
	if err != nil {
		t.Fatalf("NewFileStreamStore() error = %v", err)
	}
	stream, err := store.Create(ctx, models.StreamSpec{Name: "orders", PartitionCount: 1})
	if err != nil {
		t.Fatalf("Create(stream) error = %v", err)
	}
	for _, payload := range []string{"one", "two", "three"} {
		if _, err := store.Append(ctx, stream.ID, 0, []byte(payload)); err != nil {
			t.Fatalf("Append(%q) error = %v", payload, err)
		}
	}

	group, err := store.CreateConsumerGroup(ctx, models.ConsumerGroupSpec{StreamID: stream.ID, Name: "workers", MemberID: "worker-a"})
	if err != nil {
		t.Fatalf("CreateConsumerGroup() error = %v", err)
	}
	if group.ID != "consumer-group-00000001" || group.MemberID != "worker-a" {
		t.Fatalf("CreateConsumerGroup() = %#v, want deterministic membership", group)
	}
	initial, err := store.ReadConsumerGroup(ctx, group.ID, 0, 2)
	if err != nil {
		t.Fatalf("ReadConsumerGroup(initial) error = %v", err)
	}
	if len(initial) != 2 || string(initial[0].Payload) != "one" || string(initial[1].Payload) != "two" {
		t.Fatalf("ReadConsumerGroup(initial) = %#v, want first two records", initial)
	}
	if err := store.CommitConsumerGroupOffset(ctx, group.ID, 0, 2); err != nil {
		t.Fatalf("CommitConsumerGroupOffset() error = %v", err)
	}
	resumed, err := store.ReadConsumerGroup(ctx, group.ID, 0, 2)
	if err != nil {
		t.Fatalf("ReadConsumerGroup(resumed) error = %v", err)
	}
	if len(resumed) != 1 || string(resumed[0].Payload) != "three" {
		t.Fatalf("ReadConsumerGroup(resumed) = %#v, want remaining record", resumed)
	}
	replayed, err := store.ReplayConsumerGroup(ctx, group.ID, 0, 0, 2)
	if err != nil {
		t.Fatalf("ReplayConsumerGroup() error = %v", err)
	}
	if len(replayed) != 2 || string(replayed[0].Payload) != "one" || string(replayed[1].Payload) != "two" {
		t.Fatalf("ReplayConsumerGroup() = %#v, want first two records", replayed)
	}
	if err := store.RecordConsumerGroupFailure(ctx, group.ID, 0, "handler failed"); err != nil {
		t.Fatalf("RecordConsumerGroupFailure() error = %v", err)
	}
	progress, err := store.InspectConsumerGroup(ctx, group.ID)
	if err != nil {
		t.Fatalf("InspectConsumerGroup() error = %v", err)
	}
	if len(progress) != 1 || progress[0].CommittedOffset != 2 || progress[0].NextOffset != 3 || progress[0].Lag != 1 || progress[0].LastError != "handler failed" {
		t.Fatalf("InspectConsumerGroup() = %#v, want lag and failure state", progress)
	}

	if err := store.CommitConsumerGroupOffset(ctx, group.ID, 0, 4); !errors.Is(err, ErrInvalidConsumerGroupOffset) {
		t.Fatalf("CommitConsumerGroupOffset(overrun) error = %v, want ErrInvalidConsumerGroupOffset", err)
	}
	if err := store.CommitConsumerGroupOffset(ctx, group.ID, 0, 1); !errors.Is(err, ErrConsumerGroupOffsetRewind) {
		t.Fatalf("CommitConsumerGroupOffset(rewind) error = %v, want ErrConsumerGroupOffsetRewind", err)
	}
	if _, err := store.ReadConsumerGroup(ctx, group.ID, 1, 1); !errors.Is(err, ErrInvalidConsumerGroupPartition) {
		t.Fatalf("ReadConsumerGroup(invalid partition) error = %v, want ErrInvalidConsumerGroupPartition", err)
	}
	if err := store.RecordConsumerGroupFailure(ctx, group.ID, 0, " "); !errors.Is(err, ErrInvalidConsumerGroupFailure) {
		t.Fatalf("RecordConsumerGroupFailure(blank) error = %v, want ErrInvalidConsumerGroupFailure", err)
	}

	reopened, err := NewFileStreamStore(path, StreamOptions{MaxStreams: 1, MaxPartitions: 1, MaxRecordsPerPartition: 4, MaxBytes: 64})
	if err != nil {
		t.Fatalf("NewFileStreamStore(reopen) error = %v", err)
	}
	resumed, err = reopened.ReadConsumerGroup(ctx, group.ID, 0, 2)
	if err != nil {
		t.Fatalf("ReadConsumerGroup(reopen) error = %v", err)
	}
	if len(resumed) != 1 || string(resumed[0].Payload) != "three" {
		t.Fatalf("ReadConsumerGroup(reopen) = %#v, want committed resume", resumed)
	}
}

func TestFileStreamStoreEnforcesBoundedInputsAndIsolation(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "streams.json")
	store, err := NewFileStreamStore(path, StreamOptions{
		MaxStreams:             1,
		MaxPartitions:          1,
		MaxRecordsPerPartition: 2,
		MaxBytes:               4,
	})
	if err != nil {
		t.Fatalf("NewFileStreamStore() error = %v", err)
	}
	stream, err := store.Create(ctx, models.StreamSpec{Name: "bounded", PartitionCount: 1})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if _, err := store.Create(ctx, models.StreamSpec{Name: "duplicate", PartitionCount: 1}); !errors.Is(err, ErrStreamCountExceeded) {
		t.Fatalf("Create(over stream count) error = %v, want ErrStreamCountExceeded", err)
	}
	if _, err := store.Append(ctx, stream.ID, 1, []byte("x")); !errors.Is(err, ErrInvalidStreamPartition) {
		t.Fatalf("Append(invalid partition) error = %v, want ErrInvalidStreamPartition", err)
	}
	payload := []byte("four")
	record, err := store.Append(ctx, stream.ID, 0, payload)
	if err != nil {
		t.Fatalf("Append() error = %v", err)
	}
	payload[0] = 'X'
	record.Payload[0] = 'Y'
	read, err := store.Read(ctx, stream.ID, 0, 0, 1)
	if err != nil || len(read) != 1 || !bytes.Equal(read[0].Payload, []byte("four")) {
		t.Fatalf("Read(after caller mutation) = %#v, %v; want isolated payload", read, err)
	}
	if _, err := store.Append(ctx, stream.ID, 0, []byte("x")); !errors.Is(err, ErrStreamBytesExceeded) {
		t.Fatalf("Append(over bytes) error = %v, want ErrStreamBytesExceeded", err)
	}
	if _, err := store.Read(ctx, stream.ID, 0, 0, 0); !errors.Is(err, ErrInvalidStreamReadLimit) {
		t.Fatalf("Read(invalid limit) error = %v, want ErrInvalidStreamReadLimit", err)
	}

	if err := os.WriteFile(path, []byte(`{"version":1,"next_id":1,"streams":[],"partitions":[],"records":[],"unexpected":true}`), 0o600); err != nil {
		t.Fatalf("WriteFile(corrupt state) error = %v", err)
	}
	if _, err := NewFileStreamStore(path, StreamOptions{}); !errors.Is(err, ErrStreamStoreCorrupt) {
		t.Fatalf("NewFileStreamStore(corrupt) error = %v, want ErrStreamStoreCorrupt", err)
	}
}
