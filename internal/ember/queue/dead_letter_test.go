package queue

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestDeliverWithDeadLetterPersistsExhaustedDeliveryAndSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dead-letters.json")
	options := DeadLetterStoreOptions{MaxRecords: 3}
	store, err := NewFileDeadLetterStore(path, options)
	if err != nil {
		t.Fatalf("NewFileDeadLetterStore() error = %v", err)
	}

	delivery := Delivery{ID: "message-1", CorrelationID: "correlation-1", Payload: []byte("synthetic-secret-payload")}
	outcome, err := DeliverWithDeadLetter(
		context.Background(), delivery, RetryPolicy{MaxRetries: 1},
		func(context.Context, Delivery) error { return errors.New("consumer secret detail") },
		func(context.Context, time.Duration) error { return nil }, store,
	)
	if !errors.Is(err, ErrDeliveryFailed) {
		t.Fatalf("DeliverWithDeadLetter() error = %v, want ErrDeliveryFailed", err)
	}
	if outcome.Status != DeliveryStatusFailed || outcome.Attempts != 2 {
		t.Fatalf("DeliverWithDeadLetter() outcome = %#v, want bounded failure", outcome)
	}

	records, err := store.List(context.Background(), 3)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("List() length = %d, want one transferred message", len(records))
	}
	digest := sha256.Sum256(delivery.Payload)
	want := DeadLetterRecord{
		DeliveryID:    delivery.ID,
		CorrelationID: delivery.CorrelationID,
		Attempts:      outcome.Attempts,
		Reason:        DeliveryFailureReason,
		PayloadBytes:  len(delivery.Payload),
		PayloadSHA256: hex.EncodeToString(digest[:]),
	}
	got := records[0]
	if got.DeliveryID != want.DeliveryID || got.CorrelationID != want.CorrelationID ||
		got.Attempts != want.Attempts || got.Reason != want.Reason ||
		got.PayloadBytes != want.PayloadBytes || got.PayloadSHA256 != want.PayloadSHA256 || got.RecordedAt.IsZero() {
		t.Fatalf("List() record = %#v, want bounded failure metadata %#v", got, want)
	}

	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if bytes.Contains(persisted, delivery.Payload) || bytes.Contains(persisted, []byte("consumer secret detail")) {
		t.Fatalf("dead-letter snapshot contains payload or consumer error detail")
	}

	if _, err := DeliverWithDeadLetter(
		context.Background(), delivery, RetryPolicy{MaxRetries: 1},
		func(context.Context, Delivery) error { return errors.New("consumer secret detail") },
		func(context.Context, time.Duration) error { return nil }, store,
	); !errors.Is(err, ErrDeliveryFailed) {
		t.Fatalf("repeat DeliverWithDeadLetter() error = %v, want ErrDeliveryFailed", err)
	}
	records, err = store.List(context.Background(), 3)
	if err != nil {
		t.Fatalf("List() after repeat error = %v", err)
	}
	if len(records) != 1 {
		t.Fatalf("List() after repeat length = %d, want one record", len(records))
	}

	restarted, err := NewFileDeadLetterStore(path, options)
	if err != nil {
		t.Fatalf("NewFileDeadLetterStore(restart) error = %v", err)
	}
	restartedRecords, err := restarted.List(context.Background(), 3)
	if err != nil {
		t.Fatalf("List(restart) error = %v", err)
	}
	if !reflect.DeepEqual(restartedRecords, records) {
		t.Fatalf("List(restart) = %#v, want %#v", restartedRecords, records)
	}
}

func TestFileDeadLetterStoreEnforcesConfiguredBounds(t *testing.T) {
	store, err := NewFileDeadLetterStore(filepath.Join(t.TempDir(), "dead-letters.json"), DeadLetterStoreOptions{MaxRecords: 1})
	if err != nil {
		t.Fatalf("NewFileDeadLetterStore() error = %v", err)
	}
	first := Delivery{ID: "message-1", CorrelationID: "correlation-1", Payload: []byte("one")}
	outcome := DeliveryOutcome{
		DeliveryID:    first.ID,
		CorrelationID: first.CorrelationID,
		Attempts:      1,
		Status:        DeliveryStatusFailed,
		Reason:        DeliveryFailureReason,
	}
	if err := store.Record(context.Background(), first, outcome); err != nil {
		t.Fatalf("Record(first) error = %v", err)
	}
	second := Delivery{ID: "message-2", CorrelationID: "correlation-2", Payload: []byte("two")}
	secondOutcome := outcome
	secondOutcome.DeliveryID = second.ID
	secondOutcome.CorrelationID = second.CorrelationID
	if err := store.Record(context.Background(), second, secondOutcome); !errors.Is(err, ErrDeadLetterFull) {
		t.Fatalf("Record(second) error = %v, want ErrDeadLetterFull", err)
	}
	if _, err := store.List(context.Background(), 0); !errors.Is(err, ErrDeadLetterListLimit) {
		t.Fatalf("List(zero) error = %v, want ErrDeadLetterListLimit", err)
	}
	invalid := outcome
	invalid.Status = DeliveryStatusSucceeded
	if err := store.Record(context.Background(), second, invalid); !errors.Is(err, ErrInvalidDeadLetterRecord) {
		t.Fatalf("Record(success) error = %v, want ErrInvalidDeadLetterRecord", err)
	}
}

func TestFileDeadLetterStoreRejectsConfiguredSnapshotOverflow(t *testing.T) {
	store, err := NewFileDeadLetterStore(filepath.Join(t.TempDir(), "dead-letters.json"), DeadLetterStoreOptions{
		MaxRecords:  1,
		MaxFileSize: 64,
	})
	if err != nil {
		t.Fatalf("NewFileDeadLetterStore() error = %v", err)
	}
	delivery := Delivery{ID: "message-1", CorrelationID: "correlation-1", Payload: []byte("payload")}
	outcome := DeliveryOutcome{
		DeliveryID:    delivery.ID,
		CorrelationID: delivery.CorrelationID,
		Attempts:      1,
		Status:        DeliveryStatusFailed,
		Reason:        DeliveryFailureReason,
	}
	if err := store.Record(context.Background(), delivery, outcome); !errors.Is(err, ErrDeadLetterStoreTooLarge) {
		t.Fatalf("Record() error = %v, want ErrDeadLetterStoreTooLarge", err)
	}
}

func TestFileDeadLetterStoreListsOnlyOwnedRecords(t *testing.T) {
	store, err := NewFileDeadLetterStore(filepath.Join(t.TempDir(), "dead-letters.json"), DeadLetterStoreOptions{MaxRecords: 3})
	if err != nil {
		t.Fatalf("NewFileDeadLetterStore() error = %v", err)
	}
	for _, owned := range []struct{ tenant, scope, id string }{
		{"tenant-a", "scope-a", "message-a"},
		{"tenant-b", "scope-b", "message-b"},
	} {
		delivery := Delivery{ID: owned.id, CorrelationID: "correlation-" + owned.id, TenantID: owned.tenant, ScopeID: owned.scope, Payload: []byte("payload")}
		if err := store.Record(context.Background(), delivery, DeliveryOutcome{DeliveryID: delivery.ID, CorrelationID: delivery.CorrelationID, Attempts: 1, Status: DeliveryStatusFailed, Reason: DeliveryFailureReason}); err != nil {
			t.Fatalf("Record(%s) error = %v", owned.id, err)
		}
	}
	records, err := store.ListOwned(context.Background(), "tenant-a", "scope-a", 3)
	if err != nil {
		t.Fatalf("ListOwned() error = %v", err)
	}
	if len(records) != 1 || records[0].DeliveryID != "message-a" {
		t.Fatalf("ListOwned() = %#v, want only tenant-a/scope-a record", records)
	}
}
