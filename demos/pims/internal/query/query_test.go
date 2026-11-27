package query

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"pims/internal/compose"
)

var testCatalog = Catalog{
	Endpoint:  "https://tableflow.us-east-1.aws.confluent.cloud/iceberg/catalog/organizations/org-1/environments/env-1",
	APIKey:    "TFKEY",
	APISecret: "tf'secret",
	Namespace: "lkc-abc123",
}

// fakeExec stands in for the DuckDB container and records what it was given.
type fakeExec struct {
	out    string
	err    error
	stdin  string
	redact []string
	max    int
	cmd    []string
	// block makes the run wait until its context ends, like a slow query.
	block bool
	// before runs just before the run returns.
	before func()
}

func (f *fakeExec) ExecLimit(ctx context.Context, _ string, stdin io.Reader, redact []string, max int, cmd ...string) ([]byte, error) {
	b, _ := io.ReadAll(stdin)
	f.stdin, f.redact, f.max, f.cmd = string(b), redact, max, cmd
	if f.block {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.before != nil {
		f.before()
	}
	return []byte(f.out), f.err
}

func ready(rest string) string { return readyMarker + "\n" + rest }

func newRunner(t *testing.T, f *fakeExec, c *Catalog) *Runner {
	t.Helper()
	r, err := New(f, "duckdb", c)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRunRejectsEmptyQueryAndShellCommands(t *testing.T) {
	f := &fakeExec{}
	r := newRunner(t, f, nil)
	for _, sql := range []string{"", "  \n\t"} {
		if _, err := r.Run(context.Background(), sql); !errors.Is(err, ErrEmpty) {
			t.Errorf("Run(%q) = %v, want ErrEmpty", sql, err)
		}
	}
	// A command can follow SQL on a later line. Leading whitespace is refused
	// too, which is stricter than the pinned DuckDB needs.
	for _, sql := range []string{".shell id", "  \t.mode csv", "SELECT 1;\n.read /etc/passwd\nSELECT 2;", ".system id"} {
		if _, err := r.Run(context.Background(), sql); !errors.Is(err, ErrDotCommand) {
			t.Errorf("Run(%q) = %v, want ErrDotCommand", sql, err)
		}
	}
	if f.stdin != "" {
		t.Fatalf("a rejected query reached DuckDB: %q", f.stdin)
	}
	// A dot inside a line is just SQL.
	f.out = ready("[]\n")
	for _, sql := range []string{"SELECT 1.5", "SELECT t.a FROM t", "SELECT 1 -- .mode csv"} {
		if _, err := r.Run(context.Background(), sql); err != nil {
			t.Errorf("Run(%q) = %v", sql, err)
		}
	}
}

func TestScriptSetsUpTheSessionBeforeTheQuery(t *testing.T) {
	f := &fakeExec{out: ready("[]\n")}
	r := newRunner(t, f, &testCatalog)
	if _, err := r.Run(context.Background(), "SELECT 1 -- done"); err != nil {
		t.Fatal(err)
	}
	s := f.stdin
	// Setup runs in trash mode so none of its output is read as a result.
	order := []string{".mode trash", "SET memory_limit = '2GB';", "SET temp_directory = '/tmp/pims-duckdb-spill';", "LOAD iceberg;", "SET httpfs_connection_caching = true;", "CREATE SECRET tableflow", "ATTACH 'warehouse' AS tableflow", ".mode json", ".print " + readyMarker, "SELECT 1 -- done"}
	last := -1
	for _, want := range order {
		i := strings.Index(s, want)
		if i <= last {
			t.Fatalf("%q is missing or out of order in script:\n%s", want, s)
		}
		last = i
	}
	// The secret is quoted as a literal, so a quote in it cannot end the string.
	if !strings.Contains(s, "CLIENT_SECRET 'tf''secret'") {
		t.Errorf("secret not quoted:\n%s", s)
	}
	// The last line may be a comment, which a statement must not be glued to.
	if !strings.HasSuffix(s, "SELECT 1 -- done\n") {
		t.Errorf("script must end with the query and a newline:\n%q", s)
	}
}

func TestScriptWithoutCatalogDoesNotTouchIceberg(t *testing.T) {
	f := &fakeExec{out: ready("[]\n")}
	if _, err := newRunner(t, f, nil).Run(context.Background(), "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	for _, banned := range []string{"iceberg", "SECRET", "ATTACH"} {
		if strings.Contains(strings.ToUpper(f.stdin), strings.ToUpper(banned)) {
			t.Errorf("plain DuckDB script mentions %s:\n%s", banned, f.stdin)
		}
	}
	if len(f.redact) != 0 {
		t.Errorf("nothing to redact without a catalog, got %v", f.redact)
	}
}

func TestRunBoundsTheRunInTheContainerAndTheOutput(t *testing.T) {
	f := &fakeExec{out: ready("[]\n")}
	r := newRunner(t, f, &testCatalog)
	if _, err := r.Run(context.Background(), "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	// The container ends DuckDB at the time limit, and ends it when more than
	// maxOutput+1 bytes are printed, and DuckDB's failure is not hidden by
	// head's success.
	want := []string{"bash", "-c", fmt.Sprintf("set -o pipefail; timeout --signal=KILL 305s duckdb -batch -bail | head -c %d", maxOutput+1)}
	if !slices.Equal(f.cmd, want) {
		t.Errorf("command = %v, want %v", f.cmd, want)
	}
	if f.max != maxOutput {
		t.Errorf("output limit = %d, want %d", f.max, maxOutput)
	}
	if !slices.Contains(f.redact, testCatalog.APISecret) || !slices.Contains(f.redact, testCatalog.APIKey) {
		t.Errorf("redact = %v, want the catalog's secret and key", f.redact)
	}
}

// DuckDB echoes a failing statement, so a secret with a quote in it appears
// there as it was escaped into the script.
func TestRunScrubsTheSecretAsTheScriptSpellsIt(t *testing.T) {
	f := &fakeExec{out: ready("[]\n")}
	if _, err := newRunner(t, f, &testCatalog).Run(context.Background(), "SELECT 1"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"tf'secret", "tf''secret", "TFKEY"} {
		if !slices.Contains(f.redact, want) {
			t.Errorf("redact = %v, missing %q", f.redact, want)
		}
	}
}

func TestDuckDBCommandRoundsTheLimitUp(t *testing.T) {
	for limit, want := range map[time.Duration]string{
		time.Millisecond: "1s", time.Second: "1s", 1500 * time.Millisecond: "2s", 305 * time.Second: "305s",
	} {
		if got := duckdbCommand(limit)[2]; !strings.Contains(got, " timeout --signal=KILL "+want+" duckdb ") {
			t.Errorf("duckdbCommand(%v) = %q, want a limit of %s", limit, got, want)
		}
	}
}

func TestParseResultKeepsColumnOrderDuplicatesAndExactNumbers(t *testing.T) {
	out := `[{"z":1,"a":"x","a":null,"big":9007199254740993,"ok":true,"l":[1,2],"s":{"k":"v"},"esc":"q\"\n"}]`
	res, err := parseResult([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"z", "a", "a", "big", "ok", "l", "s", "esc"}; !slices.Equal(res.Columns, want) {
		t.Errorf("columns = %v, want %v", res.Columns, want)
	}
	str := func(s string) *string { return &s }
	want := []*string{str("1"), str("x"), nil, str("9007199254740993"), str("true"), str("[1,2]"), str(`{"k":"v"}`), str("q\"\n")}
	if len(res.Rows) != 1 || len(res.Rows[0]) != len(want) {
		t.Fatalf("rows = %v", res.Rows)
	}
	for i, w := range want {
		got := res.Rows[0][i]
		if (got == nil) != (w == nil) || (got != nil && *got != *w) {
			t.Errorf("cell %d = %v, want %v", i, deref(got), deref(w))
		}
	}
}

func deref(s *string) string {
	if s == nil {
		return "<NULL>"
	}
	return *s
}

func TestParseResultKeepsTheLastSetAndCapsRows(t *testing.T) {
	var rows []string
	for i := range MaxRows + 250 {
		rows = append(rows, fmt.Sprintf(`{"n":%d}`, i))
	}
	out := `[{"first":1}]` + "\n" + "[" + strings.Join(rows, ",\n") + "]\n"
	res, err := parseResult([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(res.Columns, []string{"n"}) {
		t.Errorf("columns = %v, want those of the last set", res.Columns)
	}
	if len(res.Rows) != MaxRows || res.TotalRows != MaxRows+250 {
		t.Errorf("rows = %d, total = %d, want %d and %d", len(res.Rows), res.TotalRows, MaxRows, MaxRows+250)
	}
	if got := deref(res.Rows[MaxRows-1][0]); got != fmt.Sprint(MaxRows-1) {
		t.Errorf("last kept row = %s", got)
	}
}

// DuckDB prints NaN and the infinities as bare tokens, which are not JSON.
func TestParseResultShowsNonFiniteFloatsAsDuckDBPrintsThem(t *testing.T) {
	out := `[{"n":NaN,"i":Infinity,"ni":-Infinity,"l":[1.5,NaN,-Infinity],"s":{"a":Infinity},"str":"NaN","q":"say \"Infinity\" NaN","ok":1}]`
	res, err := parseResult([]byte(out))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"NaN", "Infinity", "-Infinity", "[1.5,NaN,-Infinity]", `{"a":Infinity}`, "NaN", `say "Infinity" NaN`, "1"}
	var got []string
	for _, c := range res.Rows[0] {
		got = append(got, deref(c))
	}
	if !slices.Equal(got, want) {
		t.Errorf("cells = %q, want %q", got, want)
	}
	if !slices.Equal(res.Columns, []string{"n", "i", "ni", "l", "s", "str", "q", "ok"}) {
		t.Errorf("columns = %v", res.Columns)
	}
}

func TestQuoteNonFiniteLeavesOtherOutputAlone(t *testing.T) {
	for _, out := range []string{`[{"a":1}]`, `[{"a":"NaN \" Infinity"}]`, `[{"a":"\\"}]`, ""} {
		if got := string(quoteNonFinite([]byte(out))); got != out {
			t.Errorf("quoteNonFinite(%q) = %q, want it unchanged", out, got)
		}
	}
}

func TestParseResultOfNothingIsEmptyNotNull(t *testing.T) {
	// The UI iterates these, so they must encode as [] and never null.
	for _, out := range []string{"", "[]\n"} {
		res, err := parseResult([]byte(out))
		if err != nil {
			t.Fatalf("parseResult(%q) = %v", out, err)
		}
		b, _ := json.Marshal(res)
		if !strings.Contains(string(b), `"columns":[]`) || !strings.Contains(string(b), `"rows":[]`) {
			t.Errorf("parseResult(%q) encodes as %s", out, b)
		}
	}
}

func TestParseResultRejectsTruncatedOutput(t *testing.T) {
	for _, out := range []string{`[{"a":1},`, `[{"a":1}`, `[{"a":`, `[{"a":1}]` + "\n" + `[{"b":`, `oops`} {
		if _, err := parseResult([]byte(out)); err == nil {
			t.Errorf("parseResult(%q) accepted broken output", out)
		}
	}
}

func TestRunReturnsTheResultAndTime(t *testing.T) {
	f := &fakeExec{out: ready(`[{"a":1,"b":"x"}]` + "\n")}
	res, err := newRunner(t, f, nil).Run(context.Background(), "SELECT 1 AS a, 'x' AS b")
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalRows != 1 || !slices.Equal(res.Columns, []string{"a", "b"}) {
		t.Errorf("result = %+v", res)
	}
}

func TestRunShowsDuckDBsMessageForAFailedQuery(t *testing.T) {
	f := &fakeExec{
		out: ready(`[{"a":1}]` + "\n"),
		err: &compose.ExecError{Service: "duckdb", Err: errors.New("exit status 1"), Stderr: `Parser Error: syntax error at or near "SELEC"`},
	}
	_, err := newRunner(t, f, &testCatalog).Run(context.Background(), "SELEC 1")
	if err == nil || err.Error() != `Parser Error: syntax error at or near "SELEC"` {
		t.Fatalf("Run() = %v, want DuckDB's message alone", err)
	}
}

func TestRunBlamesTheSessionWhenSetupFails(t *testing.T) {
	setup := &compose.ExecError{Service: "duckdb", Err: errors.New("exit status 1"), Stderr: "IO Error: could not reach the catalog"}

	// The cause is not guessed: a container that is down fails the same way.
	for name, c := range map[string]*Catalog{"with catalog": &testCatalog, "without catalog": nil} {
		_, err := newRunner(t, &fakeExec{err: setup}, c).Run(context.Background(), "SELECT 1")
		if err == nil || err.Error() != "could not set up the DuckDB session: IO Error: could not reach the catalog" {
			t.Errorf("%s: Run() = %v", name, err)
		}
	}
	var err error
	// Not an ExecError, for example the docker CLI missing: its own text.
	_, err = newRunner(t, &fakeExec{err: errors.New("exec: docker: not found")}, nil).Run(context.Background(), "SELECT 1")
	if err == nil || !strings.Contains(err.Error(), "docker: not found") {
		t.Errorf("Run() = %v", err)
	}
}

func TestRunRejectsOutputThatIsNotTheSessions(t *testing.T) {
	_, err := newRunner(t, &fakeExec{out: `[{"a":1}]`}, nil).Run(context.Background(), "SELECT 1")
	if err == nil || !strings.Contains(err.Error(), "unexpected DuckDB output") {
		t.Fatalf("Run() = %v", err)
	}
}

func TestRunReportsOversizedOutput(t *testing.T) {
	f := &fakeExec{err: fmt.Errorf("exec in duckdb: %w", compose.ErrOutputTooLarge)}
	_, err := newRunner(t, f, nil).Run(context.Background(), "SELECT * FROM range(100000000)")
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Run() = %v, want ErrTooLarge", err)
	}
}

func TestRunTimesOut(t *testing.T) {
	r := newRunner(t, &fakeExec{block: true}, nil)
	r.timeout = 20 * time.Millisecond
	_, err := r.Run(context.Background(), "SELECT * FROM slow")
	if err == nil || err.Error() != "the query did not finish within 20ms" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() = %v, want a plain timeout that is a DeadlineExceeded", err)
	}
	// With the catalog attached the likely cause is the network, so say so.
	r, err2 := New(&fakeExec{block: true}, "duckdb", &testCatalog)
	if err2 != nil {
		t.Fatal(err2)
	}
	r.timeout = 20 * time.Millisecond
	_, err = r.Run(context.Background(), "SELECT * FROM slow")
	if err == nil || !strings.Contains(err.Error(), "slow network") || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() with a catalog = %v, want the network hint", err)
	}
}

// A run that finished is not discarded because the deadline, or a shutdown,
// came just after it.
func TestRunKeepsAResultThatArrivedAsTheContextEnded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	f := &fakeExec{out: ready(`[{"a":1}]` + "\n"), before: cancel}
	res, err := newRunner(t, f, nil).Run(ctx, "SELECT 1")
	if err != nil || res.TotalRows != 1 {
		t.Fatalf("Run() = %+v, %v, want the finished result", res, err)
	}
}

func TestRunStopsWhenTheAppShutsDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newRunner(t, &fakeExec{block: true}, nil).Run(ctx, "SELECT 1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() = %v, want context.Canceled", err)
	}
}

func TestCatalogValidate(t *testing.T) {
	if err := testCatalog.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Catalog){
		"http endpoint":  func(c *Catalog) { c.Endpoint = "http://tableflow.example" },
		"no host":        func(c *Catalog) { c.Endpoint = "https://" },
		"bad namespace":  func(c *Catalog) { c.Namespace = `lkc-1"; DROP` },
		"missing key":    func(c *Catalog) { c.APIKey = "" },
		"missing secret": func(c *Catalog) { c.APISecret = "" },
	} {
		c := testCatalog
		mutate(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s: Validate() accepted %+v", name, c)
		}
		if _, err := New(&fakeExec{}, "duckdb", &c); err == nil {
			t.Errorf("%s: New() accepted an invalid catalog", name)
		}
	}
}

func TestCatalogNeverPrintsItsSecret(t *testing.T) {
	b, err := json.Marshal(testCatalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{
		fmt.Sprint(testCatalog), fmt.Sprintf("%+v", testCatalog), fmt.Sprintf("%#v", testCatalog), string(b), testCatalog.LogValue().String(),
	} {
		if strings.Contains(s, testCatalog.APISecret) {
			t.Errorf("secret leaked: %s", s)
		}
	}
}

// dockerRun runs the DuckDB image directly, as the agent would in the task,
// so the tests need no running stack.
type dockerRun struct{}

func (dockerRun) ExecLimit(ctx context.Context, _ string, stdin io.Reader, redact []string, maxStdout int, cmd ...string) ([]byte, error) {
	c := exec.CommandContext(ctx, "docker", append([]string{"run", "--rm", "-i", "pims-duckdb"}, cmd...)...)
	var stdout, stderr bytes.Buffer
	c.Stdin, c.Stdout, c.Stderr = stdin, &stdout, &stderr
	if err := c.Run(); err != nil {
		// As duckdbagent.Client does, so the test sees what the operator would.
		msg := stderr.String()
		for _, r := range redact {
			msg = strings.ReplaceAll(msg, r, "[REDACTED]")
		}
		if maxStdout > 0 && stdout.Len() > maxStdout {
			return nil, compose.ErrOutputTooLarge
		}
		return stdout.Bytes(), &compose.ExecError{Service: "duckdb", Err: err, Stderr: strings.TrimSpace(msg)}
	}
	if maxStdout > 0 && stdout.Len() > maxStdout {
		return nil, compose.ErrOutputTooLarge
	}
	return stdout.Bytes(), nil
}

// TestRunInDuckDB runs queries in the real DuckDB image and checks what the
// operator would see. It needs Docker and the pims-duckdb image built by
// `docker build -f docker/Dockerfile --target duckdb -t pims-duckdb .`.
func TestRunInDuckDB(t *testing.T) {
	if err := exec.Command("docker", "image", "inspect", "pims-duckdb").Run(); err != nil {
		t.Skip("pims-duckdb image not built")
	}
	r, err := New(dockerRun{}, "duckdb", nil)
	if err != nil {
		t.Fatal(err)
	}
	run := func(sql string) (Result, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		return r.Run(ctx, sql)
	}
	cells := func(res Result) [][]string {
		var out [][]string
		for _, row := range res.Rows {
			var cs []string
			for _, c := range row {
				cs = append(cs, deref(c))
			}
			out = append(out, cs)
		}
		return out
	}

	t.Run("types survive exactly", func(t *testing.T) {
		res, err := run(`SELECT 1 AS i, 'x' AS s, NULL AS n, 9007199254740993::BIGINT AS big, 18446744073709551615::UBIGINT AS ubig,
			1.50::DECIMAL(5,2) AS d, DATE '2024-01-02' AS dt, [1,2] AS l, {'k': 'v'} AS st, true AS b`)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"i", "s", "n", "big", "ubig", "d", "dt", "l", "st", "b"}; !slices.Equal(res.Columns, want) {
			t.Errorf("columns = %v", res.Columns)
		}
		want := []string{"1", "x", "<NULL>", "9007199254740993", "18446744073709551615", "1.50", "2024-01-02", "[1,2]", `{"k":"v"}`, "true"}
		if got := cells(res)[0]; !slices.Equal(got, want) {
			t.Errorf("cells = %v, want %v", got, want)
		}
	})
	t.Run("duplicate column names", func(t *testing.T) {
		res, err := run(`SELECT 'x' AS a, 'y' AS a`)
		if err != nil || !slices.Equal(res.Columns, []string{"a", "a"}) || !slices.Equal(cells(res)[0], []string{"x", "y"}) {
			t.Errorf("result = %+v, err = %v", res, err)
		}
	})
	t.Run("last of several statements, one session", func(t *testing.T) {
		res, err := run("CREATE TABLE t (a INT);\nINSERT INTO t VALUES (1), (2);\nSELECT 'ignored' AS first;\nSELECT sum(a) AS total FROM t -- no semicolon")
		if err != nil || !slices.Equal(res.Columns, []string{"total"}) || cells(res)[0][0] != "3" {
			t.Errorf("result = %+v, err = %v", res, err)
		}
	})
	t.Run("no rows", func(t *testing.T) {
		res, err := run("SELECT 1 AS a WHERE false")
		if err != nil || res.TotalRows != 0 || len(res.Rows) != 0 {
			t.Errorf("result = %+v, err = %v", res, err)
		}
	})
	t.Run("statement without a result", func(t *testing.T) {
		res, err := run("CREATE TABLE t (a INT)")
		if err != nil || res.TotalRows != 0 {
			t.Errorf("result = %+v, err = %v", res, err)
		}
	})
	t.Run("rows are capped but counted", func(t *testing.T) {
		res, err := run("SELECT range AS n FROM range(2500)")
		if err != nil || len(res.Rows) != MaxRows || res.TotalRows != 2500 {
			t.Errorf("rows = %d, total = %d, err = %v", len(res.Rows), res.TotalRows, err)
		}
	})
	t.Run("stops at the first error with DuckDB's message", func(t *testing.T) {
		_, err := run("SELECT 1 AS a;\nSELEC oops;\nSELECT 2 AS b;")
		if err == nil || !strings.Contains(err.Error(), "Parser Error") || strings.Contains(err.Error(), "exit status") {
			t.Errorf("Run() = %v, want DuckDB's parser error alone", err)
		}
	})
	t.Run("a runtime error", func(t *testing.T) {
		_, err := run("SELECT * FROM no_such_table")
		if err == nil || !strings.Contains(err.Error(), "no_such_table") {
			t.Errorf("Run() = %v", err)
		}
	})
	t.Run("NaN and infinity", func(t *testing.T) {
		res, err := run(`SELECT 'nan'::DOUBLE AS n, 'inf'::DOUBLE AS i, '-inf'::DOUBLE AS ni, [1.5, 'nan'::DOUBLE] AS l, 'NaN' AS s`)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"NaN", "Infinity", "-Infinity", "[1.5,NaN]", "NaN"}; !slices.Equal(cells(res)[0], want) {
			t.Errorf("cells = %q, want %q", cells(res)[0], want)
		}
	})
	t.Run("output over the limit ends the query inside the container", func(t *testing.T) {
		// A billion rows would take minutes to print. The container's own cap
		// must stop DuckDB, so this returns at once and leaves nothing running.
		start := time.Now()
		_, err := run("SELECT * FROM range(1000000000)")
		if !errors.Is(err, ErrTooLarge) {
			t.Fatalf("Run() = %v, want ErrTooLarge", err)
		}
		if took := time.Since(start); took > 30*time.Second {
			t.Errorf("took %v, want the query stopped long before its timeout", took)
		}
	})
	t.Run("spills past the memory limit instead of failing", func(t *testing.T) {
		res, err := run("SET memory_limit = '200MB'; SELECT count(*) AS c FROM (SELECT i FROM range(60000000) t(i) ORDER BY i DESC)")
		if err != nil || cells(res)[0][0] != "60000000" {
			t.Errorf("result = %+v, err = %v", res, err)
		}
	})
	t.Run("a line starting with a dot inside a string is refused, not run", func(t *testing.T) {
		if _, err := run("SELECT 'a\n.mode csv\nb' AS s"); !errors.Is(err, ErrDotCommand) {
			t.Errorf("Run() = %v, want ErrDotCommand", err)
		}
	})
}

// A catalog that cannot be reached must fail the whole run as a catalog
// problem, not as a problem of the operator's query, and must not leak the
// secret DuckDB was given. The address refuses connections at once.
func TestRunReportsAnUnreachableCatalogInDuckDB(t *testing.T) {
	if err := exec.Command("docker", "image", "inspect", "pims-duckdb").Run(); err != nil {
		t.Skip("pims-duckdb image not built")
	}
	catalog := testCatalog
	catalog.Endpoint = "https://127.0.0.1:9/iceberg/catalog/organizations/org-1/environments/env-1"
	r, err := New(dockerRun{}, "duckdb", &catalog)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, err = r.Run(ctx, "SELECT 1")
	if err == nil || !strings.HasPrefix(err.Error(), "could not set up the DuckDB session: ") || !strings.Contains(err.Error(), "oauth/tokens") {
		t.Fatalf("Run() = %v, want a session failure that names the catalog request", err)
	}
	if strings.Contains(err.Error(), catalog.APISecret) {
		t.Fatalf("error leaks the secret: %v", err)
	}
	if strings.Contains(err.Error(), "Parser Error") {
		t.Fatalf("the setup script did not parse: %v", err)
	}
	t.Logf("the operator sees: %v", err)
}
