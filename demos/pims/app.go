package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"pims/internal/config"
	"pims/internal/latency"
	"pims/internal/logging"
	"pims/internal/pipeline"
	"pims/internal/query"
	"pims/internal/sim"
)

// Event names emitted to the frontend.
const (
	eventLog          = "log"
	eventStep         = "step"
	eventLatency      = "latency"
	eventSim          = "sim"
	eventSetupDone    = "setup:done"
	eventTeardownDone = "teardown:done"
)

// History kept for a frontend that (re)loads after events were emitted.
const maxLogs = 500

// ErrNoCredentials is returned when an action needs Confluent Cloud
// credentials that have not been entered yet.
var ErrNoCredentials = errors.New("enter your Confluent Cloud credentials first")

// ErrNotStarted is returned if a bound method is called before Wails has
// called startup.
var ErrNotStarted = errors.New("application is still starting")

// shutdownGrace bounds how long closing the window waits for a running
// setup or teardown to react to cancellation. terraform-exec interrupts
// terraform and gives it up to a minute to release its state lock.
const shutdownGrace = 90 * time.Second

// App is the Wails-bound backend. Its exported methods are callable from
// the frontend.
type App struct {
	ctx    context.Context
	cancel context.CancelFunc // cancels ctx on shutdown; Wails never does
	wg     sync.WaitGroup     // background operations and the stats publisher
	ws     config.Workspace
	log    *slog.Logger
	pipe   *pipeline.Pipeline

	mu    sync.Mutex
	creds *config.Credentials // in memory only, never persisted
	logs  ring[logging.Entry]
	// latency is the last good reading of Tableflow's freshness metrics, with
	// the error of the latest poll if it failed. Its ObservedAt is that of the
	// last good poll.
	latency   latency.Result
	busy      string // "setup" or "teardown" while one is running
	lastError string
}

// NewApp returns an App. The logger is attached later by attachLogger so
// that log records can be forwarded to the frontend.
func NewApp(ws config.Workspace) *App {
	return &App{ws: ws, logs: newRing[logging.Entry](maxLogs)}
}

// attachLogger sets the logger and builds the pipeline that uses it.
func (a *App) attachLogger(log *slog.Logger) {
	a.attachPipeline(log, pipeline.NewDeps(a.ws, log))
}

func (a *App) attachPipeline(log *slog.Logger, deps pipeline.Deps) {
	a.log = log
	a.pipe = pipeline.New(deps, log, a.onStep, a.onLatency)
}

// context returns the app context, or ErrNotStarted before startup.
func (a *App) context() (context.Context, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ctx == nil {
		return nil, ErrNotStarted
	}
	return a.ctx, nil
}

// onLog receives every slog record via the tee handler.
func (a *App) onLog(e logging.Entry) {
	a.mu.Lock()
	a.logs.push(e)
	a.mu.Unlock()
	a.emit(eventLog, e)
}

func (a *App) onStep(s pipeline.StepState) { a.emit(eventStep, s) }

// onLatency keeps the newest reading and sends it to the frontend. Each
// result covers a whole window, so it replaces the last one. A failed poll
// has no samples: it keeps the last good ones and only records the error, so
// a passing outage does not blank the charts.
func (a *App) onLatency(r latency.Result) {
	a.mu.Lock()
	if r.Error == "" {
		a.latency = r
	} else {
		a.latency.Error = r.Error
	}
	cur := a.latency
	a.mu.Unlock()
	a.emit(eventLatency, cur)
}

// emitter sends events to the frontend; replaced in tests.
var emitter = runtime.EventsEmit

func (a *App) emit(name string, data any) {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx != nil && ctx.Err() == nil {
		emitter(ctx, name, data)
	}
}

// startup is called by Wails when the window is ready.
func (a *App) startup(ctx context.Context) {
	ctx, cancel := context.WithCancel(ctx)
	a.mu.Lock()
	a.ctx, a.cancel = ctx, cancel
	a.mu.Unlock()
	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		a.publishSimStats(ctx)
	}()
	a.log.Info("PIMS started", "workspace", a.ws.Root)
}

// shutdown is called by Wails when the window closes. It cancels any
// running setup or teardown and waits (bounded) for them to stop, so
// terraform is interrupted cleanly rather than killed with the process.
// Nothing is deleted, so the demo can be resumed on the next launch.
func (a *App) shutdown(context.Context) {
	a.mu.Lock()
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	done := make(chan struct{})
	go func() { a.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(shutdownGrace):
		a.log.Warn("Background operation did not stop in time", "grace", shutdownGrace)
	}
	a.pipe.Close()
}

// publishSimStats pushes simulator counters to the UI once a second, but
// only when they changed.
func (a *App) publishSimStats(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var last sim.Stats
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			st := a.pipe.SimStats()
			if !reflect.DeepEqual(st, last) {
				a.emit(eventSim, st)
				last = st
			}
		}
	}
}

// Defaults are values to pre-fill the credentials form with.
type Defaults struct {
	AWSRegion string `json:"awsRegion"`
	Workspace string `json:"workspace"`
}

// GetDefaults returns the last used AWS region and the workspace path.
func (a *App) GetDefaults() Defaults {
	s, err := a.ws.LoadSettings()
	if err != nil {
		a.log.Warn("Could not read saved settings", "error", err)
	}
	region := s.AWSRegion
	if region == "" {
		region = "us-east-1"
	}
	return Defaults{AWSRegion: region, Workspace: a.ws.Root}
}

// SetCredentials validates and keeps the operator's Confluent Cloud
// credentials in memory for this session. Only the region is saved; the
// secrets never reach disk.
func (a *App) SetCredentials(c config.Credentials) error {
	c = c.Normalise()
	if err := c.Validate(); err != nil {
		return err
	}
	if err := a.ws.Ensure(); err != nil {
		return err
	}
	// Update the region in place: the settings also hold the resource-name
	// suffix, which must survive. A file that can't be read is left as it is,
	// not replaced, because replacing it would drop the suffix and rename the
	// live Confluent Cloud resources on the next setup. Setup then reports the
	// problem with the file's path.
	if saved, err := a.ws.LoadSettings(); err != nil {
		a.log.Warn("Could not read saved settings, leaving the file as it is", "error", err)
	} else {
		saved.AWSRegion = c.AWSRegion
		if err := a.ws.SaveSettings(saved); err != nil {
			a.log.Warn("Could not save settings", "error", err)
		}
	}
	a.mu.Lock()
	a.creds = &c
	a.mu.Unlock()
	a.log.Info("Credentials entered", "aws_region", c.AWSRegion)
	return nil
}

// State is everything the UI needs to render after a (re)load.
type State struct {
	HasCredentials bool                 `json:"hasCredentials"`
	AWSRegion      string               `json:"awsRegion"`
	Busy           string               `json:"busy"`
	LastError      string               `json:"lastError"`
	Steps          []pipeline.StepState `json:"steps"`
	Sim            sim.Stats            `json:"sim"`
	Logs           []logging.Entry      `json:"logs"`
	Latency        latency.Result       `json:"latency"`
	Limits         map[string]float64   `json:"limits"`
	Query          pipeline.QueryInfo   `json:"query"`
}

// GetState returns the current application state.
func (a *App) GetState() State {
	a.mu.Lock()
	st := State{
		HasCredentials: a.creds != nil,
		Busy:           a.busy,
		LastError:      a.lastError,
		Logs:           a.logs.items(),
		Latency:        a.latency,
	}
	if a.creds != nil {
		st.AWSRegion = a.creds.AWSRegion
	}
	a.mu.Unlock()
	st.Steps = a.pipe.StepStates()
	st.Sim = a.pipe.SimStats()
	st.Limits = map[string]float64{"minRate": sim.MinRate, "maxRate": sim.MaxRate, "defaultRate": pipeline.DefaultRate}
	st.Query = a.pipe.QueryInfo()
	return st
}

// Setup starts provisioning and setup in the background. Progress is
// reported through "step" events and completion through "setup:done".
func (a *App) Setup() error {
	return a.background("setup", eventSetupDone, a.pipe.Setup, nil)
}

// Teardown deletes every local and Confluent Cloud resource in the
// background, reporting completion through "teardown:done". The latency
// reading is cleared only once teardown has succeeded.
func (a *App) Teardown() error {
	return a.background("teardown", eventTeardownDone, a.pipe.Teardown, func() {
		a.latency = latency.Result{}
	})
}

// background runs fn on its own goroutine. onSuccess, if set, runs under
// a.mu after fn succeeds and before the done event is emitted.
func (a *App) background(name, doneEvent string, fn func(context.Context, config.Credentials) error, onSuccess func()) error {
	a.mu.Lock()
	if a.ctx == nil {
		a.mu.Unlock()
		return ErrNotStarted
	}
	if a.creds == nil {
		a.mu.Unlock()
		return ErrNoCredentials
	}
	if a.busy != "" {
		busy := a.busy
		a.mu.Unlock()
		return fmt.Errorf("%w: %s is running", pipeline.ErrBusy, busy)
	}
	creds, ctx := *a.creds, a.ctx
	a.busy, a.lastError = name, ""
	a.mu.Unlock()

	a.wg.Add(1)
	go func() {
		defer a.wg.Done()
		err := fn(ctx, creds)
		a.mu.Lock()
		a.busy = ""
		if err != nil {
			a.lastError = err.Error()
		} else if onSuccess != nil {
			onSuccess()
		}
		a.mu.Unlock()
		msg := ""
		if err != nil {
			msg = err.Error()
			a.log.Error("Operation failed", "operation", name, "error", err)
		}
		a.emit(doneEvent, map[string]string{"error": msg})
	}()
	return nil
}

// StartSimulation starts the simulator at rate events per second.
func (a *App) StartSimulation(rate float64) error {
	ctx, err := a.context()
	if err != nil {
		return err
	}
	return a.pipe.StartSimulation(ctx, rate)
}

// SetRate changes the simulator's event rate.
func (a *App) SetRate(rate float64) error { return a.pipe.SetRate(rate) }

// StopSimulation stops the simulator.
func (a *App) StopSimulation() error { return a.pipe.StopSimulation() }

// RunQuery runs sql, which may hold several statements, in the DuckDB
// container and returns the last result set. The SQL itself is not logged:
// it can be long and is the operator's own, though DuckDB's error message
// may quote part of it.
func (a *App) RunQuery(sql string) (query.Result, error) {
	ctx, err := a.context()
	if err != nil {
		return query.Result{}, err
	}
	res, err := a.pipe.Query(ctx, sql)
	if err != nil {
		// Info, not Warn: a typo in a query is the operator's to fix, and the
		// UI counts warnings against the setup.
		a.log.Info("SQL query failed", "error", err)
		return query.Result{}, err
	}
	a.log.Info("Ran SQL query", "rows", res.TotalRows, "elapsed_ms", res.ElapsedMs)
	return res, nil
}

// ring is a fixed-capacity FIFO used for UI history.
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
