package terraform

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-exec/tfexec"

	"pims/internal/config"
)

func TestWriteConfigCopiesEmbeddedFiles(t *testing.T) {
	dir := t.TempDir()
	if err := writeConfig(dir); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"main.tf", "variables.tf", "outputs.tf"} {
		info, err := os.Stat(filepath.Join(dir, f))
		if err != nil {
			t.Fatalf("%s not written: %v", f, err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %v, want 0600", f, info.Mode().Perm())
		}
	}
}

func TestEmbeddedConfigRequiresCompactedTopics(t *testing.T) {
	b, err := configFS.ReadFile("tf/main.tf")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"cleanup.policy" = "compact"`) {
		t.Fatal("CDC topics must be compacted for Tableflow Debezium support")
	}
}

func TestProcessEnvCarriesCredentialsAndDropsManagedVars(t *testing.T) {
	t.Setenv("TF_LOG", "TRACE") // managed by terraform-exec; SetEnv rejects it
	t.Setenv("PIMS_TEST_PASSTHROUGH", "yes")

	env := processEnv(config.Credentials{AWSRegion: "us-east-1", APIKey: "k", APISecret: "s"})

	if env["CONFLUENT_CLOUD_API_KEY"] != "k" || env["CONFLUENT_CLOUD_API_SECRET"] != "s" {
		t.Fatal("credentials missing from env")
	}
	if env["PIMS_TEST_PASSTHROUGH"] != "yes" {
		t.Fatal("parent environment not inherited")
	}
	if len(tfexec.ProhibitedEnv(env)) != 0 {
		t.Fatalf("env still has prohibited vars: %v", tfexec.ProhibitedEnv(env))
	}
}

func TestApplyVarsNeverIncludeSecrets(t *testing.T) {
	creds := config.Credentials{AWSRegion: "us-east-1", APIKey: "key", APISecret: "secret"}
	for _, v := range applyVars(creds, ApplyOptions{EnableTableflow: true}) {
		s := strings.ToLower(strings.TrimSpace(fmtOption(v)))
		if strings.Contains(s, "secret") || strings.Contains(s, "key") {
			t.Fatalf("apply var leaks credentials: %s", s)
		}
	}
}

// fmtOption renders the option including its unexported assignment.
func fmtOption(o tfexec.ApplyOption) string { return fmt.Sprintf("%+v", o) }

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestDecodeOutputs(t *testing.T) {
	raw := map[string]tfexec.OutputMeta{
		"environment_id":   {Value: json.RawMessage(`"env-123"`)},
		"kafka_api_secret": {Sensitive: true, Value: json.RawMessage(`"shh"`)},
		"topics":           {Value: json.RawMessage(`["appointments","patients","visits"]`)},
		"tableflow_enabled": {
			Value: json.RawMessage(`true`),
		},
	}
	out, err := decodeOutputs(raw)
	if err != nil {
		t.Fatal(err)
	}
	if out.EnvironmentID != "env-123" || out.KafkaAPISecret != "shh" || len(out.Topics) != 3 || !out.TableflowEnabled {
		t.Fatalf("unexpected outputs: %+v", out)
	}
}

func TestOutputsWithoutStateIsNotProvisioned(t *testing.T) {
	r := NewRunner(config.Workspace{Root: t.TempDir()}, discardLogger())
	if _, err := r.Outputs(context.Background()); !errors.Is(err, ErrNotProvisioned) {
		t.Fatalf("Outputs() = %v, want ErrNotProvisioned", err)
	}
}
