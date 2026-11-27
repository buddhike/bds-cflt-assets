// Package duckdbagent lets PIMS run the DuckDB CLI in another container of the
// same ECS task, which `docker exec` reaches.
//
// The agent runs in the DuckDB container. The client has the method
// query.Execer asks for, so query.Runner runs its queries through it.
//
// The agent runs one command, the pipeline that query.Runner builds, and refuses any
// other. What it does not do is judge the SQL on stdin: DuckDB's shell commands, such as
// .shell, are rejected by query.Runner, not here. So the token is what keeps the DuckDB
// container's shell from a caller, and only the server holds it.
package duckdbagent

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// Path is the one endpoint.
const Path = "/exec"

// TokenHeader carries the shared secret.
const TokenHeader = "X-Agent-Token"

const (
	maxRequest = 16 << 20
	maxStderr  = 64 << 10
	// maxStdoutLimit is the most a client may ask to be buffered.
	maxStdoutLimit = 16 << 20
)

// Request is one command run.
type Request struct {
	// Cmd is the command, as query.Runner builds it. Only that shape is accepted.
	Cmd []string `json:"cmd"`
	// Stdin is what the command reads.
	Stdin string `json:"stdin"`
	// MaxStdout stops the run once the command has printed more than this.
	MaxStdout int `json:"maxStdout"`
}

// Response is how a command ended.
type Response struct {
	Stdout   []byte `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exitCode"`
	// TooLarge is set when the command printed more than MaxStdout. Stdout is empty then.
	TooLarge bool `json:"tooLarge,omitempty"`
}

// allowed is the exact command query.Runner builds: DuckDB in batch mode, bounded
// in time by timeout and in output by head. The numbers are all it varies.
var allowed = regexp.MustCompile(`^set -o pipefail; timeout --signal=KILL [0-9]{1,5}s duckdb -batch -bail \| head -c [0-9]{1,9}$`)

func commandAllowed(cmd []string) bool {
	return len(cmd) == 3 && cmd[0] == "bash" && cmd[1] == "-c" && allowed.MatchString(cmd[2])
}

// Handler serves Path. Requests without the token are refused.
func Handler(token string, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+Path, func(w http.ResponseWriter, r *http.Request) {
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get(TokenHeader)), []byte(token)) != 1 {
			http.Error(w, "unauthorised", http.StatusUnauthorized)
			return
		}
		var req Request
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxRequest)).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !commandAllowed(req.Cmd) {
			http.Error(w, "this command is not allowed", http.StatusForbidden)
			return
		}
		if req.MaxStdout <= 0 || req.MaxStdout > maxStdoutLimit {
			http.Error(w, "maxStdout out of range", http.StatusBadRequest)
			return
		}
		resp := run(r.Context(), req)
		if resp.ExitCode != 0 {
			log.Info("DuckDB run failed", "exit_code", resp.ExitCode)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, "ok") })
	return mux
}

func run(ctx context.Context, req Request) Response {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stdout := &capped{max: req.MaxStdout, onExceed: cancel}
	stderr := &prefix{max: maxStderr}
	cmd := exec.CommandContext(ctx, req.Cmd[0], req.Cmd[1:]...)
	cmd.Stdin = strings.NewReader(req.Stdin)
	// The SQL runs with the agent's environment otherwise, and DuckDB can read it.
	cmd.Env = withoutAgentToken(os.Environ())
	cmd.Stdout, cmd.Stderr = stdout, stderr
	// A client that gives up, or output past the limit, must end the whole pipeline.
	// Killing only bash would leave DuckDB running until its own time limit.
	killGroupOnCancel(cmd)

	err := cmd.Run()
	if stdout.exceeded {
		return Response{TooLarge: true, ExitCode: 1}
	}
	resp := Response{Stdout: stdout.buf.Bytes(), Stderr: stderr.buf.String()}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			resp.ExitCode = ee.ExitCode()
		} else {
			resp.ExitCode = -1
			resp.Stderr += err.Error()
		}
		if resp.ExitCode == 0 {
			resp.ExitCode = -1 // killed by a signal
		}
	}
	return resp
}

// capped collects up to max bytes. Past that it records the fact and stops the command.
type capped struct {
	buf      bytes.Buffer
	max      int
	exceeded bool
	onExceed func()
}

func (c *capped) Write(p []byte) (int, error) {
	if c.buf.Len()+len(p) > c.max {
		c.exceeded = true
		c.onExceed()
		return 0, errors.New("output limit reached")
	}
	return c.buf.Write(p)
}

// prefix keeps the first max bytes and drops the rest without failing.
type prefix struct {
	buf bytes.Buffer
	max int
}

func (p *prefix) Write(b []byte) (int, error) {
	if room := p.max - p.buf.Len(); room > 0 {
		p.buf.Write(b[:min(len(b), room)])
	}
	return len(b), nil
}

func withoutAgentToken(env []string) []string {
	out := make([]string, 0, len(env))
	for _, e := range env {
		if !strings.HasPrefix(e, "PIMS_AGENT_TOKEN=") {
			out = append(out, e)
		}
	}
	return out
}
