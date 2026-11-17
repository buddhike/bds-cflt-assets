// Package pipeline orchestrates the PIMS demo end to end: it provisions
// Confluent Cloud, starts the local services, installs the schema,
// registers Debezium, enables Tableflow and starts the latency monitor,
// reporting progress step by step. It also owns the simulator.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"pims/internal/compose"
	"pims/internal/config"
	"pims/internal/latency"
	"pims/internal/sim"
	"pims/internal/terraform"
)

// ErrBusy is returned when a setup or teardown is already in progress.
var ErrBusy = errors.New("another setup or teardown is in progress")

// ErrNotReady is returned when the simulator is used before setup has
// prepared the database.
var ErrNotReady = errors.New("run setup first")

// DefaultRate is the event rate the simulator starts at during setup. Some
// rows must exist before Debezium registers the topic schemas, and
// Tableflow can only be enabled once they do.
const DefaultRate = 2.0

// StepID identifies a setup step.
type StepID string

// Setup steps in execution order.
const (
	StepDocker    StepID = "docker"
	StepCloud     StepID = "cloud"
	StepServices  StepID = "services"
	StepSchema    StepID = "schema"
	StepConnector StepID = "connector"
	StepSimulate  StepID = "simulate"
	StepSchemas   StepID = "schemas"
	StepTableflow StepID = "tableflow"
	StepLatency   StepID = "latency"
)

// Steps lists every setup step with its display title.
var Steps = []struct {
	ID    StepID
	Title string
}{
	{StepDocker, "Check Docker"},
	{StepCloud, "Provision Confluent Cloud"},
	{StepServices, "Start MySQL, Kafka Connect and DuckDB"},
	{StepSchema, "Install MySQL schema"},
	{StepConnector, "Start Debezium connector"},
	{StepSimulate, "Start simulator"},
	{StepSchemas, "Wait for topic schemas"},
	{StepTableflow, "Enable Tableflow"},
	{StepLatency, "Start latency monitor"},
}

// Step statuses.
const (
	StatusPending = "pending"
	StatusRunning = "running"
	StatusDone    = "done"
	StatusFailed  = "failed"
)

// StepState is a step's progress, sent to the UI.
type StepState struct {
	ID         StepID    `json:"id"`
	Title      string    `json:"title"`
	Status     string    `json:"status"`
	Detail     string    `json:"detail,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
}

// Deps are the operations the pipeline sequences. New wires the real
// implementations; tests substitute fakes.
type Deps struct {
	CheckDocker  func(ctx context.Context) error
	Provision    func(ctx context.Context, c config.Credentials, o terraform.ApplyOptions) (terraform.Outputs, error)
	Destroy      func(ctx context.Context, c config.Credentials) error
	StartLocal   func(ctx context.Context, out terraform.Outputs) (compose.MySQLSecrets, error)
	StopLocal    func(ctx context.Context, removeVolumes bool) error
	PrepareDB    func(ctx context.Context, s compose.MySQLSecrets) (sim.Store, io.Closer, error)
	StartCDC     func(ctx context.Context, out terraform.Outputs, s compose.MySQLSecrets) error
	StopCDC      func(ctx context.Context, s compose.MySQLSecrets) error
	WaitSchemas  func(ctx context.Context, out terraform.Outputs) error
	NewQuerier   func(out terraform.Outputs) (latency.Querier, error)
	PollInterval time.Duration
	// Seed seeds the simulator; zero picks a time-based seed.
	Seed uint64
}

// Pipeline runs setup and teardown and owns the simulator and monitor.
type Pipeline struct {
	deps      Deps
	log       *slog.Logger
	onStep    func(StepState)
	onLatency func(latency.Result)

	busy sync.Mutex // held for the duration of Setup/Teardown

	mu          sync.Mutex
	steps       map[StepID]StepState
	sim         *sim.Simulator
	dbCloser    io.Closer
	stopMonitor context.CancelFunc
	monitorDone chan struct{}
	outputs     *terraform.Outputs
	secrets     *compose.MySQLSecrets
}

// New returns a Pipeline. onStep and onLatency are called from background
// goroutines and must not block.
func New(deps Deps, log *slog.Logger, onStep func(StepState), onLatency func(latency.Result)) *Pipeline {
	p := &Pipeline{deps: deps, log: log.With("component", "pipeline"), onStep: onStep, onLatency: onLatency}
	p.resetSteps()
	return p
}

func (p *Pipeline) resetSteps() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.steps = make(map[StepID]StepState, len(Steps))
	for _, s := range Steps {
		p.steps[s.ID] = StepState{ID: s.ID, Title: s.Title, Status: StatusPending}
	}
}

// StepStates returns every step's state in execution order.
func (p *Pipeline) StepStates() []StepState {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]StepState, 0, len(Steps))
	for _, s := range Steps {
		out = append(out, p.steps[s.ID])
	}
	return out
}

func (p *Pipeline) update(id StepID, status, detail string) {
	p.mu.Lock()
	s := p.steps[id]
	s.Status, s.Detail = status, detail
	now := time.Now().UTC()
	switch status {
	case StatusRunning:
		s.StartedAt, s.FinishedAt = now, time.Time{}
	case StatusDone, StatusFailed:
		s.FinishedAt = now
	}
	p.steps[id] = s
	p.mu.Unlock()
	if p.onStep != nil {
		p.onStep(s)
	}
}

// run executes one step, recording its outcome.
func (p *Pipeline) run(id StepID, fn func() (string, error)) error {
	p.update(id, StatusRunning, "")
	title := p.steps[id].Title
	p.log.Info("Setup step started", "step", id, "title", title)
	detail, err := fn()
	if err != nil {
		p.update(id, StatusFailed, err.Error())
		p.log.Error("Setup step failed", "step", id, "error", err)
		return fmt.Errorf("%s: %w", title, err)
	}
	p.update(id, StatusDone, detail)
	p.log.Info("Setup step finished", "step", id)
	return nil
}

// Setup brings every component up. It is idempotent: running it again,
// for example after a restart or a failed step, resumes where things are.
func (p *Pipeline) Setup(ctx context.Context, creds config.Credentials) error {
	if !p.busy.TryLock() {
		return ErrBusy
	}
	defer p.busy.Unlock()
	if err := creds.Validate(); err != nil {
		return err
	}
	p.resetSteps()

	var (
		out     terraform.Outputs
		secrets compose.MySQLSecrets
	)
	steps := []struct {
		id StepID
		fn func() (string, error)
	}{
		{StepDocker, func() (string, error) { return "", p.deps.CheckDocker(ctx) }},
		{StepCloud, func() (string, error) {
			o, err := p.deps.Provision(ctx, creds, terraform.ApplyOptions{})
			out = o
			return fmt.Sprintf("Environment %s, cluster %s", o.EnvironmentID, o.KafkaClusterID), err
		}},
		{StepServices, func() (string, error) {
			s, err := p.deps.StartLocal(ctx, out)
			secrets = s
			return "", err
		}},
		{StepSchema, func() (string, error) {
			store, closer, err := p.deps.PrepareDB(ctx, secrets)
			if err != nil {
				return "", err
			}
			p.attachStore(store, closer)
			return "patients, appointments, visits", nil
		}},
		{StepConnector, func() (string, error) { return "", p.deps.StartCDC(ctx, out, secrets) }},
		{StepSimulate, func() (string, error) {
			// Debezium registers a topic's schema with its first record, so
			// data must flow before Tableflow can be enabled.
			err := p.StartSimulation(ctx, DefaultRate)
			if errors.Is(err, sim.ErrAlreadyRunning) {
				return "Already running", nil
			}
			return fmt.Sprintf("%.0f events/sec", DefaultRate), err
		}},
		{StepSchemas, func() (string, error) { return "", p.deps.WaitSchemas(ctx, out) }},
		{StepTableflow, func() (string, error) {
			o, err := p.deps.Provision(ctx, creds, terraform.ApplyOptions{EnableTableflow: true})
			if err == nil {
				out = o
			}
			return "Iceberg tables with Confluent managed storage", err
		}},
		{StepLatency, func() (string, error) { return "", p.startMonitor(out) }},
	}
	for _, s := range steps {
		if err := p.run(s.id, s.fn); err != nil {
			return err
		}
	}
	p.mu.Lock()
	p.outputs, p.secrets = &out, &secrets
	p.mu.Unlock()
	p.log.Info("PIMS demo is ready")
	return nil
}

func (p *Pipeline) attachStore(store sim.Store, closer io.Closer) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sim != nil {
		// Re-running setup: keep the existing simulator and connection.
		_ = closer.Close()
		return
	}
	seed := p.deps.Seed
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	p.sim = sim.New(store, p.log, seed)
	p.dbCloser = closer
}

func (p *Pipeline) startMonitor(out terraform.Outputs) error {
	q, err := p.deps.NewQuerier(out)
	if err != nil {
		return err
	}
	p.stopLatencyMonitor()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m := latency.NewMonitor(q, p.deps.PollInterval, p.log)
	p.mu.Lock()
	p.stopMonitor, p.monitorDone = cancel, done
	p.mu.Unlock()
	go func() {
		defer close(done)
		m.Run(ctx, func(r latency.Result) {
			if p.onLatency != nil {
				p.onLatency(r)
			}
		})
	}()
	return nil
}

func (p *Pipeline) stopLatencyMonitor() {
	p.mu.Lock()
	cancel, done := p.stopMonitor, p.monitorDone
	p.stopMonitor, p.monitorDone = nil, nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
}

func (p *Pipeline) simulator() (*sim.Simulator, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sim == nil {
		return nil, ErrNotReady
	}
	return p.sim, nil
}

// StartSimulation starts generating events at rate events per second. The
// simulation runs until StopSimulation, Teardown or Close, independent of
// ctx's lifetime beyond startup.
func (p *Pipeline) StartSimulation(ctx context.Context, rate float64) error {
	s, err := p.simulator()
	if err != nil {
		return err
	}
	// Detach from the caller's context: a Wails call context ends when the
	// call returns, but the simulation should keep running.
	return s.Start(context.WithoutCancel(ctx), rate)
}

// SetRate changes the simulator's event rate.
func (p *Pipeline) SetRate(rate float64) error {
	s, err := p.simulator()
	if err != nil {
		return err
	}
	s.SetRate(rate)
	return nil
}

// StopSimulation stops the simulator if it is running.
func (p *Pipeline) StopSimulation() error {
	s, err := p.simulator()
	if err != nil {
		return err
	}
	s.Stop()
	return nil
}

// SimStats returns the simulator's counters, or zero values before setup.
func (p *Pipeline) SimStats() sim.Stats {
	s, err := p.simulator()
	if err != nil {
		return sim.Stats{}
	}
	return s.Stats()
}

// Teardown stops everything and deletes all resources: the local
// containers and MySQL volume, and every Confluent Cloud resource PIMS
// created. Errors from individual stages are collected so one failure
// does not leave the cloud resources (which cost money) running.
func (p *Pipeline) Teardown(ctx context.Context, creds config.Credentials) error {
	if !p.busy.TryLock() {
		return ErrBusy
	}
	defer p.busy.Unlock()
	if err := creds.Validate(); err != nil {
		return err
	}
	p.log.Info("Tearing down the PIMS demo")
	p.shutdownLocal()

	var errs []error
	p.mu.Lock()
	secrets := p.secrets
	p.mu.Unlock()
	if secrets != nil {
		if err := p.deps.StopCDC(ctx, *secrets); err != nil {
			p.log.Warn("Could not delete Debezium connector", "error", err)
		}
	}
	if err := p.deps.StopLocal(ctx, true); err != nil {
		errs = append(errs, fmt.Errorf("stop local services: %w", err))
	}
	if err := p.deps.Destroy(ctx, creds); err != nil {
		errs = append(errs, fmt.Errorf("destroy confluent cloud resources: %w", err))
	}
	p.mu.Lock()
	p.outputs, p.secrets = nil, nil
	p.mu.Unlock()
	p.resetSteps()
	if err := errors.Join(errs...); err != nil {
		return err
	}
	p.log.Info("Teardown complete")
	return nil
}

// shutdownLocal stops the monitor and simulator and closes the database.
func (p *Pipeline) shutdownLocal() {
	p.stopLatencyMonitor()
	p.mu.Lock()
	s, closer := p.sim, p.dbCloser
	p.sim, p.dbCloser = nil, nil
	p.mu.Unlock()
	if s != nil {
		s.Stop()
	}
	if closer != nil {
		if err := closer.Close(); err != nil {
			p.log.Warn("Could not close MySQL connection", "error", err)
		}
	}
}

// Close stops background work without deleting anything, so the demo can
// be resumed on the next launch.
func (p *Pipeline) Close() {
	p.shutdownLocal()
}
