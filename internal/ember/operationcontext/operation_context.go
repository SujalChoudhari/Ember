package operationcontext

import (
	"context"
	"errors"
	"strings"
)

const MaxIdentifierLength = 128

var ErrInvalidIdentity = errors.New("invalid operation identity")

// Identity is the bounded attribution carried through one state-changing
// request and its provider effects. It contains identifiers only, never
// payloads, credentials, or provider error details.
type Identity struct {
	OperationID   string
	RequestID     string
	CorrelationID string
}

func (identity Identity) Validate() error {
	if !validIdentifier(identity.OperationID) ||
		!validIdentifier(identity.RequestID) ||
		!validIdentifier(identity.CorrelationID) {
		return ErrInvalidIdentity
	}
	return nil
}

func validIdentifier(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= MaxIdentifierLength
}

type contextKey struct{}

func With(ctx context.Context, identity Identity) (context.Context, error) {
	if ctx == nil {
		return nil, ErrInvalidIdentity
	}
	if err := identity.Validate(); err != nil {
		return nil, err
	}
	return context.WithValue(ctx, contextKey{}, identity), nil
}

func From(ctx context.Context) (Identity, bool) {
	if ctx == nil {
		return Identity{}, false
	}
	identity, ok := ctx.Value(contextKey{}).(Identity)
	return identity, ok
}
