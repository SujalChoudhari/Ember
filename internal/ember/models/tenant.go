package models

import (
	"errors"
	"strings"
	"time"
)

const (
	MaxTenantIDLength          = 64
	MaxTenantDisplayNameLength = 128
)

var (
	ErrInvalidTenantID          = errors.New("invalid tenant ID")
	ErrInvalidTenantDisplayName = errors.New("invalid tenant display name")
)

// Tenant is the platform-owned identity for one isolated tenant database.
type Tenant struct {
	ID          string    `json:"id"`
	DisplayName string    `json:"displayName"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (tenant Tenant) Validate() error {
	if !validTenantID(tenant.ID) {
		return ErrInvalidTenantID
	}
	if displayName := strings.TrimSpace(tenant.DisplayName); displayName == "" || len(displayName) > MaxTenantDisplayNameLength {
		return ErrInvalidTenantDisplayName
	}
	if !tenant.CreatedAt.IsZero() && tenant.CreatedAt.UnixNano() <= 0 {
		return ErrInvalidTenantDisplayName
	}
	return nil
}

func validTenantID(value string) bool {
	if value == "" || len(value) > MaxTenantIDLength || value == "." || value == ".." {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9' && index > 0) ||
			(character == '-' && index > 0) ||
			(character == '_' && index > 0) {
			continue
		}
		return false
	}
	return true
}
