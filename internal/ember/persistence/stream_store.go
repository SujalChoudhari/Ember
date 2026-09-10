package persistence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

const (
	streamStoreVersion      = 1
	DefaultStreamCount      = 16
	DefaultStreamPartitions = 8
	DefaultStreamRecords    = 1024
	DefaultStreamBytes      = 8 << 20
	MaxStreamCount          = 256
	MaxStreamPartitions     = models.MaxStreamPartitions
	MaxStreamRecords        = 100_000
	MaxStreamBytes          = 64 << 20
	MaxStreamRetention      = 365 * 24 * time.Hour
	MaxStreamReadLimit      = 100
	MaxStreamStoreFileBytes = 128 << 20
)

type StreamOptions struct {
	MaxStreams             int
	MaxPartitions          int
	MaxRecordsPerPartition int
	MaxBytes               int64
	Retention              time.Duration
}

func (options StreamOptions) normalized() (StreamOptions, error) {
	if options.MaxStreams == 0 {
		options.MaxStreams = DefaultStreamCount
	}
	if options.MaxPartitions == 0 {
		options.MaxPartitions = DefaultStreamPartitions
	}
	if options.MaxRecordsPerPartition == 0 {
		options.MaxRecordsPerPartition = DefaultStreamRecords
	}
	if options.MaxBytes == 0 {
		options.MaxBytes = DefaultStreamBytes
	}
	if options.MaxStreams < 1 || options.MaxStreams > MaxStreamCount ||
		options.MaxPartitions < 1 || options.MaxPartitions > MaxStreamPartitions ||
		options.MaxRecordsPerPartition < 1 || options.MaxRecordsPerPartition > MaxStreamRecords ||
		options.MaxBytes < 1 || options.MaxBytes > MaxStreamBytes ||
		options.Retention < 0 || options.Retention > MaxStreamRetention {
		return StreamOptions{}, ErrInvalidStreamOptions
	}
	return options, nil
}

var (
	ErrInvalidStreamStorePath = errors.New("invalid stream store path")
	ErrInvalidStreamOptions   = errors.New("invalid stream options")
	ErrStreamNotFound         = errors.New("stream not found")
	ErrDuplicateStream        = errors.New("duplicate stream")
	ErrStreamCountExceeded    = errors.New("stream count limit exceeded")
	ErrStreamIDExhausted      = errors.New("stream IDs exhausted")
	ErrInvalidStreamPartition = errors.New("invalid stream partition")
	ErrStreamPartitionFull    = errors.New("stream partition record limit exceeded")
	ErrStreamBytesExceeded    = errors.New("stream byte limit exceeded")
	ErrInvalidStreamOffset    = errors.New("invalid stream offset")
	ErrInvalidStreamReadLimit = errors.New("invalid stream read limit")
	ErrStreamStoreCorrupt     = errors.New("corrupt stream store")
	ErrStreamStoreTooLarge    = errors.New("stream store exceeds size limit")
	ErrStreamStoreIO          = errors.New("stream store I/O failure")
)

type streamDiskPartition struct {
	StreamID   string `json:"stream_id"`
	Partition  int    `json:"partition"`
	NextOffset int64  `json:"next_offset"`
}

type streamStoreDiskState struct {
	Version    int                   `json:"version"`
	NextID     uint64                `json:"next_id"`
	Streams    []models.Stream       `json:"streams"`
	Partitions []streamDiskPartition `json:"partitions"`
	Records    []models.StreamRecord `json:"records"`
}

type streamPartitionState struct {
	nextOffset int64
}

type FileStreamStore struct {
	mu         sync.Mutex
	path       string
	options    StreamOptions
	now        func() time.Time
	nextID     uint64
	streams    map[string]models.Stream
	partitions map[string]streamPartitionState
	records    map[string][]models.StreamRecord
}

func NewFileStreamStore(path string, options StreamOptions) (*FileStreamStore, error) {
	if strings.TrimSpace(path) == "" {
		return nil, ErrInvalidStreamStorePath
	}
	normalized, err := options.normalized()
	if err != nil {
		return nil, err
	}
	path = filepath.Clean(path)
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return nil, ErrInvalidStreamStorePath
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, ErrStreamStoreIO
	}
	store := &FileStreamStore{
		path:       path,
		options:    normalized,
		now:        time.Now,
		streams:    make(map[string]models.Stream),
		partitions: make(map[string]streamPartitionState),
		records:    make(map[string][]models.StreamRecord),
	}
	if err := store.load(); err != nil {
		return nil, err
	}
	return store, nil
}

func streamPartitionKey(streamID string, partition int) string {
	return streamID + "\x00" + strconv.Itoa(partition)
}

func cloneStream(stream models.Stream) models.Stream {
	return stream
}

func cloneStreamRecord(record models.StreamRecord) models.StreamRecord {
	record.Payload = append([]byte(nil), record.Payload...)
	return record
}

func (store *FileStreamStore) load() error {
	data, err := os.ReadFile(store.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return ErrStreamStoreIO
	}
	if len(data) > MaxStreamStoreFileBytes {
		return ErrStreamStoreTooLarge
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state streamStoreDiskState
	if err := decoder.Decode(&state); err != nil {
		return ErrStreamStoreCorrupt
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return ErrStreamStoreCorrupt
	}
	if state.Version != streamStoreVersion || len(state.Streams) > store.options.MaxStreams {
		return ErrStreamStoreCorrupt
	}

	streams := make(map[string]models.Stream, len(state.Streams))
	for _, stream := range state.Streams {
		if err := stream.Validate(); err != nil || stream.PartitionCount > store.options.MaxPartitions {
			return ErrStreamStoreCorrupt
		}
		if _, exists := streams[stream.ID]; exists {
			return ErrStreamStoreCorrupt
		}
		streams[stream.ID] = cloneStream(stream)
	}
	partitions := make(map[string]streamPartitionState, len(state.Partitions))
	for _, partition := range state.Partitions {
		stream, exists := streams[partition.StreamID]
		if !exists || partition.Partition < 0 || partition.Partition >= stream.PartitionCount || partition.NextOffset < 0 {
			return ErrStreamStoreCorrupt
		}
		key := streamPartitionKey(partition.StreamID, partition.Partition)
		if _, exists := partitions[key]; exists {
			return ErrStreamStoreCorrupt
		}
		partitions[key] = streamPartitionState{nextOffset: partition.NextOffset}
	}
	for _, stream := range streams {
		for partition := 0; partition < stream.PartitionCount; partition++ {
			if _, exists := partitions[streamPartitionKey(stream.ID, partition)]; !exists {
				return ErrStreamStoreCorrupt
			}
		}
	}
	records := make(map[string][]models.StreamRecord)
	seen := make(map[string]struct{}, len(state.Records))
	var usedBytes int64
	for _, record := range state.Records {
		stream, exists := streams[record.StreamID]
		if !exists || record.Partition < 0 || record.Partition >= stream.PartitionCount || record.Validate() != nil {
			return ErrStreamStoreCorrupt
		}
		key := streamPartitionKey(record.StreamID, record.Partition)
		recordKey := key + "\x00" + formatOffset(record.Offset)
		if _, exists := seen[recordKey]; exists {
			return ErrStreamStoreCorrupt
		}
		seen[recordKey] = struct{}{}
		partition := partitions[key]
		if record.Offset >= partition.nextOffset {
			return ErrStreamStoreCorrupt
		}
		usedBytes += int64(len(record.Payload))
		if usedBytes > store.options.MaxBytes {
			return ErrStreamStoreCorrupt
		}
		records[key] = append(records[key], cloneStreamRecord(record))
	}
	for key, partitionRecords := range records {
		if len(partitionRecords) > store.options.MaxRecordsPerPartition {
			return ErrStreamStoreCorrupt
		}
		sort.Slice(partitionRecords, func(i, j int) bool { return partitionRecords[i].Offset < partitionRecords[j].Offset })
		for index, record := range partitionRecords {
			if index > 0 && partitionRecords[index-1].Offset+1 != record.Offset {
				return ErrStreamStoreCorrupt
			}
		}
		partition := partitions[key]
		if len(partitionRecords) > 0 && partitionRecords[len(partitionRecords)-1].Offset >= partition.nextOffset {
			return ErrStreamStoreCorrupt
		}
		records[key] = partitionRecords
	}
	store.nextID = state.NextID
	store.streams = streams
	store.partitions = partitions
	store.records = records
	return nil
}

func formatOffset(offset int64) string {
	return strconv.FormatInt(offset, 10)
}

func (store *FileStreamStore) diskStateLocked() streamStoreDiskState {
	streams := make([]models.Stream, 0, len(store.streams))
	for _, stream := range store.streams {
		streams = append(streams, cloneStream(stream))
	}
	sort.Slice(streams, func(i, j int) bool { return streams[i].ID < streams[j].ID })
	partitions := make([]streamDiskPartition, 0, len(store.partitions))
	for key, state := range store.partitions {
		streamID, partition := splitStreamPartitionKey(key)
		partitions = append(partitions, streamDiskPartition{StreamID: streamID, Partition: partition, NextOffset: state.nextOffset})
	}
	sort.Slice(partitions, func(i, j int) bool {
		if partitions[i].StreamID == partitions[j].StreamID {
			return partitions[i].Partition < partitions[j].Partition
		}
		return partitions[i].StreamID < partitions[j].StreamID
	})
	records := make([]models.StreamRecord, 0)
	for _, partitionRecords := range store.records {
		for _, record := range partitionRecords {
			records = append(records, cloneStreamRecord(record))
		}
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].StreamID == records[j].StreamID {
			if records[i].Partition == records[j].Partition {
				return records[i].Offset < records[j].Offset
			}
			return records[i].Partition < records[j].Partition
		}
		return records[i].StreamID < records[j].StreamID
	})
	return streamStoreDiskState{Version: streamStoreVersion, NextID: store.nextID, Streams: streams, Partitions: partitions, Records: records}
}

func splitStreamPartitionKey(key string) (string, int) {
	separator := strings.LastIndexByte(key, 0)
	if separator < 0 {
		return key, -1
	}
	partition, err := strconv.Atoi(key[separator+1:])
	if err != nil {
		return key[:separator], -1
	}
	return key[:separator], partition
}

func (store *FileStreamStore) saveLocked() error {
	data, err := json.MarshalIndent(store.diskStateLocked(), "", "  ")
	if err != nil {
		return ErrStreamStoreIO
	}
	data = append(data, '\n')
	if len(data) > MaxStreamStoreFileBytes {
		return ErrStreamStoreTooLarge
	}
	directory := filepath.Dir(store.path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return ErrStreamStoreIO
	}
	temporary, err := os.CreateTemp(directory, ".ember-streams-*.tmp")
	if err != nil {
		return ErrStreamStoreIO
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return ErrStreamStoreIO
	}
	if written, err := temporary.Write(data); err != nil || written != len(data) {
		_ = temporary.Close()
		return ErrStreamStoreIO
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return ErrStreamStoreIO
	}
	if err := temporary.Close(); err != nil {
		return ErrStreamStoreIO
	}
	if err := os.Rename(temporaryPath, store.path); err != nil {
		return ErrStreamStoreIO
	}
	if directoryFile, err := os.Open(directory); err == nil {
		_ = directoryFile.Sync()
		_ = directoryFile.Close()
	}
	return nil
}

func (store *FileStreamStore) expireLocked(now time.Time) bool {
	if store.options.Retention == 0 {
		return false
	}
	changed := false
	for key, partitionRecords := range store.records {
		kept := partitionRecords[:0]
		for _, record := range partitionRecords {
			if !record.AppendedAt.Add(store.options.Retention).After(now) {
				changed = true
				continue
			}
			kept = append(kept, record)
		}
		if len(kept) == 0 {
			delete(store.records, key)
		} else {
			store.records[key] = kept
		}
	}
	return changed
}

func (store *FileStreamStore) validateStreamPartitionLocked(streamID string, partition int) error {
	stream, exists := store.streams[streamID]
	if !exists {
		return ErrStreamNotFound
	}
	if partition < 0 || partition >= stream.PartitionCount {
		return ErrInvalidStreamPartition
	}
	return nil
}

func (store *FileStreamStore) Create(ctx context.Context, spec models.StreamSpec) (*models.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := spec.Validate(); err != nil || spec.PartitionCount > store.options.MaxPartitions {
		return nil, models.ErrInvalidStreamSpec
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.streams) >= store.options.MaxStreams {
		return nil, ErrStreamCountExceeded
	}
	for _, stream := range store.streams {
		if stream.Name == spec.Name {
			return nil, ErrDuplicateStream
		}
	}
	if store.nextID == ^uint64(0) {
		return nil, ErrStreamIDExhausted
	}
	store.nextID++
	stream := models.Stream{ID: fmt.Sprintf("stream-%08d", store.nextID), Name: spec.Name, PartitionCount: spec.PartitionCount, CreatedAt: store.now().UTC()}
	if err := stream.Validate(); err != nil {
		store.nextID--
		return nil, err
	}
	store.streams[stream.ID] = stream
	for partition := 0; partition < stream.PartitionCount; partition++ {
		store.partitions[streamPartitionKey(stream.ID, partition)] = streamPartitionState{}
	}
	if err := store.saveLocked(); err != nil {
		delete(store.streams, stream.ID)
		for partition := 0; partition < stream.PartitionCount; partition++ {
			delete(store.partitions, streamPartitionKey(stream.ID, partition))
		}
		store.nextID--
		return nil, err
	}
	copy := cloneStream(stream)
	return &copy, nil
}

func (store *FileStreamStore) Get(ctx context.Context, streamID string) (*models.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.expireLocked(store.now().UTC()) {
		if err := store.saveLocked(); err != nil {
			return nil, err
		}
	}
	stream, exists := store.streams[streamID]
	if !exists {
		return nil, ErrStreamNotFound
	}
	copy := cloneStream(stream)
	return &copy, nil
}

func (store *FileStreamStore) List(ctx context.Context, limit int) ([]models.Stream, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 || limit > MaxStreamReadLimit {
		return nil, ErrInvalidStreamReadLimit
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.expireLocked(store.now().UTC()) {
		if err := store.saveLocked(); err != nil {
			return nil, err
		}
	}
	streams := make([]models.Stream, 0, len(store.streams))
	for _, stream := range store.streams {
		streams = append(streams, cloneStream(stream))
	}
	sort.Slice(streams, func(i, j int) bool { return streams[i].ID < streams[j].ID })
	if len(streams) > limit {
		streams = streams[:limit]
	}
	return streams, nil
}

func (store *FileStreamStore) Append(ctx context.Context, streamID string, partition int, payload []byte) (*models.StreamRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(payload) > models.MaxStreamRecordBytes {
		return nil, models.ErrInvalidStreamRecord
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.expireLocked(store.now().UTC()) {
		// The append below persists the cleanup together with the new record.
	}
	if err := store.validateStreamPartitionLocked(streamID, partition); err != nil {
		return nil, err
	}
	key := streamPartitionKey(streamID, partition)
	partitionState := store.partitions[key]
	partitionRecords := store.records[key]
	if len(partitionRecords) >= store.options.MaxRecordsPerPartition {
		return nil, ErrStreamPartitionFull
	}
	var usedBytes int64
	for partitionRecordsKey, records := range store.records {
		_ = partitionRecordsKey
		for _, record := range records {
			usedBytes += int64(len(record.Payload))
		}
	}
	if usedBytes+int64(len(payload)) > store.options.MaxBytes {
		return nil, ErrStreamBytesExceeded
	}
	record := models.StreamRecord{StreamID: streamID, Partition: partition, Offset: partitionState.nextOffset, Payload: append([]byte(nil), payload...), AppendedAt: store.now().UTC()}
	if err := record.Validate(); err != nil {
		return nil, err
	}
	previousOffset := partitionState.nextOffset
	partitionState.nextOffset++
	store.partitions[key] = partitionState
	store.records[key] = append(partitionRecords, record)
	if err := store.saveLocked(); err != nil {
		store.partitions[key] = streamPartitionState{nextOffset: previousOffset}
		store.records[key] = partitionRecords
		return nil, err
	}
	copy := cloneStreamRecord(record)
	return &copy, nil
}

func (store *FileStreamStore) Read(ctx context.Context, streamID string, partition int, fromOffset int64, limit int) ([]models.StreamRecord, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if fromOffset < 0 {
		return nil, ErrInvalidStreamOffset
	}
	if limit <= 0 || limit > MaxStreamReadLimit {
		return nil, ErrInvalidStreamReadLimit
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.validateStreamPartitionLocked(streamID, partition); err != nil {
		return nil, err
	}
	if store.expireLocked(store.now().UTC()) {
		if err := store.saveLocked(); err != nil {
			return nil, err
		}
	}
	result := make([]models.StreamRecord, 0, limit)
	for _, record := range store.records[streamPartitionKey(streamID, partition)] {
		if record.Offset >= fromOffset {
			result = append(result, cloneStreamRecord(record))
			if len(result) == limit {
				break
			}
		}
	}
	return result, nil
}

func (store *FileStreamStore) InspectPartition(ctx context.Context, streamID string, partition int) (*models.StreamPartition, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.validateStreamPartitionLocked(streamID, partition); err != nil {
		return nil, err
	}
	if store.expireLocked(store.now().UTC()) {
		if err := store.saveLocked(); err != nil {
			return nil, err
		}
	}
	key := streamPartitionKey(streamID, partition)
	state := store.partitions[key]
	partitionRecords := store.records[key]
	var bytes int64
	for _, record := range partitionRecords {
		bytes += int64(len(record.Payload))
	}
	return &models.StreamPartition{StreamID: streamID, Partition: partition, NextOffset: state.nextOffset, RecordCount: len(partitionRecords), Bytes: bytes}, nil
}

var _ StreamStore = (*FileStreamStore)(nil)

type StreamStore interface {
	Create(ctx context.Context, spec models.StreamSpec) (*models.Stream, error)
	Get(ctx context.Context, streamID string) (*models.Stream, error)
	List(ctx context.Context, limit int) ([]models.Stream, error)
	Append(ctx context.Context, streamID string, partition int, payload []byte) (*models.StreamRecord, error)
	Read(ctx context.Context, streamID string, partition int, fromOffset int64, limit int) ([]models.StreamRecord, error)
	InspectPartition(ctx context.Context, streamID string, partition int) (*models.StreamPartition, error)
}
