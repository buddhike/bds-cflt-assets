// Package latency measures end-to-end replication latency from MySQL to
// the Tableflow Iceberg tables.
//
// Each new Iceberg snapshot of a table is compared with the snapshot before
// it. The rows present in the new snapshot but not in the previous one,
// matched on (id, updated_at), are the inserts and updates that snapshot
// published. Each of them waited from its MySQL updated_at until the
// snapshot was committed, which covers the whole path: MySQL write, Debezium
// capture, Kafka, Tableflow materialisation and S3 commit. Snapshots that
// publish no new rows, such as compaction, produce no measurement.
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
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Catalog holds the Tableflow Iceberg REST catalog connection.
type Catalog struct {
	// Endpoint is the REST catalog URL, e.g.
	// https://tableflow.us-east-1.aws.confluent.cloud/iceberg/catalog/organizations/<org>/environments/<env>
	Endpoint string
	// APIKey and APISecret are a Tableflow API key, used as OAuth client
	// credentials.
	APIKey    string
	APISecret string
	// Namespace is the Kafka cluster id; Tableflow publishes each topic as
	// a table in a namespace named after its cluster.
	Namespace string
}

// LogValue keeps the secret out of logs.
func (c Catalog) LogValue() slog.Value {
	return slog.GroupValue(slog.String("endpoint", c.Endpoint), slog.String("namespace", c.Namespace))
}

var (
	namespacePattern = regexp.MustCompile(`^lkc-[a-z0-9]+$`)
	tablePattern     = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)
)

// Validate rejects anything that could not be safely embedded in SQL.
func (c Catalog) Validate() error {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("tableflow catalog endpoint must be an https URL, got %q", c.Endpoint)
	}
	if !namespacePattern.MatchString(c.Namespace) {
		return fmt.Errorf("invalid kafka cluster id %q", c.Namespace)
	}
	if c.APIKey == "" || c.APISecret == "" {
		return errors.New("tableflow api key and secret are required")
	}
	return nil
}

// Point is the latency of one Iceberg snapshot: the rows it published and
// how long they took to get there.
type Point struct {
	Table string `json:"table"`
	// SnapshotID is the Iceberg snapshot that published the rows. It is a
	// string in JSON because snapshot ids exceed JavaScript's safe integers.
	SnapshotID int64 `json:"snapshotId,string"`
	// Records is how many inserted or updated rows the snapshot published.
	Records int64 `json:"records"`
	// RecordAt is the oldest updated_at among those rows.
	RecordAt time.Time `json:"recordAt"`
	// NewestAt is the newest updated_at among those rows.
	NewestAt time.Time `json:"newestAt"`
	// CommittedAt is the snapshot's commit time.
	CommittedAt time.Time `json:"committedAt"`
	// LatencyMs is CommittedAt - RecordAt in milliseconds: the longest any
	// of the rows waited.
	LatencyMs int64 `json:"latencyMs"`
	// MinLatencyMs is CommittedAt - NewestAt in milliseconds: the shortest
	// wait.
	MinLatencyMs int64 `json:"minLatencyMs"`
	// ObservedAt is when PIMS ran the query.
	ObservedAt time.Time `json:"observedAt"`
}

// TableError reports a table that could not be queried, for example
// because Tableflow has not materialised it yet.
type TableError struct {
	Table string `json:"table"`
	Error string `json:"error"`
}

// Result is the outcome of one poll.
type Result struct {
	Points []Point      `json:"points"`
	Errors []TableError `json:"errors,omitempty"`
	// List and Compares describe the DuckDB work behind the poll. They are
	// logged, not sent to the UI.
	//
	// List is the one run that listed every table's snapshots. Compares has
	// an entry for each snapshot comparison run, in table order.
	List     RunStats       `json:"-"`
	Compares []CompareStats `json:"-"`
}

// CompareStats describes the DuckDB run that compared one snapshot with the
// snapshot before it.
type CompareStats struct {
	Table      string
	SnapshotID int64
	Run        RunStats
	// Err is why the run produced no measurement for the snapshot, or nil.
	Err error
}

// RunStats describes one DuckDB CLI run. Wall is measured by PIMS and
// covers the whole run: container exec, CLI start, extension loading,
// sign-in, attach and the queries. The rest comes from DuckDB's own
// profiling metrics and HTTP request log, and is zero when the run failed
// before reporting them.
type RunStats struct {
	Wall time.Duration `json:"-"`
	// QueryMs is the summed latency of the run's SELECT statements.
	QueryMs float64 `json:"query_ms"`
	// OperatorMs is DuckDB's CPU_TIME: time spent inside operators, summed
	// over threads. A scan waiting on an HTTP read counts too, so it is not
	// pure CPU; the HTTP figures show the I/O.
	OperatorMs float64 `json:"operator_ms"`
	// BlockedMs is time threads spent blocked waiting for work.
	BlockedMs       float64 `json:"blocked_ms"`
	RowsScanned     int64   `json:"rows_scanned"`
	PeakMemoryBytes int64   `json:"peak_memory_bytes"`
	// Catalog* cover requests to the Tableflow REST catalog: sign-in,
	// attach, table and snapshot metadata.
	CatalogRequests int64 `json:"catalog_requests"`
	CatalogHTTPMs   int64 `json:"catalog_http_ms"`
	// Storage* cover every other request: metadata, manifest and data
	// files in Tableflow's storage. Bytes sums the declared length of GET
	// responses; HEADs report a whole file's size without sending it.
	StorageRequests int64 `json:"storage_requests"`
	StorageHTTPMs   int64 `json:"storage_http_ms"`
	StorageBytes    int64 `json:"storage_bytes"`
}

// LogValue renders the run as a log group. HTTP times are summed over
// requests, which can run in parallel, so they can exceed the wall time.
func (r RunStats) LogValue() slog.Value {
	return slog.GroupValue(
		slog.Int64("wall_ms", r.Wall.Milliseconds()),
		slog.Int64("query_ms", int64(r.QueryMs)),
		slog.Int64("operator_ms", int64(r.OperatorMs)),
		slog.Int64("blocked_ms", int64(r.BlockedMs)),
		slog.Int64("rows_scanned", r.RowsScanned),
		slog.Int64("peak_memory_bytes", r.PeakMemoryBytes),
		slog.Int64("catalog_requests", r.CatalogRequests),
		slog.Int64("catalog_http_ms", r.CatalogHTTPMs),
		slog.Int64("storage_requests", r.StorageRequests),
		slog.Int64("storage_http_ms", r.StorageHTTPMs),
		slog.Int64("storage_bytes", r.StorageBytes),
	)
}

// Execer runs a program in the DuckDB container with stdin attached.
// compose.Project.Exec satisfies it.
type Execer interface {
	Exec(ctx context.Context, service string, stdin io.Reader, redact []string, cmd ...string) ([]byte, error)
}

const (
	// maxSnapshotsPerPoll bounds the snapshots measured for a table in one
	// poll, so a backlog cannot keep a poll's results from the UI for long.
	// The rest are measured on later polls.
	maxSnapshotsPerPoll = 5
	// maxAttempts is how many polls may fail to compare a snapshot before it
	// is skipped, so one unreadable snapshot cannot stall its table.
	maxAttempts = 3
	// defaultExecTimeout bounds each DuckDB run. A comparison run covers one
	// snapshot, and tables run in parallel, so a slow snapshot or table
	// cannot starve the others or discard their results.
	defaultExecTimeout = 90 * time.Second
	// killGrace is how far the container's own deadline trails the host's.
	// The host's deadline fires first, so a timeout is reported as one, and
	// the container's only has to stop what the host could not. It also
	// bounds how long a run can outlive the app when it quits mid-poll: the
	// host drops the docker client at once, and the container stops DuckDB at
	// its own deadline, at most defaultExecTimeout+killGrace after the run began.
	killGrace = 5 * time.Second
)

// duckdbCommand returns the command that runs the DuckDB CLI in the
// container, killed once limit has passed.
//
// The host's deadline cannot do this: it kills only the docker client, and
// docker exec leaves the process it started running in the container. An
// abandoned run would keep fetching from storage, competing for bandwidth with
// the runs that follow it, and every poll that timed out would leave another
// one behind. KILL rather than TERM, so DuckDB cannot finish a request first.
//
// It relies on GNU coreutils' timeout and its long options, which the
// Debian-based pims-duckdb image has. Busybox's would not do.
func duckdbCommand(limit time.Duration) []string {
	// Whole seconds, rounded up, and at least one: timeout treats 0 as no limit.
	secs := max((limit+time.Second-1)/time.Second, 1)
	return []string{"timeout", "--signal=KILL", fmt.Sprintf("%ds", secs), "duckdb", "-batch"}
}

// DuckDB queries Tableflow tables with the DuckDB CLI in its container.
//
// It remembers the last snapshot it measured for each table, so Query is
// not safe for concurrent use; the Monitor calls it from one goroutine.
// Within a poll each table is handled by its own goroutine.
type DuckDB struct {
	exec    Execer
	service string
	catalog Catalog
	tables  []string
	now     func() time.Time
	timeout time.Duration
	// state is what the querier remembers about each table. The map is
	// fixed at construction and a poll hands each table's entry to one
	// goroutine, so it needs no lock.
	state map[string]*tableState
}

// tableState is what the querier remembers about one table between polls.
type tableState struct {
	// measured is whether the table has a baseline: its first poll records
	// the latest snapshot without measuring it.
	measured bool
	// cursor is the sequence number of the last snapshot measured.
	cursor int64
	// failures counts the polls that failed to compare the table's next
	// snapshot.
	failures int
}

// NewDuckDB returns a querier for tables in the given catalog.
func NewDuckDB(exec Execer, service string, catalog Catalog, tables []string) (*DuckDB, error) {
	if err := catalog.Validate(); err != nil {
		return nil, err
	}
	state := make(map[string]*tableState, len(tables))
	for _, t := range tables {
		if !tablePattern.MatchString(t) {
			return nil, fmt.Errorf("invalid table name %q", t)
		}
		if state[t] != nil {
			// Each table's state belongs to one goroutine during a poll.
			return nil, fmt.Errorf("duplicate table name %q", t)
		}
		state[t] = &tableState{}
	}
	return &DuckDB{
		exec: exec, service: service, catalog: catalog, tables: tables, now: time.Now, timeout: defaultExecTimeout,
		state: state,
	}, nil
}

// preamble turns on DuckDB's metrics and HTTP logs and attaches the
// catalog. Setup statements run in trash mode so only the query results
// reach stdout.
func (d *DuckDB) preamble(b *strings.Builder) {
	ep := sqlString(d.catalog.Endpoint)
	b.WriteString(".mode trash\n")
	b.WriteString("SET TimeZone = 'UTC';\n")
	b.WriteString(statsSetup)
	b.WriteString("LOAD httpfs;\nLOAD iceberg;\n")
	fmt.Fprintf(b, "CREATE SECRET tableflow (TYPE ICEBERG, CLIENT_ID %s, CLIENT_SECRET %s, ENDPOINT %s, OAUTH2_SCOPE 'catalog');\n",
		sqlString(d.catalog.APIKey), sqlString(d.catalog.APISecret), ep)
	fmt.Fprintf(b, "ATTACH 'warehouse' AS tableflow (TYPE iceberg, SECRET tableflow, ENDPOINT %s);\n", ep)
	b.WriteString(".mode json\n")
}

func (d *DuckDB) ref(table string) string {
	return fmt.Sprintf(`tableflow."%s"."%s"`, d.catalog.Namespace, table)
}

// snapshotsScript lists, for every table, the snapshots that still need
// measuring. One run covers all the tables, so the CLI start, sign-in and
// attach, which cost seconds over a slow link, are paid once per poll. The
// CLI carries on after a failed statement, so a table that cannot be read
// does not hide the others' snapshots.
func (d *DuckDB) snapshotsScript() string {
	var b strings.Builder
	d.preamble(&b)
	for _, table := range d.tables {
		after := d.state[table].cursor
		if !d.state[table].measured {
			// Not measured yet: list only the latest snapshot, which becomes
			// the baseline.
			after = math.MaxInt64
		}
		b.WriteString(snapshotsSQL(table, fmt.Sprintf("iceberg_snapshots(%s)", d.ref(table)), after))
	}
	b.WriteString(statsSQL(d.catalogPrefix()))
	return b.String()
}

// catalogPrefix is the URL prefix of requests to the catalog's host.
func (d *DuckDB) catalogPrefix() string {
	u, _ := url.Parse(d.catalog.Endpoint) // checked by Validate
	return "https://" + u.Host + "/"
}

// statsSetup keeps DuckDB's profiling metrics and HTTP request log in
// memory for statsSQL to read. Using SET rather than enable_logging()
// keeps the CLI from printing a warning to stdout. Only aggregates leave
// DuckDB: the request log also holds request headers, including the
// catalog token.
const statsSetup = "SET logging_storage = 'memory';\nSET logging_level = 'debug';\nSET logging_mode = 'enable_selected';\n" +
	"SET enabled_log_types = 'Metrics,HTTP';\nSET enable_logging = true;\nSET enable_profiling = 'no_output';\n"

// statsSQL summarises the run's metrics and HTTP requests in one row
// tagged row_kind 'stats'. It runs last, so its own metrics are not
// included. Requests whose URL starts with catalogPrefix count as catalog
// requests, the rest as storage.
func statsSQL(catalogPrefix string) string {
	return fmt.Sprintf("WITH h AS (SELECT starts_with(request.url, %s) AS catalog, request.type AS type, request.duration_ms AS ms, "+
		"TRY_CAST(coalesce(response.headers['Content-Length'], response.headers['content-length']) AS BIGINT) AS bytes FROM duckdb_logs_parsed('HTTP')), "+
		"m AS (SELECT metric, TRY_CAST(value AS DOUBLE) AS v FROM duckdb_logs_parsed('Metrics')) "+
		"SELECT 'stats' AS row_kind, "+
		"(SELECT coalesce(sum(v), 0) * 1000 FROM m WHERE metric = 'LATENCY') AS query_ms, "+
		"(SELECT coalesce(sum(v), 0) * 1000 FROM m WHERE metric = 'CPU_TIME') AS operator_ms, "+
		"(SELECT coalesce(sum(v), 0) * 1000 FROM m WHERE metric = 'BLOCKED_THREAD_TIME') AS blocked_ms, "+
		"(SELECT coalesce(sum(v), 0)::BIGINT FROM m WHERE metric = 'CUMULATIVE_ROWS_SCANNED') AS rows_scanned, "+
		"(SELECT coalesce(max(v), 0)::BIGINT FROM m WHERE metric = 'SYSTEM_PEAK_BUFFER_MEMORY') AS peak_memory_bytes, "+
		"(SELECT count(*) FROM h WHERE catalog) AS catalog_requests, "+
		"(SELECT coalesce(sum(ms), 0)::BIGINT FROM h WHERE catalog) AS catalog_http_ms, "+
		"(SELECT count(*) FROM h WHERE NOT catalog) AS storage_requests, "+
		"(SELECT coalesce(sum(ms), 0)::BIGINT FROM h WHERE NOT catalog) AS storage_http_ms, "+
		"(SELECT coalesce(sum(bytes), 0)::BIGINT FROM h WHERE NOT catalog AND type = 'GET') AS storage_bytes;\n",
		sqlString(catalogPrefix))
}

// snapshotsSQL selects the snapshots in src committed after the one with
// sequence number after, plus the newest snapshot at or before it to
// compare the first of them with. src is iceberg_snapshots(...) in
// production and a plain table in tests.
//
// Ordering by sequence number relies on Iceberg format v2 or later, where
// every commit gets a higher one; v1 tables report 0 for all snapshots.
// Tableflow writes v2 tables.
func snapshotsSQL(table, src string, after int64) string {
	// iceberg_snapshots returns UBIGINTs, which DuckDB prints as JSON
	// strings; BIGINT prints as a number. Casting to TIMESTAMPTZ accepts
	// timestamp, timestamptz or ISO strings.
	return fmt.Sprintf("SELECT '%s' AS table_name, snapshot_id::BIGINT AS snapshot_id, sequence_number::BIGINT AS sequence_number, "+
		"epoch_ms(timestamp_ms::TIMESTAMPTZ) AS committed_ms FROM %s "+
		"WHERE sequence_number >= coalesce((SELECT max(sequence_number) FROM %s WHERE sequence_number <= %d), 0) "+
		"ORDER BY sequence_number;\n",
		table, src, src, after)
}

// pair is a snapshot to measure and the snapshot before it.
type pair struct {
	prev, curr snapshotRow
}

// diffScript measures one pair of the table's snapshots. Each pair gets its
// own run, and so its own deadline: a run that times out loses only its own
// snapshot, not the ones measured before it.
func (d *DuckDB) diffScript(table string, p pair) string {
	var b strings.Builder
	d.preamble(&b)
	ref := d.ref(table)
	b.WriteString(diffSQL(table, p.curr.SnapshotID,
		fmt.Sprintf("%s AT (VERSION => %d)", ref, p.curr.SnapshotID),
		fmt.Sprintf("%s AT (VERSION => %d)", ref, p.prev.SnapshotID)))
	b.WriteString(statsSQL(d.catalogPrefix()))
	return b.String()
}

// diffSQL summarises the rows in curr but not in prev. Matching on
// (id, updated_at) counts an update to an existing row as new, since MySQL
// bumps updated_at on every change, and ignores rows compaction only moved.
func diffSQL(table string, snapshotID int64, curr, prev string) string {
	return fmt.Sprintf("WITH d AS (SELECT id, updated_at FROM %s EXCEPT SELECT id, updated_at FROM %s) "+
		"SELECT '%s' AS table_name, %d AS snapshot_id, count(*) AS records, "+
		"epoch_ms(min(updated_at::TIMESTAMPTZ)) AS oldest_ms, epoch_ms(max(updated_at::TIMESTAMPTZ)) AS newest_ms FROM d;\n",
		curr, prev, table, snapshotID)
}

type snapshotRow struct {
	Table       string `json:"table_name"`
	SnapshotID  int64  `json:"snapshot_id"`
	Sequence    int64  `json:"sequence_number"`
	CommittedMs int64  `json:"committed_ms"`
}

type diffRow struct {
	Table      string `json:"table_name"`
	SnapshotID int64  `json:"snapshot_id"`
	Records    int64  `json:"records"`
	OldestMs   *int64 `json:"oldest_ms"`
	NewestMs   *int64 `json:"newest_ms"`
}

// Query runs one poll and returns a point for every snapshot committed
// since the previous poll. The first poll of a table only records its
// latest snapshot as the baseline: the rows in it may predate Tableflow
// being enabled, so their wait says nothing about the pipeline.
//
// One run lists every table's snapshots. Each new snapshot is then compared
// with its predecessor in a run of its own, and the tables are compared in
// parallel. A poll therefore takes as long as its slowest table rather than
// the sum of them, and a run that times out loses only its own snapshot.
// Tables that fail are reported in Result.Errors rather than failing the
// whole poll, since Tableflow materialises each topic independently. A
// snapshot that could not be measured is retried on the next poll, up to
// maxAttempts.
func (d *DuckDB) Query(ctx context.Context) (Result, error) {
	var res Result
	rows, execErr, err := run[snapshotRow](ctx, d, d.snapshotsScript(), &res.List)
	if err != nil {
		return Result{List: res.List}, err
	}
	snapshots := map[string][]snapshotRow{}
	for _, r := range rows {
		snapshots[r.Table] = append(snapshots[r.Table], r)
	}
	if len(snapshots) == 0 && execErr != nil {
		// Nothing could be listed: most likely the catalog could not be
		// reached. The stats still show how long the attempt took.
		return Result{List: res.List}, fmt.Errorf("duckdb query: %w", execErr)
	}

	outcomes := make([]tableOutcome, len(d.tables))
	var wg sync.WaitGroup
	for i, table := range d.tables {
		snaps := snapshots[table]
		if len(snaps) == 0 {
			outcomes[i].problem = "no snapshots yet"
			if execErr != nil {
				// The run's error is not attributed to a table; DuckDB's
				// message names the one that failed.
				outcomes[i].problem = fmt.Sprintf("duckdb query: %v", execErr)
			}
			continue
		}
		pairs := d.plan(table, snaps)
		if len(pairs) == 0 {
			continue
		}
		wg.Go(func() { d.measure(ctx, table, pairs, &outcomes[i]) })
	}
	wg.Wait()
	if ctx.Err() != nil {
		return Result{}, fmt.Errorf("duckdb query: %w", ctx.Err())
	}

	for i, o := range outcomes {
		res.Points = append(res.Points, o.points...)
		res.Compares = append(res.Compares, o.compares...)
		if o.problem != "" {
			res.Errors = append(res.Errors, TableError{Table: d.tables[i], Error: o.problem})
		}
	}
	return res, nil
}

// tableOutcome is what one poll found for one table.
type tableOutcome struct {
	points   []Point
	compares []CompareStats
	// problem explains a table that is readable but could not be fully
	// measured.
	problem string
}

// plan returns the snapshot pairs to measure for the table, given its
// listing. It also sets the table's baseline, and moves its cursor when the
// snapshots it was measuring have expired.
func (d *DuckDB) plan(table string, snaps []snapshotRow) []pair {
	st := d.state[table]
	if !st.measured {
		// Baseline: measuring starts after the latest snapshot.
		st.measured, st.cursor = true, snaps[len(snaps)-1].Sequence
		return nil
	}
	if snaps[0].Sequence > st.cursor {
		// The snapshots up to and after the last measured one have expired,
		// so the first one listed has nothing to compare with.
		st.cursor = snaps[0].Sequence
	}
	var pairs []pair
	for i := 1; i < len(snaps) && len(pairs) < maxSnapshotsPerPoll; i++ {
		if snaps[i].Sequence > st.cursor {
			pairs = append(pairs, pair{prev: snaps[i-1], curr: snaps[i]})
		}
	}
	return pairs
}

// measure compares each pair's snapshot with its predecessor, one run per
// pair and in order. It moves the table's cursor past a snapshot as soon as
// it has been measured, so a later failure keeps the progress made.
func (d *DuckDB) measure(ctx context.Context, table string, pairs []pair, out *tableOutcome) {
	st := d.state[table]
	for _, p := range pairs {
		cs := CompareStats{Table: table, SnapshotID: p.curr.SnapshotID}
		diffs, diffErr, err := run[diffRow](ctx, d, d.diffScript(table, p), &cs.Run)
		if ctx.Err() != nil {
			// Shutting down, which is no fault of the snapshot's.
			return
		}
		if err != nil {
			// A timeout or unparseable output.
			diffs, diffErr = nil, err
		}
		var r diffRow
		found := false
		for _, row := range diffs {
			if row.SnapshotID == p.curr.SnapshotID {
				r, found = row, true
				break
			}
		}
		if !found {
			cs.Err = errors.New("snapshot comparison returned no result")
			if diffErr != nil {
				cs.Err = diffErr
			}
		}
		out.compares = append(out.compares, cs)
		if !found {
			if st.failures++; st.failures < maxAttempts {
				// Keep the cursor here so this snapshot is retried, and stop
				// so the table's snapshots are measured in order.
				out.problem = fmt.Sprintf("comparing snapshot %d: %v", p.curr.SnapshotID, cs.Err)
				return
			}
			out.problem = fmt.Sprintf("skipped snapshot %d after %d failed comparisons: %v", p.curr.SnapshotID, maxAttempts, cs.Err)
		}
		st.cursor = p.curr.Sequence
		st.failures = 0
		if !found || r.Records == 0 || r.OldestMs == nil || r.NewestMs == nil {
			continue
		}
		com := time.UnixMilli(p.curr.CommittedMs).UTC()
		oldest, newest := time.UnixMilli(*r.OldestMs).UTC(), time.UnixMilli(*r.NewestMs).UTC()
		out.points = append(out.points, Point{
			Table:        table,
			SnapshotID:   p.curr.SnapshotID,
			Records:      r.Records,
			RecordAt:     oldest,
			NewestAt:     newest,
			CommittedAt:  com,
			LatencyMs:    com.Sub(oldest).Milliseconds(),
			MinLatencyMs: com.Sub(newest).Milliseconds(),
			ObservedAt:   d.now().UTC(),
		})
	}
}

// run executes script with its own deadline and decodes its rows, filling
// stats with the run's wall time and DuckDB's own figures. execErr is
// DuckDB's own failure, which may still come with rows from the statements
// that succeeded; err means no rows could be read.
func run[T any](ctx context.Context, d *DuckDB, script string, stats *RunStats) (rows []T, execErr, err error) {
	ctx, cancel := context.WithTimeout(ctx, d.timeout)
	defer cancel()
	redact := []string{d.catalog.APISecret, d.catalog.APIKey}
	start := time.Now()
	out, execErr := d.exec.Exec(ctx, d.service, strings.NewReader(script), redact, duckdbCommand(d.timeout+killGrace)...)
	stats.Wall = time.Since(start)
	if ctx.Err() != nil {
		return nil, nil, fmt.Errorf("duckdb query: %w", ctx.Err())
	}
	rows, err = parseRows[T](out, stats)
	if err != nil {
		return nil, nil, fmt.Errorf("parse duckdb output: %w", err)
	}
	return rows, execErr, nil
}

// parseRows decodes the concatenated JSON arrays the CLI prints, one per
// successful SELECT. The statsSQL row is decoded into stats instead.
func parseRows[T any](out []byte, stats *RunStats) ([]T, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	var rows []T
	for {
		var batch []json.RawMessage
		err := dec.Decode(&batch)
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		for _, raw := range batch {
			var kind struct {
				Kind string `json:"row_kind"`
			}
			if err := json.Unmarshal(raw, &kind); err != nil {
				return nil, err
			}
			if kind.Kind == "stats" {
				if err := json.Unmarshal(raw, stats); err != nil {
					return nil, err
				}
				continue
			}
			var r T
			if err := json.Unmarshal(raw, &r); err != nil {
				return nil, err
			}
			rows = append(rows, r)
		}
	}
}

// sqlString quotes s as a SQL string literal.
func sqlString(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// Querier is satisfied by DuckDB; tests substitute a fake.
type Querier interface {
	Query(ctx context.Context) (Result, error)
}

// Monitor polls a Querier and reports new latency points.
type Monitor struct {
	q        Querier
	interval time.Duration
	log      *slog.Logger
}

// DefaultInterval is used when NewMonitor is given a non-positive interval.
const DefaultInterval = 30 * time.Second

// NewMonitor returns a Monitor that polls every interval.
func NewMonitor(q Querier, interval time.Duration, log *slog.Logger) *Monitor {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Monitor{q: q, interval: interval, log: log.With("component", "latency")}
}

// Run polls until ctx is cancelled, calling onResult with the points for
// new Iceberg snapshots and the tables that could not be read.
func (m *Monitor) Run(ctx context.Context, onResult func(Result)) {
	t := time.NewTicker(m.interval)
	defer t.Stop()
	for {
		res, err := m.q.Query(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			m.logRuns(ctx, res)
			m.log.Warn("Latency query failed", "error", err)
			onResult(Result{Errors: []TableError{{Table: "*", Error: err.Error()}}})
		default:
			m.logRuns(ctx, res)
			for _, p := range res.Points {
				m.log.Info("Measured replication latency", "table", p.Table, "records", p.Records,
					"latency_ms", p.LatencyMs, "min_latency_ms", p.MinLatencyMs, "committed_at", p.CommittedAt)
			}
			onResult(res)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// logRuns logs the DuckDB work behind a poll. The snapshot listing runs
// every interval, so it is logged at debug level. Each comparison is logged
// at info, or at warn when it produced no measurement, since its figures are
// what show whether the runs are slow.
func (m *Monitor) logRuns(ctx context.Context, res Result) {
	m.log.Log(ctx, slog.LevelDebug, "Listed table snapshots", "list", res.List)
	for _, c := range res.Compares {
		attrs := []any{"table", c.Table, "snapshot_id", c.SnapshotID, "compare", c.Run}
		if c.Err != nil {
			m.log.Log(ctx, slog.LevelWarn, "Snapshot comparison failed", append(attrs, "error", c.Err)...)
			continue
		}
		m.log.Log(ctx, slog.LevelInfo, "Ran latency query", attrs...)
	}
}
