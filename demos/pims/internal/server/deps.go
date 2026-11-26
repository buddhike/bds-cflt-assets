package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"pims/internal/compose"
	"pims/internal/config"
	"pims/internal/connector"
	"pims/internal/duckdbagent"
	"pims/internal/latency"
	"pims/internal/pipeline"
	"pims/internal/query"
	"pims/internal/retry"
	"pims/internal/schema"
	"pims/internal/sim"
	"pims/internal/tableflow"
	"pims/internal/terraform"
)

// ErrManaged is returned for anything that deletes the demo: Launchpad does that.
var ErrManaged = errors.New("this demo is managed by Launchpad: tear it down there")

// Timeouts for the waits. Connect is a JVM and takes a minute or two to start.
const (
	servicesTimeout  = 10 * time.Minute
	connectorTimeout = 5 * time.Minute
	schemasTimeout   = 20 * time.Minute
)

// Titles describe the steps as the ECS task performs them.
var Titles = map[pipeline.StepID]string{
	pipeline.StepDocker:    "Check configuration",
	pipeline.StepCloud:     "Confluent Cloud (provisioned by Launchpad)",
	pipeline.StepServices:  "Wait for MySQL, Kafka Connect and DuckDB",
	pipeline.StepTableflow: "Enable Tableflow",
}

// NewDeps wires the pipeline for the ECS task. It is the desktop wiring with
// Terraform and docker replaced by what Launchpad and the task already provide.
func NewDeps(cfg Config, log *slog.Logger) (pipeline.Deps, error) {
	agent, err := duckdbagent.NewClient(cfg.AgentURL, cfg.AgentToken)
	if err != nil {
		return pipeline.Deps{}, err
	}
	connectBase, err := loopbackURL(cfg.ConnectURL)
	if err != nil {
		return pipeline.Deps{}, fmt.Errorf("connect url: %w", err)
	}

	endpoint := cfg.TableflowAPIURL
	if endpoint == "" {
		endpoint = tableflow.DefaultEndpoint
	}
	tf, err := tableflow.New(endpoint, cfg.Outputs.TableflowAPIKey, cfg.Outputs.TableflowAPISecret,
		cfg.Outputs.EnvironmentID, cfg.Outputs.KafkaClusterID, log)
	if err != nil {
		return pipeline.Deps{}, err
	}

	generation := newGenerationSource(log)

	return pipeline.Deps{
		Titles:      Titles,
		CheckDocker: func(context.Context) error { return nil }, // LoadConfig checked everything
		Provision: func(ctx context.Context, _ config.Credentials, o terraform.ApplyOptions) (terraform.Outputs, error) {
			out := cfg.Outputs
			if !o.EnableTableflow {
				return out, nil
			}
			if err := tf.Enable(ctx, out.Topics); err != nil {
				return terraform.Outputs{}, err
			}
			out.TableflowEnabled = true
			return out, nil
		},
		Destroy: func(context.Context, config.Credentials) error { return ErrManaged },
		StartLocal: func(ctx context.Context, _ terraform.Outputs) (compose.MySQLSecrets, error) {
			ctx, cancel := context.WithTimeout(ctx, servicesTimeout)
			defer cancel()
			if err := waitForServices(ctx, cfg, agent, connectBase, log); err != nil {
				return compose.MySQLSecrets{}, err
			}
			gen, err := generation.Get(ctx)
			if err != nil {
				return compose.MySQLSecrets{}, err
			}
			return compose.MySQLSecrets{
				RootPassword:     cfg.MySQLRootPassword,
				AppPassword:      cfg.MySQLAppPassword,
				DebeziumPassword: cfg.MySQLDebeziumPassword,
				Generation:       gen,
			}, nil
		},
		StopLocal: func(context.Context, bool) error { return ErrManaged },
		PrepareDB: func(ctx context.Context, s compose.MySQLSecrets) (sim.Store, io.Closer, error) {
			return prepareDB(ctx, cfg, s, log)
		},
		StartCDC: func(ctx context.Context, out terraform.Outputs, s compose.MySQLSecrets) error {
			ctx, cancel := context.WithTimeout(ctx, connectorTimeout)
			defer cancel()
			cc, err := connector.Config(connector.Settings{
				Generation:     s.Generation,
				KafkaBootstrap: out.KafkaBootstrap,
				// Debezium runs in a sibling container of the same task.
				MySQLHost: cfg.MySQLHost,
				MySQLPort: cfg.MySQLPort,
			})
			if err != nil {
				return err
			}
			c := connector.NewClient(connectBase, s.Generation, log)
			if err := c.Register(ctx, cc); err != nil {
				return err
			}
			return c.WaitRunning(ctx)
		},
		StopCDC: func(context.Context, compose.MySQLSecrets) error { return ErrManaged },
		WaitSchemas: func(ctx context.Context, out terraform.Outputs) error {
			ctx, cancel := context.WithTimeout(ctx, schemasTimeout)
			defer cancel()
			r, err := connector.NewRegistry(out.SchemaRegistryURL, out.SchemaRegistryAPIKey, out.SchemaRegistryAPISecret, log)
			if err != nil {
				return err
			}
			return r.WaitForValueSchemas(ctx, out.Topics)
		},
		DisableTableflow: func(ctx context.Context) error { return tf.Disable(ctx, cfg.Outputs.Topics) },
		NewQuerier: func(c config.Credentials, out terraform.Outputs) (latency.Querier, error) {
			return latency.NewMetricsAPI(latency.DefaultEndpoint, out.KafkaClusterID, c.APIKey, c.APISecret, schema.Tables)
		},
		RunQuery: func(ctx context.Context, out terraform.Outputs, sql string) (query.Result, error) {
			var catalog *query.Catalog
			if out.TableflowEnabled {
				catalog = &query.Catalog{
					Endpoint:  out.TableflowCatalogEndpoint,
					APIKey:    out.TableflowAPIKey,
					APISecret: out.TableflowAPISecret,
					Namespace: out.KafkaClusterID,
				}
			}
			r, err := query.New(agent, "duckdb", catalog)
			if err != nil {
				return query.Result{}, err
			}
			return r.Run(ctx, sql)
		},
		PollInterval: latency.DefaultInterval,
	}, nil
}

// Credentials are what Setup is called with: the Cloud API key, for the Metrics API.
func (c Config) Credentials() config.Credentials {
	return config.Credentials{AWSRegion: c.AWSRegion, APIKey: c.CloudAPIKey, APISecret: c.CloudAPISecret}
}

func loopbackURL(raw string) (string, error) {
	h := strings.TrimPrefix(strings.TrimPrefix(raw, "http://"), "https://")
	host, _, err := net.SplitHostPort(strings.SplitN(h, "/", 2)[0])
	if err != nil || !strings.HasPrefix(raw, "http://") || (host != "127.0.0.1" && host != "localhost") {
		return "", fmt.Errorf("must be http://127.0.0.1:<port>, got %q", raw)
	}
	return strings.TrimRight(raw, "/"), nil
}

// waitForServices waits until the sibling containers answer.
func waitForServices(ctx context.Context, cfg Config, agent *duckdbagent.Client, connectBase string, log *slog.Logger) error {
	p := retry.Policy{Attempts: 1000, Initial: time.Second, Max: 5 * time.Second}
	wait := func(name string, check func(context.Context) error) error {
		p.OnRetry = func(attempt int, err error, wait time.Duration) {
			if attempt%5 == 1 {
				log.Info("Waiting for a service", "service", name, "attempt", attempt, "error", err)
			}
		}
		if err := retry.Do(ctx, p, check); err != nil {
			return fmt.Errorf("wait for %s: %w", name, err)
		}
		log.Info("Service is up", "service", name)
		return nil
	}
	if err := wait("MySQL", func(ctx context.Context) error {
		d := net.Dialer{Timeout: 3 * time.Second}
		c, err := d.DialContext(ctx, "tcp", net.JoinHostPort(cfg.MySQLHost, strconv.Itoa(cfg.MySQLPort)))
		if err != nil {
			return err
		}
		return c.Close()
	}); err != nil {
		return err
	}
	if err := wait("the DuckDB agent", agent.Ready); err != nil {
		return err
	}
	return wait("Kafka Connect", func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, connectBase+"/connectors", nil)
		if err != nil {
			return retry.Permanent(err)
		}
		res, err := (&http.Client{Timeout: 5 * time.Second}).Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("kafka connect answered %d", res.StatusCode)
		}
		return nil
	})
}

var generationPattern = regexp.MustCompile(`^[0-9a-f]{12}$`)

// generationSource decides the MySQL generation once and keeps it. Setup can run again,
// for example from Retry setup, and a different answer then would start a second Debezium
// connector next to the first, on a database that already has its schema.
type generationSource struct {
	mu    sync.Mutex
	value string
	log   *slog.Logger
	// lookup reads the ECS task id; replaced in tests.
	lookup func(context.Context) (string, error)
}

func newGenerationSource(log *slog.Logger) *generationSource {
	return &generationSource{log: log, lookup: func(ctx context.Context) (string, error) { return taskGeneration(ctx, os.Getenv) }}
}

// Get returns the generation. MySQL's data is on the task's ephemeral storage, so a
// database is as old as its task: the ECS task id is the generation. A server container
// restarted inside the same task then keeps the connector it already has, and a replacement
// task, which starts with an empty database, gets a connector of its own and a new schema
// history topic. Inside ECS a failed lookup is an error, never a guess. Anywhere else a
// random one is used.
func (g *generationSource) Get(ctx context.Context) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.value != "" {
		return g.value, nil
	}
	p := retry.Policy{Attempts: 5, Initial: time.Second, Max: 5 * time.Second}
	var id string
	err := retry.Do(ctx, p, func(ctx context.Context) (err error) {
		id, err = g.lookup(ctx)
		if errors.Is(err, errNoMetadata) {
			return retry.Permanent(err)
		}
		return err
	})
	switch {
	case err == nil:
		g.value = id
	case errors.Is(err, errNoMetadata):
		b := make([]byte, 6)
		_, _ = rand.Read(b)
		g.value = hex.EncodeToString(b)
	default:
		return "", fmt.Errorf("read the ECS task id: %w", err)
	}
	g.log.Info("MySQL generation chosen", "generation", g.value)
	return g.value, nil
}

var errNoMetadata = errors.New("not running in ECS")

func taskGeneration(ctx context.Context, env func(string) string) (string, error) {
	base := env("ECS_CONTAINER_METADATA_URI_V4")
	if base == "" {
		return "", errNoMetadata
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/task", nil)
	if err != nil {
		return "", err
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var task struct {
		TaskARN string `json:"TaskARN"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&task); err != nil {
		return "", err
	}
	// arn:aws:ecs:<region>:<account>:task/<cluster>/<task id>
	id := task.TaskARN[strings.LastIndex(task.TaskARN, "/")+1:]
	id = strings.ToLower(strings.ReplaceAll(id, "-", ""))
	if len(id) < 12 || !generationPattern.MatchString(id[:12]) {
		return "", fmt.Errorf("unexpected task ARN %q", task.TaskARN)
	}
	return id[:12], nil
}

// prepareDB installs the schema as root, then returns a store that connects as
// the least-privileged application user.
func prepareDB(ctx context.Context, cfg Config, s compose.MySQLSecrets, log *slog.Logger) (sim.Store, io.Closer, error) {
	root, err := schema.Open(ctx, schema.Conn{Host: cfg.MySQLHost, Port: cfg.MySQLPort, User: "root", Password: s.RootPassword}, log)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	if err := schema.Install(ctx, root, s.DebeziumPassword, log); err != nil {
		return nil, nil, err
	}
	app, err := schema.Open(ctx, schema.Conn{Host: cfg.MySQLHost, Port: cfg.MySQLPort, User: "pims", Password: s.AppPassword}, log)
	if err != nil {
		return nil, nil, fmt.Errorf("connect as application user: %w", err)
	}
	return sim.NewMySQLStore(app), app, nil
}
