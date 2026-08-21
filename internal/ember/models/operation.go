package models

import (
	"errors"
	"strings"
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
	ID         string
	ResourceID string
	Status     OperationStatus
}

var ErrInvalidOperation = errors.New("invalid operation")

func (operation Operation) Validate() error {
	if strings.TrimSpace(operation.ID) == "" ||
		strings.TrimSpace(operation.ResourceID) == "" ||
		!operation.Status.valid() {
		return ErrInvalidOperation
	}
	return nil
}
