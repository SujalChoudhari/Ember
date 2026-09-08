package events

import (
	"context"

	"github.com/SujalChoudhari/Ember/internal/ember/queue"
)

// InspectDeadLetters exposes the bounded, payload-redacted dead-letter records
// produced by event delivery through the existing queue store.
func InspectDeadLetters(ctx context.Context, store queue.DeadLetterStore, limit int) ([]queue.DeadLetterRecord, error) {
	if store == nil {
		return nil, queue.ErrInvalidDeadLetterStore
	}
	return store.List(ctx, limit)
}
