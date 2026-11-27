// Package config holds the Confluent Cloud credentials the pipeline is run
// with, and their validation.
//
// The secret is kept in memory only and redacts itself from every format
// that could log or send it.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
)

// awsRegionPattern matches AWS region codes such as us-east-1 or
// ap-southeast-2, including GovCloud regions.
var awsRegionPattern = regexp.MustCompile(`^[a-z]{2}(-gov)?-[a-z]+-[0-9]$`)

// ErrInvalidCredentials is returned when credentials fail validation.
var ErrInvalidCredentials = errors.New("invalid credentials")

// Credentials are the Confluent Cloud values the pipeline runs with.
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

// GoString redacts the secret for %#v.
func (c Credentials) GoString() string { return c.String() }

// MarshalJSON redacts the secret so credentials cannot leak through a
// state or error payload. Credentials are still decoded from JSON normally.
func (c Credentials) MarshalJSON() ([]byte, error) {
	type redacted struct {
		AWSRegion string `json:"awsRegion"`
		APIKey    string `json:"apiKey"`
		APISecret string `json:"apiSecret"`
	}
	return json.Marshal(redacted{AWSRegion: c.AWSRegion, APIKey: c.APIKey, APISecret: "[REDACTED]"})
}

// LogValue redacts the secret when credentials are passed to slog.
func (c Credentials) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("aws_region", c.AWSRegion),
		slog.String("api_key", maskKey(c.APIKey)),
		slog.String("api_secret", "[REDACTED]"),
	)
}

// maskKey keeps only a short prefix of an API key id for logs.
func maskKey(k string) string {
	if len(k) <= 4 {
		return "****"
	}
	return k[:4] + "****"
}
