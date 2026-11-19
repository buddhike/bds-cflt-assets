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
			return terraform.Outputs{EnvironmentID: "env-1", KafkaClusterID: "lkc-1", Topics: []string{"patients"}}, nil
		},
		Destroy: func(context.Context, config.Credentials) error { r.add("destroy"); return nil },
		StartLocal: func(context.Context, terraform.Outputs) (compose.MySQLSecrets, error) {
			r.add("up")
			return compose.MySQLSecrets{Generation: "g1"}, nil
		},
		StopLocal: func(_ context.Context, rm bool) error {
			if rm {
				r.add("down-volumes")
			} else {
				r.add("down")
			}
			return nil
		},
		PrepareDB: func(context.Context, compose.MySQLSecrets) (sim.Store, io.Closer, error) {
			r.add("schema")
			return nopStore{}, closerFunc(func() error { r.add("db-close"); return nil }), nil
		},
		StartCDC:    func(context.Context, terraform.Outputs, compose.MySQLSecrets) error { r.add("cdc"); return nil },
		StopCDC:     func(context.Context, compose.MySQLSecrets) error { r.add("cdc-delete"); return nil },
		WaitSchemas: func(context.Context, terraform.Outputs) error { r.add("schemas"); return nil },
		NewQuerier: func(c config.Credentials, o terraform.Outputs) (latency.Querier, error) {
			if c != creds || o.KafkaClusterID != "lkc-1" {
				return nil, fmt.Errorf("monitor got credentials %v and cluster %q", c, o.KafkaClusterID)
			}
			r.add("monitor")
			return fakeQuerier{}, nil
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

func TestTeardownDestroysEverythingEvenIfLocalStopFails(t *testing.T) {
	r := &recorder{}
	deps := fakeDeps(r)
	p := New(deps, slog.New(slog.DiscardHandler), nil, nil)
	if err := p.Setup(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
	p.deps.StopLocal = func(context.Context, bool) error { r.add("down-failed"); return errors.New("docker gone") }

	err := p.Teardown(context.Background(), creds)
	if err == nil {
		t.Fatal("Teardown() = nil, want the local error reported")
	}
	got := r.list()
	if !slices.Contains(got, "destroy") || !slices.Contains(got, "db-close") || !slices.Contains(got, "cdc-delete") {
		t.Fatalf("calls = %v, want connector delete, db close and cloud destroy", got)
	}
	if p.SimStats().Running {
		t.Fatal("simulator still running after teardown")
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

func TestTeardownAfterPartialSetupDeletesConnector(t *testing.T) {
	r := &recorder{}
	deps := fakeDeps(r)
	deps.WaitSchemas = func(context.Context, terraform.Outputs) error { return errors.New("timed out") }
	p := New(deps, slog.New(slog.DiscardHandler), nil, nil)
	if err := p.Setup(context.Background(), creds); err == nil {
		t.Fatal("Setup() = nil, want failure")
	}
	if err := p.Teardown(context.Background(), creds); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(r.list(), "cdc-delete") {
		t.Fatalf("calls = %v, want the connector deleted after a partial setup", r.list())
	}
	// The pipeline is usable again after teardown.
	if err := p.Setup(context.Background(), creds); errors.Is(err, ErrClosed) {
		t.Fatal("Setup after Teardown must not report closed")
	}
	p.Close()
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
