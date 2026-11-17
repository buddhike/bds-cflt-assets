package retry

import (
	"context"
	"errors"
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
	calls := 0
	err := Do(context.Background(), fast, func(context.Context) error {
		calls++
		return Permanent(sentinel)
	})
	if !errors.Is(err, sentinel) || IsPermanent(err) {
		t.Fatalf("Do() = %v, want unwrapped sentinel", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestDoHonoursCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	slow := Policy{Attempts: 10, Initial: time.Hour, Max: time.Hour}
	calls := 0
	err := Do(ctx, slow, func(context.Context) error {
		calls++
		cancel()
		return errors.New("fail")
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Do() = %v, want context.Canceled", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
}

func TestPermanentNil(t *testing.T) {
	if Permanent(nil) != nil {
		t.Fatal("Permanent(nil) should be nil")
	}
}
