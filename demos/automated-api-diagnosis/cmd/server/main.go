// Command server is the API diagnosis demo for Launchpad: the simulator, the health
// monitor, the AI report viewer and their web UI, in one binary. Terraform deploys it
// into ECS next to the Confluent Cloud resources it creates, and tells it about them
// in environment variables (see docs/specs/launchpad.md).
package main

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"apidiag/frontend"
	"apidiag/internal/server"
)

// ECS sends SIGTERM and kills the task after 30 seconds by default. Stopping the service
// and then the HTTP server each get this long, so together they fit.
const shutdownGrace = 10 * time.Second

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if len(os.Args) > 1 && os.Args[1] != "serve" {
		log.Error("Unknown command: use serve, or no command", "command", os.Args[1])
		os.Exit(2)
	}
	if err := run(ctx, log, os.Getenv); err != nil {
		log.Error("The demo exited with an error", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger, getenv func(string) string) error {
	cfg, err := server.LoadConfig(getenv)
	if err != nil {
		return err
	}
	log.Info("Starting", "config", cfg)
	svc, err := server.New(cfg, log, server.NewDeps(cfg))
	if err != nil {
		return err
	}
	assets, err := fs.Sub(frontend.Dist, "dist")
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           svc.Handler(assets),
		ReadHeaderTimeout: 10 * time.Second,
		// No write timeout: the event stream stays open for as long as a tab does.
		IdleTimeout: 120 * time.Second,
	}
	svc.Start(ctx)

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		svc.Close(shutdownGrace)
		return err
	case <-ctx.Done():
	}
	svc.Close(shutdownGrace) // ends the event streams, which Shutdown would otherwise wait for
	shutdown, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
