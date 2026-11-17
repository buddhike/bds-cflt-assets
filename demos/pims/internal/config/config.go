// Package config holds the operator-supplied settings and the on-disk
// workspace layout used by PIMS.
//
// Confluent Cloud API secrets are kept in memory only. They are never
// written to disk by this package; the only persisted value is the last
// AWS region used, so the setup form can be pre-filled.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// awsRegionPattern matches AWS region codes such as us-east-1 or
// ap-southeast-2, including GovCloud regions.
var awsRegionPattern = regexp.MustCompile(`^[a-z]{2}(-gov)?-[a-z]+-[0-9]$`)

// ErrInvalidCredentials is returned when operator-supplied settings fail
// validation.
var ErrInvalidCredentials = errors.New("invalid credentials")

// Credentials are the values the operator enters when PIMS launches.
type Credentials struct {
	AWSRegion string `json:"awsRegion"`
	APIKey    string `json:"apiKey"`
	APISecret string `json:"apiSecret"`
}

// Normalise trims surrounding whitespace that is commonly picked up when
// pasting keys from the Confluent Cloud console.
func (c Credentials) Normalise() Credentials {
	return Credentials{
		AWSRegion: strings.ToLower(strings.TrimSpace(c.AWSRegion)),
		APIKey:    strings.TrimSpace(c.APIKey),
		APISecret: strings.TrimSpace(c.APISecret),
	}
}

// Validate reports every problem with the credentials at once so the
// operator can fix them in a single pass.
func (c Credentials) Validate() error {
	var problems []string
	if !awsRegionPattern.MatchString(c.AWSRegion) {
		problems = append(problems, fmt.Sprintf("AWS region %q is not a valid region code", c.AWSRegion))
	}
	if c.APIKey == "" || strings.ContainsAny(c.APIKey, " \t\r\n") {
		problems = append(problems, "Confluent Cloud API key is required and must not contain whitespace")
	}
	if c.APISecret == "" || strings.ContainsAny(c.APISecret, " \t\r\n") {
		problems = append(problems, "Confluent Cloud API secret is required and must not contain whitespace")
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidCredentials, strings.Join(problems, "; "))
	}
	return nil
}

// String redacts the secret so credentials can never leak through %v.
func (c Credentials) String() string {
	return fmt.Sprintf("Credentials{AWSRegion:%s APIKey:%s APISecret:[REDACTED]}", c.AWSRegion, c.APIKey)
}

// LogValue redacts the secret when credentials are passed to slog.
func (c Credentials) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("aws_region", c.AWSRegion),
		slog.String("api_key", c.APIKey),
		slog.String("api_secret", "[REDACTED]"),
	)
}

// Workspace is the directory where PIMS keeps its working files: the
// Terraform binary and state, and the rendered Docker Compose project.
type Workspace struct {
	Root string
}

// DefaultWorkspace returns the per-user workspace, for example
// ~/Library/Application Support/pims on macOS.
func DefaultWorkspace() (Workspace, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return Workspace{}, fmt.Errorf("resolve user config dir: %w", err)
	}
	return Workspace{Root: filepath.Join(dir, "pims")}, nil
}

// BinDir holds downloaded tool binaries such as terraform.
func (w Workspace) BinDir() string { return filepath.Join(w.Root, "bin") }

// TerraformDir holds the Terraform configuration and its state.
func (w Workspace) TerraformDir() string { return filepath.Join(w.Root, "terraform") }

// ComposeDir holds the Docker Compose project.
func (w Workspace) ComposeDir() string { return filepath.Join(w.Root, "compose") }

func (w Workspace) settingsPath() string { return filepath.Join(w.Root, "settings.json") }

// Ensure creates the workspace directories. They are private to the
// current user because Terraform state contains generated API secrets.
func (w Workspace) Ensure() error {
	for _, d := range []string{w.Root, w.BinDir(), w.TerraformDir(), w.ComposeDir()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("create workspace dir %s: %w", d, err)
		}
	}
	return nil
}

// Settings are the non-secret values remembered between launches.
type Settings struct {
	AWSRegion string `json:"awsRegion"`
}

// LoadSettings reads persisted settings. A missing file is not an error.
func (w Workspace) LoadSettings() (Settings, error) {
	var s Settings
	b, err := os.ReadFile(w.settingsPath())
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return s, fmt.Errorf("read settings: %w", err)
	}
	if err := json.Unmarshal(b, &s); err != nil {
		return s, fmt.Errorf("parse settings %s: %w", w.settingsPath(), err)
	}
	return s, nil
}

// SaveSettings persists the non-secret settings.
func (w Workspace) SaveSettings(s Settings) error {
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("encode settings: %w", err)
	}
	if err := os.WriteFile(w.settingsPath(), b, 0o600); err != nil {
		return fmt.Errorf("write settings: %w", err)
	}
	return nil
}
