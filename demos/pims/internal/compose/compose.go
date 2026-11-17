// Package compose runs the local PIMS services (MySQL, Kafka Connect with
// Debezium, and DuckDB) through the docker compose CLI.
//
// Docker is the only tool PIMS requires the operator to install, so the
// compose project is embedded in the binary and rendered into the
// workspace before each run.
package compose

import (
	"bytes"
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"pims/internal/config"
	"pims/internal/logging"
)

// ProjectName is the docker compose project name used for every command.
const ProjectName = "pims"

// Host ports published on 127.0.0.1. MySQL avoids 3306 so it does not
// collide with a MySQL server the operator may already run.
const (
	MySQLHostPort   = 33306
	ConnectHostPort = 8083
)

// Service names from docker-compose.yml.
const (
	ServiceMySQL   = "mysql"
	ServiceConnect = "connect"
	ServiceDuckDB  = "duckdb"
)

//go:embed all:project
var projectFS embed.FS

// ErrDockerUnavailable is returned when the docker CLI or daemon cannot be
// reached.
var ErrDockerUnavailable = errors.New("docker is not available")

// KafkaEnv is the Confluent Cloud connection the Connect worker uses.
type KafkaEnv struct {
	Bootstrap               string
	APIKey                  string
	APISecret               string
	SchemaRegistryURL       string
	SchemaRegistryAPIKey    string
	SchemaRegistryAPISecret string
}

func (k KafkaEnv) validate() error {
	var missing []string
	for name, v := range map[string]string{
		"bootstrap": k.Bootstrap, "api key": k.APIKey, "api secret": k.APISecret,
		"schema registry url": k.SchemaRegistryURL, "schema registry api key": k.SchemaRegistryAPIKey,
		"schema registry api secret": k.SchemaRegistryAPISecret,
	} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("kafka connection is missing %s", strings.Join(missing, ", "))
	}
	return nil
}

// MySQLSecrets are the passwords for the local demo database. They are
// generated once per workspace and stored with 0600 permissions.
type MySQLSecrets struct {
	RootPassword     string `json:"rootPassword"`
	AppPassword      string `json:"appPassword"`
	DebeziumPassword string `json:"debeziumPassword"`
}

// LogValue keeps passwords out of logs.
func (MySQLSecrets) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }

// Project runs docker compose against the rendered project directory.
type Project struct {
	ws  config.Workspace
	log *slog.Logger
	// docker is the CLI to invoke; overridden in tests.
	docker string
}

// NewProject returns a Project that renders files into ws.ComposeDir().
func NewProject(ws config.Workspace, log *slog.Logger) *Project {
	return &Project{ws: ws, log: log.With("component", "compose"), docker: "docker"}
}

// CheckDocker verifies that the docker CLI, the compose plugin and the
// daemon are all usable, with a hint the UI can show the operator.
func (p *Project) CheckDocker(ctx context.Context) error {
	if _, err := exec.LookPath(p.docker); err != nil {
		return fmt.Errorf("%w: docker CLI not found on PATH, install Docker Desktop: %v", ErrDockerUnavailable, err)
	}
	if out, err := exec.CommandContext(ctx, p.docker, "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		return fmt.Errorf("%w: docker daemon is not running (%s): %v", ErrDockerUnavailable, strings.TrimSpace(string(out)), err)
	}
	if out, err := exec.CommandContext(ctx, p.docker, "compose", "version", "--short").CombinedOutput(); err != nil {
		return fmt.Errorf("%w: docker compose plugin missing (%s): %v", ErrDockerUnavailable, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// MySQLSecrets returns the workspace's MySQL passwords, generating them on
// first use.
func (p *Project) MySQLSecrets() (MySQLSecrets, error) {
	if err := p.ws.Ensure(); err != nil {
		return MySQLSecrets{}, err
	}
	path := filepath.Join(p.ws.ComposeDir(), "mysql-secrets.json")
	b, err := os.ReadFile(path)
	if err == nil {
		var s MySQLSecrets
		if err := json.Unmarshal(b, &s); err != nil {
			return MySQLSecrets{}, fmt.Errorf("parse %s: %w", path, err)
		}
		return s, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return MySQLSecrets{}, fmt.Errorf("read mysql secrets: %w", err)
	}

	s := MySQLSecrets{RootPassword: randomPassword(), AppPassword: randomPassword(), DebeziumPassword: randomPassword()}
	b, err = json.Marshal(s)
	if err != nil {
		return MySQLSecrets{}, fmt.Errorf("encode mysql secrets: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return MySQLSecrets{}, fmt.Errorf("write mysql secrets: %w", err)
	}
	return s, nil
}

// Up renders the project and starts every service, building images as
// needed, and waits until their health checks pass.
func (p *Project) Up(ctx context.Context, kafka KafkaEnv) error {
	if err := kafka.validate(); err != nil {
		return err
	}
	secrets, err := p.MySQLSecrets()
	if err != nil {
		return err
	}
	if err := p.render(); err != nil {
		return err
	}
	p.log.Info("Starting local services", "services", []string{ServiceMySQL, ServiceConnect, ServiceDuckDB})
	env := serviceEnv(kafka, secrets)
	if err := p.run(ctx, env, nil, "up", "--detach", "--build", "--wait", "--wait-timeout", "600"); err != nil {
		return fmt.Errorf("docker compose up: %w", err)
	}
	return nil
}

// Down stops the services. With removeVolumes the MySQL data is deleted
// too, so the next Up starts from an empty database.
func (p *Project) Down(ctx context.Context, removeVolumes bool) error {
	if err := p.render(); err != nil {
		return err
	}
	args := []string{"down", "--remove-orphans"}
	if removeVolumes {
		args = append(args, "--volumes")
	}
	p.log.Info("Stopping local services", "remove_volumes", removeVolumes)
	if err := p.run(ctx, nil, nil, args...); err != nil {
		return fmt.Errorf("docker compose down: %w", err)
	}
	return nil
}

// Exec runs a command inside a running service with stdin attached and
// returns its stdout. Secrets should be passed on stdin rather than as
// arguments so they do not show up in process listings.
func (p *Project) Exec(ctx context.Context, service string, stdin io.Reader, cmd ...string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	args := append([]string{"exec", "-T", service}, cmd...)
	c := p.command(ctx, nil, args...)
	c.Stdin = stdin
	c.Stdout = &stdout
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("exec %s in %s: %w: %s", cmd[0], service, err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// run executes a compose subcommand, streaming its output to the log.
func (p *Project) run(ctx context.Context, env []string, stdin io.Reader, args ...string) error {
	out := logging.NewLineWriter(p.log, "Docker compose output")
	defer out.Flush()
	c := p.command(ctx, env, args...)
	c.Stdin = stdin
	c.Stdout = out
	c.Stderr = out
	return c.Run()
}

func (p *Project) command(ctx context.Context, env []string, args ...string) *exec.Cmd {
	full := append([]string{
		"compose",
		"--project-name", ProjectName,
		"--project-directory", p.ws.ComposeDir(),
		"--file", filepath.Join(p.ws.ComposeDir(), "docker-compose.yml"),
	}, args...)
	c := exec.CommandContext(ctx, p.docker, full...)
	c.Env = append(os.Environ(), env...)
	return c
}

// render writes the embedded project into the workspace.
func (p *Project) render() error {
	if err := p.ws.Ensure(); err != nil {
		return err
	}
	root := p.ws.ComposeDir()
	return fs.WalkDir(projectFS, "project", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel("project", path)
		if err != nil {
			return fmt.Errorf("resolve %s: %w", path, err)
		}
		dst := filepath.Join(root, rel)
		if d.IsDir() {
			if err := os.MkdirAll(dst, 0o700); err != nil {
				return fmt.Errorf("create %s: %w", dst, err)
			}
			return nil
		}
		b, err := projectFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", dst, err)
		}
		return nil
	})
}

// serviceEnv maps connection details to the variables docker-compose.yml
// interpolates. They are set on the docker process only.
func serviceEnv(k KafkaEnv, s MySQLSecrets) []string {
	return []string{
		"KAFKA_BOOTSTRAP=" + k.Bootstrap,
		"KAFKA_API_KEY=" + k.APIKey,
		"KAFKA_API_SECRET=" + k.APISecret,
		"SCHEMA_REGISTRY_URL=" + k.SchemaRegistryURL,
		"SCHEMA_REGISTRY_API_KEY=" + k.SchemaRegistryAPIKey,
		"SCHEMA_REGISTRY_API_SECRET=" + k.SchemaRegistryAPISecret,
		"MYSQL_ROOT_PASSWORD=" + s.RootPassword,
		"MYSQL_APP_PASSWORD=" + s.AppPassword,
		"MYSQL_DEBEZIUM_PASSWORD=" + s.DebeziumPassword,
		"MYSQL_HOST_PORT=" + strconv.Itoa(MySQLHostPort),
		"CONNECT_HOST_PORT=" + strconv.Itoa(ConnectHostPort),
	}
}

// randomPassword returns 32 hex characters. Hex keeps passwords safe to
// embed in compose interpolation, shell health checks and JAAS strings.
func randomPassword() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error
	return hex.EncodeToString(b)
}
