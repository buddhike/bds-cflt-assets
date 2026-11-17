package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	"pims/internal/config"
	"pims/internal/latency"
	"pims/internal/logging"
	"pims/internal/pipeline"
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
const (
	maxLogs    = 500
	maxLatency = 5000
)

// ErrNoCredentials is returned when an action needs Confluent Cloud
// credentials that have not been entered yet.
var ErrNoCredentials = errors.New("enter your Confluent Cloud credentials first")

// App is the Wails-bound backend. Its exported methods are callable from
// the frontend.
type App struct {
	ctx  context.Context
	ws   config.Workspace
	log  *slog.Logger
	pipe *pipeline.Pipeline

	mu          sync.Mutex
	creds       *config.Credentials // in memory only, never persisted
	logs        ring[logging.Entry]
	points      ring[latency.Point]
	tableErrors []latency.TableError
	busy        string // "setup" or "teardown" while one is running
	lastError   string
}

// NewApp returns an App. The logger is attached later by attachLogger so
// that log records can be forwarded to the frontend.
func NewApp(ws config.Workspace) *App {
	return &App{ws: ws, logs: newRing[logging.Entry](maxLogs), points: newRing[latency.Point](maxLatency)}
}

// attachLogger sets the logger and builds the pipeline that uses it.
func (a *App) attachLogger(log *slog.Logger) {
	a.log = log
	a.pipe = pipeline.New(pipeline.NewDeps(a.ws, log), log, a.onStep, a.onLatency)
}

// onLog receives every slog record via the tee handler.
func (a *App) onLog(e logging.Entry) {
	a.mu.Lock()
	a.logs.push(e)
	a.mu.Unlock()
	a.emit(eventLog, e)
}

func (a *App) onStep(s pipeline.StepState) { a.emit(eventStep, s) }

func (a *App) onLatency(r latency.Result) {
	a.mu.Lock()
	for _, p := range r.Points {
		a.points.push(p)
	}
	a.tableErrors = r.Errors
	a.mu.Unlock()
	a.emit(eventLatency, r)
}

func (a *App) emit(name string, data any) {
	a.mu.Lock()
	ctx := a.ctx
	a.mu.Unlock()
	if ctx != nil {
		runtime.EventsEmit(ctx, name, data)
	}
}

// startup is called by Wails when the window is ready.
func (a *App) startup(ctx context.Context) {
	a.mu.Lock()
	a.ctx = ctx
	a.mu.Unlock()
	go a.publishSimStats(ctx)
	a.log.Info("PIMS started", "workspace", a.ws.Root)
}

// shutdown is called by Wails when the window closes. Background work is
// stopped but nothing is deleted, so the demo can be resumed.
func (a *App) shutdown(context.Context) {
	a.pipe.Close()
}

// publishSimStats pushes simulator counters to the UI once a second.
func (a *App) publishSimStats(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			a.emit(eventSim, a.pipe.SimStats())
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
// credentials in memory for this session. Only the region is saved.
func (a *App) SetCredentials(c config.Credentials) error {
	c = c.Normalise()
	if err := c.Validate(); err != nil {
		return err
	}
	if err := a.ws.Ensure(); err != nil {
		return err
	}
	if err := a.ws.SaveSettings(config.Settings{AWSRegion: c.AWSRegion}); err != nil {
		a.log.Warn("Could not save settings", "error", err)
	}
	a.mu.Lock()
	a.creds = &c
	a.mu.Unlock()
	a.log.Info("Credentials entered", "credentials", c)
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
	Latency        []latency.Point      `json:"latency"`
	TableErrors    []latency.TableError `json:"tableErrors"`
	Limits         map[string]float64   `json:"limits"`
}

// GetState returns the current application state.
func (a *App) GetState() State {
	a.mu.Lock()
	st := State{
		HasCredentials: a.creds != nil,
		Busy:           a.busy,
		LastError:      a.lastError,
		Logs:           a.logs.items(),
		Latency:        a.points.items(),
		TableErrors:    a.tableErrors,
	}
	if a.creds != nil {
		st.AWSRegion = a.creds.AWSRegion
	}
	a.mu.Unlock()
	st.Steps = a.pipe.StepStates()
	st.Sim = a.pipe.SimStats()
	st.Limits = map[string]float64{"minRate": sim.MinRate, "maxRate": sim.MaxRate, "defaultRate": pipeline.DefaultRate}
	return st
}

// Setup starts provisioning and setup in the background. Progress is
// reported through "step" events and completion through "setup:done".
func (a *App) Setup() error {
	return a.background("setup", eventSetupDone, a.pipe.Setup)
}

// Teardown deletes every local and Confluent Cloud resource in the
// background, reporting completion through "teardown:done".
func (a *App) Teardown() error {
	err := a.background("teardown", eventTeardownDone, a.pipe.Teardown)
	if err == nil {
		a.mu.Lock()
		a.points = newRing[latency.Point](maxLatency)
		a.tableErrors = nil
		a.mu.Unlock()
	}
	return err
}

func (a *App) background(name, doneEvent string, fn func(context.Context, config.Credentials) error) error {
	a.mu.Lock()
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

	go func() {
		err := fn(ctx, creds)
		a.mu.Lock()
		a.busy = ""
		if err != nil {
			a.lastError = err.Error()
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
	return a.pipe.StartSimulation(a.ctx, rate)
}

// SetRate changes the simulator's event rate.
func (a *App) SetRate(rate float64) error { return a.pipe.SetRate(rate) }

// StopSimulation stops the simulator.
func (a *App) StopSimulation() error { return a.pipe.StopSimulation() }

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
