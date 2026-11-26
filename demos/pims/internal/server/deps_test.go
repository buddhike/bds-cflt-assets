package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path"
	"slices"
	"strings"
	"sync"
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

func testConfig(t *testing.T) Config {
	t.Helper()
	c, err := load(fullEnv())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// tableflowAPI stands in for the Confluent Cloud API and records what it is asked.
type tableflowAPI struct {
	mu       sync.Mutex
	enabled  []string
	disabled []string
}

func (f *tableflowAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.Method {
	case http.MethodPost:
		var b struct {
			Spec struct {
				DisplayName string `json:"display_name"`
			} `json:"spec"`
		}
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.enabled = append(f.enabled, b.Spec.DisplayName)
		w.WriteHeader(http.StatusAccepted)
	case http.MethodDelete:
		f.disabled = append(f.disabled, path.Base(r.URL.Path))
		w.WriteHeader(http.StatusNoContent)
	}
}

func apiConfig(t *testing.T) (Config, *tableflowAPI) {
	t.Helper()
	api := &tableflowAPI{}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	cfg := testConfig(t)
	cfg.TableflowAPIURL = srv.URL
	return cfg, api
}

func TestProvisionHandsOverTheOutputsAndEnablesTableflowInThePhaseThatAsksForIt(t *testing.T) {
	cfg, api := apiConfig(t)
	d, err := NewDeps(cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	out, err := d.Provision(context.Background(), cfg.Credentials(), terraform.ApplyOptions{})
	if err != nil || out.TableflowEnabled || out.KafkaClusterID != "lkc-1" || len(api.enabled) != 0 {
		t.Fatalf("first phase: %+v, %v, enabled %v", out, err, api.enabled)
	}
	out, err = d.Provision(context.Background(), cfg.Credentials(), terraform.ApplyOptions{EnableTableflow: true})
	if err != nil || !out.TableflowEnabled || !slices.Equal(api.enabled, cfg.Outputs.Topics) {
		t.Fatalf("second phase: %+v, %v, enabled %v, want %v", out, err, api.enabled, cfg.Outputs.Topics)
	}
}

func TestDisableTableflowDisablesEveryTopic(t *testing.T) {
	cfg, api := apiConfig(t)
	d, err := NewDeps(cfg, quiet)
	if err != nil {
		t.Fatal(err)
	}
	if err := d.DisableTableflow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(api.disabled, cfg.Outputs.Topics) {
		t.Errorf("disabled %v, want %v", api.disabled, cfg.Outputs.Topics)
	}
}

func TestEverythingThatDeletesIsRefused(t *testing.T) {
	cfg := testConfig(t)
	d, err := NewDeps(cfg, quiet)
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
	d, err := NewDeps(cfg, quiet)
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
	if _, err := NewDeps(cfg, quiet); err == nil {
		t.Error("a connect url off the host was accepted")
	}
	cfg = testConfig(t)
	cfg.AgentURL = "http://example.com:8081"
	if _, err := NewDeps(cfg, quiet); err == nil {
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

func TestGenerationIsDecidedOncePerProcess(t *testing.T) {
	calls := 0
	g := newGenerationSource(quiet)
	g.lookup = func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "3fa9c1d2e4b6", nil
		}
		return "ffffffffffff", nil // what a later lookup would say
	}
	a, _ := g.Get(context.Background())
	b, _ := g.Get(context.Background())
	if a != "3fa9c1d2e4b6" || a != b || calls != 1 {
		t.Errorf("generations %q and %q after %d lookups", a, b, calls)
	}
}

func TestInsideECSAFailedLookupIsAnErrorNotARandomGeneration(t *testing.T) {
	g := newGenerationSource(quiet)
	g.lookup = func(context.Context) (string, error) { return "", errors.New("metadata endpoint down") }
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if got, err := g.Get(ctx); err == nil {
		t.Fatalf("got generation %q, want an error", got)
	}
	// And once the endpoint recovers, the next call succeeds and is then fixed.
	g.lookup = func(context.Context) (string, error) { return "3fa9c1d2e4b6", nil }
	if got, err := g.Get(context.Background()); err != nil || got != "3fa9c1d2e4b6" {
		t.Errorf("after recovery: %q, %v", got, err)
	}
}

func TestOutsideECSTheGenerationIsRandomButValidForConnectorNames(t *testing.T) {
	t.Setenv("ECS_CONTAINER_METADATA_URI_V4", "")
	a, err := newGenerationSource(quiet).Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newGenerationSource(quiet).Get(context.Background())
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
