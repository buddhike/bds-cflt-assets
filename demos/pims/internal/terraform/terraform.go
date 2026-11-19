// Package terraform provisions the PIMS Confluent Cloud resources by
// running an embedded Terraform configuration through terraform-exec.
//
// The terraform binary is downloaded with hc-install on first use (and its
// signature verified), so operators never install Terraform themselves.
package terraform

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/hashicorp/go-version"
	install "github.com/hashicorp/hc-install"
	hcfs "github.com/hashicorp/hc-install/fs"
	"github.com/hashicorp/hc-install/product"
	"github.com/hashicorp/hc-install/releases"
	"github.com/hashicorp/hc-install/src"
	"github.com/hashicorp/terraform-exec/tfexec"

	"pims/internal/config"
	"pims/internal/logging"
)

// Version is the Terraform CLI version PIMS downloads and runs.
const Version = "1.16.7"

// The lock file pins the Confluent provider version and checksums so every
// workspace resolves the same provider that `terraform validate` was run
// against.
//
//go:embed tf/*.tf tf/.terraform.lock.hcl
var configFS embed.FS

// ErrNotProvisioned is returned by Outputs when no state exists yet.
var ErrNotProvisioned = errors.New("confluent cloud resources are not provisioned")

// ErrRegionChanged is returned by Apply when the requested AWS region
// differs from the provisioned one. Applying would replace the cluster and
// lose all data, so the operator must destroy first.
var ErrRegionChanged = errors.New("aws region differs from provisioned resources")

// Outputs are the values PIMS needs from the provisioned resources.
type Outputs struct {
	AWSRegion                string   `json:"aws_region"`
	OrganizationID           string   `json:"organization_id"`
	EnvironmentID            string   `json:"environment_id"`
	KafkaClusterID           string   `json:"kafka_cluster_id"`
	KafkaBootstrap           string   `json:"kafka_bootstrap"`
	KafkaAPIKey              string   `json:"kafka_api_key"`
	KafkaAPISecret           string   `json:"kafka_api_secret"`
	SchemaRegistryURL        string   `json:"schema_registry_url"`
	SchemaRegistryAPIKey     string   `json:"schema_registry_api_key"`
	SchemaRegistryAPISecret  string   `json:"schema_registry_api_secret"`
	TableflowAPIKey          string   `json:"tableflow_api_key"`
	TableflowAPISecret       string   `json:"tableflow_api_secret"`
	TableflowCatalogEndpoint string   `json:"tableflow_catalog_endpoint"`
	Topics                   []string `json:"topics"`
	TableflowEnabled         bool     `json:"tableflow_enabled"`
}

// LogValue keeps generated secrets out of logs.
func (o Outputs) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("environment_id", o.EnvironmentID),
		slog.String("kafka_cluster_id", o.KafkaClusterID),
		slog.String("kafka_bootstrap", o.KafkaBootstrap),
		slog.String("schema_registry_url", o.SchemaRegistryURL),
		slog.Any("topics", o.Topics),
		slog.Bool("tableflow_enabled", o.TableflowEnabled),
	)
}

// ApplyOptions select which phase of the configuration to apply.
type ApplyOptions struct {
	// EnableTableflow turns on Tableflow for the CDC topics. Only set it
	// after the topic schemas have been registered.
	EnableTableflow bool
}

// Runner applies and destroys the embedded configuration.
type Runner struct {
	ws  config.Workspace
	log *slog.Logger
}

// NewRunner returns a Runner that keeps files and state in ws.
func NewRunner(ws config.Workspace, log *slog.Logger) *Runner {
	return &Runner{ws: ws, log: log.With("component", "terraform")}
}

// Apply provisions (or updates) the Confluent Cloud resources and returns
// their outputs.
//
// Once Tableflow has been enabled it stays enabled: re-running phase one
// (for example on the next launch) must not destroy the Iceberg tables.
func (r *Runner) Apply(ctx context.Context, creds config.Credentials, opts ApplyOptions) (Outputs, error) {
	tf, out, err := r.prepare(ctx, creds)
	if err != nil {
		return Outputs{}, err
	}
	defer out.Flush()

	suffix, err := r.ws.Suffix()
	if err != nil {
		return Outputs{}, err
	}

	prev, err := r.existingOutputs(ctx, tf)
	if err != nil {
		return Outputs{}, err
	}
	opts, err = reconcile(prev, creds, opts)
	if err != nil {
		return Outputs{}, err
	}

	r.log.Info("Applying Confluent Cloud configuration", "aws_region", creds.AWSRegion, "name_suffix", suffix, "enable_tableflow", opts.EnableTableflow)
	if err := tf.Apply(ctx, applyVars(creds, suffix, opts)...); err != nil {
		return Outputs{}, fmt.Errorf("terraform apply: %w", err)
	}
	return r.outputs(ctx, tf)
}

// reconcile adjusts the requested options against what is already
// provisioned. prev is nil when nothing has been applied yet.
func reconcile(prev *Outputs, creds config.Credentials, opts ApplyOptions) (ApplyOptions, error) {
	if prev == nil {
		return opts, nil
	}
	if prev.AWSRegion != "" && prev.AWSRegion != creds.AWSRegion {
		return opts, fmt.Errorf("%w: provisioned in %s, requested %s; destroy the demo first to switch regions",
			ErrRegionChanged, prev.AWSRegion, creds.AWSRegion)
	}
	opts.EnableTableflow = opts.EnableTableflow || prev.TableflowEnabled
	return opts, nil
}

// existingOutputs returns the outputs from current state, or nil when no
// state exists.
func (r *Runner) existingOutputs(ctx context.Context, tf *tfexec.Terraform) (*Outputs, error) {
	exists, err := r.hasState()
	if err != nil || !exists {
		return nil, err
	}
	raw, err := tf.Output(ctx)
	if err != nil {
		return nil, fmt.Errorf("read existing terraform outputs: %w", err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	prev, err := decodeOutputs(raw)
	if err != nil {
		return nil, err
	}
	return &prev, nil
}

func (r *Runner) hasState() (bool, error) {
	_, err := os.Stat(filepath.Join(r.ws.TerraformDir(), "terraform.tfstate"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("check terraform state: %w", err)
	}
	return true, nil
}

// Destroy removes every resource PIMS created in Confluent Cloud.
func (r *Runner) Destroy(ctx context.Context, creds config.Credentials) error {
	tf, out, err := r.prepare(ctx, creds)
	if err != nil {
		return err
	}
	defer out.Flush()
	suffix := r.destroySuffix()
	r.log.Info("Destroying Confluent Cloud resources", "aws_region", creds.AWSRegion, "name_suffix", suffix)
	if err := tf.Destroy(ctx, destroyVars(creds, suffix)...); err != nil {
		return fmt.Errorf("terraform destroy: %w", err)
	}
	return nil
}

// Outputs reads outputs from existing state without contacting Confluent
// Cloud. It returns ErrNotProvisioned when nothing has been applied.
func (r *Runner) Outputs(ctx context.Context) (Outputs, error) {
	exists, err := r.hasState()
	if err != nil {
		return Outputs{}, err
	}
	if !exists {
		return Outputs{}, ErrNotProvisioned
	}
	execPath, err := r.install(ctx)
	if err != nil {
		return Outputs{}, err
	}
	tf, err := tfexec.NewTerraform(r.ws.TerraformDir(), execPath)
	if err != nil {
		return Outputs{}, fmt.Errorf("create terraform runner: %w", err)
	}
	return r.outputs(ctx, tf)
}

// prepare installs terraform, writes the embedded configuration and runs
// terraform init. Callers must Flush the returned writer when done so a
// trailing line without a newline still reaches the log.
func (r *Runner) prepare(ctx context.Context, creds config.Credentials) (*tfexec.Terraform, *logging.LineWriter, error) {
	if err := creds.Validate(); err != nil {
		return nil, nil, err
	}
	if err := r.ws.Ensure(); err != nil {
		return nil, nil, err
	}
	execPath, err := r.install(ctx)
	if err != nil {
		return nil, nil, err
	}
	if err := writeConfig(r.ws.TerraformDir()); err != nil {
		return nil, nil, err
	}

	tf, err := tfexec.NewTerraform(r.ws.TerraformDir(), execPath)
	if err != nil {
		return nil, nil, fmt.Errorf("create terraform runner: %w", err)
	}
	if err := tf.SetEnv(processEnv(creds)); err != nil {
		return nil, nil, fmt.Errorf("set terraform env: %w", err)
	}
	out := logging.NewLineWriter(r.log, "Terraform output")
	tf.SetStdout(out)
	tf.SetStderr(out)

	r.log.Info("Initialising Terraform providers")
	if err := tf.Init(ctx); err != nil {
		out.Flush()
		return nil, nil, fmt.Errorf("terraform init: %w", err)
	}
	return tf, out, nil
}

// install returns the path to a terraform binary of the pinned version,
// reusing one in the workspace (or on PATH) before downloading.
func (r *Runner) install(ctx context.Context) (string, error) {
	v := version.Must(version.NewVersion(Version))
	installer := install.NewInstaller()
	execPath, err := installer.Ensure(ctx, []src.Source{
		&hcfs.ExactVersion{Product: product.Terraform, Version: v, ExtraPaths: []string{r.ws.BinDir()}},
		&releases.ExactVersion{Product: product.Terraform, Version: v, InstallDir: r.ws.BinDir()},
	})
	if err != nil {
		return "", fmt.Errorf("install terraform %s: %w", Version, err)
	}
	r.log.Debug("Using terraform binary", "path", execPath)
	return execPath, nil
}

func (r *Runner) outputs(ctx context.Context, tf *tfexec.Terraform) (Outputs, error) {
	raw, err := tf.Output(ctx)
	if err != nil {
		return Outputs{}, fmt.Errorf("terraform output: %w", err)
	}
	if len(raw) == 0 {
		return Outputs{}, ErrNotProvisioned
	}
	out, err := decodeOutputs(raw)
	if err != nil {
		return Outputs{}, err
	}
	r.log.Info("Confluent Cloud resources ready", "outputs", out)
	return out, nil
}

// decodeOutputs converts terraform's output map into Outputs by
// re-marshalling just the values.
func decodeOutputs(raw map[string]tfexec.OutputMeta) (Outputs, error) {
	values := make(map[string]json.RawMessage, len(raw))
	for k, v := range raw {
		values[k] = v.Value
	}
	b, err := json.Marshal(values)
	if err != nil {
		return Outputs{}, fmt.Errorf("encode terraform outputs: %w", err)
	}
	var out Outputs
	if err := json.Unmarshal(b, &out); err != nil {
		return Outputs{}, fmt.Errorf("decode terraform outputs: %w", err)
	}
	return out, nil
}

func applyVars(creds config.Credentials, suffix string, opts ApplyOptions) []tfexec.ApplyOption {
	return []tfexec.ApplyOption{
		tfexec.Var("aws_region=" + creds.AWSRegion),
		tfexec.Var("name_suffix=" + suffix),
		tfexec.Var("enable_tableflow=" + strconv.FormatBool(opts.EnableTableflow)),
	}
}

// placeholderSuffix stands in for the suffix when destroy cannot read it.
const placeholderSuffix = "destroy"

// destroySuffix returns the saved suffix, or a placeholder when settings.json
// is unreadable. Destroy removes resources by ID, so the name makes no
// difference to it, and a damaged settings file must never stand in the way of
// tearing down billable resources.
func (r *Runner) destroySuffix() string {
	suffix, err := r.ws.Suffix()
	if err != nil {
		r.log.Warn("Could not read the saved suffix, destroying with a placeholder", "error", err)
		return placeholderSuffix
	}
	return suffix
}

// destroyVars supplies the variables that have no default. Names do not
// affect what destroy removes, but Terraform still needs a valid value.
func destroyVars(creds config.Credentials, suffix string) []tfexec.DestroyOption {
	return []tfexec.DestroyOption{
		tfexec.Var("aws_region=" + creds.AWSRegion),
		tfexec.Var("name_suffix=" + suffix),
	}
}

// processEnv builds the terraform process environment: the parent
// environment minus variables terraform-exec manages itself and any
// TF_VAR_/TF_CLI_ARGS overrides (which could silently change the demo's
// configuration), plus the Confluent Cloud credentials. Secrets are passed
// via env, never as -var arguments, so they do not appear in process
// listings.
func processEnv(creds config.Credentials) map[string]string {
	env := make(map[string]string)
	for _, kv := range os.Environ() {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || strings.HasPrefix(k, "TF_VAR_") || strings.HasPrefix(k, "TF_CLI_ARGS") {
			continue
		}
		env[k] = v
	}
	for _, k := range tfexec.ProhibitedEnv(env) {
		delete(env, k)
	}
	env["CONFLUENT_CLOUD_API_KEY"] = creds.APIKey
	env["CONFLUENT_CLOUD_API_SECRET"] = creds.APISecret
	return env
}

// writeConfig copies the embedded configuration into dir, replacing older
// copies and deleting .tf files that are no longer embedded, so upgrades
// of PIMS pick up configuration changes.
func writeConfig(dir string) error {
	stale, err := filepath.Glob(filepath.Join(dir, "*.tf"))
	if err != nil {
		return fmt.Errorf("list existing terraform files: %w", err)
	}
	for _, f := range stale {
		if _, err := fs.Stat(configFS, "tf/"+filepath.Base(f)); errors.Is(err, fs.ErrNotExist) {
			if err := os.Remove(f); err != nil {
				return fmt.Errorf("remove stale %s: %w", f, err)
			}
		}
	}
	return fs.WalkDir(configFS, "tf", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := configFS.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read embedded %s: %w", path, err)
		}
		dst := filepath.Join(dir, filepath.Base(path))
		if err := os.WriteFile(dst, b, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", dst, err)
		}
		return nil
	})
}
