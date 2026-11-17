package connector

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func discard() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestConfigKeepsSecretsOutOfConfig(t *testing.T) {
	cfg := Config(Settings{KafkaBootstrap: "pkc:9092", MySQLHost: "mysql", MySQLPort: 3306})

	if cfg["database.password"] != "${env:PIMS_MYSQL_DEBEZIUM_PASSWORD}" {
		t.Fatalf("database.password = %q, want env reference", cfg["database.password"])
	}
	for _, k := range []string{"schema.history.internal.producer.sasl.jaas.config", "schema.history.internal.consumer.sasl.jaas.config"} {
		if !strings.Contains(cfg[k], "${env:PIMS_KAFKA_API_SECRET}") {
			t.Fatalf("%s must reference the secret via the env provider", k)
		}
	}
	if cfg["table.include.list"] != "pims.patients,pims.appointments,pims.visits" {
		t.Fatalf("table.include.list = %q", cfg["table.include.list"])
	}
	if cfg["time.precision.mode"] != "connect" {
		t.Fatal("time.precision.mode must be connect so Tableflow gets timestamp types")
	}
}

func TestRouteRegexMapsTopicsToTableNames(t *testing.T) {
	cfg := Config(Settings{})
	if cfg["transforms.route.regex"] != `pims\.pims\.(.*)` || cfg["transforms.route.replacement"] != "$1" {
		t.Fatalf("unexpected route transform: %q -> %q", cfg["transforms.route.regex"], cfg["transforms.route.replacement"])
	}
}

func TestRegisterRetriesTransientErrorsAndSendsUserAgent(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut || r.URL.Path != "/connectors/pims-mysql/config" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		if calls.Add(1) == 1 {
			http.Error(w, "rebalance in progress", http.StatusConflict)
			return
		}
		var cfg map[string]string
		b, _ := io.ReadAll(r.Body)
		if err := json.Unmarshal(b, &cfg); err != nil {
			t.Errorf("bad body: %v", err)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, discard())
	if err := c.Register(context.Background(), map[string]string{"a": "b"}); err != nil {
		t.Fatalf("Register() = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}
}

func TestRegisterStopsOnBadRequest(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		http.Error(w, `{"message":"Connector configuration is invalid"}`, http.StatusBadRequest)
	}))
	defer srv.Close()

	err := NewClient(srv.URL, discard()).Register(context.Background(), map[string]string{})
	var he *HTTPError
	if !errors.As(err, &he) || he.StatusCode != http.StatusBadRequest {
		t.Fatalf("Register() = %v, want HTTP 400", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls = %d, want 1: 400 must not be retried", calls.Load())
	}
}

func statusServer(t *testing.T, bodies ...string) *httptest.Server {
	t.Helper()
	var i atomic.Int32
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(i.Add(1)) - 1
		if n >= len(bodies) {
			n = len(bodies) - 1
		}
		_, _ = w.Write([]byte(bodies[n]))
	}))
}

func TestWaitRunning(t *testing.T) {
	srv := statusServer(t,
		`{"connector":{"state":"UNASSIGNED"},"tasks":[]}`,
		`{"connector":{"state":"RUNNING"},"tasks":[{"id":0,"state":"RUNNING"}]}`,
	)
	defer srv.Close()
	c := NewClient(srv.URL, discard())
	c.pollInterval = time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.WaitRunning(ctx); err != nil {
		t.Fatalf("WaitRunning() = %v", err)
	}
}

func TestWaitRunningReportsTaskFailure(t *testing.T) {
	srv := statusServer(t, `{"connector":{"state":"RUNNING"},"tasks":[{"id":0,"state":"FAILED","trace":"Access denied for user 'debezium'\n\tat ..."}]}`)
	defer srv.Close()
	c := NewClient(srv.URL, discard())
	c.pollInterval = time.Millisecond

	err := c.WaitRunning(context.Background())
	if !errors.Is(err, ErrConnectorFailed) || !strings.Contains(err.Error(), "Access denied") || strings.Contains(err.Error(), "\tat") {
		t.Fatalf("WaitRunning() = %v, want first trace line only", err)
	}
}

func TestDeleteIgnoresMissingConnector(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	if err := NewClient(srv.URL, discard()).Delete(context.Background()); err != nil {
		t.Fatalf("Delete() = %v, want nil for 404", err)
	}
}

func TestRegistryWaitForValueSchemas(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "key" || pass != "secret" {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		if calls.Add(1) == 1 {
			_, _ = w.Write([]byte(`["patients-value"]`))
			return
		}
		_, _ = w.Write([]byte(`["patients-key","patients-value","visits-value","appointments-value"]`))
	}))
	defer srv.Close()

	r := NewRegistry(srv.URL, "key", "secret", discard())
	r.pollInterval = time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.WaitForValueSchemas(ctx, []string{"patients", "visits", "appointments"}); err != nil {
		t.Fatalf("WaitForValueSchemas() = %v", err)
	}
	if calls.Load() < 2 {
		t.Fatalf("calls = %d, want at least 2", calls.Load())
	}
}

func TestStatusRunningNeedsTasks(t *testing.T) {
	var s Status
	s.Connector.State = "RUNNING"
	if s.Running() {
		t.Fatal("a connector with no tasks is not running yet")
	}
}
