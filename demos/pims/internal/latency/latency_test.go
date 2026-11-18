package latency

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"regexp"
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

// fakeExec answers each table's snapshot listing and snapshot comparison
// scripts from per-table outputs, and records every script it was sent.
type fakeExec struct {
	list, diff       map[string]string
	listErr, diffErr map[string]error
	// hang makes the table's comparison wait for its deadline.
	hang    map[string]bool
	scripts []string
}

func newFakeExec() *fakeExec {
	return &fakeExec{list: map[string]string{}, diff: map[string]string{}, listErr: map[string]error{}, diffErr: map[string]error{}, hang: map[string]bool{}}
}

func (f *fakeExec) Exec(ctx context.Context, _ string, stdin io.Reader, _ []string, _ ...string) ([]byte, error) {
	b, _ := io.ReadAll(stdin)
	script := string(b)
	f.scripts = append(f.scripts, script)
	table := regexp.MustCompile(`\."([a-z_]+)"`).FindStringSubmatch(script[strings.Index(script, ".mode json"):])[1]
	if strings.Contains(script, "EXCEPT") {
		if f.hang[table] {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return []byte(f.diff[table]), f.diffErr[table]
	}
	return []byte(f.list[table]), f.listErr[table]
}

// diffs counts the comparison scripts sent and the snapshots in them.
func (f *fakeExec) diffs() (scripts, snapshots int) {
	for _, s := range f.scripts {
		if n := strings.Count(s, "EXCEPT"); n > 0 {
			scripts++
			snapshots += n
		}
	}
	return scripts, snapshots
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
	d, err := NewDuckDB(newFakeExec(), "duckdb", testCatalog, []string{"patients"})
	if err != nil {
		t.Fatal(err)
	}
	s := d.snapshotsScript("patients")
	if !strings.Contains(s, `CLIENT_SECRET 'tf''secret'`) {
		t.Fatalf("secret not escaped as a SQL literal:\n%s", s)
	}
	if strings.Index(s, ".mode trash") > strings.Index(s, "CREATE SECRET") || strings.Index(s, ".mode json") < strings.Index(s, "ATTACH") {
		t.Fatalf("setup must run in trash mode before json output:\n%s", s)
	}
	if !strings.Contains(s, `FROM iceberg_snapshots(tableflow."lkc-abc123"."patients")`) {
		t.Fatalf("snapshot listing must read the table's snapshots:\n%s", s)
	}
}

// snaps renders a snapshot listing: snapshot 100+n has sequence number n
// and commits at minute n.
func snaps(table string, seqs ...int64) string {
	var rows []string
	for _, n := range seqs {
		rows = append(rows, fmt.Sprintf(`{"table_name":%q,"snapshot_id":%d,"sequence_number":%d,"committed_ms":%d}`, table, 100+n, n, n*60000))
	}
	return "[" + strings.Join(rows, ",") + "]\n"
}

func diffOut(table string, id, records int64, oldestMs, newestMs int64) string {
	if records == 0 {
		return fmt.Sprintf(`[{"table_name":%q,"snapshot_id":%d,"records":0,"oldest_ms":null,"newest_ms":null}]`+"\n", table, id)
	}
	return fmt.Sprintf(`[{"table_name":%q,"snapshot_id":%d,"records":%d,"oldest_ms":%d,"newest_ms":%d}]`+"\n", table, id, records, oldestMs, newestMs)
}

func seqRange(from, to int64) []int64 {
	var s []int64
	for n := from; n <= to; n++ {
		s = append(s, n)
	}
	return s
}

func cursorIs(t *testing.T, d *DuckDB, table string, seq int64) {
	t.Helper()
	if !strings.Contains(d.snapshotsScript(table), fmt.Sprintf("sequence_number <= %d)", seq)) {
		t.Fatalf("%s cursor = %d, want %d", table, d.cursor[table], seq)
	}
}

func TestQueryMeasuresEachNewSnapshotOnce(t *testing.T) {
	fe := newFakeExec()
	fe.list["patients"] = snaps("patients", 1, 2)
	d, err := NewDuckDB(fe, "duckdb", testCatalog, []string{"patients"})
	if err != nil {
		t.Fatal(err)
	}

	// First poll: the latest snapshot becomes the baseline; nothing is
	// compared, since its rows may predate Tableflow being enabled.
	res, err := d.Query(context.Background())
	if err != nil || len(res.Points) != 0 || len(res.Errors) != 0 || len(fe.scripts) != 1 {
		t.Fatalf("baseline poll: res=%+v err=%v scripts=%d", res, err, len(fe.scripts))
	}
	if !strings.Contains(fe.scripts[0], fmt.Sprintf("sequence_number <= %d", math.MaxInt64)) {
		t.Fatalf("baseline must list only the latest snapshot:\n%s", fe.scripts[0])
	}
	cursorIs(t, d, "patients", 2)

	// Snapshot 3 publishes two rows; snapshot 4 is a compaction with none.
	fe.list["patients"] = snaps("patients", 2, 3, 4)
	fe.diff["patients"] = diffOut("patients", 103, 2, 120000, 170000) + diffOut("patients", 104, 0, 0, 0)
	res, err = d.Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	diff := fe.scripts[2]
	for _, want := range []string{"AT (VERSION => 103)", "AT (VERSION => 102)", "AT (VERSION => 104)"} {
		if !strings.Contains(diff, want) {
			t.Fatalf("comparison script missing %q:\n%s", want, diff)
		}
	}
	if len(res.Points) != 1 {
		t.Fatalf("points = %+v, want one for snapshot 103", res.Points)
	}
	p := res.Points[0]
	if p.SnapshotID != 103 || p.Records != 2 || p.LatencyMs != 60000 || p.MinLatencyMs != 10000 {
		t.Fatalf("point = %+v", p)
	}
	cursorIs(t, d, "patients", 4)

	// Nothing new: no comparison runs.
	fe.list["patients"] = snaps("patients", 4)
	res, err = d.Query(context.Background())
	if err != nil || len(res.Points) != 0 || len(fe.scripts) != 4 {
		t.Fatalf("idle poll: res=%+v err=%v scripts=%d", res, err, len(fe.scripts))
	}
}

func TestQueryReportsStatsPerRun(t *testing.T) {
	fe := newFakeExec()
	stats := `[{"row_kind":"stats","query_ms":12.5,"operator_ms":30.25,"blocked_ms":0,"rows_scanned":4000,"peak_memory_bytes":1048576,` +
		`"catalog_requests":4,"catalog_http_ms":300,"storage_requests":9,"storage_http_ms":800,"storage_bytes":65536}]` + "\n"
	fe.list["patients"] = snaps("patients", 1) + stats
	d, _ := NewDuckDB(fe, "duckdb", testCatalog, []string{"patients"})
	res, err := d.Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Stats) != 1 || res.Stats[0].Compare != nil || res.Stats[0].List.StorageBytes != 65536 || res.Stats[0].List.QueryMs != 12.5 {
		t.Fatalf("baseline stats = %+v, want the listing's figures and no comparison", res.Stats)
	}
	if len(res.Errors) != 0 {
		t.Fatalf("the stats row must not be read as a snapshot: %+v", res.Errors)
	}

	fe.list["patients"] = snaps("patients", 1, 2) + stats
	fe.diff["patients"] = diffOut("patients", 102, 1, 60000, 60000) + stats
	res, err = d.Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	st := res.Stats[0]
	if st.Snapshots != 1 || st.Compare == nil || st.Compare.StorageRequests != 9 || len(res.Points) != 1 {
		t.Fatalf("stats = %+v, points = %+v", st, res.Points)
	}
	for _, script := range fe.scripts {
		if !strings.Contains(script, statsSetup) || !strings.HasSuffix(script, statsSQL("https://tableflow.us-east-1.aws.confluent.cloud/")) {
			t.Fatalf("every run must enable and end with the stats query:\n%s", script)
		}
	}
}

func TestMonitorLogsQueryStats(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo}))
	listOnly := QueryStats{Table: "visits", List: RunStats{Wall: time.Second}}
	compared := QueryStats{Table: "patients", Snapshots: 2, List: RunStats{Wall: time.Second},
		Compare: &RunStats{Wall: 2 * time.Second, StorageBytes: 4096, StorageHTTPMs: 900}}
	q := &seqQuerier{results: []Result{{Stats: []QueryStats{listOnly, compared}}}}
	m := NewMonitor(q, time.Hour, log)
	ctx, cancel := context.WithCancel(context.Background())
	m.Run(ctx, func(Result) { cancel() })

	out := buf.String()
	if strings.Contains(out, "table=visits") {
		t.Fatalf("a poll that only listed snapshots must log at debug level:\n%s", out)
	}
	for _, want := range []string{`msg="Ran latency query"`, "table=patients", "snapshots=2", "duration_ms=3000",
		"list.wall_ms=1000", "compare.wall_ms=2000", "compare.storage_bytes=4096", "compare.storage_http_ms=900"} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing %q:\n%s", want, out)
		}
	}
}

func TestQueryRetriesThenSkipsFailedSnapshot(t *testing.T) {
	fe := newFakeExec()
	fe.list["patients"] = snaps("patients", 1)
	d, _ := NewDuckDB(fe, "duckdb", testCatalog, []string{"patients"})
	if _, err := d.Query(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Snapshot 102 cannot be read; 103 can.
	fe.list["patients"] = snaps("patients", 1, 2, 3)
	fe.diff["patients"] = diffOut("patients", 103, 1, 170000, 170000)
	fe.diffErr["patients"] = errors.New("exit status 1: IO Error: snapshot 102 unreadable")
	for i := 1; i < maxAttempts; i++ {
		res, err := d.Query(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Points) != 0 || len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Error, "comparing snapshot 102") {
			t.Fatalf("attempt %d: a failed snapshot must hold the table's later ones back: %+v", i, res)
		}
		cursorIs(t, d, "patients", 1)
	}

	res, err := d.Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Error, "skipped snapshot 102") {
		t.Fatalf("errors = %+v, want snapshot 102 skipped", res.Errors)
	}
	if len(res.Points) != 1 || res.Points[0].SnapshotID != 103 {
		t.Fatalf("points = %+v, want snapshot 103 measured once 102 is skipped", res.Points)
	}
	cursorIs(t, d, "patients", 3)
}

func TestQueryIsolatesTables(t *testing.T) {
	fe := newFakeExec()
	tables := []string{"patients", "visits", "appointments"}
	d, _ := NewDuckDB(fe, "duckdb", testCatalog, tables)
	d.timeout = 20 * time.Millisecond
	fe.list["patients"] = snaps("patients", 1)
	fe.list["visits"] = snaps("visits", 1)
	fe.list["appointments"] = "[]\n"
	if _, err := d.Query(context.Background()); err != nil {
		t.Fatal(err)
	}

	// patients' comparison times out, visits' succeeds, appointments has
	// no snapshots yet.
	fe.list["patients"] = snaps("patients", 1, 2)
	fe.hang["patients"] = true
	fe.list["visits"] = snaps("visits", 1, 2)
	fe.diff["visits"] = diffOut("visits", 102, 1, 100000, 100000)
	res, err := d.Query(context.Background())
	if err != nil {
		t.Fatalf("Query() = %v, want per-table errors", err)
	}
	if len(res.Points) != 1 || res.Points[0].Table != "visits" {
		t.Fatalf("points = %+v, want visits measured", res.Points)
	}
	want := map[string]string{"patients": "deadline exceeded", "appointments": "no snapshots yet"}
	if len(res.Errors) != len(want) {
		t.Fatalf("errors = %+v", res.Errors)
	}
	for _, e := range res.Errors {
		if !strings.Contains(e.Error, want[e.Table]) {
			t.Fatalf("error for %s = %q, want %q", e.Table, e.Error, want[e.Table])
		}
	}
	cursorIs(t, d, "patients", 1)
	cursorIs(t, d, "visits", 2)
}

func TestQueryCapsSnapshotsPerPoll(t *testing.T) {
	fe := newFakeExec()
	fe.list["patients"] = snaps("patients", 1)
	d, _ := NewDuckDB(fe, "duckdb", testCatalog, []string{"patients"})
	if _, err := d.Query(context.Background()); err != nil {
		t.Fatal(err)
	}

	backlog := maxSnapshotsPerPoll + 3
	fe.list["patients"] = snaps("patients", seqRange(1, int64(1+backlog))...)
	var out strings.Builder
	for n := int64(2); n <= int64(1+backlog); n++ {
		out.WriteString(diffOut("patients", 100+n, 0, 0, 0))
	}
	fe.diff["patients"] = out.String()
	if _, err := d.Query(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, n := fe.diffs(); n != maxSnapshotsPerPoll {
		t.Fatalf("compared %d snapshots, want %d", n, maxSnapshotsPerPoll)
	}
	cursorIs(t, d, "patients", int64(1+maxSnapshotsPerPoll))

	if _, err := d.Query(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, n := fe.diffs(); n != backlog {
		t.Fatalf("compared %d snapshots over two polls, want %d", n, backlog)
	}
}

func TestQueryRestartsAfterExpiredSnapshots(t *testing.T) {
	fe := newFakeExec()
	fe.list["patients"] = snaps("patients", 2)
	d, _ := NewDuckDB(fe, "duckdb", testCatalog, []string{"patients"})
	if _, err := d.Query(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Snapshots up to 4 expired; the listing falls back to everything left.
	fe.list["patients"] = snaps("patients", 5, 6)
	fe.diff["patients"] = diffOut("patients", 106, 1, 300000, 300000)
	res, err := d.Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, n := fe.diffs(); n != 1 || !strings.Contains(fe.scripts[len(fe.scripts)-1], "AT (VERSION => 105)") {
		t.Fatalf("want only 106 compared with 105:\n%s", fe.scripts[len(fe.scripts)-1])
	}
	if len(res.Points) != 1 || res.Points[0].SnapshotID != 106 {
		t.Fatalf("points = %+v", res.Points)
	}
	cursorIs(t, d, "patients", 6)
}

func TestQueryFailsWhenNothingSucceeded(t *testing.T) {
	fe := newFakeExec()
	fe.listErr["patients"] = errors.New("401 Unauthorized")
	fe.listErr["visits"] = errors.New("401 Unauthorized")
	d, _ := NewDuckDB(fe, "duckdb", testCatalog, []string{"patients", "visits"})
	res, err := d.Query(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("Query() = %v, want catalog error", err)
	}
	if len(res.Stats) != 2 {
		t.Fatalf("stats = %+v, want the failed runs' timings kept", res.Stats)
	}
	if !strings.Contains(fe.scripts[0], "CREATE SECRET") {
		t.Fatal("script must be sent on stdin, not as arguments")
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

func TestMonitorReportsEveryResult(t *testing.T) {
	p := Point{Table: "patients", LatencyMs: 1000}
	q := &seqQuerier{results: []Result{{Points: []Point{p}}, {}, {Points: []Point{p, p}}}}
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
	if len(got) != 3 {
		t.Fatalf("got %d points, want 3: %+v", len(got), got)
	}
}

// TestSQLRunsInDuckDB executes the generated snapshot listing and
// comparison SQL in the real DuckDB image against local tables standing in
// for iceberg_snapshots and the table versions, which checks the SQL parses,
// selects the right rows and its JSON output decodes. It needs the
// pims-duckdb image built by `docker compose build duckdb`.
func TestSQLRunsInDuckDB(t *testing.T) {
	if err := exec.Command("docker", "image", "inspect", "pims-duckdb").Run(); err != nil {
		t.Skip("pims-duckdb image not built")
	}
	script := strings.Join([]string{
		".mode trash",
		"SET TimeZone = 'UTC';",
		// Same column types as iceberg_snapshots.
		"CREATE TABLE snaps AS SELECT * FROM (VALUES (1::UBIGINT, 101::UBIGINT, TIMESTAMP '2030-01-01 09:00:00'), " +
			"(2::UBIGINT, 102::UBIGINT, TIMESTAMP '2030-01-01 10:00:00'), (3::UBIGINT, 103::UBIGINT, TIMESTAMP '2030-01-01 10:01:00')) v(sequence_number, snapshot_id, timestamp_ms);",
		// v3 updates row 1, inserts row 3, and keeps row 2 unchanged.
		"CREATE TABLE v2 AS SELECT * FROM (VALUES (1, TIMESTAMP '2030-01-01 09:59:00'), (2, TIMESTAMP '2030-01-01 09:30:00')) v(id, updated_at);",
		"CREATE TABLE v3 AS SELECT * FROM (VALUES (1, TIMESTAMP '2030-01-01 10:00:30.500'), (2, TIMESTAMP '2030-01-01 09:30:00'), (3, TIMESTAMP '2030-01-01 10:00:50')) v(id, updated_at);",
		// Tableflow may produce TIMESTAMPTZ rather than TIMESTAMP.
		"CREATE TABLE tz2 AS SELECT id, updated_at::TIMESTAMPTZ AS updated_at FROM v2;",
		"CREATE TABLE tz3 AS SELECT id, updated_at::TIMESTAMPTZ AS updated_at FROM v3;",
		// After the fixtures, so only the queries under test are profiled.
		statsSetup,
		".mode json",
		snapshotsSQL("patients", "snaps", 2),
		snapshotsSQL("patients", "snaps", math.MaxInt64), // baseline: latest only
		snapshotsSQL("patients", "snaps", 0),             // all expired: everything
		diffSQL("patients", 102, "missing", "v2"),        // fails; the CLI must carry on
		diffSQL("patients", 103, "v3", "v2"),
		diffSQL("patients", 102, "v2", "v2"),
		diffSQL("patients", 103, "tz3", "tz2"),
		statsSQL("https://catalog.invalid/"),
	}, "\n")
	cmd := exec.Command("docker", "run", "--rm", "-i", "pims-duckdb", "duckdb", "-batch")
	cmd.Stdin = strings.NewReader(script)
	cmd.Stderr = &bytes.Buffer{}
	out, err := cmd.Output()
	if err == nil || !bytes.Contains(cmd.Stderr.(*bytes.Buffer).Bytes(), []byte("missing")) {
		t.Fatalf("duckdb: err=%v, want the comparison against a missing table to fail", err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	var ss, latest, all []snapshotRow
	var diff, empty, tz []diffRow
	var stats []RunStats
	for _, v := range []any{&ss, &latest, &all, &diff, &empty, &tz, &stats} {
		if err := dec.Decode(v); err != nil {
			t.Fatalf("decode %q: %v", out, err)
		}
	}
	if len(ss) != 2 || ss[0].SnapshotID != 102 || ss[1].Sequence != 3 || ss[1].CommittedMs != time.Date(2030, 1, 1, 10, 1, 0, 0, time.UTC).UnixMilli() {
		t.Fatalf("snapshots = %+v, want 102 and 103", ss)
	}
	if len(diff) != 1 || diff[0].Records != 2 || diff[0].SnapshotID != 103 ||
		*diff[0].OldestMs != time.Date(2030, 1, 1, 10, 0, 30, 5e8, time.UTC).UnixMilli() ||
		*diff[0].NewestMs != time.Date(2030, 1, 1, 10, 0, 50, 0, time.UTC).UnixMilli() {
		t.Fatalf("diff = %+v, want the update to row 1 and the insert of row 3", diff)
	}
	if len(latest) != 1 || latest[0].SnapshotID != 103 {
		t.Fatalf("baseline listing = %+v, want only 103", latest)
	}
	if len(all) != 3 {
		t.Fatalf("listing with nothing at or before the cursor = %+v, want all 3", all)
	}
	if len(empty) != 1 || empty[0].Records != 0 || empty[0].OldestMs != nil {
		t.Fatalf("identical versions = %+v, want no rows", empty)
	}
	if len(tz) != 1 || tz[0].Records != 2 || *tz[0].OldestMs != *diff[0].OldestMs {
		t.Fatalf("timestamptz diff = %+v, want the same as timestamp", tz)
	}
	if len(stats) != 1 || stats[0].QueryMs <= 0 || stats[0].RowsScanned == 0 || stats[0].StorageRequests != 0 {
		t.Fatalf("stats = %+v, want query time and rows scanned from the profiler and no HTTP requests", stats)
	}
	if dec.More() {
		t.Fatalf("unexpected trailing output: %q", out)
	}
}

// TestStatsSQLCountsHTTPInDuckDB reads a file over HTTP in the real DuckDB
// image and checks statsSQL splits the requests by prefix and counts the
// file's bytes once, not again for httpfs's HEAD. It needs the pims-duckdb
// image.
func TestStatsSQLCountsHTTPInDuckDB(t *testing.T) {
	if err := exec.Command("docker", "image", "inspect", "pims-duckdb").Run(); err != nil {
		t.Skip("pims-duckdb image not built")
	}
	body := "id,name\n" + strings.Repeat("1,abcdefghij\n", 500)
	// Listen on every interface: on Linux the container reaches the host
	// through the bridge, not loopback.
	ln, err := net.Listen("tcp", "0.0.0.0:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.ServeContent(w, r, "f.csv", time.Time{}, strings.NewReader(body))
	}))
	srv.Listener = ln
	srv.Start()
	defer srv.Close()
	base := fmt.Sprintf("http://host.docker.internal:%d/", ln.Addr().(*net.TCPAddr).Port)

	script := strings.Join([]string{
		".mode trash",
		"LOAD httpfs;",
		statsSetup,
		fmt.Sprintf("SELECT count(*) FROM read_csv('%sf.csv');", base),
		".mode json",
		statsSQL("https://catalog.invalid/"), // the file is storage
		statsSQL(base),                       // the file is catalog
	}, "\n")
	cmd := exec.Command("docker", "run", "--rm", "-i", "--add-host=host.docker.internal:host-gateway", "pims-duckdb", "duckdb", "-batch")
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("duckdb: %v", err)
	}
	if rows, err := parseRows[snapshotRow](out, &RunStats{}); err != nil || len(rows) != 0 {
		t.Fatalf("parse %q: rows=%v err=%v, want only stats rows", out, rows, err)
	}
	dec := json.NewDecoder(bytes.NewReader(out))
	var first, second []RunStats
	if err := dec.Decode(&first); err != nil {
		t.Fatal(err)
	}
	if err := dec.Decode(&second); err != nil {
		t.Fatal(err)
	}
	storage, catalog := first[0], second[0]
	if storage.StorageRequests < 2 || storage.CatalogRequests != 0 || storage.StorageHTTPMs < 0 {
		t.Fatalf("storage stats = %+v, want the HEAD and GET as storage requests", storage)
	}
	if storage.StorageBytes != int64(len(body)) {
		t.Fatalf("storage bytes = %d, want the file's %d bytes counted once", storage.StorageBytes, len(body))
	}
	if catalog.CatalogRequests != storage.StorageRequests || catalog.StorageRequests != 0 || catalog.StorageBytes != 0 {
		t.Fatalf("catalog stats = %+v, want every request counted as catalog", catalog)
	}
	if storage.QueryMs <= 0 || storage.RowsScanned == 0 {
		t.Fatalf("stats = %+v, want query time and rows scanned from the profiler", storage)
	}
}
