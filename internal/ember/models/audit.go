package models

import (
	"errors"
	"strings"
	"time"
)

// AuditEntry is the bounded, attribution-only record for a state-changing
// request. It deliberately contains no arbitrary details or payload fields.
type AuditEntry struct {
	ID            string
	OperationID   string
	ResourceID    string
	ScopeID       string
	CorrelationID string
	RequestID     string
	Action        string
	Outcome       string
	CreatedAt     time.Time
}

const (
	MaxAuditEntryIDLength       = 128
	MaxAuditOperationIDLength   = 128
	MaxAuditResourceIDLength    = 128
	MaxAuditScopeIDLength       = 128
	MaxAuditCorrelationIDLength = 128
	MaxAuditRequestIDLength     = 128
	MaxAuditActionLength        = 64
	MaxAuditOutcomeLength       = 128
)

var ErrInvalidAuditEntry = errors.New("invalid audit entry")

func (entry AuditEntry) Validate() error {
	if !validAuditText(entry.ID, MaxAuditEntryIDLength) ||
		!validAuditText(entry.OperationID, MaxAuditOperationIDLength) ||
		!validAuditText(entry.ResourceID, MaxAuditResourceIDLength) ||
		!validOptionalAuditText(entry.ScopeID, MaxAuditScopeIDLength) ||
		!validAuditText(entry.CorrelationID, MaxAuditCorrelationIDLength) ||
		!validAuditText(entry.RequestID, MaxAuditRequestIDLength) ||
		!validAuditText(entry.Action, MaxAuditActionLength) ||
		!validAuditText(entry.Outcome, MaxAuditOutcomeLength) ||
		entry.CreatedAt.IsZero() {
		return ErrInvalidAuditEntry
	}
	return nil
}

func validAuditText(value string, maxLength int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maxLength
}

func validOptionalAuditText(value string, maxLength int) bool {
	return value == "" || validAuditText(value, maxLength)
}
