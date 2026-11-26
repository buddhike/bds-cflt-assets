// Command server is PIMS for Launchpad. It is one binary with two jobs, chosen by
// its first argument:
//
//	server               the simulator, the web UI and its API (the default)
//	server duckdb-agent  the DuckDB container's entry point: lets the server run
//	                     queries in that container
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"pims/frontend"
	"pims/internal/duckdbagent"
	"pims/internal/pipeline"
	"pims/internal/server"
)

// ECS sends SIGTERM and kills the task after 30 seconds by default. Stopping the service
// and then the HTTP server each get this long, so together they fit.
const shutdownGrace = 10 * time.Second

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var err error
	switch {
	case len(os.Args) > 1 && os.Args[1] == "duckdb-agent":
		err = runAgent(ctx, log, os.Getenv)
	case len(os.Args) == 1 || os.Args[1] == "serve":
		err = runServer(ctx, log, os.Getenv)
	default:
		err = fmt.Errorf("unknown command %q: use serve or duckdb-agent", os.Args[1])
	}
	if err != nil {
		log.Error("PIMS exited with an error", "error", err)
		os.Exit(1)
	}
}

func runServer(ctx context.Context, log *slog.Logger, getenv func(string) string) error {
	cfg, err := server.LoadConfig(getenv)
	if err != nil {
		return err
	}
	svc, err := server.New(cfg, log.Handler(), func(l *slog.Logger) (pipeline.Deps, error) {
		return server.NewDeps(cfg, l)
	})
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
	return serve(ctx, srv, func() { svc.Close(shutdownGrace) })
}

func runAgent(ctx context.Context, log *slog.Logger, getenv func(string) string) error {
	addr := getenv("PIMS_AGENT_LISTEN")
	if addr == "" {
		addr = "127.0.0.1:8081"
	}
	if err := requireLoopback(addr); err != nil {
		return err
	}
	token := getenv("PIMS_AGENT_TOKEN")
	if len(token) < 16 {
		// Without one the agent would refuse every request, and the server would still report it as up.
		return errors.New("PIMS_AGENT_TOKEN must be set to at least 16 characters")
	}
	srv := &http.Server{
		Addr:              addr,
		Handler:           duckdbagent.Handler(token, log),
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Info("DuckDB agent listening", "addr", addr)
	return serve(ctx, srv, nil)
}

// requireLoopback refuses an address other containers, or the network, could reach.
func requireLoopback(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("PIMS_AGENT_LISTEN %q: %w", addr, err)
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return fmt.Errorf("PIMS_AGENT_LISTEN %q must be a loopback address: the agent runs commands", addr)
	}
	return nil
}

// serve runs srv until ctx ends, then shuts it down.
func serve(ctx context.Context, srv *http.Server, onStop func()) error {
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		if onStop != nil {
			onStop()
		}
		return err
	case <-ctx.Done():
	}
	shutdown, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if onStop != nil {
		onStop() // ends the event streams, which Shutdown would otherwise wait for
	}
	if err := srv.Shutdown(shutdown); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
