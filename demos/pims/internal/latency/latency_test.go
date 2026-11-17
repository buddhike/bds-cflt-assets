package latency

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"
)

var testCatalog = Catalog{
	Endpoint:  "https://tableflow.us-east-1.aws.confluent.cloud/iceberg/catalog/organizations/org-1/environments/env-1",
	APIKey:    "TFKEY",
	APISecret: "tf'secret",
	Namespace: "lkc-abc123",
}

type fakeExec struct {
	out   string
	err   error
	stdin string
}

func (f *fakeExec) Exec(_ context.Context, _ string, stdin io.Reader, _ []string, _ ...string) ([]byte, error) {
	b, _ := io.ReadAll(stdin)
	f.stdin = string(b)
	return []byte(f.out), f.err
}

func TestCatalogValidate(t *testing.T) {
	bad := []Catalog{
		{Endpoint: "http://x", APIKey: "k", APISecret: "s", Namespace: "lkc-1"},
		{Endpoint: testCatalog.Endpoint, APIKey: "k", APISecret: "s", Namespace: `lkc-1"; DROP`},
		{Endpoint: testCatalog.Endpoint, Namespace: "lkc-1"},
	}
	for _, c := range bad {
		if err := c.Validate(); err == nil {
			t.Errorf("Validate(%+v) = nil, want error", c)
		}
	}
	if err := testCatalog.Validate(); err != nil {
		t.Fatalf("Validate(valid) = %v", err)
	}
	if _, err := NewDuckDB(&fakeExec{}, "duckdb", testCatalog, []string{"bad-name"}); err == nil {
		t.Fatal("table names with dashes must be rejected")
	}
}

func TestScriptQuotesSecretAndHidesSetupOutput(t *testing.T) {
	d, err := NewDuckDB(&fakeExec{}, "duckdb", testCatalog, []string{"patients"})
	if err != nil {
		t.Fatal(err)
	}
	s := d.Script()
	if !strings.Contains(s, `CLIENT_SECRET 'tf''secret'`) {
		t.Fatalf("secret not escaped as a SQL literal:\n%s", s)
	}
	if strings.Index(s, ".mode trash") > strings.Index(s, "CREATE SECRET") || strings.Index(s, ".mode json") < strings.Index(s, "ATTACH") {
		t.Fatalf("setup must run in trash mode before json output:\n%s", s)
	}
	if !strings.Contains(s, `iceberg_snapshots(tableflow."lkc-abc123"."patients")`) {
		t.Fatalf("snapshot query missing:\n%s", s)
	}
}

func TestQueryParsesRowsAndReportsMissingTables(t *testing.T) {
	fe := &fakeExec{
		out: `[{"table_name":"patients","record_ms":1000,"committed_ms":61000}]` + "\n" +
			`[{"table_name":"visits","record_ms":null,"committed_ms":null}]` + "\n",
		err: errors.New("exit status 1: Catalog Error: Table appointments does not exist"),
	}
	d, err := NewDuckDB(fe, "duckdb", testCatalog, []string{"patients", "visits", "appointments"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.Query(context.Background())
	if err != nil {
		t.Fatalf("Query() = %v", err)
	}
	if len(res.Points) != 1 || res.Points[0].Table != "patients" || res.Points[0].LatencyMs != 60000 {
		t.Fatalf("points = %+v", res.Points)
	}
	if len(res.Errors) != 2 {
		t.Fatalf("errors = %+v, want visits (no data) and appointments (failed)", res.Errors)
	}
	if !strings.Contains(fe.stdin, "CREATE SECRET") {
		t.Fatal("script must be sent on stdin, not as arguments")
	}
}

func TestQueryFailsWhenNothingSucceeded(t *testing.T) {
	d, _ := NewDuckDB(&fakeExec{err: errors.New("401 Unauthorized")}, "duckdb", testCatalog, []string{"patients"})
	if _, err := d.Query(context.Background()); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("Query() = %v, want catalog error", err)
	}
}

type seqQuerier struct {
	mu      sync.Mutex
	results []Result
	i       int
}

func (s *seqQuerier) Query(context.Context) (Result, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.results[min(s.i, len(s.results)-1)]
	s.i++
	return r, nil
}

func TestMonitorReportsEachCommitOnce(t *testing.T) {
	t0 := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	p := func(commit time.Time) Point { return Point{Table: "patients", CommittedAt: commit} }
	q := &seqQuerier{results: []Result{
		{Points: []Point{p(t0)}},
		{Points: []Point{p(t0)}}, // same snapshot: not reported again
		{Points: []Point{p(t0.Add(time.Minute))}},
	}}
	m := NewMonitor(q, time.Millisecond, slog.New(slog.DiscardHandler))

	ctx, cancel := context.WithCancel(context.Background())
	var got []Point
	var calls int
	m.Run(ctx, func(r Result) {
		got = append(got, r.Points...)
		if calls++; calls == 3 {
			cancel()
		}
	})
	if len(got) != 2 {
		t.Fatalf("got %d points, want 2 distinct commits: %+v", len(got), got)
	}
}

// TestScriptRunsInDuckDB executes the generated script's query shape in the
// real DuckDB image against local tables, which checks the SQL parses and
// the JSON output decodes. It needs the pims-duckdb image built by
// `docker compose build duckdb`.
func TestScriptRunsInDuckDB(t *testing.T) {
	if err := exec.Command("docker", "image", "inspect", "pims-duckdb").Run(); err != nil {
		t.Skip("pims-duckdb image not built")
	}
	script := strings.Join([]string{
		".mode trash",
		"SET TimeZone = 'UTC';",
		"CREATE TABLE patients AS SELECT TIMESTAMP '2030-01-01 10:00:00.500' AS updated_at;",
		"CREATE TABLE snaps AS SELECT TIMESTAMP '2030-01-01 10:01:00' AS timestamp_ms;",
		".mode json",
		"SELECT 'patients' AS table_name, (SELECT epoch_ms(max(updated_at)::TIMESTAMPTZ) FROM patients) AS record_ms, (SELECT epoch_ms(max(timestamp_ms)::TIMESTAMPTZ) FROM snaps) AS committed_ms;",
	}, "\n")
	cmd := exec.Command("docker", "run", "--rm", "-i", "pims-duckdb", "duckdb", "-batch")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("duckdb: %v", err)
	}
	rows, err := parseRows(out)
	if err != nil {
		t.Fatalf("parse %q: %v", out, err)
	}
	if len(rows) != 1 || *rows[0].CommittedMs-*rows[0].RecordMs != 59500 {
		t.Fatalf("rows = %+v", rows)
	}
}
