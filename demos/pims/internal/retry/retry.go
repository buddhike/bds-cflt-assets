// Package retry provides a small exponential-backoff helper shared by all
// PIMS clients (Terraform, Docker, Kafka Connect, MySQL and DuckDB).
package retry

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"time"
)

// Policy controls how often and how long an operation is retried.
type Policy struct {
	// Attempts is the total number of tries, including the first.
	Attempts int
	// Initial is the delay before the second attempt.
	Initial time.Duration
	// Max caps the delay between attempts.
	Max time.Duration
}

// Default suits calls to local services that are still starting up.
var Default = Policy{Attempts: 30, Initial: 500 * time.Millisecond, Max: 10 * time.Second}

type permanentError struct{ err error }

func (p permanentError) Error() string { return p.err.Error() }
func (p permanentError) Unwrap() error { return p.err }

// Permanent marks an error as non-retryable; Do returns it immediately.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return permanentError{err: err}
}

// IsPermanent reports whether err was marked with Permanent.
func IsPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}

// Do calls fn until it succeeds, returns a Permanent error, the attempts
// are exhausted, or ctx is cancelled. The delay doubles after each failed
// attempt, with up to 20% jitter so parallel callers do not synchronise.
func Do(ctx context.Context, p Policy, fn func(ctx context.Context) error) error {
	if p.Attempts < 1 {
		p.Attempts = 1
	}
	delay := p.Initial
	var err error
	for attempt := 1; ; attempt++ {
		if err = fn(ctx); err == nil {
			return nil
		}
		if IsPermanent(err) {
			return errors.Unwrap(err)
		}
		if attempt >= p.Attempts {
			return fmt.Errorf("gave up after %d attempts: %w", attempt, err)
		}

		wait := delay + time.Duration(rand.Int64N(int64(delay)/5+1))
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return fmt.Errorf("%w (last error: %v)", ctx.Err(), err)
		case <-t.C:
		}
		delay = min(delay*2, p.Max)
	}
}
