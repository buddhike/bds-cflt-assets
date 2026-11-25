package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"pims/internal/compose"
	"pims/internal/config"
	"pims/internal/duckdbagent"
	"pims/internal/pipeline"
	"pims/internal/terraform"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeSignal struct {
	answers []bool
	err     error
	calls   atomic.Int32
}

func (f *fakeSignal) Enabled(context.Context) (bool, error) {
	n := int(f.calls.Add(1)) - 1
	if f.err != nil {
		return false, f.err
	}
	return f.answers[min(n, len(f.answers)-1)], nil
}

func TestWaitForSignalReturnsOnceEnabled(t *testing.T) {
	s := &fakeSignal{answers: []bool{false, false, true}}
	if err := waitForSignal(context.Background(), s, time.Millisecond, time.Second, quiet); err != nil {
		t.Fatal(err)
	}
	if s.calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", s.calls.Load())
	}
}

func TestWaitForSignalKeepsTryingThroughErrorsAndGivesUp(t *testing.T) {
	s := &fakeSignal{err: errors.New("throttled")}
	err := waitForSignal(context.Background(), s, time.Millisecond, 50*time.Millisecond, quiet)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want a deadline", err)
	}
	if s.calls.Load() < 2 {
		t.Errorf("gave up after %d calls", s.calls.Load())
	}
}

func testConfig(t *testing.T) Config {
	t.Helper()
	c, err := load(fullEnv())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestProvisionHandsOverTheOutputsAndWaitsForTableflow(t *testing.T) {
	cfg := testConfig(t)
	s := &fakeSignal{answers: []bool{true}}
	d, err := NewDeps(cfg, s, quiet)
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Provision(context.Background(), cfg.Credentials(), terraform.ApplyOptions{})
	if err != nil || out.TableflowEnabled || out.KafkaClusterID != "lkc-1" || s.calls.Load() != 0 {
		t.Fatalf("first phase: %+v, %v, signal calls %d", out, err, s.calls.Load())
	}
	out, err = d.Provision(context.Background(), cfg.Credentials(), terraform.ApplyOptions{EnableTableflow: true})
	if err != nil || !out.TableflowEnabled || s.calls.Load() != 1 {
		t.Fatalf("second phase: %+v, %v, signal calls %d", out, err, s.calls.Load())
	}
}

func TestEverythingThatDeletesIsRefused(t *testing.T) {
	cfg := testConfig(t)
	d, err := NewDeps(cfg, &fakeSignal{answers: []bool{true}}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for name, err := range map[string]error{
		"destroy":   d.Destroy(ctx, cfg.Credentials()),
		"stoplocal": d.StopLocal(ctx, true),
		"stopcdc":   d.StopCDC(ctx, compose.MySQLSecrets{}),
	} {
		if !errors.Is(err, ErrManaged) {
			t.Errorf("%s: err = %v, want ErrManaged", name, err)
		}
	}
}

func TestTheStepTitlesAreOverriddenForTheTask(t *testing.T) {
	cfg := testConfig(t)
	d, err := NewDeps(cfg, &fakeSignal{answers: []bool{true}}, quiet)
	if err != nil {
		t.Fatal(err)
	}
	p := pipeline.New(d, quiet, nil, nil)
	for _, s := range p.StepStates() {
		if strings.Contains(s.Title, "Docker") {
			t.Errorf("step %s still says %q", s.ID, s.Title)
		}
	}
}

func TestNewDepsRefusesNonLoopbackServices(t *testing.T) {
	cfg := testConfig(t)
	cfg.ConnectURL = "http://10.1.2.3:8083"
	if _, err := NewDeps(cfg, nil, quiet); err == nil {
		t.Error("a connect url off the host was accepted")
	}
	cfg = testConfig(t)
	cfg.AgentURL = "http://example.com:8081"
	if _, err := NewDeps(cfg, nil, quiet); err == nil {
		t.Error("an agent url off the host was accepted")
	}
}

func TestWaitForServicesWaitsForAllThree(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0") // stands in for MySQL
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			c.Close()
		}
	}()
	var connectReady atomic.Bool
	connect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !connectReady.Load() {
			http.Error(w, "starting", http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "[]")
	}))
	defer connect.Close()
	agentSrv := httptest.NewServer(duckdbagent.Handler("agent-token-value", quiet))
	defer agentSrv.Close()
	agent, _ := duckdbagent.NewClient(agentSrv.URL, "agent-token-value")

	cfg := testConfig(t)
	cfg.MySQLHost, cfg.MySQLPort = "127.0.0.1", ln.Addr().(*net.TCPAddr).Port
	go func() { time.Sleep(300 * time.Millisecond); connectReady.Store(true) }()
	start := time.Now()
	if err := waitForServices(context.Background(), cfg, agent, connect.URL, quiet); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 250*time.Millisecond {
		t.Error("returned before Kafka Connect was ready")
	}
}

func TestGenerationIsTheECSTaskID(t *testing.T) {
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/task" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, `{"TaskARN":"arn:aws:ecs:us-east-1:123456789012:task/pims-cluster/3fa9c1d2e4b64f0a8c7d5e6f7a8b9c0d"}`)
	}))
	defer meta.Close()
	got, err := taskGeneration(context.Background(), func(string) string { return meta.URL })
	if err != nil || got != "3fa9c1d2e4b6" {
		t.Fatalf("generation = %q, %v", got, err)
	}
	if _, err := taskGeneration(context.Background(), func(string) string { return "" }); !errors.Is(err, errNoMetadata) {
		t.Errorf("outside ECS: err = %v", err)
	}
}

func TestGenerationRejectsAnUnexpectedARN(t *testing.T) {
	meta := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"TaskARN":"arn:aws:ecs:x:y:task/c/NOT-HEX-AT-ALL!!"}`)
	}))
	defer meta.Close()
	if _, err := taskGeneration(context.Background(), func(string) string { return meta.URL }); err == nil {
		t.Fatal("want an error")
	}
}

func TestGenerationFallsBackToARandomOneThatIsValidForConnectorNames(t *testing.T) {
	t.Setenv("ECS_CONTAINER_METADATA_URI_V4", "")
	a, b := Generation(context.Background(), quiet), Generation(context.Background(), quiet)
	if !generationPattern.MatchString(a) || a == b {
		t.Errorf("generations %q and %q", a, b)
	}
}

func TestCredentialsAreTheCloudKeyForTheMetricsAPI(t *testing.T) {
	c := testConfig(t).Credentials()
	if c != (config.Credentials{AWSRegion: "us-east-1", APIKey: "CCKEY", APISecret: "ccsecret-value"}) {
		t.Errorf("%v", c)
	}
	if err := c.Validate(); err != nil {
		t.Error(err)
	}
}
