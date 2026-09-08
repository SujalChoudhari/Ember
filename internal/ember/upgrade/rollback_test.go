package upgrade

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/SujalChoudhari/Ember/internal/ember/models"
)

func rollbackTestSnapshot(version string) Snapshot {
	return Snapshot{
		Version: version,
		Resources: []models.Resource{{
			ID: "resource-1",
			Spec: models.ResourceSpec{
				Type:         models.ResourceTypeGroup,
				Name:         "platform",
				Tags:         map[string]string{"environment": "test"},
				DesiredState: models.ResourceStateReady,
			},
			ObservedState: models.ResourceStateUnknown,
		}},
	}
}

func TestRollbackRestoresSafeStateAfterInjectedUpgradeFailure(t *testing.T) {
	ctx := context.Background()
	safeState := rollbackTestSnapshot(VersionV1)
	data, err := EncodeSnapshot(safeState)
	if err != nil {
		t.Fatalf("EncodeSnapshot() error = %v", err)
	}

	journal := NewRollbackJournal()
	record, err := journal.Prepare(ctx, "upgrade-request-1", safeState, CurrentVersion)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	upgradeResult, err := Upgrade(ctx, data, CurrentVersion)
	if err != nil {
		t.Fatalf("Upgrade() error = %v", err)
	}
	currentState := upgradeResult.Snapshot
	injected := errors.New("apply failed: secret-value")
	if err := func() error {
		currentState = Snapshot{Version: CurrentVersion}
		return injected
	}(); !errors.Is(err, injected) {
		t.Fatalf("failure injection error = %v, want injected error", err)
	}

	rollback, err := journal.Rollback(ctx, record.RequestID, func(_ context.Context, snapshot Snapshot) error {
		currentState = snapshot
		return nil
	})
	if err != nil {
		t.Fatalf("Rollback() error = %v", err)
	}
	if !reflect.DeepEqual(currentState, safeState) || !reflect.DeepEqual(rollback.Snapshot, safeState) {
		t.Fatalf("rolled-back state = %#v, result = %#v; want safe state %#v", currentState, rollback.Snapshot, safeState)
	}
	if rollback.Record.Status != RollbackStatusSucceeded || rollback.Evidence.Code != RollbackCodeSucceeded {
		t.Fatalf("rollback result = %#v, want successful bounded evidence", rollback)
	}
}

func TestRollbackIsIdempotentAndRetentionIsExplicitlyBounded(t *testing.T) {
	ctx := context.Background()
	safeState := rollbackTestSnapshot(CurrentVersion)
	journal := NewRollbackJournal()
	if _, err := journal.Prepare(ctx, "upgrade-request-1", safeState, CurrentVersion); err != nil {
		t.Fatalf("Prepare(first) error = %v", err)
	}

	calls := 0
	restore := func(_ context.Context, _ Snapshot) error {
		calls++
		return nil
	}
	first, err := journal.Rollback(ctx, "upgrade-request-1", restore)
	if err != nil {
		t.Fatalf("Rollback(first) error = %v", err)
	}
	second, err := journal.Rollback(ctx, "upgrade-request-1", restore)
	if err != nil {
		t.Fatalf("Rollback(repeated) error = %v", err)
	}
	if calls != 1 || !second.Replayed || !reflect.DeepEqual(first.Snapshot, second.Snapshot) {
		t.Fatalf("rollback calls/results = %d, %#v, %#v; want one restore and idempotent result", calls, first, second)
	}

	for index := 2; index <= MaxRollbackRecords; index++ {
		if _, err := journal.Prepare(ctx, fmt.Sprintf("upgrade-request-%02d", index), safeState, CurrentVersion); err != nil {
			t.Fatalf("Prepare(%d) error = %v", index, err)
		}
	}
	if _, err := journal.Prepare(ctx, "upgrade-request-overflow", safeState, CurrentVersion); !errors.Is(err, ErrRollbackStoreFull) {
		t.Fatalf("Prepare(overflow) error = %v, want ErrRollbackStoreFull", err)
	}
	if err := journal.Discard(ctx, "upgrade-request-1"); err != nil {
		t.Fatalf("Discard() error = %v", err)
	}
	if _, err := journal.Prepare(ctx, "upgrade-request-reused", safeState, CurrentVersion); err != nil {
		t.Fatalf("Prepare(after explicit discard) error = %v", err)
	}
}

func TestRollbackBoundsRetriesAndRedactsExecutorErrors(t *testing.T) {
	ctx := context.Background()
	journal := NewRollbackJournal()
	if _, err := journal.Prepare(ctx, "upgrade-request-1", rollbackTestSnapshot(CurrentVersion), CurrentVersion); err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	calls := 0
	secretError := errors.New("provider token secret-value")
	for attempt := 1; attempt <= MaxRollbackAttempts; attempt++ {
		result, err := journal.Rollback(ctx, "upgrade-request-1", func(_ context.Context, _ Snapshot) error {
			calls++
			return secretError
		})
		if attempt < MaxRollbackAttempts && !errors.Is(err, ErrRollbackExecution) {
			t.Fatalf("Rollback(attempt %d) error = %v, want ErrRollbackExecution", attempt, err)
		}
		if attempt == MaxRollbackAttempts && !errors.Is(err, ErrRollbackAttemptsExceeded) {
			t.Fatalf("Rollback(attempt %d) error = %v, want ErrRollbackAttemptsExceeded", attempt, err)
		}
		if strings.Contains(result.Evidence.String(), "secret-value") {
			t.Fatalf("rollback evidence leaked executor error: %q", result.Evidence.String())
		}
	}
	if calls != MaxRollbackAttempts {
		t.Fatalf("restore calls = %d, want bounded %d", calls, MaxRollbackAttempts)
	}
	if _, err := journal.Rollback(ctx, "upgrade-request-1", func(_ context.Context, _ Snapshot) error {
		t.Fatalf("restore callback called after retry bound")
		return nil
	}); !errors.Is(err, ErrRollbackAttemptsExceeded) {
		t.Fatalf("Rollback(after bound) error = %v, want ErrRollbackAttemptsExceeded", err)
	}
}
