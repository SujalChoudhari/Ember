package models

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func validAuditEntry() AuditEntry {
	return AuditEntry{
		ID:            "audit-1",
		OperationID:   "operation-1",
		ResourceID:    "resource-1",
		CorrelationID: "correlation-1",
		RequestID:     "request-1",
		Action:        "resource.update",
		Outcome:       "succeeded",
		CreatedAt:     time.Unix(100, 0).UTC(),
	}
}

func TestAuditEntryValidatesBoundedAttribution(t *testing.T) {
	tests := []struct {
		name  string
		entry AuditEntry
	}{
		{
			name:  "root scope",
			entry: validAuditEntry(),
		},
		{
			name: "nested scope",
			entry: func() AuditEntry {
				entry := validAuditEntry()
				entry.ScopeID = "resource-parent"
				return entry
			}(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.entry.Validate(); err != nil {
				t.Fatalf("Validate() error = %v, want nil", err)
			}
		})
	}
}

func TestAuditEntryRejectsMissingRequiredAttribution(t *testing.T) {
	base := validAuditEntry()
	tests := []struct {
		name  string
		entry AuditEntry
	}{
		{name: "id", entry: func() AuditEntry { entry := base; entry.ID = ""; return entry }()},
		{name: "operation id", entry: func() AuditEntry { entry := base; entry.OperationID = " "; return entry }()},
		{name: "resource id", entry: func() AuditEntry { entry := base; entry.ResourceID = ""; return entry }()},
		{name: "correlation id", entry: func() AuditEntry { entry := base; entry.CorrelationID = "\t"; return entry }()},
		{name: "request id", entry: func() AuditEntry { entry := base; entry.RequestID = ""; return entry }()},
		{name: "action", entry: func() AuditEntry { entry := base; entry.Action = " "; return entry }()},
		{name: "outcome", entry: func() AuditEntry { entry := base; entry.Outcome = "\t"; return entry }()},
		{name: "scope", entry: func() AuditEntry { entry := base; entry.ScopeID = " \t"; return entry }()},
		{name: "timestamp", entry: func() AuditEntry { entry := base; entry.CreatedAt = time.Time{}; return entry }()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.entry.Validate(); !errors.Is(err, ErrInvalidAuditEntry) {
				t.Fatalf("Validate() error = %v, want ErrInvalidAuditEntry", err)
			}
		})
	}
}

func TestAuditEntryRejectsUnboundedMetadata(t *testing.T) {
	base := validAuditEntry()
	boundary := base
	boundary.ID = strings.Repeat("i", MaxAuditEntryIDLength)
	boundary.OperationID = strings.Repeat("o", MaxAuditOperationIDLength)
	boundary.ResourceID = strings.Repeat("r", MaxAuditResourceIDLength)
	boundary.ScopeID = strings.Repeat("s", MaxAuditScopeIDLength)
	boundary.CorrelationID = strings.Repeat("c", MaxAuditCorrelationIDLength)
	boundary.RequestID = strings.Repeat("q", MaxAuditRequestIDLength)
	boundary.Action = strings.Repeat("a", MaxAuditActionLength)
	boundary.Outcome = strings.Repeat("u", MaxAuditOutcomeLength)
	if err := boundary.Validate(); err != nil {
		t.Fatalf("boundary Validate() error = %v, want nil", err)
	}

	tests := []struct {
		name  string
		entry AuditEntry
	}{
		{name: "id", entry: func() AuditEntry {
			entry := base
			entry.ID = strings.Repeat("i", MaxAuditEntryIDLength+1)
			return entry
		}()},
		{name: "operation id", entry: func() AuditEntry {
			entry := base
			entry.OperationID = strings.Repeat("o", MaxAuditOperationIDLength+1)
			return entry
		}()},
		{name: "resource id", entry: func() AuditEntry {
			entry := base
			entry.ResourceID = strings.Repeat("r", MaxAuditResourceIDLength+1)
			return entry
		}()},
		{name: "scope", entry: func() AuditEntry {
			entry := base
			entry.ScopeID = strings.Repeat("s", MaxAuditScopeIDLength+1)
			return entry
		}()},
		{name: "correlation id", entry: func() AuditEntry {
			entry := base
			entry.CorrelationID = strings.Repeat("c", MaxAuditCorrelationIDLength+1)
			return entry
		}()},
		{name: "request id", entry: func() AuditEntry {
			entry := base
			entry.RequestID = strings.Repeat("q", MaxAuditRequestIDLength+1)
			return entry
		}()},
		{name: "action", entry: func() AuditEntry {
			entry := base
			entry.Action = strings.Repeat("a", MaxAuditActionLength+1)
			return entry
		}()},
		{name: "outcome", entry: func() AuditEntry {
			entry := base
			entry.Outcome = strings.Repeat("u", MaxAuditOutcomeLength+1)
			return entry
		}()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.entry.Validate(); !errors.Is(err, ErrInvalidAuditEntry) {
				t.Fatalf("Validate() error = %v, want ErrInvalidAuditEntry", err)
			}
		})
	}
}
