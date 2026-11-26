package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"apidiag/internal/health"
	"apidiag/internal/reports"
	"apidiag/internal/simulator"
)

const (
	// Backlogs are what a tab opened later reads to catch up. The Flink job emits an
	// event only when an API turns unhealthy or recovers, so without them a new tab
	// would show every API as healthy until the next change.
	maxHealthBacklog = 500
	maxReportBacklog = 200

	// Limits on what the UI may ask the simulator to do. The page is open to anyone
	// holding the access token, and the events go to a Kafka cluster that is billed.
	maxRate = 100.0
	// minRate is the simulator's own floor: below it the ticker's interval overflows.
	minRate      = 0.01
	maxLatencyMs = 10_000

	// errorEvery is how often a stream of failures is reported, to the log and to the UI.
	errorEvery = 5 * time.Second
)

// Deps are the parts of the service that need Kafka. NewDeps makes the real ones;
// tests supply fakes.
type Deps struct {
	// NewSink makes the destination of the simulator's events, and a function that closes it.
	NewSink func() (simulator.Sink, func(), error)
	// RunHealth reads the unhealthy-events topic from its tail until ctx ends. It returns
	// an error only if it could not start or broke; the service starts it again.
	RunHealth func(ctx context.Context, onEvent func(health.HealthEvent), onErr func(error)) error
	// RunReports is RunHealth for the AI report topic.
	RunReports func(ctx context.Context, onReport func(reports.Report), onErr func(error)) error
}

// Service is the demo behind the web UI: the simulator, the two Kafka readers that feed
// the monitor and the report list, and the browsers watching them.
type Service struct {
	cfg  Config
	log  *slog.Logger
	deps Deps
	hub  *hub

	// RetryEvery is how long to wait before starting a reader that stopped.
	RetryEvery time.Duration

	engine    *simulator.Engine
	closeSink func()

	mu      sync.Mutex
	current []simulator.APIConfig
	health  []health.HealthEvent
	reports []reports.Report

	started   bool
	closed    bool
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	lastError map[string]*atomic.Int64
}

// New builds the service. Nothing runs until Start.
func New(cfg Config, log *slog.Logger, deps Deps) (*Service, error) {
	sink, closeSink, err := deps.NewSink()
	if err != nil {
		return nil, fmt.Errorf("creating the Kafka producer: %w", err)
	}
	s := &Service{
		cfg:        cfg,
		log:        log,
		deps:       deps,
		hub:        newHub(),
		RetryEvery: 10 * time.Second,
		closeSink:  closeSink,
		current:    simulator.DefaultAPIs(),
		lastError:  map[string]*atomic.Int64{"simulator": {}, "health": {}, "reports": {}},
	}
	s.engine = simulator.NewEngine(sink,
		func(api string, err error) {
			if s.throttled("simulator") {
				return
			}
			s.log.Warn("Could not produce a gateway log", "api", api, "error", err)
			s.hub.publish("simulator:error", map[string]string{"api": api, "error": err.Error()})
		},
		func(api string, st simulator.APIStats) { s.hub.publish("api:stats:"+api, st) },
	)
	return s, nil
}

// throttled reports whether kind has already reported a failure in the last errorEvery,
// and if not, records that it is about to.
func (s *Service) throttled(kind string) bool {
	now := time.Now().UnixNano()
	last := s.lastError[kind]
	prev := last.Load()
	if prev != 0 && now-prev < int64(errorEvery) {
		return true
	}
	return !last.CompareAndSwap(prev, now)
}

// Start begins reading the monitor and report topics, and starts the simulation if the
// configuration says so. It returns at once.
func (s *Service) Start(ctx context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	ctx, s.cancel = context.WithCancel(ctx)
	s.mu.Unlock()

	s.supervise(ctx, "health", func(ctx context.Context) error {
		return s.deps.RunHealth(ctx, s.addHealth, func(err error) { s.readerError("health", err) })
	})
	s.supervise(ctx, "reports", func(ctx context.Context) error {
		return s.deps.RunReports(ctx, s.addReport, func(err error) { s.readerError("reports", err) })
	})

	if s.cfg.AutoStart {
		if err := s.StartSimulation(nil); err != nil {
			s.log.Error("Could not start the simulation", "error", err)
		}
	}
}

// supervise runs a reader until ctx ends, starting it again if it stops.
func (s *Service) supervise(ctx context.Context, kind string, run func(context.Context) error) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for ctx.Err() == nil {
			err := run(ctx)
			if ctx.Err() != nil {
				return
			}
			if err == nil {
				err = errors.New("it stopped")
			}
			s.readerError(kind, fmt.Errorf("the %s reader stopped: %w", kind, err))
			select {
			case <-ctx.Done():
				return
			case <-time.After(s.RetryEvery):
			}
		}
	}()
}

func (s *Service) readerError(kind string, err error) {
	if s.throttled(kind) {
		return
	}
	s.log.Warn("Kafka reader failed", "reader", kind, "error", err)
	s.hub.publish(map[string]string{"health": "health:error", "reports": "report:error"}[kind], err.Error())
}

func (s *Service) addHealth(ev health.HealthEvent) {
	s.mu.Lock()
	s.health = append(s.health, ev)
	if n := len(s.health); n > maxHealthBacklog {
		s.health = append([]health.HealthEvent(nil), s.health[n-maxHealthBacklog:]...)
	}
	s.mu.Unlock()
	s.hub.publish("health:event", ev)
}

func (s *Service) addReport(r reports.Report) {
	s.mu.Lock()
	s.reports = append(s.reports, r)
	if n := len(s.reports); n > maxReportBacklog {
		s.reports = append([]reports.Report(nil), s.reports[n-maxReportBacklog:]...)
	}
	s.mu.Unlock()
	s.hub.publish("report:event", r)
}

// Close stops everything and waits up to grace for the readers to finish.
func (s *Service) Close(grace time.Duration) {
	s.mu.Lock()
	// From here no request may start the simulation against the producer closed below.
	s.closed = true
	cancel := s.cancel
	s.engine.Stop()
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.hub.close()
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(grace):
		s.log.Warn("The Kafka readers did not stop in time")
	}
	s.closeSink()
}

// State is what the page needs to draw itself.
type State struct {
	ClusterName   string `json:"clusterName"`
	Provider      string `json:"provider"`
	Region        string `json:"region"`
	EnvironmentID string `json:"environmentId"`
	ClusterID     string `json:"clusterId"`
	TopicName     string `json:"topicName"`
	// AIReports is whether an AI agent was deployed to write the reports.
	AIReports bool `json:"aiReports"`
	Running   bool `json:"running"`
}

// State reports what the page shows in its header and which tabs have something to say.
func (s *Service) State() State {
	return State{
		ClusterName:   s.cfg.DemoName,
		Provider:      "AWS",
		Region:        s.cfg.AWSRegion,
		EnvironmentID: s.cfg.EnvironmentID,
		ClusterID:     s.cfg.KafkaClusterID,
		TopicName:     s.cfg.Topic,
		AIReports:     s.cfg.AIReports,
		Running:       s.engine.IsRunning(),
	}
}

// APIs is the simulated APIs as they are now, next to their defaults, which Reset returns to.
type APIs struct {
	Current  []simulator.APIConfig `json:"current"`
	Defaults []simulator.APIConfig `json:"defaults"`
}

// APIs returns copies, so a caller cannot change the service's settings.
func (s *Service) APIs() APIs {
	s.mu.Lock()
	defer s.mu.Unlock()
	return APIs{Current: cloneAll(s.current), Defaults: simulator.DefaultAPIs()}
}

func cloneAll(in []simulator.APIConfig) []simulator.APIConfig {
	out := make([]simulator.APIConfig, len(in))
	for i, c := range in {
		c.DependsOn = append([]string{}, c.DependsOn...)
		out[i] = c
	}
	return out
}

// ErrInvalid marks a request the simulator was not asked to do, because of what it said.
var ErrInvalid = errors.New("invalid request")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalid, fmt.Sprintf(format, args...))
}

// apply validates edits and merges them into the current settings. Only the adjustable
// fields come from the caller: an API's name and path are the service's.
func apply(current []simulator.APIConfig, edits []simulator.APIConfig) ([]simulator.APIConfig, error) {
	next := cloneAll(current)
	index := map[string]int{}
	for i, c := range next {
		index[c.Name] = i
	}
	for _, e := range edits {
		i, ok := index[e.Name]
		if !ok {
			return nil, invalid("%q is not one of the simulated APIs", e.Name)
		}
		if e.Rate != 0 && (e.Rate < minRate || e.Rate > maxRate) {
			return nil, invalid("%s: the rate must be 0, or between %g and %g events a second", e.Name, minRate, maxRate)
		}
		for _, pct := range []float64{e.Pct500, e.Pct403, e.Pct404} {
			if pct < 0 || pct > 100 {
				return nil, invalid("%s: a percentage must be between 0 and 100", e.Name)
			}
		}
		if e.LatencyMs < 1 || e.LatencyMs > maxLatencyMs {
			return nil, invalid("%s: the latency must be between 1 and %d ms", e.Name, maxLatencyMs)
		}
		seen := map[string]bool{}
		deps := []string{}
		for _, d := range e.DependsOn {
			if _, ok := index[d]; !ok {
				return nil, invalid("%s: %q is not one of the simulated APIs", e.Name, d)
			}
			if d == e.Name {
				return nil, invalid("%s cannot depend on itself", e.Name)
			}
			if !seen[d] {
				seen[d] = true
				deps = append(deps, d)
			}
		}
		c := &next[i]
		c.Rate, c.Pct500, c.Pct403, c.Pct404 = e.Rate, e.Pct500, e.Pct403, e.Pct404
		c.LatencyMs, c.GatewayError, c.DependsOn = e.LatencyMs, e.GatewayError, deps
	}
	return next, nil
}

// ErrClosed is what a change asked of a service that is shutting down gets.
var ErrClosed = errors.New("the demo is shutting down")

// The three calls below hold s.mu while they call the engine, so the settings the page
// shows and the ones the engine runs cannot disagree. The engine's lock is a leaf: its
// callbacks publish events and never take s.mu.

// StartSimulation applies edits, if any, and starts producing. It does nothing more
// if the simulation is already running, so two tabs clicking Start agree.
func (s *Service) StartSimulation(edits []simulator.APIConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return ErrClosed
	}
	next, err := apply(s.current, edits)
	if err != nil {
		return err
	}
	s.current = next
	snapshot := cloneAll(next)

	if s.engine.IsRunning() {
		for _, c := range snapshot {
			s.engine.UpdateConfig(c)
		}
		return nil
	}
	s.engine.Start(snapshot)
	s.hub.publish("simulator:state", map[string]bool{"running": true})
	return nil
}

// UpdateAPI changes one API. The change is kept whether or not the simulation is
// running, and takes effect at once if it is.
func (s *Service) UpdateAPI(edit simulator.APIConfig) (running bool, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false, ErrClosed
	}
	next, err := apply(s.current, []simulator.APIConfig{edit})
	if err != nil {
		return false, err
	}
	s.current = next
	for _, c := range next {
		if c.Name == edit.Name {
			return s.engine.UpdateConfig(cloneAll([]simulator.APIConfig{c})[0]), nil
		}
	}
	return false, nil
}

// StopSimulation stops producing. It is safe to call when nothing is running.
func (s *Service) StopSimulation() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopLocked()
}

func (s *Service) stopLocked() {
	if s.engine.IsRunning() {
		s.engine.Stop()
		s.hub.publish("simulator:state", map[string]bool{"running": false})
	}
}

// HealthBacklog is the monitor's recent events, oldest first.
func (s *Service) HealthBacklog() []health.HealthEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]health.HealthEvent{}, s.health...)
}

// ReportBacklog is the AI reports so far, oldest first.
func (s *Service) ReportBacklog() []reports.Report {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]reports.Report{}, s.reports...)
}
