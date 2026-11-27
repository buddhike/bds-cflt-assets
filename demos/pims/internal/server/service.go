package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"time"

	"pims/internal/latency"
	"pims/internal/logging"
	"pims/internal/pipeline"
	"pims/internal/query"
	"pims/internal/sim"
)

// Event names sent to the browser. The UI listens for these names.
const (
	eventLog       = "log"
	eventStep      = "step"
	eventLatency   = "latency"
	eventSim       = "sim"
	eventSetupDone = "setup:done"
)

const maxLogs = 500

// shutdownWait (a variable for tests) is how long Shutdown lets a running setup react to being stopped.
var shutdownWait = 20 * time.Second

// ErrNotStarted is returned if the service is used before Start.
var ErrNotStarted = errors.New("the service is still starting")

// State is everything the UI needs after a (re)load.
type State struct {
	AWSRegion string               `json:"awsRegion"`
	Busy      string               `json:"busy"`
	LastError string               `json:"lastError"`
	Steps     []pipeline.StepState `json:"steps"`
	Sim       sim.Stats            `json:"sim"`
	Logs      []logging.Entry      `json:"logs"`
	Latency   latency.Result       `json:"latency"`
	Limits    map[string]float64   `json:"limits"`
	Query     pipeline.QueryInfo   `json:"query"`
}

// Service is the server's backend: the pipeline plus what the UI shows of it.
type Service struct {
	cfg  Config
	log  *slog.Logger
	pipe *pipeline.Pipeline
	hub  *hub

	mu        sync.Mutex
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	logs      ring[logging.Entry]
	latency   latency.Result
	busy      string
	lastError string
	// closing is set by Close. Setup then refuses to start, so it cannot add to wg while
	// Close waits on it.
	closing bool
	// shutdownMu serialises Shutdown.
	shutdownMu sync.Mutex
}

// New builds the service. base receives the log records, which also go to the UI.
// mkDeps gets the logger so that the pipeline's own records reach the UI too.
func New(cfg Config, base slog.Handler, mkDeps func(*slog.Logger) (pipeline.Deps, error)) (*Service, error) {
	s := &Service{cfg: cfg, hub: newHub(), logs: newRing[logging.Entry](maxLogs)}
	s.log = slog.New(logging.NewTeeHandler(base, s.onLog))
	deps, err := mkDeps(s.log)
	if err != nil {
		return nil, err
	}
	s.pipe = pipeline.New(deps, s.log, func(st pipeline.StepState) { s.hub.publish(eventStep, st) }, s.onLatency)
	return s, nil
}

// Logger is the logger whose records reach the UI.
func (s *Service) Logger() *slog.Logger { return s.log }

func (s *Service) onLog(e logging.Entry) {
	s.mu.Lock()
	s.logs.push(e)
	s.mu.Unlock()
	s.hub.publish(eventLog, e)
}

// onLatency keeps the newest reading. A failed poll keeps the last good samples
// and only records the error, so a passing outage does not blank the charts.
func (s *Service) onLatency(r latency.Result) {
	s.mu.Lock()
	if r.Error == "" {
		s.latency = r
	} else {
		s.latency.Error = r.Error
	}
	cur := s.latency
	s.mu.Unlock()
	s.hub.publish(eventLatency, cur)
}

// Start runs setup in the background and publishes simulator counters. It returns at once.
func (s *Service) Start(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.ctx, s.cancel = ctx, cancel
	s.mu.Unlock()
	s.wg.Add(1)
	go func() { defer s.wg.Done(); s.publishSimStats(ctx) }()
	s.log.Info("PIMS started", "config", s.cfg)
	if err := s.Setup(); err != nil {
		s.log.Error("Could not start setup", "error", err)
	}
}

// stop cancels background work and waits up to grace for it to react. It reports whether
// all of it has stopped.
func (s *Service) stop(grace time.Duration) bool {
	s.mu.Lock()
	s.closing = true
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return true
	case <-time.After(grace):
		s.log.Warn("Background work did not stop in time", "grace", grace)
		return false
	}
}

// Close stops background work, waiting up to grace for it to react.
func (s *Service) Close(grace time.Duration) {
	s.stop(grace)
	s.pipe.Close()
	s.hub.close()
}

// Shutdown is what Launchpad asks for before it destroys the demo. It stops the setup, the
// simulator and the metrics monitor, then turns Tableflow off on the topics, which
// Terraform could not delete otherwise. Calling it again is safe, so a failed teardown can
// be retried. The demo stays stopped after it, even if it fails: only a new task runs it
// again. Requests are served one at a time, so a retry does not race the call it repeats.
func (s *Service) Shutdown(ctx context.Context) error {
	s.shutdownMu.Lock()
	defer s.shutdownMu.Unlock()
	s.log.Info("Shutting down the demo")
	// A setup still running could enable Tableflow after it was disabled, so it stops first,
	// and if it will not, the topics are not touched: the caller must try again.
	if !s.stop(shutdownWait) {
		return errors.New("the setup did not stop in time")
	}
	s.pipe.Close()
	if err := s.pipe.DisableTableflow(ctx); err != nil {
		s.log.Error("Could not disable Tableflow", "error", err)
		return err
	}
	s.log.Info("Demo is shut down")
	return nil
}

func (s *Service) publishSimStats(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var last sim.Stats
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if st := s.pipe.SimStats(); !reflect.DeepEqual(st, last) {
				s.hub.publish(eventSim, st)
				last = st
			}
		}
	}
}

func (s *Service) context() (context.Context, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx == nil {
		return nil, ErrNotStarted
	}
	return s.ctx, nil
}

// Setup runs the setup steps in the background. It is safe to run again, for example
// after a failed step: the steps resume where things are.
func (s *Service) Setup() error {
	s.mu.Lock()
	if s.ctx == nil {
		s.mu.Unlock()
		return ErrNotStarted
	}
	if s.closing {
		s.mu.Unlock()
		return pipeline.ErrClosed
	}
	if s.busy != "" {
		s.mu.Unlock()
		return fmt.Errorf("%w: %s is running", pipeline.ErrBusy, s.busy)
	}
	ctx := s.ctx
	s.busy, s.lastError = "setup", ""
	// Added before the lock is released: stop sets closing under the same lock and then
	// waits, so it either refuses this setup or waits for it.
	s.wg.Add(1)
	s.mu.Unlock()

	go func() {
		defer s.wg.Done()
		err := s.pipe.Setup(ctx, s.cfg.Credentials())
		msg := ""
		s.mu.Lock()
		s.busy = ""
		if err != nil {
			msg = err.Error()
			s.lastError = msg
		}
		s.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			s.log.Error("Operation failed", "operation", "setup", "error", err)
		}
		s.hub.publish(eventSetupDone, map[string]string{"error": msg})
	}()
	return nil
}

// State returns the current state.
func (s *Service) State() State {
	s.mu.Lock()
	st := State{
		AWSRegion: s.cfg.AWSRegion,
		Busy:      s.busy,
		LastError: s.lastError,
		Logs:      s.logs.items(),
		Latency:   s.latency,
	}
	s.mu.Unlock()
	st.Steps = s.pipe.StepStates()
	st.Sim = s.pipe.SimStats()
	st.Limits = map[string]float64{"minRate": sim.MinRate, "maxRate": sim.MaxRate, "defaultRate": pipeline.DefaultRate}
	st.Query = s.pipe.QueryInfo()
	return st
}

// StartSimulation starts the simulator at rate events per second.
func (s *Service) StartSimulation(rate float64) error {
	ctx, err := s.context()
	if err != nil {
		return err
	}
	return s.pipe.StartSimulation(ctx, rate)
}

// SetRate changes the simulator's event rate.
func (s *Service) SetRate(rate float64) error { return s.pipe.SetRate(rate) }

// StopSimulation stops the simulator.
func (s *Service) StopSimulation() error { return s.pipe.StopSimulation() }

// RunQuery runs sql in the DuckDB container.
func (s *Service) RunQuery(sql string) (query.Result, error) {
	ctx, err := s.context()
	if err != nil {
		return query.Result{}, err
	}
	res, err := s.pipe.Query(ctx, sql)
	if err != nil {
		// Info, not Warn: a typo in a query is the user's to fix, and the UI counts warnings.
		s.log.Info("SQL query failed", "error", err)
		return query.Result{}, err
	}
	s.log.Info("Ran SQL query", "rows", res.TotalRows, "elapsed_ms", res.ElapsedMs)
	return res, nil
}

// ring is a fixed-capacity FIFO used for the UI's log history.
type ring[T any] struct {
	buf   []T
	start int
	n     int
}

func newRing[T any](capacity int) ring[T] { return ring[T]{buf: make([]T, capacity)} }

func (r *ring[T]) push(v T) {
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = v
		r.n++
		return
	}
	r.buf[r.start] = v
	r.start = (r.start + 1) % len(r.buf)
}

func (r *ring[T]) items() []T {
	out := make([]T, r.n)
	for i := range r.n {
		out[i] = r.buf[(r.start+i)%len(r.buf)]
	}
	return out
}
