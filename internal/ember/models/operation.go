package models

import (
	"errors"
	"strings"
	"time"
)

type OperationStatus string

const (
	OperationStatusPending   OperationStatus = "pending"
	OperationStatusRunning   OperationStatus = "running"
	OperationStatusSucceeded OperationStatus = "succeeded"
	OperationStatusFailed    OperationStatus = "failed"
)

func (status OperationStatus) valid() bool {
	switch status {
	case OperationStatusPending, OperationStatusRunning, OperationStatusSucceeded, OperationStatusFailed:
		return true
	default:
		return false
	}
}

type Operation struct {
	ID            string
	ResourceID    string
	CorrelationID string
	RequestID     string
	Status        OperationStatus
	CreatedAt     time.Time
	UpdatedAt     time.Time
	Outcome       string
}

const (
	MaxOperationIDLength            = 128
	MaxOperationResourceIDLength    = 128
	MaxOperationCorrelationIDLength = 128
	MaxOperationRequestIDLength     = 128
	MaxOperationOutcomeLength       = 256
)

var ErrInvalidOperation = errors.New("invalid operation")

func (operation Operation) Validate() error {
	if !validOperationText(operation.ID, MaxOperationIDLength) ||
		!validOperationText(operation.ResourceID, MaxOperationResourceIDLength) ||
		!validOperationText(operation.CorrelationID, MaxOperationCorrelationIDLength) ||
		!validOperationText(operation.RequestID, MaxOperationRequestIDLength) ||
		operation.CreatedAt.IsZero() ||
		operation.UpdatedAt.IsZero() ||
		len(operation.Outcome) > MaxOperationOutcomeLength ||
		operation.UpdatedAt.Before(operation.CreatedAt) ||
		!operation.Status.valid() {
		return ErrInvalidOperation
	}
	switch operation.Status {
	case OperationStatusPending, OperationStatusRunning:
		if operation.Outcome != "" {
			return ErrInvalidOperation
		}
	case OperationStatusSucceeded, OperationStatusFailed:
		if strings.TrimSpace(operation.Outcome) == "" {
			return ErrInvalidOperation
		}
	}
	return nil
}

func validOperationText(value string, maxLength int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= maxLength
}
