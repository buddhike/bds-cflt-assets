// Package latency measures end-to-end replication latency from MySQL to
// the Tableflow Iceberg tables.
//
// For each table, DuckDB reads the newest record that has reached Iceberg
// (its MySQL updated_at) and the commit time of the latest Iceberg
// snapshot, which is when the S3 data file holding that record was
// published. The difference is the end-to-end latency: MySQL write,
// Debezium capture, Kafka, Tableflow materialisation and S3 commit.
package latency

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"regexp"
	"strings"
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

// Point is one latency observation for a table.
type Point struct {
	Table string `json:"table"`
	// RecordAt is the updated_at of the newest record visible in Iceberg.
	RecordAt time.Time `json:"recordAt"`
	// CommittedAt is the commit time of the latest Iceberg snapshot.
	CommittedAt time.Time `json:"committedAt"`
	// LatencyMs is CommittedAt - RecordAt in milliseconds.
	LatencyMs int64 `json:"latencyMs"`
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
}

// Execer runs a program in the DuckDB container with stdin attached.
// compose.Project.Exec satisfies it.
type Execer interface {
	Exec(ctx context.Context, service string, stdin io.Reader, redact []string, cmd ...string) ([]byte, error)
}

// DuckDB queries Tableflow tables with the DuckDB CLI in its container.
type DuckDB struct {
	exec    Execer
	service string
	catalog Catalog
	tables  []string
	now     func() time.Time
}

// NewDuckDB returns a querier for tables in the given catalog.
func NewDuckDB(exec Execer, service string, catalog Catalog, tables []string) (*DuckDB, error) {
	if err := catalog.Validate(); err != nil {
		return nil, err
	}
	for _, t := range tables {
		if !tablePattern.MatchString(t) {
			return nil, fmt.Errorf("invalid table name %q", t)
		}
	}
	return &DuckDB{exec: exec, service: service, catalog: catalog, tables: tables, now: time.Now}, nil
}

// Script returns the DuckDB CLI script for one poll. Setup statements run
// in trash mode so only the per-table JSON results reach stdout.
func (d *DuckDB) Script() string {
	ep := sqlString(d.catalog.Endpoint)
	var b strings.Builder
	b.WriteString(".mode trash\n")
	b.WriteString("SET TimeZone = 'UTC';\n")
	b.WriteString("LOAD httpfs;\nLOAD iceberg;\n")
	fmt.Fprintf(&b, "CREATE SECRET tableflow (TYPE ICEBERG, CLIENT_ID %s, CLIENT_SECRET %s, ENDPOINT %s, OAUTH2_SCOPE 'catalog');\n",
		sqlString(d.catalog.APIKey), sqlString(d.catalog.APISecret), ep)
	fmt.Fprintf(&b, "ATTACH 'warehouse' AS tableflow (TYPE iceberg, SECRET tableflow, ENDPOINT %s);\n", ep)
	b.WriteString(".mode json\n")
	for _, t := range d.tables {
		ref := fmt.Sprintf(`tableflow."%s"."%s"`, d.catalog.Namespace, t)
		// The commit that published the newest record is the earliest
		// snapshot committed at or after it was written. Using that rather
		// than the latest snapshot keeps later compaction or maintenance
		// commits, and commits landing between the two reads, from
		// inflating the latency. Casting to TIMESTAMPTZ accepts timestamp,
		// timestamptz or ISO strings, whichever Tableflow produced.
		fmt.Fprintf(&b, "WITH r AS (SELECT max(updated_at)::TIMESTAMPTZ AS rec FROM %s) "+
			"SELECT '%s' AS table_name, epoch_ms(r.rec) AS record_ms, "+
			"(SELECT epoch_ms(min(timestamp_ms)::TIMESTAMPTZ) FROM iceberg_snapshots(%s) WHERE timestamp_ms::TIMESTAMPTZ >= r.rec) AS committed_ms "+
			"FROM r;\n",
			ref, t, ref)
	}
	return b.String()
}

type row struct {
	Table       string `json:"table_name"`
	RecordMs    *int64 `json:"record_ms"`
	CommittedMs *int64 `json:"committed_ms"`
}

// Query runs one poll. Tables that fail are reported in Result.Errors
// rather than failing the whole poll, since Tableflow materialises each
// topic independently.
func (d *DuckDB) Query(ctx context.Context) (Result, error) {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()

	redact := []string{d.catalog.APISecret, d.catalog.APIKey}
	out, execErr := d.exec.Exec(ctx, d.service, strings.NewReader(d.Script()), redact, "duckdb", "-batch")
	if ctx.Err() != nil {
		return Result{}, fmt.Errorf("duckdb query: %w", ctx.Err())
	}
	observed := d.now().UTC()
	rows, err := parseRows(out)
	if err != nil {
		return Result{}, fmt.Errorf("parse duckdb output: %w", err)
	}
	if len(rows) == 0 && execErr != nil {
		// Nothing succeeded: most likely the catalog could not be reached.
		return Result{}, fmt.Errorf("duckdb query: %w", execErr)
	}

	var res Result
	seen := map[string]bool{}
	for _, r := range rows {
		seen[r.Table] = true
		if r.RecordMs == nil {
			res.Errors = append(res.Errors, TableError{Table: r.Table, Error: "table has no data yet"})
			continue
		}
		if r.CommittedMs == nil {
			res.Errors = append(res.Errors, TableError{Table: r.Table, Error: "newest record is not in a committed snapshot yet"})
			continue
		}
		rec, com := time.UnixMilli(*r.RecordMs).UTC(), time.UnixMilli(*r.CommittedMs).UTC()
		res.Points = append(res.Points, Point{Table: r.Table, RecordAt: rec, CommittedAt: com, LatencyMs: com.Sub(rec).Milliseconds(), ObservedAt: observed})
	}
	detail := "not queryable yet"
	if execErr != nil {
		detail = execErr.Error()
	}
	for _, t := range d.tables {
		if !seen[t] {
			res.Errors = append(res.Errors, TableError{Table: t, Error: detail})
		}
	}
	return res, nil
}

// parseRows decodes the concatenated JSON arrays the CLI prints, one per
// successful SELECT.
func parseRows(out []byte) ([]row, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	var rows []row
	for {
		var batch []row
		err := dec.Decode(&batch)
		if errors.Is(err, io.EOF) {
			return rows, nil
		}
		if err != nil {
			return nil, err
		}
		rows = append(rows, batch...)
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
	// last remembers the newest record reported per table, so each record
	// that reaches Iceberg is reported once.
	last map[string]time.Time
}

// DefaultInterval is used when NewMonitor is given a non-positive interval.
const DefaultInterval = 30 * time.Second

// NewMonitor returns a Monitor that polls every interval.
func NewMonitor(q Querier, interval time.Duration, log *slog.Logger) *Monitor {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Monitor{q: q, interval: interval, log: log.With("component", "latency"), last: map[string]time.Time{}}
}

// Run polls until ctx is cancelled, calling onResult with the points for
// new Iceberg commits and the tables that could not be read.
func (m *Monitor) Run(ctx context.Context, onResult func(Result)) {
	t := time.NewTicker(m.interval)
	defer t.Stop()
	for {
		res, err := m.q.Query(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			m.log.Warn("Latency query failed", "error", err)
			onResult(Result{Errors: []TableError{{Table: "*", Error: err.Error()}}})
		default:
			onResult(m.fresh(res))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// fresh keeps only points whose newest record has moved on since the last
// report, so a snapshot that brought no new data is never plotted.
func (m *Monitor) fresh(res Result) Result {
	out := Result{Errors: res.Errors}
	for _, p := range res.Points {
		if prev, ok := m.last[p.Table]; ok && !p.RecordAt.After(prev) {
			continue
		}
		m.last[p.Table] = p.RecordAt
		out.Points = append(out.Points, p)
		m.log.Info("Measured replication latency", "table", p.Table, "latency_ms", p.LatencyMs, "committed_at", p.CommittedAt)
	}
	return out
}
