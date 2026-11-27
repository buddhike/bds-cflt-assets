package pipeline

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"pims/internal/compose"
	"pims/internal/config"
	"pims/internal/latency"
	"pims/internal/query"
	"pims/internal/sim"
	"pims/internal/terraform"
)

var creds = config.Credentials{AWSRegion: "us-east-1", APIKey: "k", APISecret: "s"}

// recorder captures the order of operations the pipeline performs.
type recorder struct {
	mu    sync.Mutex
	calls []string
}

func (r *recorder) add(s string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, s)
}

func (r *recorder) list() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.calls)
}

type nopStore struct{}

func (nopStore) Load(context.Context, time.Time) (sim.Snapshot, error) { return sim.Snapshot{}, nil }
func (nopStore) InsertPatient(context.Context, sim.Patient) (int64, error) {
	return 1, nil
}
func (nopStore) UpdatePatientContact(context.Context, int64, sim.ContactChange) error { return nil }
func (nopStore) InsertAppointment(context.Context, sim.Appointment) (int64, error) {
	return 1, nil
}
func (nopStore) SetAppointmentStatus(context.Context, int64, string) error { return nil }
func (nopStore) CheckIn(context.Context, sim.Visit) (int64, error)         { return 1, nil }
func (nopStore) CompleteVisit(context.Context, sim.Visit, time.Time, string) error {
	return nil
}

type closerFunc func() error

func (f closerFunc) Close() error { return f() }

type fakeQuerier struct{}

func (fakeQuerier) Query(context.Context) (latency.Result, error) {
	return latency.Result{Event: []latency.Sample{{Table: "patients", Time: time.Now(), Ms: 1000}}}, nil
}

func fakeDeps(r *recorder) Deps {
	return Deps{
		CheckDocker: func(context.Context) error { r.add("docker"); return nil },
		Provision: func(_ context.Context, _ config.Credentials, o terraform.ApplyOptions) (terraform.Outputs, error) {
			if o.EnableTableflow {
				r.add("tableflow")
			} else {
				r.add("provision")
			}
			return terraform.Outputs{EnvironmentID: "env-1", KafkaClusterID: "lkc-1", Topics: []string{"patients"}, TableflowEnabled: o.EnableTableflow}, nil
		},
		StartLocal: func(context.Context, terraform.Outputs) (compose.MySQLSecrets, error) {
			r.add("up")
			return compose.MySQLSecrets{Generation: "g1"}, nil
		},
		PrepareDB: func(context.Context, compose.MySQLSecrets) (sim.Store, io.Closer, error) {
			r.add("schema")
			return nopStore{}, closerFunc(func() error { r.add("db-close"); return nil }), nil
		},
		StartCDC:    func(context.Context, terraform.Outputs, compose.MySQLSecrets) error { r.add("cdc"); return nil },
		WaitSchemas: func(context.Context, terraform.Outputs) error { r.add("schemas"); return nil },
		NewQuerier: func(c config.Credentials, o terraform.Outputs) (latency.Querier, error) {
			if c != creds || o.KafkaClusterID != "lkc-1" {
				return nil, fmt.Errorf("monitor got credentials %v and cluster %q", c, o.KafkaClusterID)
			}
			r.add("monitor")
			return fakeQuerier{}, nil
		},
		RunQuery: func(_ context.Context, o terraform.Outputs, sql string) (query.Result, error) {
			r.add(fmt.Sprintf("query tableflow=%t %s", o.TableflowEnabled, sql))
			return query.Result{Columns: []string{"a"}, TotalRows: 1}, nil
		},
		PollInterval: time.Hour,
		Seed:         1,
	}
}

func TestSetupRunsStepsInOrder(t *testing.T) {
	r := &recorder{}
	var latencyResults sync.WaitGroup
	latencyResults.Add(1)
	var once sync.Once
	p := New(fakeDeps(r), slog.New(slog.DiscardHandler), nil, func(latency.Result) { once.Do(latencyResults.Done) })
	defer p.Close()

	if err := p.Setup(context.Background(), creds); err != nil {
		t.Fatalf("Setup() = %v", err)
	}
	want := []string{"docker", "provision", "up", "schema", "cdc", "schemas", "tableflow", "monitor"}
	if got := r.list(); !slices.Equal(got, want) {
		t.Fatalf("calls = %v, want %v", got, want)
	}
	for _, s := range p.StepStates() {
		if s.Status != StatusDone {
			t.Errorf("step %s = %s, want done", s.ID, s.Status)
		}
	}
	if !p.SimStats().Running {
		t.Fatal("simulator must be running before Tableflow is enabled")
	}
	latencyResults.Wait() // the monitor polls immediately
}

func TestSetupStopsAtFailedStep(t *testing.T) {
	r := &recorder{}
	deps := fakeDeps(r)
	deps.StartCDC = func(context.Context, terraform.Outputs, compose.MySQLSecrets) error {
		return errors.New("access denied for user debezium")
	}
	var steps []StepState
	var mu sync.Mutex
	p := New(deps, slog.New(slog.DiscardHandler), func(s StepState) { mu.Lock(); steps = append(steps, s); mu.Unlock() }, nil)
	defer p.Close()

	err := p.Setup(context.Background(), creds)
	if err == nil {
		t.Fatal("Setup() = nil, want error")
	}
	if slices.Contains(r.list(), "tableflow") {
		t.Fatal("tableflow must not be enabled after a failed step")
	}
	states := map[StepID]string{}
	for _, s := range p.StepStates() {
		states[s.ID] = s.Status
	}
	if states[StepConnector] != StatusFailed || states[StepTableflow] != StatusPending {
		t.Fatalf("states = %v", states)
	}
}

func TestSetupRejectsConcurrentRunsAndBadCredentials(t *testing.T) {
	r := &recorder{}
	block := make(chan struct{})
	deps := fakeDeps(r)
	deps.CheckDocker = func(context.Context) error { <-block; return nil }
	p := New(deps, slog.New(slog.DiscardHandler), nil, nil)
	defer p.Close()

	done := make(chan error, 1)
	go func() { done <- p.Setup(context.Background(), creds) }()
	// Wait for the first Setup to hold the lock.
	for p.busy.TryLock() {
		p.busy.Unlock()
		time.Sleep(time.Millisecond)
	}
	if err := p.Setup(context.Background(), creds); !errors.Is(err, ErrBusy) {
		t.Fatalf("concurrent Setup() = %v, want ErrBusy", err)
	}
	close(block)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := p.Setup(context.Background(), config.Credentials{}); !errors.Is(err, config.ErrInvalidCredentials) {
		t.Fatalf("Setup(empty) = %v, want ErrInvalidCredentials", err)
	}
}

func TestSimulatorNeedsSetup(t *testing.T) {
	p := New(fakeDeps(&recorder{}), slog.New(slog.DiscardHandler), nil, nil)
	if err := p.StartSimulation(context.Background(), 1); !errors.Is(err, ErrNotReady) {
		t.Fatalf("StartSimulation() = %v, want ErrNotReady", err)
	}
}

func TestCloseDuringSetupStartsNothingNew(t *testing.T) {
	r := &recorder{}
	deps := fakeDeps(r)
	release := make(chan struct{})
	deps.StartLocal = func(context.Context, terraform.Outputs) (compose.MySQLSecrets, error) {
		<-release // Close happens while services are starting
		return compose.MySQLSecrets{Generation: "g1"}, nil
	}
	p := New(deps, slog.New(slog.DiscardHandler), nil, nil)

	done := make(chan error, 1)
	go func() { done <- p.Setup(context.Background(), creds) }()
	for !slices.Contains(r.list(), "provision") {
		time.Sleep(time.Millisecond)
	}
	p.Close()
	close(release)

	if err := <-done; !errors.Is(err, ErrClosed) {
		t.Fatalf("Setup() after Close = %v, want ErrClosed", err)
	}
	if !slices.Contains(r.list(), "db-close") {
		t.Fatal("connection opened after Close must be closed again")
	}
	if p.SimStats().Running {
		t.Fatal("simulator started after Close")
	}
}

func TestSetupReusesOperatorRate(t *testing.T) {
	p := New(fakeDeps(&recorder{}), slog.New(slog.DiscardHandler), nil, nil)
	defer p.Close()
	ctx := context.Background()
	if err := p.Setup(ctx, creds); err != nil {
		t.Fatal(err)
	}
	if err := p.SetRate(25); err != nil {
		t.Fatal(err)
	}
	if err := p.StopSimulation(); err != nil {
		t.Fatal(err)
	}
	if err := p.Setup(ctx, creds); err != nil {
		t.Fatal(err)
	}
	if got := p.SimStats().Rate; got != 25 {
		t.Fatalf("rate after re-running setup = %v, want 25", got)
	}
}

func TestQueryNeedsSetup(t *testing.T) {
	r := &recorder{}
	p := New(fakeDeps(r), slog.New(slog.DiscardHandler), nil, nil)
	if _, err := p.Query(context.Background(), "SELECT 1"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Query() = %v, want ErrNotReady", err)
	}
	if got := p.QueryInfo(); got.Catalog != "" || len(got.Tables) != 0 {
		t.Fatalf("QueryInfo() = %+v before setup, want none", got)
	}
	if len(r.list()) != 0 {
		t.Fatalf("calls = %v, want none", r.list())
	}
}

func TestQueryRunsWithTheOutputsOfSetup(t *testing.T) {
	r := &recorder{}
	p := New(fakeDeps(r), slog.New(slog.DiscardHandler), nil, nil)
	defer p.Close()
	if err := p.Setup(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
	res, err := p.Query(context.Background(), "SELECT 1")
	if err != nil || res.TotalRows != 1 {
		t.Fatalf("Query() = %+v, %v", res, err)
	}
	if got := r.list(); got[len(got)-1] != "query tableflow=true SELECT 1" {
		t.Fatalf("calls = %v, want the query to see Tableflow enabled", got)
	}
	want := QueryInfo{Catalog: "tableflow", Namespace: "lkc-1", Tables: []string{"patients"}}
	if got := p.QueryInfo(); got.Catalog != want.Catalog || got.Namespace != want.Namespace || !slices.Equal(got.Tables, want.Tables) {
		t.Fatalf("QueryInfo() = %+v, want %+v", got, want)
	}
}

// A setup that fails after Tableflow is enabled leaves its tables there to
// query.
func TestQuerySeesTableflowAfterALaterStepFails(t *testing.T) {
	r := &recorder{}
	deps := fakeDeps(r)
	deps.NewQuerier = func(config.Credentials, terraform.Outputs) (latency.Querier, error) {
		return nil, errors.New("metrics api unreachable")
	}
	p := New(deps, slog.New(slog.DiscardHandler), nil, nil)
	defer p.Close()
	if err := p.Setup(context.Background(), creds); err == nil {
		t.Fatal("Setup() = nil, want the monitor failure")
	}
	if _, err := p.Query(context.Background(), "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	if got := r.list(); got[len(got)-1] != "query tableflow=true SELECT 1" {
		t.Fatalf("calls = %v, want the query to see Tableflow enabled", got)
	}
	if p.QueryInfo().Catalog == "" {
		t.Fatal("QueryInfo() must offer the catalog once Tableflow is enabled")
	}
}

// Before Tableflow is enabled there are no tables, but plain DuckDB works.
func TestQueryBeforeTableflowIsPlainDuckDB(t *testing.T) {
	r := &recorder{}
	deps := fakeDeps(r)
	deps.WaitSchemas = func(context.Context, terraform.Outputs) error { return errors.New("no schemas yet") }
	p := New(deps, slog.New(slog.DiscardHandler), nil, nil)
	defer p.Close()
	if err := p.Setup(context.Background(), creds); err == nil {
		t.Fatal("Setup() = nil, want the schema wait to fail")
	}
	if _, err := p.Query(context.Background(), "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	if got := r.list(); got[len(got)-1] != "query tableflow=false SELECT 1" {
		t.Fatalf("calls = %v, want a query without Tableflow", got)
	}
	if got := p.QueryInfo(); got.Catalog != "" || len(got.Tables) != 0 {
		t.Fatalf("QueryInfo() = %+v, want none", got)
	}
}

func TestQueryStopsWithTheDemo(t *testing.T) {
	r := &recorder{}
	p := New(fakeDeps(r), slog.New(slog.DiscardHandler), nil, nil)
	if err := p.Setup(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
	p.Close()
	if _, err := p.Query(context.Background(), "SELECT 1"); !errors.Is(err, ErrClosed) {
		t.Fatalf("Query() after close = %v, want ErrClosed", err)
	}
}

// Terraform's outputs are known long before the DuckDB container is up, and
// a re-run of setup stops the old container's step from counting.
func TestQueryWaitsForTheDuckDBContainer(t *testing.T) {
	r := &recorder{}
	deps := fakeDeps(r)
	release := make(chan struct{})
	deps.StartLocal = func(context.Context, terraform.Outputs) (compose.MySQLSecrets, error) {
		<-release
		return compose.MySQLSecrets{Generation: "g1"}, nil
	}
	p := New(deps, slog.New(slog.DiscardHandler), nil, nil)
	defer p.Close()
	done := make(chan error, 1)
	go func() { done <- p.Setup(context.Background(), creds) }()
	for { // wait until setup is in the step that starts the containers
		p.mu.Lock()
		running := p.steps[StepServices].Status == StatusRunning
		known := p.outputs != nil
		p.mu.Unlock()
		if running && known {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := p.Query(context.Background(), "SELECT 1"); !errors.Is(err, ErrNotReady) {
		t.Fatalf("Query() while the containers start = %v, want ErrNotReady", err)
	}
	if got := p.QueryInfo(); got.Catalog != "" {
		t.Fatalf("QueryInfo() = %+v while the containers start, want none", got)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, err := p.Query(context.Background(), "SELECT 1"); err != nil {
		t.Fatalf("Query() after setup = %v", err)
	}
}

func TestQueryRunsOneAtATime(t *testing.T) {
	r := &recorder{}
	deps := fakeDeps(r)
	started, release := make(chan struct{}), make(chan struct{})
	deps.RunQuery = func(context.Context, terraform.Outputs, string) (query.Result, error) {
		close(started)
		<-release
		return query.Result{}, nil
	}
	p := New(deps, slog.New(slog.DiscardHandler), nil, nil)
	defer p.Close()
	if err := p.Setup(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { _, err := p.Query(context.Background(), "SELECT 1"); done <- err }()
	<-started
	if _, err := p.Query(context.Background(), "SELECT 2"); !errors.Is(err, ErrQueryBusy) {
		t.Fatalf("second Query() = %v, want ErrQueryBusy", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The guard is released once the first query ends.
	deps.RunQuery = func(context.Context, terraform.Outputs, string) (query.Result, error) { return query.Result{}, nil }
	p.deps = deps
	if _, err := p.Query(context.Background(), "SELECT 3"); err != nil {
		t.Fatalf("Query() after the first ended = %v", err)
	}
}

func TestTitlesCanBeOverridden(t *testing.T) {
	d := fakeDeps(&recorder{})
	d.Titles = map[StepID]string{StepDocker: "Wait for the task"}
	p := New(d, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	got := map[StepID]string{}
	for _, s := range p.StepStates() {
		got[s.ID] = s.Title
	}
	if got[StepDocker] != "Wait for the task" {
		t.Errorf("docker title = %q", got[StepDocker])
	}
	if got[StepCloud] != "Provision Confluent Cloud" {
		t.Errorf("a title without an override changed: %q", got[StepCloud])
	}
}

func TestDisableTableflowIsNothingWithoutADep(t *testing.T) {
	p := New(Deps{}, slog.New(slog.NewTextHandler(io.Discard, nil)), nil, nil)
	if err := p.DisableTableflow(context.Background()); err != nil {
		t.Fatal(err)
	}
}
