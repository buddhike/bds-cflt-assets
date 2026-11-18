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
	"slices"
	"strconv"
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

var (
	// listedTable matches the table name in each of a listing script's
	// statements.
	listedTable = regexp.MustCompile(`'([a-z_]+)' AS table_name, snapshot_id`)
	// comparedSnapshot matches the table and snapshot a comparison script
	// measures: the first table version in it is the snapshot itself.
	comparedSnapshot = regexp.MustCompile(`\."([a-z_]+)" AT \(VERSION => (\d+)\)`)
)

// fakeExec answers the snapshot listing and each snapshot comparison from
// per-table outputs, and records every script it was sent. Tables are
// compared in parallel, so it is safe for concurrent use. The maps are read
// during a poll, so tests set them between polls.
type fakeExec struct {
	mu      sync.Mutex
	scripts []string
	// cmds is the command each script was run with.
	cmds [][]string

	// list is each table's snapshot listing output. A table with no entry
	// prints nothing, as when its listing fails.
	list map[string]string
	// listErr is the error of the listing run.
	listErr error
	// diff is each table's comparison output, one line per snapshot. A
	// comparison prints the lines for its own snapshot and any stats line.
	diff    map[string]string
	diffErr map[string]error
	// hang makes the table's comparisons wait for their deadline; hangOn does
	// the same for the snapshots with these ids.
	hang   map[string]bool
	hangOn map[int64]bool
	// onCompare is called when a comparison starts.
	onCompare func()
	// rendezvous, when positive, holds every comparison until that many have
	// started, which only happens if they run in parallel.
	rendezvous int
	started    int
	ready      chan struct{}
}

func newFakeExec() *fakeExec {
	return &fakeExec{list: map[string]string{}, diff: map[string]string{}, diffErr: map[string]error{}, hang: map[string]bool{}, hangOn: map[int64]bool{}}
}

func (f *fakeExec) Exec(ctx context.Context, _ string, stdin io.Reader, _ []string, cmd ...string) ([]byte, error) {
	b, _ := io.ReadAll(stdin)
	script := string(b)
	f.mu.Lock()
	f.scripts = append(f.scripts, script)
	f.cmds = append(f.cmds, cmd)
	f.mu.Unlock()

	if !strings.Contains(script, "EXCEPT") {
		var out strings.Builder
		for _, m := range listedTable.FindAllStringSubmatch(script, -1) {
			out.WriteString(f.list[m[1]])
		}
		return []byte(out.String()), f.listErr
	}

	m := comparedSnapshot.FindStringSubmatch(script)
	table := m[1]
	id, _ := strconv.ParseInt(m[2], 10, 64)
	if f.onCompare != nil {
		f.onCompare()
	}
	if f.rendezvous > 0 {
		f.mu.Lock()
		if f.ready == nil {
			f.ready = make(chan struct{})
		}
		if f.started++; f.started == f.rendezvous {
			close(f.ready)
		}
		ready := f.ready
		f.mu.Unlock()
		select {
		case <-ready:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.hang[table] || f.hangOn[id] {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	var out strings.Builder
	for _, line := range strings.SplitAfter(f.diff[table], "\n") {
		if strings.Contains(line, fmt.Sprintf(`"snapshot_id":%d,`, id)) || strings.Contains(line, `"row_kind":"stats"`) {
			out.WriteString(line)
		}
	}
	return []byte(out.String()), f.diffErr[table]
}

// lists counts the snapshot listing scripts sent.
func (f *fakeExec) lists() (n int) {
	for _, s := range f.scripts {
		if !strings.Contains(s, "EXCEPT") {
			n++
		}
	}
	return n
}

// diffs counts the comparison scripts sent. Each compares one snapshot.
func (f *fakeExec) diffs() (n int) {
	for _, s := range f.scripts {
		if strings.Contains(s, "EXCEPT") {
			n++
		}
	}
	return n
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
	if _, err := NewDuckDB(&fakeExec{}, "duckdb", testCatalog, []string{"patients", "visits", "patients"}); err == nil {
		t.Fatal("a table listed twice must be rejected, since its state is owned by one goroutine")
	}
}

func TestScriptQuotesSecretAndHidesSetupOutput(t *testing.T) {
	d, err := NewDuckDB(newFakeExec(), "duckdb", testCatalog, []string{"patients", "visits"})
	if err != nil {
		t.Fatal(err)
	}
	s := d.snapshotsScript()
	if !strings.Contains(s, `CLIENT_SECRET 'tf''secret'`) {
		t.Fatalf("secret not escaped as a SQL literal:\n%s", s)
	}
	if strings.Index(s, ".mode trash") > strings.Index(s, "CREATE SECRET") || strings.Index(s, ".mode json") < strings.Index(s, "ATTACH") {
		t.Fatalf("setup must run in trash mode before json output:\n%s", s)
	}
	for _, table := range []string{"patients", "visits"} {
		if !strings.Contains(s, fmt.Sprintf(`FROM iceberg_snapshots(tableflow."lkc-abc123"."%s")`, table)) {
			t.Fatalf("snapshot listing must read the %s snapshots:\n%s", table, s)
		}
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

// cursorIs checks the cursor the table's statement in the listing script
// uses.
func cursorIs(t *testing.T, d *DuckDB, table string, seq int64) {
	t.Helper()
	for _, stmt := range strings.Split(d.snapshotsScript(), "\n") {
		if strings.Contains(stmt, fmt.Sprintf("SELECT '%s' AS table_name", table)) {
			if !strings.Contains(stmt, fmt.Sprintf("sequence_number <= %d)", seq)) {
				t.Fatalf("%s cursor, want %d in:\n%s", table, seq, stmt)
			}
			return
		}
	}
	t.Fatalf("no listing statement for %s", table)
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
	// Each is compared with the snapshot before it, in a run of its own.
	fe.list["patients"] = snaps("patients", 2, 3, 4)
	fe.diff["patients"] = diffOut("patients", 103, 2, 120000, 170000) + diffOut("patients", 104, 0, 0, 0)
	res, err = d.Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(fe.scripts) != 4 {
		t.Fatalf("scripts = %d, want a listing and one comparison per snapshot", len(fe.scripts))
	}
	for i, want := range [][2]string{{"AT (VERSION => 103)", "AT (VERSION => 102)"}, {"AT (VERSION => 104)", "AT (VERSION => 103)"}} {
		for _, w := range want {
			if !strings.Contains(fe.scripts[2+i], w) {
				t.Fatalf("comparison script %d missing %q:\n%s", i, w, fe.scripts[2+i])
			}
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
	if err != nil || len(res.Points) != 0 || len(fe.scripts) != 5 {
		t.Fatalf("idle poll: res=%+v err=%v scripts=%d", res, err, len(fe.scripts))
	}
}

func TestQueryListsEveryTableInOneRun(t *testing.T) {
	fe := newFakeExec()
	tables := []string{"patients", "visits", "appointments"}
	for _, table := range tables {
		fe.list[table] = snaps(table, 1)
	}
	d, _ := NewDuckDB(fe, "duckdb", testCatalog, tables)
	if _, err := d.Query(context.Background()); err != nil {
		t.Fatal(err)
	}
	if fe.lists() != 1 || fe.diffs() != 0 {
		t.Fatalf("lists=%d diffs=%d, want one listing run for all tables", fe.lists(), fe.diffs())
	}
	for _, table := range tables {
		if !strings.Contains(fe.scripts[0], fmt.Sprintf(`"%s")`, table)) {
			t.Fatalf("listing misses %s:\n%s", table, fe.scripts[0])
		}
		cursorIs(t, d, table, 1)
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
	if len(res.Compares) != 0 || res.List.StorageBytes != 65536 || res.List.QueryMs != 12.5 {
		t.Fatalf("baseline stats = %+v, want the listing's figures and no comparison", res)
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
	if len(res.Compares) != 1 || len(res.Points) != 1 {
		t.Fatalf("compares = %+v, points = %+v", res.Compares, res.Points)
	}
	if c := res.Compares[0]; c.Table != "patients" || c.SnapshotID != 102 || c.Err != nil || c.Run.StorageRequests != 9 {
		t.Fatalf("compare = %+v", c)
	}
	for _, script := range fe.scripts {
		if !strings.Contains(script, statsSetup) || !strings.HasSuffix(script, statsSQL("https://tableflow.us-east-1.aws.confluent.cloud/")) {
			t.Fatalf("every run must enable and end with the stats query:\n%s", script)
		}
	}
}

func TestMonitorLogsRuns(t *testing.T) {
	res := Result{
		List: RunStats{Wall: time.Second},
		Compares: []CompareStats{
			{Table: "patients", SnapshotID: 102, Run: RunStats{Wall: 2 * time.Second, StorageBytes: 4096, StorageHTTPMs: 900}},
			{Table: "visits", SnapshotID: 203, Run: RunStats{Wall: 90 * time.Second}, Err: errors.New("duckdb query: context deadline exceeded")},
		},
	}
	runMonitor := func(level slog.Level) string {
		var buf bytes.Buffer
		log := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: level}))
		m := NewMonitor(&seqQuerier{results: []Result{res}}, time.Hour, log)
		ctx, cancel := context.WithCancel(context.Background())
		m.Run(ctx, func(Result) { cancel() })
		return buf.String()
	}

	out := runMonitor(slog.LevelInfo)
	if strings.Contains(out, "Listed table snapshots") {
		t.Fatalf("the snapshot listing runs every poll, so it must log at debug level:\n%s", out)
	}
	for _, want := range []string{
		`level=INFO msg="Ran latency query"`, "table=patients snapshot_id=102", "compare.wall_ms=2000", "compare.storage_bytes=4096", "compare.storage_http_ms=900",
		`level=WARN msg="Snapshot comparison failed"`, "table=visits snapshot_id=203", "compare.wall_ms=90000", `error="duckdb query: context deadline exceeded"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("log missing %q:\n%s", want, out)
		}
	}
	if out := runMonitor(slog.LevelDebug); !strings.Contains(out, `msg="Listed table snapshots"`) || !strings.Contains(out, "list.wall_ms=1000") {
		t.Fatalf("debug log must show the listing's figures:\n%s", out)
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
		if len(res.Compares) != 1 || res.Compares[0].Err == nil {
			t.Fatalf("attempt %d: compares = %+v, want the failed run reported", i, res.Compares)
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

func TestQueryResetsFailureCountAfterASuccess(t *testing.T) {
	fe := newFakeExec()
	fe.list["patients"] = snaps("patients", 1)
	d, _ := NewDuckDB(fe, "duckdb", testCatalog, []string{"patients"})
	if _, err := d.Query(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Snapshot 102 fails once and is then measured.
	fe.list["patients"] = snaps("patients", 1, 2)
	fe.diffErr["patients"] = errors.New("exit status 1: IO Error: unreadable")
	if res, err := d.Query(context.Background()); err != nil || len(res.Points) != 0 || len(res.Errors) != 1 {
		t.Fatalf("first attempt: res=%+v err=%v", res, err)
	}
	fe.diff["patients"] = diffOut("patients", 102, 1, 60000, 60000)
	if res, err := d.Query(context.Background()); err != nil || len(res.Points) != 1 {
		t.Fatalf("second attempt: res=%+v err=%v", res, err)
	}
	cursorIs(t, d, "patients", 2)

	// Snapshot 103 fails fewer times than maxAttempts. The earlier failure of
	// 102 must not count against it, or it would be skipped too soon.
	fe.list["patients"] = snaps("patients", 2, 3)
	fe.diff["patients"] = ""
	for i := 1; i < maxAttempts; i++ {
		res, err := d.Query(context.Background())
		if err != nil || len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Error, "comparing snapshot 103") {
			t.Fatalf("attempt %d: res=%+v err=%v, want 103 held back, not skipped", i, res, err)
		}
		cursorIs(t, d, "patients", 2)
	}
}

func TestQueryKeepsProgressWhenASnapshotTimesOut(t *testing.T) {
	fe := newFakeExec()
	fe.list["patients"] = snaps("patients", 1)
	d, _ := NewDuckDB(fe, "duckdb", testCatalog, []string{"patients"})
	d.timeout = 250 * time.Millisecond
	if _, err := d.Query(context.Background()); err != nil {
		t.Fatal(err)
	}

	// Snapshot 103 hangs. 102 has its own run, so it is still measured, and
	// 104 waits behind 103 so the table is measured in order.
	fe.list["patients"] = snaps("patients", 1, 2, 3, 4)
	fe.diff["patients"] = diffOut("patients", 102, 1, 60000, 60000) + diffOut("patients", 103, 1, 120000, 120000) + diffOut("patients", 104, 1, 180000, 180000)
	fe.hangOn[103] = true
	res, err := d.Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Points) != 1 || res.Points[0].SnapshotID != 102 {
		t.Fatalf("points = %+v, want 102 kept although 103 timed out", res.Points)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Error, "comparing snapshot 103") || !strings.Contains(res.Errors[0].Error, "deadline exceeded") {
		t.Fatalf("errors = %+v, want 103 reported as timed out", res.Errors)
	}
	if fe.diffs() != 2 {
		t.Fatalf("compared %d snapshots, want 102 and 103 only", fe.diffs())
	}
	cursorIs(t, d, "patients", 2)

	// The next poll resumes at 103 instead of redoing 102.
	delete(fe.hangOn, 103)
	res, err = d.Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Points) != 2 || res.Points[0].SnapshotID != 103 || res.Points[1].SnapshotID != 104 || len(res.Errors) != 0 {
		t.Fatalf("res = %+v, want 103 and 104 measured", res)
	}
	cursorIs(t, d, "patients", 4)
}

func TestQueryIsolatesTables(t *testing.T) {
	fe := newFakeExec()
	tables := []string{"patients", "visits", "appointments"}
	d, _ := NewDuckDB(fe, "duckdb", testCatalog, tables)
	d.timeout = 100 * time.Millisecond
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
	if fe.lists() != 2 {
		t.Fatalf("listing runs = %d, want one per poll", fe.lists())
	}
	cursorIs(t, d, "patients", 1)
	cursorIs(t, d, "visits", 2)
}

func TestQueryComparesTablesInParallel(t *testing.T) {
	fe := newFakeExec()
	tables := []string{"patients", "visits", "appointments"}
	for _, table := range tables {
		fe.list[table] = snaps(table, 1)
	}
	d, _ := NewDuckDB(fe, "duckdb", testCatalog, tables)
	d.timeout = 2 * time.Second
	if _, err := d.Query(context.Background()); err != nil {
		t.Fatal(err)
	}

	// No comparison finishes until all three have started, so the poll only
	// completes if the tables are compared at the same time.
	fe.rendezvous = len(tables)
	for _, table := range tables {
		fe.list[table] = snaps(table, 1, 2)
		fe.diff[table] = diffOut(table, 102, 1, 60000, 60000)
	}
	res, err := d.Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Errors) != 0 || len(res.Points) != len(tables) {
		t.Fatalf("res = %+v, want a point per table", res)
	}
	for i, table := range tables {
		if res.Points[i].Table != table {
			t.Fatalf("points = %+v, want them in table order", res.Points)
		}
	}
}

func TestQueryReportsTableThatCannotBeListed(t *testing.T) {
	fe := newFakeExec()
	d, _ := NewDuckDB(fe, "duckdb", testCatalog, []string{"patients", "appointments"})
	fe.list["patients"] = snaps("patients", 1)
	fe.list["appointments"] = snaps("appointments", 1)
	if _, err := d.Query(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The CLI carries on after the failed statement, so patients' listing
	// still arrives, and the poll's error is not blamed on it.
	delete(fe.list, "appointments")
	fe.list["patients"] = snaps("patients", 1, 2)
	fe.diff["patients"] = diffOut("patients", 102, 1, 60000, 60000)
	fe.listErr = errors.New("exit status 1: Catalog Error: Table appointments does not exist")
	res, err := d.Query(context.Background())
	if err != nil {
		t.Fatalf("Query() = %v, want the table's error and the others' results", err)
	}
	if len(res.Points) != 1 || res.Points[0].Table != "patients" {
		t.Fatalf("points = %+v, want patients measured", res.Points)
	}
	if len(res.Errors) != 1 || res.Errors[0].Table != "appointments" || !strings.Contains(res.Errors[0].Error, "does not exist") {
		t.Fatalf("errors = %+v, want appointments' listing error", res.Errors)
	}
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
	if n := fe.diffs(); n != maxSnapshotsPerPoll {
		t.Fatalf("compared %d snapshots, want %d", n, maxSnapshotsPerPoll)
	}
	cursorIs(t, d, "patients", int64(1+maxSnapshotsPerPoll))

	if _, err := d.Query(context.Background()); err != nil {
		t.Fatal(err)
	}
	if n := fe.diffs(); n != backlog {
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
	if n := fe.diffs(); n != 1 || !strings.Contains(fe.scripts[len(fe.scripts)-1], "AT (VERSION => 105)") {
		t.Fatalf("want only 106 compared with 105:\n%s", fe.scripts[len(fe.scripts)-1])
	}
	if len(res.Points) != 1 || res.Points[0].SnapshotID != 106 {
		t.Fatalf("points = %+v", res.Points)
	}
	cursorIs(t, d, "patients", 6)
}

func TestQueryFailsWhenNothingSucceeded(t *testing.T) {
	fe := newFakeExec()
	fe.listErr = errors.New("401 Unauthorized")
	d, _ := NewDuckDB(fe, "duckdb", testCatalog, []string{"patients", "visits"})
	_, err := d.Query(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("Query() = %v, want catalog error", err)
	}
	if fe.lists() != 1 {
		t.Fatalf("listing runs = %d, want a single run for both tables", fe.lists())
	}
	if !strings.Contains(fe.scripts[0], "CREATE SECRET") {
		t.Fatal("script must be sent on stdin, not as arguments")
	}
}

func TestQueryDoesNotCountShutdownAsAFailure(t *testing.T) {
	fe := newFakeExec()
	fe.list["patients"] = snaps("patients", 1)
	d, _ := NewDuckDB(fe, "duckdb", testCatalog, []string{"patients"})
	if _, err := d.Query(context.Background()); err != nil {
		t.Fatal(err)
	}

	// The poll is cancelled as the comparison starts, so the cancellation
	// lands in the comparison and not in the listing.
	fe.list["patients"] = snaps("patients", 1, 2)
	fe.hang["patients"] = true
	ctx, cancel := context.WithCancel(context.Background())
	fe.onCompare = cancel
	if _, err := d.Query(ctx); !errors.Is(err, context.Canceled) || fe.diffs() != 1 {
		t.Fatalf("Query() = %v after %d comparisons, want the cancellation of the first", err, fe.diffs())
	}
	if got := d.state["patients"].failures; got != 0 {
		t.Fatalf("failures = %d, want a cancelled run not to count against the snapshot", got)
	}
	cursorIs(t, d, "patients", 1)
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

func TestDuckDBCommandRoundsTheLimitUp(t *testing.T) {
	for _, c := range []struct {
		limit time.Duration
		want  string
	}{
		{95 * time.Second, "95s"},
		{250*time.Millisecond + killGrace, "6s"},
		{time.Millisecond, "1s"},
		{0, "1s"}, // timeout treats 0 as no limit at all
	} {
		got := duckdbCommand(c.limit)
		want := []string{"timeout", "--signal=KILL", c.want, "duckdb", "-batch"}
		if !slices.Equal(got, want) {
			t.Errorf("duckdbCommand(%v) = %q, want %q", c.limit, got, want)
		}
	}
}

// TestQueryRunsEveryScriptUnderAContainerTimeout checks the container is
// told to kill DuckDB shortly after the host gives up on it, for the listing
// and the comparisons alike. Killing the docker client does not stop the
// process it started.
func TestQueryRunsEveryScriptUnderAContainerTimeout(t *testing.T) {
	fe := newFakeExec()
	fe.list["patients"] = snaps("patients", 1)
	d, _ := NewDuckDB(fe, "duckdb", testCatalog, []string{"patients"})
	d.timeout = 20 * time.Second
	if _, err := d.Query(context.Background()); err != nil {
		t.Fatal(err)
	}
	fe.list["patients"] = snaps("patients", 1, 2)
	fe.diff["patients"] = diffOut("patients", 102, 1, 1000, 1000)
	if _, err := d.Query(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fe.cmds) != 3 {
		t.Fatalf("ran %d scripts, want 2 listings and a comparison", len(fe.cmds))
	}
	want := []string{"timeout", "--signal=KILL", "25s", "duckdb", "-batch"}
	for i, cmd := range fe.cmds {
		if !slices.Equal(cmd, want) {
			t.Errorf("script %d ran as %q, want %q", i, cmd, want)
		}
	}
}

// TestContainerKillsDuckDBWhenItsClientIsGone starts the real DuckDB image
// on a query that would run for minutes, kills the docker client as a host
// deadline would, and checks the container's own timeout still ends the
// process. It needs the pims-duckdb image.
func TestContainerKillsDuckDBWhenItsClientIsGone(t *testing.T) {
	if err := exec.Command("docker", "image", "inspect", "pims-duckdb").Run(); err != nil {
		t.Skip("pims-duckdb image not built")
	}
	name := fmt.Sprintf("pims-test-timeout-%d", time.Now().UnixNano())
	t.Cleanup(func() { exec.Command("docker", "rm", "-f", name).Run() })
	running := func() bool {
		out, err := exec.Command("docker", "ps", "-q", "--filter", "name=^"+name+"$").Output()
		if err != nil {
			t.Fatalf("docker ps: %v", err)
		}
		return len(bytes.TrimSpace(out)) > 0
	}

	ctx, cancel := context.WithCancel(context.Background())
	args := append([]string{"run", "--rm", "-i", "--name", name, "pims-duckdb"}, duckdbCommand(5*time.Second)...)
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdin = strings.NewReader("SELECT sum(hash(i)) FROM range(100000000000) t(i);\n")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	// Cancel before waiting, so a failure below cannot leave Wait hanging on a
	// client that is still attached.
	defer func() { cancel(); cmd.Wait() }()

	start := time.Now()
	for !running() {
		if time.Since(start) > 30*time.Second {
			t.Fatal("container did not start")
		}
		time.Sleep(100 * time.Millisecond)
	}
	cancel() // kills the client only
	killed := time.Now()
	// The premise: the process outlives its client. If it is gone already, the
	// limit ran out first or the client took it down, and what follows would
	// pass without showing the timeout works.
	time.Sleep(time.Second)
	if !running() {
		t.Fatal("container ended with its client, so the timeout was not what stopped it")
	}
	for running() {
		if time.Since(killed) > 20*time.Second {
			t.Fatal("DuckDB is still running in the container after its timeout")
		}
		time.Sleep(200 * time.Millisecond)
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
	// Through the timeout wrapper the app uses: it must pass stdin and the exit
	// status of a failed statement through.
	cmd := exec.Command("docker", append([]string{"run", "--rm", "-i", "pims-duckdb"}, duckdbCommand(time.Minute)...)...)
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
