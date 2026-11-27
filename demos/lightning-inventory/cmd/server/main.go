// Command server is the Lightning inventory demo: one binary that runs as the control
// service (UI, API, the read-only view of the Lightning Table), as a producer task or as a
// query task, by
// LIGHTNING_ROLE. Terraform deploys the three as ECS services and tells each what it needs
// in environment variables (see docs/specs/launchpad.md).
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

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"

	"lightninginv/frontend"
	"lightninginv/internal/catalog"
	"lightninginv/internal/config"
	"lightninginv/internal/confluentmetrics"
	"lightninginv/internal/cw"
	"lightninginv/internal/lightning"
	"lightninginv/internal/server"
	"lightninginv/internal/worker"
)

// ECS sends SIGTERM and kills the task after 30 seconds by default.
const shutdownGrace = 10 * time.Second

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, log, os.Getenv); err != nil {
		log.Error("The task exited with an error", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger, getenv func(string) string) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	log.Info("Starting", "config", cfg)
	switch cfg.Role {
	case config.RoleControl:
		return runControl(ctx, cfg)
	case config.RoleProducer:
		return runProducer(ctx, cfg)
	default:
		return runQuery(ctx, cfg)
	}
}

// seedFor gives every task a different random stream, so two tasks do not send the same
// events or ask the same questions.
func seedFor(c *worker.Control) uint64 {
	var s uint64
	for _, b := range []byte(c.ID) {
		s = s*131 + uint64(b)
	}
	return s ^ uint64(time.Now().UnixNano())
}

func runControl(ctx context.Context, cfg config.Config) error {
	if !lightning.ValidTopic(cfg.Topic) {
		return fmt.Errorf("the topic %q is not a valid Lightning Table topic name", cfg.Topic)
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
	if err != nil {
		return fmt.Errorf("could not load the AWS configuration: %w", err)
	}
	hc := &http.Client{Timeout: 30 * time.Second}
	svc := server.New(server.Options{
		DemoName: cfg.DemoName, Region: cfg.AWSRegion, Topic: cfg.Topic, Namespace: cfg.MetricsNamespace,
		AccessToken: cfg.AccessToken, WorkerToken: cfg.WorkerToken,
		Tables:  lightning.NewTables(cfg.EnvironmentID, cfg.KafkaClusterID, cfg.Topic, lightning.Auth{Key: cfg.StatusAPIKey, Secret: cfg.StatusAPISecret}, hc),
		Latency: &cw.Reader{API: cloudwatch.NewFromConfig(awsCfg), Namespace: cfg.MetricsNamespace},
		Usage:   confluentmetrics.New(cfg.CloudAPIKey, cfg.CloudAPISecret, cfg.KafkaClusterID, cfg.Topic, hc),
	})
	assets, err := fs.Sub(frontend.Dist, "dist")
	if err != nil {
		return err
	}
	bg, cancelBG := context.WithCancel(ctx)
	defer cancelBG()
	go svc.Run(bg)

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           svc.Handler(assets),
		ReadHeaderTimeout: 10 * time.Second,
		// CloudFront waits 30 s for an origin by default; the slowest answer is a chart read
		// (10 s).
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	// SIGTERM: stop taking requests and let the ones in flight finish. ECS kills the task
	// after its stop timeout (30 s by default), so the grace is shorter. Nothing is left to
	// undo: Terraform owns everything the demo made.
	cancelBG()
	sctx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()
	if err := srv.Shutdown(sctx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func runProducer(ctx context.Context, cfg config.Config) error {
	cat, err := catalog.New(cfg.LocationsPerSKU)
	if err != nil {
		return err
	}
	codec, err := worker.NewCodec(cfg.ValueSchemaID)
	if err != nil {
		return err
	}
	sender, err := worker.NewKafkaSender(cfg.KafkaBootstrap, cfg.KafkaAPIKey, cfg.KafkaAPISecret, cfg.Topic)
	if err != nil {
		return err
	}
	defer sender.Close()
	ctl := worker.NewControl(cfg.ControlURL, cfg.WorkerToken)
	p := &worker.Producer{Ctl: ctl, Sender: sender, Codec: codec, Catalog: cat, Seed: seedFor(ctl)}
	p.Run(ctx)
	return nil
}

func runQuery(ctx context.Context, cfg config.Config) error {
	cat, err := catalog.New(cfg.LocationsPerSKU)
	if err != nil {
		return err
	}
	if !lightning.ValidTopic(cfg.Topic) {
		return fmt.Errorf("the topic %q is not a valid Lightning Table topic name", cfg.Topic)
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
	if err != nil {
		return fmt.Errorf("could not load the AWS configuration: %w", err)
	}
	// Many requests in flight to one host: keep enough connections (HTTP/2 shares them).
	tr := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        worker.MaxInFlight,
		MaxIdleConnsPerHost: worker.MaxInFlight,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	qc := lightning.NewQuerier(cfg.AWSRegion, cfg.EnvironmentID, cfg.KafkaClusterID,
		lightning.Auth{Key: cfg.GlobalAPIKey, Secret: cfg.GlobalAPISecret}, &http.Client{Transport: tr})
	ctl := worker.NewControl(cfg.ControlURL, cfg.WorkerToken)
	q := &worker.Querier{
		Ctl: ctl, Query: qc.Query, Rec: cw.NewRecorder(),
		Pub:   &cw.Publisher{API: cloudwatch.NewFromConfig(awsCfg), Namespace: cfg.MetricsNamespace},
		Topic: cfg.Topic, Catalog: cat, MissRatio: cfg.MissRatio, Seed: seedFor(ctl),
	}
	q.Run(ctx)
	return nil
}
