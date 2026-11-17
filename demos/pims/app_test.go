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
