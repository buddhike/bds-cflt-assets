package server

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"lightninginv/internal/confluentmetrics"
	"lightninginv/internal/lightning"
)

// How long to wait between reads of the table's status.
const (
	// pollActive: the table is ACTIVE and only needs watching.
	pollActive = 30 * time.Second
	// pollSettling: it is being made (or is not there yet): Terraform may have returned
	// before it is ACTIVE, and the UI gates the loads on it.
	pollSettling = 5 * time.Second
	// pollErrorMin and pollErrorMax bound the growing wait after an error.
	pollErrorMin = 10 * time.Second
	pollErrorMax = 5 * time.Minute
	// pollClientErrorMax caps the wait after a 401, 403 or other 4xx. Right after an apply
	// a new key or role binding may not be in effect yet, so such an answer is often
	// transient and must not keep the gate shut for minutes.
	pollClientErrorMax = 60 * time.Second
	// usageEvery is how often Confluent's usage numbers are read; usageErrorMax bounds the
	// growing wait after an error, and usageRateLimited is the wait after a 429.
	usageEvery       = time.Minute
	usageErrorMax    = 10 * time.Minute
	usageRateLimited = 5 * time.Minute
)

// Run keeps the Lightning Table's phase and Confluent's usage numbers fresh, until ctx
// ends. It only reads: Terraform makes the table and removes it.
func (s *Service) Run(ctx context.Context) {
	go s.usageLoop(ctx)
	var errWait time.Duration
	for ctx.Err() == nil {
		wait, failed := s.pollTable(ctx, errWait)
		if failed {
			errWait = wait
		} else {
			errWait = 0
		}
		if !sleepCtx(ctx, wait) {
			return
		}
	}
}

// sleepCtx waits d and reports false if ctx ended first. It stops its timer.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// pollTable reads the table's phase once and says how long to wait before the next read. After
// an error the wait doubles from pollErrorMin to pollErrorMax. A 429 goes straight to the top
// (Confluent is asking for less). A 4xx (a refused key, a missing role) doubles the same way
// but stops at pollClientErrorMax: it is often only a key that has not taken effect yet.
func (s *Service) pollTable(ctx context.Context, prevErrWait time.Duration) (wait time.Duration, failed bool) {
	phase, msg, err := s.o.Tables.Status(ctx)
	if ctx.Err() != nil {
		// Shutting down: the read was cut off. That is not a table problem to log or show.
		return pollSettling, false
	}
	if errors.Is(err, lightning.ErrNotFound) {
		// Terraform has not made it yet (or has just removed it).
		s.setTable("", "", "")
		return pollSettling, false
	}
	if err != nil {
		wait = min(max(prevErrWait*2, pollErrorMin), pollErrorMax)
		var se *lightning.StatusError
		switch {
		case errors.Is(err, lightning.ErrRateLimited):
			wait = pollErrorMax
		case errors.As(err, &se) && se.Client():
			wait = min(wait, pollClientErrorMax)
		}
		s.mu.Lock()
		s.table.err = "could not read the Lightning Table: " + err.Error()
		s.mu.Unlock()
		slog.Warn("Could not read the Lightning Table", "error", err, "retry_in", wait.String())
		return wait, true
	}
	s.mu.Lock()
	changed := s.table.phase != phase
	s.mu.Unlock()
	if changed {
		slog.Info("The Lightning Table changed phase", "phase", string(phase))
	}
	s.setTable(phase, msg, "")
	if phase == lightning.PhaseActive {
		return pollActive, false
	}
	return pollSettling, false
}

func (s *Service) setTable(p lightning.Phase, msg, errText string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.table.phase, s.table.message, s.table.err = p, msg, errText
}

// usageWait is how long to wait before the next read of Confluent's usage numbers.
func usageWait(prev time.Duration, err error) time.Duration {
	switch {
	case err == nil:
		return usageEvery
	case errors.Is(err, confluentmetrics.ErrRateLimited):
		return usageRateLimited
	}
	return min(max(prev*2, usageEvery), usageErrorMax)
}

func (s *Service) usageLoop(ctx context.Context) {
	if s.o.Usage == nil {
		return
	}
	wait := usageEvery
	for ctx.Err() == nil {
		u, err := s.o.Usage.Usage(ctx, s.o.Now())
		if ctx.Err() != nil {
			return // shutting down: a cut-off read is not an error to log or show
		}
		s.mu.Lock()
		if err != nil {
			s.usageErr = err.Error()
		} else {
			s.usage, s.usageErr = u, ""
		}
		s.mu.Unlock()
		wait = usageWait(wait, err)
		if err != nil {
			slog.Warn("Could not read Confluent's metrics for the table", "error", err, "retry_in", wait.String())
		}
		if !sleepCtx(ctx, wait) {
			return
		}
	}
}
