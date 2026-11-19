package compose

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pims/internal/config"
)

func newTestProject(t *testing.T) *Project {
	t.Helper()
	return NewProject(config.Workspace{Root: t.TempDir()}, slog.New(slog.DiscardHandler))
}

func TestRenderWritesProject(t *testing.T) {
	p := newTestProject(t)
	if err := p.render(); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"docker-compose.yml", "connect/Dockerfile", "duckdb/Dockerfile"} {
		if _, err := os.Stat(filepath.Join(p.ws.ComposeDir(), f)); err != nil {
			t.Errorf("%s not rendered: %v", f, err)
		}
	}
}

func TestMySQLSecretsArePersistedAndPrivate(t *testing.T) {
	p := newTestProject(t)
	first, err := p.MySQLSecrets()
	if err != nil {
		t.Fatal(err)
	}
	if len(first.RootPassword) != 32 || first.RootPassword == first.AppPassword || len(first.Generation) != 8 {
		t.Fatalf("unexpected generated secrets")
	}
	second, err := p.MySQLSecrets()
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("secrets must be stable across calls")
	}
	info, err := os.Stat(filepath.Join(p.ws.ComposeDir(), "mysql-secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("secrets file mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestUpRejectsIncompleteKafkaEnv(t *testing.T) {
	p := newTestProject(t)
	err := p.Up(context.Background(), KafkaEnv{Bootstrap: "b:9092"})
	if err == nil || !strings.Contains(err.Error(), "api key") {
		t.Fatalf("Up() = %v, want missing field error", err)
	}
}

func TestCheckDockerReportsMissingCLI(t *testing.T) {
	p := newTestProject(t)
	p.docker = "pims-no-such-docker-binary"
	if err := p.CheckDocker(context.Background()); !errors.Is(err, ErrDockerUnavailable) {
		t.Fatalf("CheckDocker() = %v, want ErrDockerUnavailable", err)
	}
}

// TestComposeConfigInterpolates asks docker compose to parse the rendered
// project, which catches YAML and interpolation mistakes. Skipped when
// docker is not installed.
func TestComposeConfigInterpolates(t *testing.T) {
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed")
	}
	p := newTestProject(t)
	if err := p.render(); err != nil {
		t.Fatal(err)
	}
	env := serviceEnv(KafkaEnv{
		Bootstrap: "pkc-test:9092", APIKey: "KAFKAKEY", APISecret: "kafka+secret/1",
		SchemaRegistryURL: "https://psrc-test", SchemaRegistryAPIKey: "SRKEY", SchemaRegistryAPISecret: "srsecret",
	}, MySQLSecrets{RootPassword: "root", AppPassword: "app", DebeziumPassword: "dbz"})

	c := p.command(context.Background(), env, "config")
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("docker compose config: %v\n%s", err, out)
	}
	s := string(out)
	for _, want := range []string{
		`username="KAFKAKEY" password="kafka+secret/1";`,
		"SRKEY:srsecret",
		"127.0.0.1",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered config missing %q", want)
		}
	}
}

func TestMySQLSecretsRejectsIncompleteFile(t *testing.T) {
	p := newTestProject(t)
	if err := p.ws.Ensure(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.secretsPath(), []byte(`{"rootPassword":"x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.MySQLSecrets(); err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("MySQLSecrets() = %v, want incomplete file error", err)
	}
}

func TestScrubRedactsAndTruncates(t *testing.T) {
	got := scrub("Parser Error near CLIENT_SECRET 'hunter2'\n", []string{"hunter2", ""})
	if strings.Contains(got, "hunter2") || !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("scrub() = %q", got)
	}
	if n := len(scrub(strings.Repeat("x", 2000), nil)); n > 510 {
		t.Fatalf("scrub() length = %d, want truncated", n)
	}
}

// A failure must not blame the first word of the command: callers wrap the
// program in others such as timeout, which would read as the cause.
func TestExecErrorDoesNotNameTheCommand(t *testing.T) {
	p := newTestProject(t)
	p.docker = "false" // exits 1, whatever it is asked
	_, err := p.Exec(context.Background(), ServiceDuckDB, nil, nil, "timeout", "--signal=KILL", "5s", "duckdb")
	if err == nil {
		t.Fatal("Exec must report the command's failure")
	}
	if got := err.Error(); strings.Contains(got, "timeout") || !strings.Contains(got, ServiceDuckDB) {
		t.Fatalf("error = %q, want the service and not the command", got)
	}
}

func TestExecRequiresCommand(t *testing.T) {
	if _, err := newTestProject(t).Exec(context.Background(), ServiceDuckDB, nil, nil); err == nil {
		t.Fatal("Exec without a command must fail")
	}
}

// fakeDocker writes an executable that stands in for the docker CLI.
func fakeDocker(t *testing.T, script string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExecErrorCarriesScrubbedStderrAndKeepsStdout(t *testing.T) {
	p := newTestProject(t)
	p.docker = fakeDocker(t, `echo partial; echo "Parser Error near hunter2" >&2; exit 3`)
	out, err := p.Exec(context.Background(), ServiceDuckDB, nil, []string{"hunter2"}, "duckdb")
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("Exec() = %v, want an *ExecError", err)
	}
	if ee.Service != ServiceDuckDB || ee.Stderr != "Parser Error near [REDACTED]" {
		t.Fatalf("ExecError = %+v", ee)
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("Exec() = %v, want the exit status to be reachable", err)
	}
	if string(out) != "partial\n" {
		t.Fatalf("stdout = %q, want what was written before the failure", out)
	}
	if want := "exec in duckdb: exit status 3: Parser Error near [REDACTED]"; err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
}

// The docker CLI is replaced by yes(1), which prints its arguments forever.
func TestExecLimitStopsOutputThatKeepsComing(t *testing.T) {
	p := newTestProject(t)
	p.docker = "yes"
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := p.ExecLimit(ctx, ServiceDuckDB, nil, nil, 4096, "duckdb")
	if !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("ExecLimit() = %v, want ErrOutputTooLarge", err)
	}
	if out != nil {
		t.Fatalf("ExecLimit() returned %d bytes with the error, want none", len(out))
	}
	if ctx.Err() != nil {
		t.Fatal("ExecLimit must stop the command itself, not wait for the deadline")
	}
}

func TestExecLimitReturnsOutputWithinTheLimit(t *testing.T) {
	p := newTestProject(t)
	p.docker = fakeDocker(t, `printf '0123456789'`)
	out, err := p.ExecLimit(context.Background(), ServiceDuckDB, nil, nil, 10, "duckdb")
	if err != nil || string(out) != "0123456789" {
		t.Fatalf("ExecLimit() = %q, %v, want output of exactly the limit", out, err)
	}
	if _, err := p.ExecLimit(context.Background(), ServiceDuckDB, nil, nil, 9, "duckdb"); !errors.Is(err, ErrOutputTooLarge) {
		t.Fatalf("ExecLimit() = %v, want ErrOutputTooLarge one byte over", err)
	}
	// Zero means unlimited, which is what Exec uses.
	if out, err := p.Exec(context.Background(), ServiceDuckDB, nil, nil, "duckdb"); err != nil || len(out) != 10 {
		t.Fatalf("Exec() = %q, %v", out, err)
	}
}

// A command that writes a flood to stderr is neither stopped nor buffered
// whole, and the error still starts with the beginning of what it said.
func TestExecKeepsOnlyTheStartOfStderr(t *testing.T) {
	p := newTestProject(t)
	p.docker = fakeDocker(t, `echo "Binder Error: first line" >&2; head -c 3000000 /dev/zero | tr '\0' 'x' >&2; exit 1`)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_, err := p.Exec(ctx, ServiceDuckDB, nil, nil, "duckdb")
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("Exec() = %v, want an *ExecError", err)
	}
	if !strings.HasPrefix(ee.Stderr, "Binder Error: first line") || len(ee.Stderr) > 510 {
		t.Fatalf("Stderr = %d bytes starting %.40q, want the start of the output, truncated", len(ee.Stderr), ee.Stderr)
	}
	var b prefixBuffer
	b.max = 5
	if n, err := b.Write([]byte("0123456789")); n != 10 || err != nil || b.String() != "01234" {
		t.Fatalf("prefixBuffer kept %q after Write = %d, %v", b.String(), n, err)
	}
}
