package retry

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"
)

var fast = Policy{Attempts: 4, Initial: time.Millisecond, Max: 2 * time.Millisecond}

func TestDoSucceedsAfterTransientFailures(t *testing.T) {
	calls := 0
	err := Do(context.Background(), fast, func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("not yet")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do() = %v, want nil", err)
	}
	if calls != 3 {
		t.Fatalf("calls = %d, want 3", calls)
	}
}

func TestDoGivesUpAfterAttempts(t *testing.T) {
	sentinel := errors.New("boom")
	calls := 0
	err := Do(context.Background(), fast, func(context.Context) error {
		calls++
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Do() = %v, want wrapped sentinel", err)
	}
	if calls != fast.Attempts {
		t.Fatalf("calls = %d, want %d", calls, fast.Attempts)
	}
}

func TestDoStopsOnPermanentError(t *testing.T) {
	sentinel := errors.New("bad request")
	tests := map[string]func() error{
		"direct":  func() error { return Permanent(sentinel) },
		"wrapped": func() error { return fmt.Errorf("create topic: %w", Permanent(sentinel)) },
		"joined":  func() error { return errors.Join(errors.New("other"), Permanent(sentinel)) },
	}
	for name, mk := range tests {
		t.Run(name, func(t *testing.T) {
			calls := 0
			err := Do(context.Background(), fast, func(context.Context) error {
				calls++
				return mk()
			})
			if err == nil || !errors.Is(err, sentinel) {
				t.Fatalf("Do() = %v, want error wrapping sentinel", err)
			}
			if calls != 1 {
				t.Fatalf("calls = %d, want 1", calls)
			}
		})
	}
}

func TestDoKeepsCallerContextOnPermanentError(t *testing.T) {
	err := Do(context.Background(), fast, func(context.Context) error {
		return fmt.Errorf("create topic: %w", Permanent(errors.New("bad request")))
	})
	if got := err.Error(); got != "create topic: bad request" {
		t.Fatalf("Do() = %q, want caller context preserved", got)
	}
}

func TestDoHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	slow := Policy{Attempts: 10, Initial: time.Hour, Max: time.Hour}
	sentinel := errors.New("fail")
	calls := 0
	err := Do(ctx, slow, func(context.Context) error {
		calls++
		cancel()
		return sentinel
	})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, sentinel) {
		t.Fatalf("Do() = %v, want both context.Canceled and the last error", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestDoBackoffDoublesAndIsCapped(t *testing.T) {
	var waits []time.Duration
	p := Policy{
		Attempts: 5,
		Initial:  time.Millisecond,
		Max:      3 * time.Millisecond,
		OnRetry:  func(_ int, _ error, w time.Duration) { waits = append(waits, w) },
	}
	_ = Do(context.Background(), p, func(context.Context) error { return errors.New("x") })

	// Base delays are 1ms, 2ms, 3ms (capped), 3ms; jitter adds at most 20%.
	bases := []time.Duration{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond, 3 * time.Millisecond}
	if len(waits) != len(bases) {
		t.Fatalf("got %d waits, want %d", len(waits), len(bases))
	}
	for i, base := range bases {
		if waits[i] < base || waits[i] > base+base/5 {
			t.Errorf("wait[%d] = %v, want in [%v, %v]", i, waits[i], base, base+base/5)
		}
	}
}

func TestPolicyNormalised(t *testing.T) {
	p := Policy{Attempts: 0, Initial: -time.Second}.normalised()
	if p.Attempts != 1 || p.Initial != Default.Initial || p.Max != Default.Initial {
		t.Fatalf("normalised() = %+v", p)
	}
}

func TestPermanentNil(t *testing.T) {
	if Permanent(nil) != nil {
		t.Fatal("Permanent(nil) should be nil")
	}
}
