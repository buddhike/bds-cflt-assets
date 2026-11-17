package main

import (
	"errors"
	"log/slog"
	"slices"
	"testing"

	"pims/internal/config"
)

func TestRingKeepsMostRecent(t *testing.T) {
	r := newRing[int](3)
	for i := range 5 {
		r.push(i)
	}
	if got := r.items(); !slices.Equal(got, []int{2, 3, 4}) {
		t.Fatalf("items() = %v, want [2 3 4]", got)
	}
	empty := newRing[int](2)
	if len(empty.items()) != 0 {
		t.Fatal("new ring should be empty")
	}
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	a := NewApp(config.Workspace{Root: t.TempDir()})
	a.attachLogger(slog.New(slog.DiscardHandler))
	return a
}

func TestSetCredentialsKeepsSecretInMemoryOnly(t *testing.T) {
	a := newTestApp(t)
	if err := a.SetCredentials(config.Credentials{AWSRegion: " EU-WEST-1 ", APIKey: "key", APISecret: "secret"}); err != nil {
		t.Fatal(err)
	}
	st := a.GetState()
	if !st.HasCredentials || st.AWSRegion != "eu-west-1" {
		t.Fatalf("state = %+v", st)
	}
	saved, err := a.ws.LoadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if saved.AWSRegion != "eu-west-1" {
		t.Fatalf("saved region = %q", saved.AWSRegion)
	}
	if got := a.GetDefaults().AWSRegion; got != "eu-west-1" {
		t.Fatalf("defaults region = %q", got)
	}
}

func TestSetupRequiresCredentials(t *testing.T) {
	a := newTestApp(t)
	if err := a.Setup(); !errors.Is(err, ErrNoCredentials) {
		t.Fatalf("Setup() = %v, want ErrNoCredentials", err)
	}
	if err := a.SetCredentials(config.Credentials{AWSRegion: "nope"}); !errors.Is(err, config.ErrInvalidCredentials) {
		t.Fatalf("SetCredentials(bad) = %v, want ErrInvalidCredentials", err)
	}
}
