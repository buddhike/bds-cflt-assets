package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

func TestCredentialsValidate(t *testing.T) {
	valid := Credentials{AWSRegion: "us-east-1", APIKey: "KEY123", APISecret: "s3cr3t+/="}

	tests := []struct {
		name    string
		creds   Credentials
		wantErr string
	}{
		{name: "valid", creds: valid},
		{name: "govcloud region", creds: Credentials{AWSRegion: "us-gov-west-1", APIKey: "k", APISecret: "s"}},
		{name: "bad region", creds: Credentials{AWSRegion: "useast1", APIKey: "k", APISecret: "s"}, wantErr: "AWS region"},
		{name: "missing key", creds: Credentials{AWSRegion: "us-east-1", APISecret: "s"}, wantErr: "API key"},
		{name: "secret with space", creds: Credentials{AWSRegion: "us-east-1", APIKey: "k", APISecret: "a b"}, wantErr: "API secret"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.creds.Validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if !errors.Is(err, ErrInvalidCredentials) {
				t.Fatalf("Validate() = %v, want ErrInvalidCredentials", err)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("Validate() = %q, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestCredentialsNormalise(t *testing.T) {
	got := Credentials{AWSRegion: " US-EAST-1 ", APIKey: " key\n", APISecret: "\tsecret "}.Normalise()
	want := Credentials{AWSRegion: "us-east-1", APIKey: "key", APISecret: "secret"}
	if got != want {
		t.Fatalf("Normalise() = %+v, want %+v", got, want)
	}
}

func TestCredentialsNeverLeakSecret(t *testing.T) {
	c := Credentials{AWSRegion: "us-east-1", APIKey: "key", APISecret: "topsecret"}

	if s := fmt.Sprintf("%v %+v %s %#v", c, c, c, c); strings.Contains(s, "topsecret") {
		t.Fatalf("fmt output leaked secret: %s", s)
	}

	b, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "topsecret") {
		t.Fatalf("json output leaked secret: %s", b)
	}

	var decoded Credentials
	if err := json.Unmarshal([]byte(`{"awsRegion":"us-east-1","apiKey":"k","apiSecret":"in"}`), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.APISecret != "in" {
		t.Fatalf("decoding must keep the secret, got %q", decoded.APISecret)
	}

	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("Credentials entered", "creds", c)
	if strings.Contains(buf.String(), "topsecret") {
		t.Fatalf("slog output leaked secret: %s", buf.String())
	}
}

func TestSettingsRoundTrip(t *testing.T) {
	w := Workspace{Root: t.TempDir()}
	if err := w.Ensure(); err != nil {
		t.Fatal(err)
	}

	got, err := w.LoadSettings()
	if err != nil {
		t.Fatalf("LoadSettings() on empty workspace = %v", err)
	}
	if got != (Settings{}) {
		t.Fatalf("LoadSettings() = %+v, want zero value", got)
	}

	if err := w.SaveSettings(Settings{AWSRegion: "eu-west-1"}); err != nil {
		t.Fatal(err)
	}
	got, err = w.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if got.AWSRegion != "eu-west-1" {
		t.Fatalf("AWSRegion = %q, want eu-west-1", got.AWSRegion)
	}
}

func TestSuffixIsCreatedOnceAndPersisted(t *testing.T) {
	w := Workspace{Root: t.TempDir()}
	if err := w.Ensure(); err != nil {
		t.Fatal(err)
	}

	first, err := w.Suffix()
	if err != nil {
		t.Fatal(err)
	}
	if !suffixPattern.MatchString(first) {
		t.Fatalf("Suffix() = %q, want 3 to 12 lowercase letters or digits", first)
	}
	if len(first) != suffixBytes*2 {
		t.Fatalf("Suffix() = %q, want %d characters", first, suffixBytes*2)
	}

	saved, err := w.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if saved.Suffix != first {
		t.Fatalf("settings.json suffix = %q, want %q", saved.Suffix, first)
	}

	second, err := w.Suffix()
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("Suffix() changed from %q to %q", first, second)
	}
}

func TestSuffixSurvivesRegionUpdate(t *testing.T) {
	w := Workspace{Root: t.TempDir()}
	if err := w.Ensure(); err != nil {
		t.Fatal(err)
	}
	suffix, err := w.Suffix()
	if err != nil {
		t.Fatal(err)
	}

	s, err := w.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	s.AWSRegion = "eu-west-1"
	if err := w.SaveSettings(s); err != nil {
		t.Fatal(err)
	}

	got, err := w.Suffix()
	if err != nil {
		t.Fatal(err)
	}
	if got != suffix {
		t.Fatalf("Suffix() = %q after region update, want %q", got, suffix)
	}
}

func TestSuffixKeepsHandEditedValueAndReplacesInvalid(t *testing.T) {
	tests := []struct {
		name     string
		saved    string
		wantKept bool
	}{
		{name: "hand-edited initials", saved: "bds", wantKept: true},
		{name: "uppercase", saved: "ABC123"},
		{name: "too short", saved: "ab"},
		{name: "punctuation", saved: "a_b-c1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := Workspace{Root: t.TempDir()}
			if err := w.Ensure(); err != nil {
				t.Fatal(err)
			}
			if err := w.SaveSettings(Settings{Suffix: tt.saved}); err != nil {
				t.Fatal(err)
			}
			got, err := w.Suffix()
			if err != nil {
				t.Fatal(err)
			}
			if tt.wantKept != (got == tt.saved) {
				t.Fatalf("Suffix() = %q, saved %q, kept = %v", got, tt.saved, tt.wantKept)
			}
			if !suffixPattern.MatchString(got) {
				t.Fatalf("Suffix() = %q is not a valid suffix", got)
			}
		})
	}
}

func TestNewSuffixDiffers(t *testing.T) {
	seen := map[string]bool{}
	for range 50 {
		s, err := NewSuffix()
		if err != nil {
			t.Fatal(err)
		}
		seen[s] = true
	}
	if len(seen) < 49 {
		t.Fatalf("50 suffixes gave only %d distinct values", len(seen))
	}
}
