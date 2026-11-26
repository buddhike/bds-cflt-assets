package server

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"pims/internal/compose"
	"pims/internal/pipeline"
	"pims/internal/terraform"
)

// shutdownRig is a service whose Tableflow shutdown is counted and can be made to fail.
func shutdownRig(t *testing.T, disable func(context.Context) error) (*rig, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	cfg := testConfig(t)
	svc, err := New(cfg, slog.NewTextHandler(io.Discard, nil), func(*slog.Logger) (pipeline.Deps, error) {
		d := fakeDeps(nil)
		d.DisableTableflow = func(ctx context.Context) error { calls.Add(1); return disable(ctx) }
		return d, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(svc.Handler(assets))
	t.Cleanup(func() { srv.Close(); svc.Close(5 * time.Second) })
	return &rig{svc: svc, srv: srv, tok: cfg.AccessToken}, &calls
}

func bearer(tok string) map[string]string { return map[string]string{"Authorization": "Bearer " + tok} }

func TestShutdownNeedsTheAccessTokenAsABearerToken(t *testing.T) {
	r, calls := shutdownRig(t, func(context.Context) error { return nil })
	c := r.client()
	for name, hdr := range map[string]map[string]string{
		"none":         nil,
		"wrong":        bearer("wrong-token-wrong-token-xx"),
		"not a bearer": {"Authorization": r.tok},
		"as a query":   nil,
	} {
		url := r.srv.URL + "/shutdown"
		if name == "as a query" {
			url += "?" + AccessParam + "=" + r.tok
		}
		res, _ := do(t, c, "POST", url, "", hdr)
		if res.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status %d, want 401", name, res.StatusCode)
		}
	}
	if calls.Load() != 0 {
		t.Errorf("Tableflow was disabled %d times without a valid token", calls.Load())
	}
}

func TestShutdownCookieAloneIsNotEnough(t *testing.T) {
	// A browser session must not be able to tear the demo down by a forged request.
	r, calls := shutdownRig(t, func(context.Context) error { return nil })
	c := r.signedIn(t)
	res, _ := do(t, c, "POST", r.srv.URL+"/shutdown", "", post)
	if res.StatusCode != http.StatusUnauthorized || calls.Load() != 0 {
		t.Errorf("status %d, disabled %d times", res.StatusCode, calls.Load())
	}
}

func TestShutdownStopsTheDemoAndDisablesTableflow(t *testing.T) {
	r, calls := shutdownRig(t, func(context.Context) error { return nil })
	r.svc.Start(context.Background())
	res, body := do(t, r.client(), "POST", r.srv.URL+"/shutdown", "", bearer(r.tok))
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", res.StatusCode, body)
	}
	if calls.Load() != 1 {
		t.Errorf("Tableflow disabled %d times, want 1", calls.Load())
	}
	// Once shut down, the demo does not start itself again.
	if err := r.svc.Setup(); !errors.Is(err, pipeline.ErrClosed) {
		t.Errorf("Setup after shutdown: %v, want ErrClosed", err)
	}
}

func TestShutdownCanBeRepeatedAfterItFails(t *testing.T) {
	var fail atomic.Bool
	fail.Store(true)
	r, calls := shutdownRig(t, func(context.Context) error {
		if fail.Load() {
			return errors.New("the Confluent Cloud API is down")
		}
		return nil
	})
	res, body := do(t, r.client(), "POST", r.srv.URL+"/shutdown", "", bearer(r.tok))
	if res.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status %d: %s", res.StatusCode, body)
	}
	fail.Store(false)
	res, body = do(t, r.client(), "POST", r.srv.URL+"/shutdown", "", bearer(r.tok))
	if res.StatusCode != http.StatusOK || calls.Load() != 2 {
		t.Fatalf("retry: status %d (%s), %d calls", res.StatusCode, body, calls.Load())
	}
}

func TestShutdownLeavesTheTopicsAloneIfTheSetupWillNotStop(t *testing.T) {
	old := shutdownWait
	shutdownWait = 30 * time.Millisecond
	t.Cleanup(func() { shutdownWait = old })

	release := make(chan struct{})
	var calls atomic.Int32
	cfg := testConfig(t)
	svc, err := New(cfg, slog.NewTextHandler(io.Discard, nil), func(*slog.Logger) (pipeline.Deps, error) {
		d := fakeDeps(nil)
		// A setup that ignores its context, as a hung call might.
		d.StartLocal = func(context.Context, terraform.Outputs) (compose.MySQLSecrets, error) {
			<-release
			return compose.MySQLSecrets{Generation: "abc123abc123"}, nil
		}
		d.DisableTableflow = func(context.Context) error { calls.Add(1); return nil }
		return d, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.Start(context.Background())
	t.Cleanup(func() { close(release); svc.Close(5 * time.Second) })

	if err := svc.Shutdown(context.Background()); err == nil {
		t.Fatal("Shutdown succeeded while the setup was still running")
	}
	if calls.Load() != 0 {
		t.Error("Tableflow was disabled while a setup could still enable it")
	}
}
