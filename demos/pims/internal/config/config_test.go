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
