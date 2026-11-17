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
	if len(first.RootPassword) != 32 || first.RootPassword == first.AppPassword {
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
