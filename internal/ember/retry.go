package ember

import (
	"errors"
	"time"
)

// RetryKnownSafe retries only operations whose caller has proved that replay is safe.
// Unknown provider outcomes are returned as ErrRecovery; success is never guessed.
func RetryKnownSafe(attemptOperation func() error) error {
	backoffMilliseconds := []int{100, 500, 2000}
	for attemptIndex := 0; attemptIndex < len(backoffMilliseconds); attemptIndex++ {
		err := attemptOperation()
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrProvider) {
			return err
		}
		if attemptIndex < len(backoffMilliseconds)-1 {
			time.Sleep(time.Duration(backoffMilliseconds[attemptIndex]) * time.Millisecond)
		}
	}
	return ErrRecovery
}
