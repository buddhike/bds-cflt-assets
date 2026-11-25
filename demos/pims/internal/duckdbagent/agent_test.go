package duckdbagent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pims/internal/compose"
)

const token = "agent-token-123"

// fakeDuckDB puts a `duckdb` on PATH that behaves as the test says, so the real
// command pipeline runs: bash, timeout, head. The DuckDB image has GNU timeout. A
// machine without it, such as a Mac, gets a stand-in that runs the command.
func fakeDuckDB(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "duckdb"), []byte("#!/bin/bash\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := exec.LookPath("timeout"); err != nil {
		shim := "#!/bin/bash\nshift 2 # --signal=KILL and the duration\nexec \"$@\"\n"
		if err := os.WriteFile(filepath.Join(dir, "timeout"), []byte(shim), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func newPair(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(Handler(token, slog.New(slog.NewTextHandler(io.Discard, nil))))
	t.Cleanup(srv.Close)
	c, err := NewClient(srv.URL, token)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRunsDuckDBWithStdinAndReturnsStdout(t *testing.T) {
	fakeDuckDB(t, `cat; echo " done"`)
	c := newPair(t)
	out, err := c.ExecLimit(context.Background(), "duckdb", strings.NewReader("SELECT 1;"), nil, 1000, "bash", "-c", "set -o pipefail; timeout --signal=KILL 30s duckdb -batch -bail | head -c 1001")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "SELECT 1; done\n" {
		t.Fatalf("stdout = %q", out)
	}
}

func TestFailureCarriesScrubbedStderrInAnExecError(t *testing.T) {
	fakeDuckDB(t, `echo "Error: bad secret hunter2 here" >&2; exit 1`)
	c := newPair(t)
	_, err := c.ExecLimit(context.Background(), "duckdb", strings.NewReader(""), []string{"hunter2"}, 1000, "bash", "-c", "set -o pipefail; timeout --signal=KILL 30s duckdb -batch -bail | head -c 1001")
	var ee *compose.ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("err = %v, want *compose.ExecError", err)
	}
	if strings.Contains(ee.Stderr, "hunter2") || !strings.Contains(ee.Stderr, "bad secret *** here") {
		t.Fatalf("stderr = %q", ee.Stderr)
	}
}

func TestOutputPastTheLimitIsTooLargeAndStopsTheCommand(t *testing.T) {
	// yes never ends, so the run only finishes because the agent stops it.
	fakeDuckDB(t, `exec yes 0123456789`)
	c := newPair(t)
	start := time.Now()
	_, err := c.ExecLimit(context.Background(), "duckdb", strings.NewReader(""), nil, 4096, "bash", "-c", "set -o pipefail; timeout --signal=KILL 30s duckdb -batch -bail | head -c 100000000")
	if !errors.Is(err, compose.ErrOutputTooLarge) {
		t.Fatalf("err = %v, want ErrOutputTooLarge", err)
	}
	if time.Since(start) > 10*time.Second {
		t.Fatal("took too long to stop the command")
	}
}

func TestClientGivingUpKillsTheWholePipeline(t *testing.T) {
	marker := filepath.Join(t.TempDir(), "alive")
	fakeDuckDB(t, `while true; do touch `+marker+`; sleep 0.1; done`)
	c := newPair(t)
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	_, err := c.ExecLimit(ctx, "duckdb", strings.NewReader(""), nil, 1000, "bash", "-c", "set -o pipefail; timeout --signal=KILL 30s duckdb -batch -bail | head -c 1001")
	if err == nil {
		t.Fatal("want an error")
	}
	time.Sleep(500 * time.Millisecond)
	_ = os.Remove(marker)
	time.Sleep(600 * time.Millisecond)
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the command kept running after the client gave up")
	}
}

func TestOnlyTheQueryPipelineIsAllowed(t *testing.T) {
	fakeDuckDB(t, `echo hi`)
	c := newPair(t)
	for _, cmd := range [][]string{
		{"sh", "-c", "id"},
		{"bash", "-c", "id"},
		{"bash", "-c", "set -o pipefail; timeout --signal=KILL 5s duckdb -batch -bail | head -c 5; id"},
		{"bash", "-c", "set -o pipefail; timeout --signal=KILL 5s duckdb -batch -bail -c 'x' | head -c 5"},
		{"bash", "-c", "set -o pipefail; timeout --signal=KILL $(id)s duckdb -batch -bail | head -c 5"},
		{"bash"},
	} {
		_, err := c.ExecLimit(context.Background(), "duckdb", strings.NewReader(""), nil, 100, cmd...)
		if err == nil || !strings.Contains(err.Error(), "403") {
			t.Errorf("%q: err = %v, want a 403", cmd, err)
		}
	}
}

func TestWrongTokenIsRefused(t *testing.T) {
	srv := httptest.NewServer(Handler(token, slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv.Close()
	c, _ := NewClient(srv.URL, "wrong")
	_, err := c.ExecLimit(context.Background(), "duckdb", strings.NewReader(""), nil, 100, "bash", "-c", "set -o pipefail; timeout --signal=KILL 5s duckdb -batch -bail | head -c 5")
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("err = %v, want 401", err)
	}
	// And an agent started without a token refuses everyone, even with an empty header.
	srv2 := httptest.NewServer(Handler("", slog.New(slog.NewTextHandler(io.Discard, nil))))
	defer srv2.Close()
	res, err := http.Post(srv2.URL+Path, "application/json", strings.NewReader("{}"))
	if err != nil || res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("empty token: %v %v", res, err)
	}
}

func TestNewClientRejectsRemoteAddressesAndMissingToken(t *testing.T) {
	for _, u := range []string{"http://10.0.0.5:8081", "https://127.0.0.1:8081", "http://example.com", "::"} {
		if _, err := NewClient(u, token); err == nil {
			t.Errorf("NewClient(%q) = nil error", u)
		}
	}
	if _, err := NewClient("http://127.0.0.1:8081", ""); err == nil {
		t.Error("empty token accepted")
	}
}

func TestReady(t *testing.T) {
	c := newPair(t)
	if err := c.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	dead, _ := NewClient("http://127.0.0.1:1", token)
	if err := dead.Ready(context.Background()); err == nil {
		t.Fatal("want an error from a dead agent")
	}
}
