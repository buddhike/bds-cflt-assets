package pipeline

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"time"

	"pims/internal/compose"
	"pims/internal/config"
	"pims/internal/connector"
	"pims/internal/latency"
	"pims/internal/schema"
	"pims/internal/sim"
	"pims/internal/terraform"
)

// Timeouts for the slower setup stages. Provisioning a cluster takes a few
// minutes; the first image build downloads several hundred megabytes.
const (
	provisionTimeout = 30 * time.Minute
	servicesTimeout  = 20 * time.Minute
	connectorTimeout = 5 * time.Minute
	schemasTimeout   = 10 * time.Minute
	pollInterval     = latency.DefaultInterval
)

// mysqlInternalHost/Port are how Kafka Connect reaches MySQL on the
// compose network.
const (
	mysqlInternalHost = "mysql"
	mysqlInternalPort = 3306
)

// NewDeps wires the real Terraform, Docker, MySQL, Kafka Connect, Schema
// Registry and Metrics API implementations.
func NewDeps(ws config.Workspace, log *slog.Logger) Deps {
	tf := terraform.NewRunner(ws, log)
	project := compose.NewProject(ws, log)
	connectURL := "http://127.0.0.1:" + strconv.Itoa(compose.ConnectHostPort)

	return Deps{
		CheckDocker: project.CheckDocker,
		Provision: func(ctx context.Context, c config.Credentials, o terraform.ApplyOptions) (terraform.Outputs, error) {
			ctx, cancel := context.WithTimeout(ctx, provisionTimeout)
			defer cancel()
			return tf.Apply(ctx, c, o)
		},
		Destroy: func(ctx context.Context, c config.Credentials) error {
			ctx, cancel := context.WithTimeout(ctx, provisionTimeout)
			defer cancel()
			return tf.Destroy(ctx, c)
		},
		StartLocal: func(ctx context.Context, out terraform.Outputs) (compose.MySQLSecrets, error) {
			ctx, cancel := context.WithTimeout(ctx, servicesTimeout)
			defer cancel()
			err := project.Up(ctx, compose.KafkaEnv{
				Bootstrap:               out.KafkaBootstrap,
				APIKey:                  out.KafkaAPIKey,
				APISecret:               out.KafkaAPISecret,
				SchemaRegistryURL:       out.SchemaRegistryURL,
				SchemaRegistryAPIKey:    out.SchemaRegistryAPIKey,
				SchemaRegistryAPISecret: out.SchemaRegistryAPISecret,
			})
			if err != nil {
				return compose.MySQLSecrets{}, err
			}
			return project.MySQLSecrets()
		},
		StopLocal: func(ctx context.Context, removeVolumes bool) error {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
			defer cancel()
			return project.Down(ctx, removeVolumes)
		},
		PrepareDB: func(ctx context.Context, s compose.MySQLSecrets) (sim.Store, io.Closer, error) {
			return prepareDB(ctx, s, log)
		},
		StartCDC: func(ctx context.Context, out terraform.Outputs, s compose.MySQLSecrets) error {
			ctx, cancel := context.WithTimeout(ctx, connectorTimeout)
			defer cancel()
			cfg, err := connector.Config(connector.Settings{
				Generation:     s.Generation,
				KafkaBootstrap: out.KafkaBootstrap,
				MySQLHost:      mysqlInternalHost,
				MySQLPort:      mysqlInternalPort,
			})
			if err != nil {
				return err
			}
			c := connector.NewClient(connectURL, s.Generation, log)
			if err := c.Register(ctx, cfg); err != nil {
				return err
			}
			return c.WaitRunning(ctx)
		},
		StopCDC: func(ctx context.Context, s compose.MySQLSecrets) error {
			ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			defer cancel()
			return connector.NewClient(connectURL, s.Generation, log).Delete(ctx)
		},
		WaitSchemas: func(ctx context.Context, out terraform.Outputs) error {
			ctx, cancel := context.WithTimeout(ctx, schemasTimeout)
			defer cancel()
			r, err := connector.NewRegistry(out.SchemaRegistryURL, out.SchemaRegistryAPIKey, out.SchemaRegistryAPISecret, log)
			if err != nil {
				return err
			}
			return r.WaitForValueSchemas(ctx, out.Topics)
		},
		NewQuerier: func(c config.Credentials, out terraform.Outputs) (latency.Querier, error) {
			return latency.NewMetricsAPI(latency.DefaultEndpoint, out.KafkaClusterID, c.APIKey, c.APISecret, schema.Tables)
		},
		PollInterval: pollInterval,
	}
}

// prepareDB installs the schema as root, then returns a store that
// connects as the least-privileged application user.
func prepareDB(ctx context.Context, s compose.MySQLSecrets, log *slog.Logger) (sim.Store, io.Closer, error) {
	root, err := schema.Open(ctx, schema.Conn{Host: "127.0.0.1", Port: compose.MySQLHostPort, User: "root", Password: s.RootPassword}, log)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	if err := schema.Install(ctx, root, s.DebeziumPassword, log); err != nil {
		return nil, nil, err
	}

	app, err := schema.Open(ctx, schema.Conn{Host: "127.0.0.1", Port: compose.MySQLHostPort, User: "pims", Password: s.AppPassword}, log)
	if err != nil {
		return nil, nil, fmt.Errorf("connect as application user: %w", err)
	}
	return sim.NewMySQLStore(app), app, nil
}
