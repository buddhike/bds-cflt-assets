// Package query runs the operator's own SQL in the DuckDB container.
//
// Each run is a fresh DuckDB CLI session: the script attaches the Tableflow
// Iceberg REST catalog (when Tableflow is enabled), then runs the operator's
// statements and prints their results as JSON. Nothing persists between runs.
package query

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

	"pims/internal/compose"
)

// CatalogName is the DuckDB catalog the Tableflow tables are attached as:
// tableflow."<kafka cluster id>".<table>.
const CatalogName = "tableflow"

const (
	// MaxRows is how many rows of a result are returned. The rest are
	// counted, not sent, so a large table cannot freeze the UI.
	MaxRows = 1000
	// maxOutput bounds what the DuckDB CLI may print before the run is
	// abandoned, so a runaway query cannot exhaust the app's memory.
	maxOutput = 8 << 20
	// defaultTimeout bounds each run, including the container exec, CLI
	// start, catalog sign-in and attach, and the operator's statements. Reading
	// a Tableflow table is hundreds of small requests, one after another: on a
	// 320 ms link, SELECT * ... LIMIT 10 on a table with 130 manifests and 394
	// data files took 187 s with connection caching and 511 s without. So the
	// limit is minutes, not seconds.
	defaultTimeout = 5 * time.Minute
	// killGrace is how far the container's own deadline trails the host's.
	// The host's deadline fires first, so a timeout is reported as one, and
	// the container's only has to stop what the host could not.
	killGrace = 5 * time.Second
	// memoryLimit keeps a heavy query from starving MySQL and Connect, which
	// share the Docker VM. DuckDB spills to spillDir beyond it, or fails with
	// an out of memory error.
	memoryLimit = "2GB"
	// spillDir is writable by the container's user, unlike DuckDB's default of
	// .tmp in the working directory, so spilling to disk works.
	spillDir = "/tmp/pims-duckdb-spill"
)

// readyMarker is printed once the session is set up and before the
// operator's statements run, so a failure of the setup is not mistaken for
// one of the query.
const readyMarker = "pims-session-ready"

var (
	// ErrEmpty is returned for a query with no text.
	ErrEmpty = errors.New("enter a SQL query to run")
	// ErrDotCommand is returned for a line the DuckDB CLI would take as one of
	// its own commands. They can run programs and change the output format
	// PIMS reads, so only SQL is accepted.
	ErrDotCommand = errors.New("DuckDB shell commands such as .mode are not supported; write SQL only")
	// ErrTooLarge is returned when a query prints more than the limit.
	ErrTooLarge = fmt.Errorf("the result is larger than %d MiB; select fewer columns or add a LIMIT", maxOutput>>20)
)

// nonFinite marks a NaN or infinity that DuckDB's JSON mode printed as a bare
// token, which is not valid JSON. quoteNonFinite wraps each in a string that
// starts with this noncharacter, and cellText takes it off again.
const nonFinite = "\ufdd0"

var (
	namespacePattern = regexp.MustCompile(`^lkc-[a-z0-9]+$`)
	// dotLine matches a line that is, or could be, a CLI command. The pinned
	// DuckDB only takes a dot in the first column, but refusing leading
	// whitespace too costs nothing and stays safe if the shell changes.
	dotLine = regexp.MustCompile(`(?m)^[ \t\f\v]*\.`)
	// markedNonFinite finds the strings quoteNonFinite made, inside nested values.
	markedNonFinite = regexp.MustCompile(`"` + nonFinite + `(NaN|-?Infinity)"`)
)

// Catalog is the Tableflow Iceberg REST catalog connection.
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

// String redacts the secret so the catalog can never leak through %v.
func (c Catalog) String() string {
	return fmt.Sprintf("Catalog{Endpoint:%s Namespace:%s APIKey:%s APISecret:[REDACTED]}", c.Endpoint, c.Namespace, c.APIKey)
}

// GoString redacts the secret for %#v.
func (c Catalog) GoString() string { return c.String() }

// MarshalJSON redacts the secret.
func (c Catalog) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Endpoint  string `json:"endpoint"`
		Namespace string `json:"namespace"`
		APIKey    string `json:"apiKey"`
		APISecret string `json:"apiSecret"`
	}{c.Endpoint, c.Namespace, c.APIKey, "[REDACTED]"})
}

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

// Result is the last result set of a query.
type Result struct {
	// Columns are the names of the columns of the last statement that
	// returned rows. Empty when the query returned none.
	Columns []string `json:"columns"`
	// Rows holds at most MaxRows rows. Every cell is the value as DuckDB
	// prints it, or null for NULL: numbers and nested values keep their exact
	// text, so a BIGINT is not rounded by JavaScript.
	Rows [][]*string `json:"rows"`
	// TotalRows is how many rows the statement returned, which exceeds
	// len(Rows) when the result was cut at MaxRows.
	TotalRows int `json:"totalRows"`
	// ElapsedMs is how long the whole run took, session setup included.
	ElapsedMs int64 `json:"elapsedMs"`
}

// Execer runs a program in the DuckDB container with stdin attached.
// duckdbagent.Client satisfies it.
type Execer interface {
	ExecLimit(ctx context.Context, service string, stdin io.Reader, redact []string, maxStdout int, cmd ...string) ([]byte, error)
}

// Runner runs queries with the DuckDB CLI in its container.
type Runner struct {
	exec    Execer
	service string
	catalog *Catalog
	timeout time.Duration
}

// New returns a Runner. catalog may be nil, for example before Tableflow is
// enabled, in which case queries run in plain DuckDB.
func New(exec Execer, service string, catalog *Catalog) (*Runner, error) {
	if catalog != nil {
		if err := catalog.Validate(); err != nil {
			return nil, err
		}
	}
	return &Runner{exec: exec, service: service, catalog: catalog, timeout: defaultTimeout}, nil
}

// duckdbCommand returns the command that runs the DuckDB CLI in the
// container, bounded there in time and in output.
//
// The host cannot bound it: stopping the docker client does not stop the
// process docker exec started in the container, so an abandoned query would
// keep running and growing there. Two things end it from inside instead:
//
//   - timeout kills it once limit has passed. KILL rather than TERM, so
//     DuckDB cannot finish a request first.
//   - head ends the pipe after maxOutput+1 bytes, so a query printing more
//     than the host accepts dies of SIGPIPE at its next write rather than
//     carrying on after the host has given up on it.
//
// pipefail keeps DuckDB's own failure from being hidden by head's success.
// -bail stops at the first failing statement. This relies on bash and GNU
// coreutils' timeout, which the Debian-based pims-duckdb image has.
func duckdbCommand(limit time.Duration) []string {
	// Whole seconds, rounded up, and at least one: timeout treats 0 as no limit.
	secs := max((limit+time.Second-1)/time.Second, 1)
	return []string{"bash", "-c", fmt.Sprintf("set -o pipefail; timeout --signal=KILL %ds duckdb -batch -bail | head -c %d", secs, maxOutput+1)}
}

// Run executes sql, which may hold several statements, and returns the
// result of the last one that returned rows. It stops at the first failing
// statement.
func (r *Runner) Run(ctx context.Context, sql string) (Result, error) {
	if strings.TrimSpace(sql) == "" {
		return Result{}, ErrEmpty
	}
	if dotLine.MatchString(sql) {
		return Result{}, ErrDotCommand
	}

	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	start := time.Now()
	out, err := r.exec.ExecLimit(ctx, r.service, strings.NewReader(r.script(sql)), r.secrets(), maxOutput, duckdbCommand(r.timeout+killGrace)...)
	elapsed := time.Since(start)

	if errors.Is(err, compose.ErrOutputTooLarge) {
		return Result{}, ErrTooLarge
	}
	// A run that finished is reported as one even if the deadline passed just
	// after; only a failed run is blamed on the deadline.
	if err != nil && ctx.Err() != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return Result{}, timeoutError{limit: r.timeout, tableflow: r.catalog != nil}
		}
		return Result{}, ctx.Err()
	}
	body, ready := bytes.CutPrefix(out, []byte(readyMarker+"\n"))
	if err != nil {
		return Result{}, r.failure(err, ready)
	}
	if !ready {
		return Result{}, fmt.Errorf("unexpected DuckDB output: %.200s", out)
	}
	res, err := parseResult(body)
	if err != nil {
		return Result{}, fmt.Errorf("parse DuckDB output: %w", err)
	}
	res.ElapsedMs = elapsed.Milliseconds()
	return res, nil
}

// timeoutError is a run that did not finish in time.
type timeoutError struct {
	limit     time.Duration
	tableflow bool
}

func (e timeoutError) Error() string {
	msg := fmt.Sprintf("the query did not finish within %s", e.limit)
	if e.tableflow {
		// Measured on a 320 ms link: SELECT * ... LIMIT 10 on a table with 124
		// manifests and 391 data files made over 500 requests.
		msg += ". Reading a Tableflow table takes hundreds of small requests, so a slow network to AWS can make even a small query take minutes"
	}
	return msg
}

// Is lets errors.Is(err, context.DeadlineExceeded) recognise a timeout.
func (e timeoutError) Is(target error) bool { return target == context.DeadlineExceeded }

// failure turns a failed run into the message the operator should see:
// DuckDB's own, without the docker exec wrapping.
func (r *Runner) failure(err error, sessionReady bool) error {
	msg := err.Error()
	var ee *compose.ExecError
	if errors.As(err, &ee) && ee.Stderr != "" {
		msg = ee.Stderr
	}
	if sessionReady {
		return errors.New(msg)
	}
	// The setup failed, or docker could not run it at all, for example because
	// the container is not up. DuckDB's own text names the catalog when that
	// is the problem, so no cause is guessed here.
	return fmt.Errorf("could not set up the DuckDB session: %s", msg)
}

// secrets are scrubbed from DuckDB's stderr, which echoes failing statements.
// A statement echoes them as sqlString wrote them, so those forms are listed
// too.
func (r *Runner) secrets() []string {
	if r.catalog == nil {
		return nil
	}
	var out []string
	for _, s := range []string{r.catalog.APISecret, r.catalog.APIKey} {
		out = append(out, s)
		if q := quoteContent(s); q != s {
			out = append(out, q)
		}
	}
	return out
}

// script is what the DuckDB CLI reads on stdin: the session setup in trash
// mode, so only the operator's results reach stdout, then the statements.
func (r *Runner) script(sql string) string {
	var b strings.Builder
	b.WriteString(".mode trash\n")
	b.WriteString("SET TimeZone = 'UTC';\n")
	fmt.Fprintf(&b, "SET memory_limit = %s;\nSET temp_directory = %s;\n", sqlString(memoryLimit), sqlString(spillDir))
	if c := r.catalog; c != nil {
		ep := sqlString(c.Endpoint)
		// Reading a table is hundreds of small requests, one after another. DuckDB
		// opens a new connection for each by default, which over a link with a
		// high round trip time costs three round trips apiece. Reusing
		// connections more than doubled how many requests a scan made per
		// second on a 320 ms link.
		b.WriteString("LOAD httpfs;\nLOAD iceberg;\nSET httpfs_connection_caching = true;\n")
		fmt.Fprintf(&b, "CREATE SECRET %s (TYPE ICEBERG, CLIENT_ID %s, CLIENT_SECRET %s, ENDPOINT %s, OAUTH2_SCOPE 'catalog');\n",
			CatalogName, sqlString(c.APIKey), sqlString(c.APISecret), ep)
		fmt.Fprintf(&b, "ATTACH 'warehouse' AS %s (TYPE iceberg, SECRET %s, ENDPOINT %s);\n", CatalogName, CatalogName, ep)
	}
	b.WriteString(".mode json\n")
	b.WriteString(".print " + readyMarker + "\n")
	b.WriteString(sql)
	// A final newline lets the CLI see the end of a last line that is a comment.
	b.WriteString("\n")
	return b.String()
}

// sqlString quotes s as a SQL string literal.
func sqlString(s string) string { return "'" + quoteContent(s) + "'" }

// quoteContent escapes s for use inside a SQL string literal.
func quoteContent(s string) string { return strings.ReplaceAll(s, "'", "''") }

// parseResult decodes the concatenated JSON arrays the CLI prints, one per
// statement that returned rows, and keeps the last. Cells are decoded
// as raw JSON so column order, duplicate column names and exact numbers
// survive, none of which a map or float64 would keep.
func parseResult(out []byte) (Result, error) {
	dec := json.NewDecoder(bytes.NewReader(quoteNonFinite(out)))
	res := Result{Columns: []string{}, Rows: [][]*string{}}
	// More is false only at the end of the input, so an array that stops
	// short is an error rather than a clean end.
	for dec.More() {
		next, err := decodeArray(dec)
		if err != nil {
			return Result{}, err
		}
		res = next
	}
	return res, nil
}

// decodeArray reads one JSON array of row objects.
func decodeArray(dec *json.Decoder) (Result, error) {
	if err := expectDelim(dec, '['); err != nil {
		return Result{}, err
	}
	res := Result{Columns: []string{}, Rows: [][]*string{}}
	for dec.More() {
		if err := expectDelim(dec, '{'); err != nil {
			return Result{}, err
		}
		var cols []string
		var row []*string
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return Result{}, err
			}
			name, ok := key.(string)
			if !ok {
				return Result{}, fmt.Errorf("unexpected token %v in a row", key)
			}
			var raw json.RawMessage
			if err := dec.Decode(&raw); err != nil {
				return Result{}, err
			}
			cell, err := cellText(raw)
			if err != nil {
				return Result{}, err
			}
			cols = append(cols, name)
			row = append(row, cell)
		}
		if err := expectDelim(dec, '}'); err != nil {
			return Result{}, err
		}
		if res.TotalRows == 0 {
			res.Columns = cols
		}
		res.TotalRows++
		if len(res.Rows) < MaxRows {
			res.Rows = append(res.Rows, row)
		}
	}
	return res, expectDelim(dec, ']')
}

func expectDelim(dec *json.Decoder, want json.Delim) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if d, ok := tok.(json.Delim); !ok || d != want {
		return fmt.Errorf("expected %q, found %v", want, tok)
	}
	return nil
}

// quoteNonFinite turns the bare NaN, Infinity and -Infinity DuckDB prints for
// floats into strings the JSON decoder accepts, marked so cellText can show
// them as DuckDB printed them. It leaves string contents alone.
func quoteNonFinite(b []byte) []byte {
	var out []byte
	last := 0 // b[last:] has not been copied to out yet
	inString := false
	for i := 0; i < len(b); i++ {
		c := b[i]
		if inString {
			switch c {
			case '\\':
				i++ // the escaped byte cannot end the string
			case '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			continue
		}
		for _, tok := range []string{"NaN", "-Infinity", "Infinity"} {
			if c == tok[0] && bytes.HasPrefix(b[i:], []byte(tok)) {
				out = append(out, b[last:i]...)
				out = append(out, '"')
				out = append(out, nonFinite...)
				out = append(out, tok...)
				out = append(out, '"')
				i += len(tok) - 1
				last = i + 1
				break
			}
		}
	}
	if out == nil {
		return b
	}
	return append(out, b[last:]...)
}

// cellText renders one JSON value as the text to display, or nil for NULL.
func cellText(raw json.RawMessage) (*string, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		s = strings.TrimPrefix(s, nonFinite)
		return &s, nil
	}
	// Numbers, booleans and nested values keep DuckDB's own text.
	s := markedNonFinite.ReplaceAllString(string(raw), "$1")
	return &s, nil
}
