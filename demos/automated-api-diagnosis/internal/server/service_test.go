package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"apidiag/internal/health"
	"apidiag/internal/reports"
	"apidiag/internal/simulator"
)

// sink counts events instead of sending them.
type sink struct {
	mu     sync.Mutex
	events []simulator.KongLogEvent
	err    error
}

func (s *sink) Produce(_ context.Context, _ string, ev simulator.KongLogEvent, onErr func(error)) {
	s.mu.Lock()
	s.events = append(s.events, ev)
	s.mu.Unlock()
	if s.err != nil && onErr != nil {
		onErr(s.err)
	}
}

func (s *sink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

// feed lets a test push events into the readers.
type feed struct {
	health  chan health.HealthEvent
	reports chan reports.Report
	starts  atomic.Int32
	failing atomic.Bool
}

func newFeed() *feed {
	return &feed{health: make(chan health.HealthEvent, 8), reports: make(chan reports.Report, 8)}
}

func (f *feed) deps(s *sink) Deps {
	return Deps{
		NewSink: func() (simulator.Sink, func(), error) { return s, func() {}, nil },
		RunHealth: func(ctx context.Context, on func(health.HealthEvent), _ func(error)) error {
			f.starts.Add(1)
			if f.failing.Load() {
				return errors.New("no route to Kafka")
			}
			for {
				select {
				case <-ctx.Done():
					return nil
				case ev := <-f.health:
					on(ev)
				}
			}
		},
		RunReports: func(ctx context.Context, on func(reports.Report), _ func(error)) error {
			for {
				select {
				case <-ctx.Done():
					return nil
				case r := <-f.reports:
					on(r)
				}
			}
		},
	}
}

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func testConfig() Config {
	c, err := LoadConfig(getenv(fullEnv()))
	if err != nil {
		panic(err)
	}
	c.AutoStart = false
	return c
}

func newService(t *testing.T, cfg Config) (*Service, *sink, *feed) {
	t.Helper()
	sk, f := &sink{}, newFeed()
	svc, err := New(cfg, quiet(), f.deps(sk))
	if err != nil {
		t.Fatal(err)
	}
	svc.RetryEvery = 10 * time.Millisecond
	t.Cleanup(func() { svc.Close(time.Second) })
	return svc, sk, f
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestAutoStartProducesForEveryAPI(t *testing.T) {
	cfg := testConfig()
	cfg.AutoStart = true
	svc, sk, _ := newService(t, cfg)
	svc.Start(context.Background())

	if !svc.State().Running {
		t.Fatal("the simulation did not start by itself")
	}
	eventually(t, "events from every API", func() bool {
		sk.mu.Lock()
		defer sk.mu.Unlock()
		seen := map[string]bool{}
		for _, e := range sk.events {
			seen[e.APIName] = true
		}
		return len(seen) == len(simulator.DefaultAPIs())
	})
}

func TestNoAutoStartWaitsForTheUser(t *testing.T) {
	svc, sk, _ := newService(t, testConfig())
	svc.Start(context.Background())
	time.Sleep(50 * time.Millisecond)
	if svc.State().Running || sk.count() != 0 {
		t.Fatal("the simulation ran without being asked")
	}
	if err := svc.StartSimulation(nil); err != nil || !svc.State().Running {
		t.Fatalf("start: %v running=%v", err, svc.State().Running)
	}
	// Starting twice is not an error: two tabs may both click Start.
	if err := svc.StartSimulation(nil); err != nil {
		t.Fatal(err)
	}
	svc.StopSimulation()
	svc.StopSimulation() // safe when stopped
	if svc.State().Running {
		t.Fatal("still running after stop")
	}
}

func TestEditsAreKeptAndApplied(t *testing.T) {
	svc, _, _ := newService(t, testConfig())
	name := simulator.DefaultAPIs()[1].Name

	running, err := svc.UpdateAPI(simulator.APIConfig{Name: name, Rate: 5, Pct500: 10, LatencyMs: 200, DependsOn: []string{simulator.DefaultAPIs()[0].Name, simulator.DefaultAPIs()[0].Name}})
	if err != nil || running {
		t.Fatalf("update while stopped: running=%v err=%v", running, err)
	}
	got := svc.APIs()
	if got.Current[1].Rate != 5 || got.Current[1].Pct500 != 10 || len(got.Current[1].DependsOn) != 1 {
		t.Errorf("the edit was not kept, or the duplicate dependency was: %+v", got.Current[1])
	}
	if got.Defaults[1].Rate == 5 {
		t.Error("the defaults changed")
	}
	if got.Current[1].Path != simulator.DefaultAPIs()[1].Path {
		t.Error("the path is not the caller's to change")
	}

	if err := svc.StartSimulation(nil); err != nil {
		t.Fatal(err)
	}
	if running, err = svc.UpdateAPI(simulator.APIConfig{Name: name, Rate: 6, LatencyMs: 100}); err != nil || !running {
		t.Fatalf("update while running: running=%v err=%v", running, err)
	}
}

func TestReturnedSettingsAreCopies(t *testing.T) {
	svc, _, _ := newService(t, testConfig())
	got := svc.APIs()
	got.Current[0].Rate = 99
	got.Current[0].DependsOn = append(got.Current[0].DependsOn, "X")
	if svc.APIs().Current[0].Rate == 99 {
		t.Fatal("a caller changed the service's settings")
	}
}

func TestEditValidation(t *testing.T) {
	apis := simulator.DefaultAPIs()
	a, b := apis[0].Name, apis[1].Name
	ok := simulator.APIConfig{Name: a, Rate: 1, LatencyMs: 10}
	cases := map[string]func(c *simulator.APIConfig){
		"unknown api":      func(c *simulator.APIConfig) { c.Name = "NOPE" },
		"negative rate":    func(c *simulator.APIConfig) { c.Rate = -1 },
		"overflowing rate": func(c *simulator.APIConfig) { c.Rate = 1e-300 },
		"below the floor":  func(c *simulator.APIConfig) { c.Rate = minRate / 2 },
		"flooding rate":    func(c *simulator.APIConfig) { c.Rate = maxRate + 1 },
		"percentage > 100": func(c *simulator.APIConfig) { c.Pct403 = 101 },
		"negative pct":     func(c *simulator.APIConfig) { c.Pct404 = -1 },
		"zero latency":     func(c *simulator.APIConfig) { c.LatencyMs = 0 },
		"huge latency":     func(c *simulator.APIConfig) { c.LatencyMs = maxLatencyMs + 1 },
		"unknown dep":      func(c *simulator.APIConfig) { c.DependsOn = []string{"NOPE"} },
		"self dep":         func(c *simulator.APIConfig) { c.DependsOn = []string{a} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c := ok
			mutate(&c)
			if _, err := apply(apis, []simulator.APIConfig{c}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted: %v", err)
			}
		})
	}
	good := ok
	good.DependsOn = []string{b}
	if _, err := apply(apis, []simulator.APIConfig{good}); err != nil {
		t.Fatalf("rejected a valid edit: %v", err)
	}
	// A failed edit changes nothing.
	bad := ok
	bad.Rate = -1
	before := apis[0].Rate
	_, _ = apply(apis, []simulator.APIConfig{good, bad})
	if apis[0].Rate != before {
		t.Fatal("apply modified its input")
	}
}

func TestBacklogsCatchUpALateTab(t *testing.T) {
	svc, _, f := newService(t, testConfig())
	svc.Start(context.Background())
	f.health <- health.HealthEvent{APIID: "PAYMENTS_API_V1", HTTPStatus: "500", IsAnomaly: true}
	f.reports <- reports.Report{ID: "0-1", APIID: "PAYMENTS_API_V1", Report: "# why"}
	eventually(t, "both backlogs", func() bool { return len(svc.HealthBacklog()) == 1 && len(svc.ReportBacklog()) == 1 })
	if svc.HealthBacklog()[0].APIID != "PAYMENTS_API_V1" || svc.ReportBacklog()[0].Report != "# why" {
		t.Fatal("the backlog holds the wrong events")
	}
}

func TestBacklogsAreBounded(t *testing.T) {
	svc, _, _ := newService(t, testConfig())
	for i := 0; i < maxHealthBacklog+50; i++ {
		svc.addHealth(health.HealthEvent{StatusCount: int64(i)})
	}
	for i := 0; i < maxReportBacklog+50; i++ {
		svc.addReport(reports.Report{ID: string(rune('a' + i%26))})
	}
	h := svc.HealthBacklog()
	if len(h) != maxHealthBacklog || h[len(h)-1].StatusCount != int64(maxHealthBacklog+49) || h[0].StatusCount != 50 {
		t.Errorf("health backlog: len %d, first %d, last %d", len(h), h[0].StatusCount, h[len(h)-1].StatusCount)
	}
	if len(svc.ReportBacklog()) != maxReportBacklog {
		t.Errorf("report backlog: len %d", len(svc.ReportBacklog()))
	}
}

func TestAReaderThatFailsIsStartedAgain(t *testing.T) {
	svc, _, f := newService(t, testConfig())
	f.failing.Store(true)
	svc.Start(context.Background())
	eventually(t, "a second attempt", func() bool { return f.starts.Load() >= 2 })
	f.failing.Store(false)
	f.health <- health.HealthEvent{APIID: "A"}
	eventually(t, "the recovered reader to deliver", func() bool { return len(svc.HealthBacklog()) == 1 })
}

func TestProducerFailuresAreReportedOncePerInterval(t *testing.T) {
	cfg := testConfig()
	cfg.AutoStart = true
	svc, sk, _ := newService(t, cfg)
	sk.err = errors.New("broker down")
	events, cancel := svc.hub.subscribe()
	defer cancel()
	svc.Start(context.Background())
	eventually(t, "many failing sends", func() bool { return sk.count() > 20 })
	svc.StopSimulation()

	var errs int
	for drained := false; !drained; {
		select {
		case e := <-events:
			if e.name == "simulator:error" {
				errs++
			}
		default:
			drained = true
		}
	}
	if errs != 1 {
		t.Fatalf("got %d error events for one burst of failures, want 1", errs)
	}
}

func TestNewFailsIfTheProducerCannotBeMade(t *testing.T) {
	_, err := New(testConfig(), quiet(), Deps{NewSink: func() (simulator.Sink, func(), error) { return nil, nil, errors.New("bad key") }})
	if err == nil {
		t.Fatal("no error")
	}
}

func TestNothingStartsAfterClose(t *testing.T) {
	svc, _, _ := newService(t, testConfig())
	svc.Start(context.Background())
	svc.Close(time.Second)
	if err := svc.StartSimulation(nil); !errors.Is(err, ErrClosed) {
		t.Fatalf("start after close: %v", err)
	}
	if _, err := svc.UpdateAPI(simulator.APIConfig{Name: simulator.DefaultAPIs()[0].Name, Rate: 1, LatencyMs: 10}); !errors.Is(err, ErrClosed) {
		t.Fatalf("update after close: %v", err)
	}
	if svc.State().Running {
		t.Fatal("the simulation runs after close")
	}
}

func TestConcurrentEditsLeaveTheEngineAndTheSettingsAgreeing(t *testing.T) {
	svc, _, _ := newService(t, testConfig())
	name := simulator.DefaultAPIs()[0].Name
	var wg sync.WaitGroup
	for i := 1; i <= 40; i++ {
		wg.Add(1)
		go func(rate float64) {
			defer wg.Done()
			_ = svc.StartSimulation([]simulator.APIConfig{{Name: name, Rate: rate, LatencyMs: 10}})
			_, _ = svc.UpdateAPI(simulator.APIConfig{Name: name, Rate: rate, LatencyMs: 10})
		}(float64(i))
	}
	wg.Wait()
	if !svc.State().Running || svc.APIs().Current[0].Rate == 0 {
		t.Fatal("the simulation did not end up running with a rate")
	}
}
