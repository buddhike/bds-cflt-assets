package main

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"pims/internal/config"
	"pims/internal/latency"
	"pims/internal/pipeline"
)

func TestRingKeepsMostRecent(t *testing.T) {
	r := newRing[int](3)
	for i := range 5 {
		r.push(i)
	}
	if got := r.items(); !slices.Equal(got, []int{2, 3, 4}) {
		t.Fatalf("items() = %v, want [2 3 4]", got)
	}
	empty := newRing[int](2)
	if len(empty.items()) != 0 {
		t.Fatal("new ring should be empty")
	}
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	a := NewApp(config.Workspace{Root: t.TempDir()})
	a.attachLogger(slog.New(slog.DiscardHandler))
	return a
}

func TestSetCredentialsKeepsSecretInMemoryOnly(t *testing.T) {
	a := newTestApp(t)
	if err := a.SetCredentials(config.Credentials{AWSRegion: " EU-WEST-1 ", APIKey: "key", APISecret: "secret"}); err != nil {
		t.Fatal(err)
	}
	st := a.GetState()
	if !st.HasCredentials || st.AWSRegion != "eu-west-1" {
		t.Fatalf("state = %+v", st)
	}
	saved, err := a.ws.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if saved.AWSRegion != "eu-west-1" {
		t.Fatalf("saved region = %q", saved.AWSRegion)
	}
	if got := a.GetDefaults().AWSRegion; got != "eu-west-1" {
		t.Fatalf("defaults region = %q", got)
	}
}

func TestSetupRequiresCredentials(t *testing.T) {
	a := newTestApp(t)
	a.ctx = context.Background() // as if Wails had called startup
	if err := a.Setup(); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("Setup() = %v, want ErrNoCredentials", err)
	}
	if err := a.SetCredentials(config.Credentials{AWSRegion: "nope"}); !errors.Is(err, config.ErrInvalidCredentials) {
		t.Fatalf("SetCredentials(bad) = %v, want ErrInvalidCredentials", err)
	}
}

// fakeDeps returns pipeline deps whose CheckDocker blocks until release
// is closed and then fails, so tests control the background operation.
func fakeDeps(release <-chan struct{}) pipeline.Deps {
	return pipeline.Deps{
		CheckDocker: func(ctx context.Context) error {
			select {
			case <-release:
				return errors.New("docker daemon is not running")
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		Destroy:   func(context.Context, config.Credentials) error { return nil },
		StopLocal: func(context.Context, bool) error { return nil },
	}
}

type emitted struct {
	mu     sync.Mutex
	events map[string][]any
}

func captureEvents(t *testing.T) *emitted {
	t.Helper()
	e := &emitted{events: map[string][]any{}}
	prev := emitter
	emitter = func(_ context.Context, name string, data ...any) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.events[name] = append(e.events[name], data...)
	}
	t.Cleanup(func() { emitter = prev })
	return e
}

func (e *emitted) wait(t *testing.T, name string) any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		e.mu.Lock()
		if v := e.events[name]; len(v) > 0 {
			e.mu.Unlock()
			return v[0]
		}
		e.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no %s event", name)
	return nil
}

func TestBackgroundSetupBusyAndError(t *testing.T) {
	ev := captureEvents(t)
	release := make(chan struct{})
	a := NewApp(config.Workspace{Root: t.TempDir()})
	a.attachPipeline(slog.New(slog.DiscardHandler), fakeDeps(release))

	if err := a.StartSimulation(1); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("StartSimulation before startup = %v, want ErrNotStarted", err)
	}
	a.startup(context.Background())
	if err := a.SetCredentials(config.Credentials{AWSRegion: "us-east-1", APIKey: "key", APISecret: "secret"}); err != nil {
		t.Fatal(err)
	}

	if err := a.Setup(); err != nil {
		t.Fatal(err)
	}
	if err := a.Teardown(); !errors.Is(err, pipeline.ErrBusy) {
		t.Fatalf("Teardown during setup = %v, want ErrBusy", err)
	}
	if a.GetState().Busy != "setup" {
		t.Fatal("state should report setup as busy")
	}
	close(release)

	done := ev.wait(t, eventSetupDone).(map[string]string)
	if done["error"] == "" {
		t.Fatal("setup:done must carry the failure")
	}
	st := a.GetState()
	if st.Busy != "" || st.LastError == "" {
		t.Fatalf("state after failed setup = busy %q, lastError %q", st.Busy, st.LastError)
	}
	a.shutdown(context.Background())
}

func TestShutdownCancelsRunningSetup(t *testing.T) {
	ev := captureEvents(t)
	a := NewApp(config.Workspace{Root: t.TempDir()})
	a.attachPipeline(slog.New(slog.DiscardHandler), fakeDeps(make(chan struct{}))) // never released
	a.startup(context.Background())
	if err := a.SetCredentials(config.Credentials{AWSRegion: "us-east-1", APIKey: "key", APISecret: "secret"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Setup(); err != nil {
		t.Fatal(err)
	}

	finished := make(chan struct{})
	go func() { a.shutdown(context.Background()); close(finished) }()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not cancel the running setup")
	}
	// Events are no longer emitted once the window is closing, but the
	// cancellation is still recorded.
	if st := a.GetState(); st.Busy != "" || st.LastError == "" {
		t.Fatalf("after shutdown: busy %q, lastError %q", st.Busy, st.LastError)
	}
	_ = ev
}

// latencyResult is a reading with one sample of each metric.
func latencyResult(ms float64) latency.Result {
	at := time.Unix(1_700_000_000, 0).UTC()
	return latency.Result{
		Event:      []latency.Sample{{Table: "patients", Time: at, Ms: ms}},
		Processing: []latency.Sample{{Table: "patients", Time: at, Ms: ms / 2}},
		ObservedAt: at,
	}
}

func TestLatencyKeepsLastGoodReadingThroughAFailedPoll(t *testing.T) {
	ev := captureEvents(t)
	a := newTestApp(t)
	a.ctx = context.Background() // as if Wails had called startup

	last := func() latency.Result {
		ev.mu.Lock()
		defer ev.mu.Unlock()
		return ev.events[eventLatency][len(ev.events[eventLatency])-1].(latency.Result)
	}

	// A result covers a whole window, so a newer one replaces the older.
	a.onLatency(latencyResult(1000))
	a.onLatency(latencyResult(2000))
	if got := a.GetState().Latency; len(got.Event) != 1 || got.Event[0].Ms != 2000 {
		t.Fatalf("state after two results = %+v, want only the newer", got)
	}

	// A failed poll keeps the last good reading, and when it was read, and
	// adds its error, in both the state a reloaded UI gets and the event the
	// open UI gets.
	a.onLatency(latency.Result{Error: "metrics api returned 401 Unauthorized", ObservedAt: time.Unix(1_800_000_000, 0).UTC()})
	got := a.GetState().Latency
	if len(got.Event) != 1 || got.Event[0].Ms != 2000 || len(got.Processing) != 1 || got.Error == "" {
		t.Fatalf("state after a failed poll = %+v", got)
	}
	if !got.ObservedAt.Equal(latencyResult(0).ObservedAt) {
		t.Fatalf("observedAt = %v, want the last good poll's", got.ObservedAt)
	}
	if e := last(); len(e.Event) != 1 || e.Error != got.Error {
		t.Fatalf("event after a failed poll = %+v", e)
	}

	// The next good result clears the error.
	a.onLatency(latencyResult(3000))
	if got := a.GetState().Latency; got.Error != "" || got.Event[0].Ms != 3000 {
		t.Fatalf("state after recovering = %+v", got)
	}
}

func TestTeardownClearsLatency(t *testing.T) {
	ev := captureEvents(t)
	a := NewApp(config.Workspace{Root: t.TempDir()})
	a.attachPipeline(slog.New(slog.DiscardHandler), fakeDeps(make(chan struct{})))
	a.startup(context.Background())
	t.Cleanup(func() { a.shutdown(context.Background()) })
	if err := a.SetCredentials(config.Credentials{AWSRegion: "us-east-1", APIKey: "key", APISecret: "secret"}); err != nil {
		t.Fatal(err)
	}

	a.onLatency(latencyResult(1000))
	if got := a.GetState().Latency; len(got.Event) == 0 {
		t.Fatal("setup: no reading to clear")
	}
	if err := a.Teardown(); err != nil {
		t.Fatal(err)
	}
	if done := ev.wait(t, eventTeardownDone).(map[string]string); done["error"] != "" {
		t.Fatalf("teardown failed: %v", done)
	}
	if got := a.GetState().Latency; len(got.Event) != 0 || len(got.Processing) != 0 || got.Error != "" || !got.ObservedAt.IsZero() {
		t.Fatalf("state after teardown = %+v, want it cleared", got)
	}
}

func TestRunQueryNeedsStartupAndSetup(t *testing.T) {
	a := newTestApp(t)
	if _, err := a.RunQuery("SELECT 1"); !errors.Is(err, ErrNotStarted) {
		t.Fatalf("RunQuery() before startup = %v, want ErrNotStarted", err)
	}
	a.ctx = context.Background() // as if Wails had called startup
	if _, err := a.RunQuery("SELECT 1"); !errors.Is(err, pipeline.ErrNotReady) {
		t.Fatalf("RunQuery() before setup = %v, want ErrNotReady", err)
	}
	if q := a.GetState().Query; q.Catalog != "" || len(q.Tables) != 0 {
		t.Fatalf("state.Query = %+v before setup, want none", q)
	}
}
