package ember

import (
	"errors"
	"time"
)

// RetryKnownSafe retries only operations whose caller has proved that replay is safe.
// Unknown provider outcomes are returned as ErrRecovery; success is never guessed.
func RetryKnownSafe(attempt func() error) error {
	backoff := []int{100, 500, 2000}
	for i := 0; i < len(backoff); i++ {
		err := attempt()
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrProvider) {
			return err
		}
		if i < len(backoff)-1 {
			time.Sleep(time.Duration(backoff[i]) * time.Millisecond)
		}
	}
	return ErrRecovery
}
