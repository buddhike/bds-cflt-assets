// Package pipeline orchestrates the PIMS demo end to end: it provisions
// Confluent Cloud, starts the local services, installs the schema,
// registers Debezium, enables Tableflow and starts the monitor of Tableflow's
// freshness metrics, reporting progress step by step. It also owns the
// simulator.
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
	"pims/internal/query"
	"pims/internal/sim"
	"pims/internal/terraform"
)

// ErrBusy is returned when a setup is already in progress.
var ErrBusy = errors.New("another setup is in progress")

// ErrNotReady is returned when the simulator is used before setup has
// prepared the database.
var ErrNotReady = errors.New("run setup first")

// ErrQueryBusy is returned when a query is run while another is running.
var ErrQueryBusy = errors.New("a query is already running")

// ErrClosed is returned once the pipeline is closed.
var ErrClosed = errors.New("pipeline is shutting down")

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
	{StepLatency, "Start Tableflow metrics monitor"},
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

// Deps are the operations the pipeline sequences. server.NewDeps wires the real
// implementations; tests substitute fakes.
type Deps struct {
	CheckDocker func(ctx context.Context) error
	Provision   func(ctx context.Context, c config.Credentials, o terraform.ApplyOptions) (terraform.Outputs, error)
	StartLocal  func(ctx context.Context, out terraform.Outputs) (compose.MySQLSecrets, error)
	PrepareDB   func(ctx context.Context, s compose.MySQLSecrets) (sim.Store, io.Closer, error)
	StartCDC    func(ctx context.Context, out terraform.Outputs, s compose.MySQLSecrets) error
	WaitSchemas func(ctx context.Context, out terraform.Outputs) error
	// DisableTableflow turns Tableflow off on the topics before their owner deletes
	// them. The server sets it.
	DisableTableflow func(ctx context.Context) error
	// NewQuerier returns the reader of Tableflow's freshness metrics. The
	// Metrics API takes a Cloud API key, so it gets the operator's.
	NewQuerier func(c config.Credentials, out terraform.Outputs) (latency.Querier, error)
	// RunQuery runs the operator's SQL in the DuckDB container, with the
	// Tableflow catalog attached when out says Tableflow is enabled.
	RunQuery     func(ctx context.Context, out terraform.Outputs, sql string) (query.Result, error)
	PollInterval time.Duration
	// Seed seeds the simulator; zero picks a time-based seed.
	Seed uint64
	// Titles replaces the display title of a step. The defaults name steps that
	// run Docker and Terraform; the server overrides them for the ECS task,
	// where Launchpad and the task provide those.
	Titles map[StepID]string
}

// Pipeline runs setup and owns the simulator and monitor.
type Pipeline struct {
	deps      Deps
	log       *slog.Logger
	onStep    func(StepState)
	onLatency func(latency.Result)

	busy      sync.Mutex // held for the duration of Setup
	queryBusy sync.Mutex // held while a query runs

	mu          sync.Mutex
	steps       map[StepID]StepState
	sim         *sim.Simulator
	dbCloser    io.Closer
	stopMonitor context.CancelFunc
	monitorDone chan struct{}
	outputs     *terraform.Outputs
	// closed is set by Close so nothing new (simulator, monitor, connection)
	// starts while resources are being released.
	closed bool
	// rate is the last event rate requested, reused when setup restarts
	// the simulator.
	rate float64
}

// New returns a Pipeline. onStep and onLatency are called from background
// goroutines and must not block.
func New(deps Deps, log *slog.Logger, onStep func(StepState), onLatency func(latency.Result)) *Pipeline {
	p := &Pipeline{deps: deps, log: log.With("component", "pipeline"), onStep: onStep, onLatency: onLatency, rate: DefaultRate}
	p.resetSteps()
	return p
}

func (p *Pipeline) resetSteps() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.steps = make(map[StepID]StepState, len(Steps))
	for _, s := range Steps {
		p.steps[s.ID] = StepState{ID: s.ID, Title: p.stepTitle(s.ID), Status: StatusPending}
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

func (p *Pipeline) stepTitle(id StepID) string {
	if t, ok := p.deps.Titles[id]; ok {
		return t
	}
	for _, s := range Steps {
		if s.ID == id {
			return s.Title
		}
	}
	return string(id)
}

// run executes one step, recording its outcome.
func (p *Pipeline) run(id StepID, fn func() (string, error)) error {
	p.update(id, StatusRunning, "")
	title := p.stepTitle(id)
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
	p.mu.Lock()
	closed := p.closed
	p.mu.Unlock()
	if closed {
		return ErrClosed
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
			if err != nil {
				return "", err
			}
			out = o
			p.mu.Lock()
			p.outputs = &o
			p.mu.Unlock()
			return fmt.Sprintf("Environment %s, cluster %s", o.EnvironmentID, o.KafkaClusterID), nil
		}},
		{StepServices, func() (string, error) {
			s, err := p.deps.StartLocal(ctx, out)
			if err != nil {
				return "", err
			}
			secrets = s
			return "", nil
		}},
		{StepSchema, func() (string, error) {
			store, closer, err := p.deps.PrepareDB(ctx, secrets)
			if err != nil {
				return "", err
			}
			if err := p.attachStore(store, closer); err != nil {
				return "", err
			}
			return "patients, appointments, visits", nil
		}},
		{StepConnector, func() (string, error) { return "", p.deps.StartCDC(ctx, out, secrets) }},
		{StepSimulate, func() (string, error) {
			// Debezium registers a topic's schema with its first record, so
			// data must flow before Tableflow can be enabled.
			p.mu.Lock()
			rate := p.rate
			p.mu.Unlock()
			err := p.StartSimulation(ctx, rate)
			if errors.Is(err, sim.ErrAlreadyRunning) {
				return "Already running", nil
			}
			return fmt.Sprintf("%g events/sec", rate), err
		}},
		{StepSchemas, func() (string, error) { return "", p.deps.WaitSchemas(ctx, out) }},
		{StepTableflow, func() (string, error) {
			o, err := p.deps.Provision(ctx, creds, terraform.ApplyOptions{EnableTableflow: true})
			if err == nil {
				out = o
				// The query window attaches the catalog as soon as Tableflow
				// is on, even if a later step fails.
				p.mu.Lock()
				p.outputs = &o
				p.mu.Unlock()
			}
			return "Iceberg tables with Confluent managed storage", err
		}},
		{StepLatency, func() (string, error) { return "", p.startMonitor(creds, out) }},
	}
	for _, s := range steps {
		if err := p.run(s.id, s.fn); err != nil {
			return err
		}
	}
	p.mu.Lock()
	p.outputs = &out
	p.mu.Unlock()
	p.log.Info("PIMS demo is ready")
	return nil
}

func (p *Pipeline) attachStore(store sim.Store, closer io.Closer) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		_ = closer.Close()
		return ErrClosed
	}
	if p.sim != nil {
		// Re-running setup: keep the existing simulator and connection.
		_ = closer.Close()
		return nil
	}
	seed := p.deps.Seed
	if seed == 0 {
		seed = uint64(time.Now().UnixNano())
	}
	p.sim = sim.New(store, p.log, seed)
	p.dbCloser = closer
	return nil
}

func (p *Pipeline) startMonitor(creds config.Credentials, out terraform.Outputs) error {
	q, err := p.deps.NewQuerier(creds, out)
	if err != nil {
		return err
	}
	p.stopLatencyMonitor()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	m := latency.NewMonitor(q, p.deps.PollInterval, p.log)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		cancel()
		return ErrClosed
	}
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
	if p.closed {
		return nil, ErrClosed
	}
	if p.sim == nil {
		return nil, ErrNotReady
	}
	return p.sim, nil
}

// StartSimulation starts generating events at rate events per second. The
// simulation runs until StopSimulation or Close, independent of
// ctx's lifetime beyond startup.
func (p *Pipeline) StartSimulation(ctx context.Context, rate float64) error {
	s, err := p.simulator()
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.rate = rate
	p.mu.Unlock()
	// Detach from the caller's context: a request context ends when the
	// request returns, but the simulation should keep running.
	return s.Start(context.WithoutCancel(ctx), rate)
}

// SetRate changes the simulator's event rate.
func (p *Pipeline) SetRate(rate float64) error {
	s, err := p.simulator()
	if err != nil {
		return err
	}
	s.SetRate(rate)
	p.mu.Lock()
	p.rate = rate
	p.mu.Unlock()
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

// QueryInfo tells the UI what the query window can reach. It is empty until
// Tableflow is enabled, when queries run in plain DuckDB.
type QueryInfo struct {
	// Catalog is the DuckDB catalog the Tableflow tables are attached as.
	Catalog string `json:"catalog,omitempty"`
	// Namespace is the schema in Catalog that holds the tables.
	Namespace string   `json:"namespace,omitempty"`
	Tables    []string `json:"tables,omitempty"`
}

// queryOutputs returns the outputs to run queries with, which carry the
// Tableflow catalog credentials. They are those of a setup run in this
// session, and only once it has started the DuckDB container: Terraform's
// outputs are known long before that. The caller must hold p.mu.
func (p *Pipeline) queryOutputs() (*terraform.Outputs, error) {
	switch {
	case p.closed:
		return nil, ErrClosed
	case p.outputs == nil || p.steps[StepServices].Status != StatusDone:
		return nil, ErrNotReady
	}
	return p.outputs, nil
}

// QueryInfo returns where the Tableflow tables can be queried from.
func (p *Pipeline) QueryInfo() QueryInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	out, err := p.queryOutputs()
	if err != nil || !out.TableflowEnabled {
		return QueryInfo{}
	}
	return QueryInfo{Catalog: query.CatalogName, Namespace: out.KafkaClusterID, Tables: out.Topics}
}

// Query runs sql in the DuckDB container and returns the last result set.
// One query runs at a time.
func (p *Pipeline) Query(ctx context.Context, sql string) (query.Result, error) {
	p.mu.Lock()
	out, err := p.queryOutputs()
	p.mu.Unlock()
	if err != nil {
		return query.Result{}, err
	}
	if !p.queryBusy.TryLock() {
		return query.Result{}, ErrQueryBusy
	}
	defer p.queryBusy.Unlock()
	return p.deps.RunQuery(ctx, *out, sql)
}

// DisableTableflow turns Tableflow off, so the topics can be deleted by whoever owns them.
// It does nothing if the dep is not set. Call Close first: a setup that is still running
// could otherwise enable Tableflow again after this has disabled it.
func (p *Pipeline) DisableTableflow(ctx context.Context) error {
	if p.deps.DisableTableflow == nil {
		return nil
	}
	if err := p.deps.DisableTableflow(ctx); err != nil {
		return err
	}
	p.mu.Lock()
	if p.outputs != nil {
		p.outputs.TableflowEnabled = false // the query window must not attach a catalog that is gone
	}
	p.mu.Unlock()
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

// Close stops background work. It deletes nothing. A Setup still in progress
// will not start anything new after Close.
func (p *Pipeline) Close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
	p.shutdownLocal()
}
