// Package connector registers the Debezium MySQL source connector with the
// local Kafka Connect worker and waits for it to start streaming.
package connector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"pims/internal/retry"
	"pims/internal/schema"
)

// NamePrefix starts every PIMS connector name.
const NamePrefix = "pims-mysql"

// Name returns the connector name for a MySQL generation.
//
// The generation changes whenever the MySQL volume is recreated. Connect
// keys source offsets by connector name, and Debezium's schema history
// lives in a Kafka topic, both in Confluent Cloud. A fresh database
// therefore needs a fresh name and history topic, otherwise Debezium
// would resume from binlog positions that no longer exist.
func Name(generation string) string { return NamePrefix + "-" + generation }

// historyTopic returns the Debezium schema history topic for a generation.
func historyTopic(generation string) string { return "_pims-schema-history-" + generation }

// TopicPrefix is Debezium's topic.prefix. A RegexRouter then strips it so
// the topics are named after the tables (patients, visits, appointments).
const TopicPrefix = "pims"

// userAgent identifies PIMS to every HTTP API it calls.
const userAgent = "pims/0.1"

// ErrConnectorFailed is returned when the connector or a task reports
// FAILED. It is not retryable: the configuration must be fixed first.
var ErrConnectorFailed = errors.New("debezium connector failed")

// Settings are the non-secret inputs to the connector configuration.
// Secrets are referenced through Kafka Connect's env config provider and
// resolved inside the Connect container.
type Settings struct {
	// Generation identifies the MySQL database instance; see Name.
	Generation string
	// KafkaBootstrap is used by Debezium's schema history producer and
	// consumer, which do not inherit the worker's connection settings.
	KafkaBootstrap string
	// MySQLHost and MySQLPort are as seen from inside the compose network.
	MySQLHost string
	MySQLPort int
}

// Config builds the Debezium MySQL connector configuration.
func Config(s Settings) (map[string]string, error) {
	var missing []string
	if s.Generation == "" {
		missing = append(missing, "generation")
	}
	if s.KafkaBootstrap == "" {
		missing = append(missing, "kafka bootstrap")
	}
	if s.MySQLHost == "" || s.MySQLPort <= 0 {
		missing = append(missing, "mysql host and port")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("connector settings are missing %s", strings.Join(missing, ", "))
	}

	jaas := `org.apache.kafka.common.security.plain.PlainLoginModule required ` +
		`username="${env:PIMS_KAFKA_API_KEY}" password="${env:PIMS_KAFKA_API_SECRET}";`

	tables := make([]string, len(schema.Tables))
	for i, t := range schema.Tables {
		tables[i] = schema.Database + "." + t
	}

	return map[string]string{
		"name":              Name(s.Generation),
		"connector.class":   "io.debezium.connector.mysql.MySqlConnector",
		"tasks.max":         "1",
		"database.hostname": s.MySQLHost,
		"database.port":     fmt.Sprint(s.MySQLPort),
		"database.user":     schema.DebeziumUser,
		"database.password": "${env:PIMS_MYSQL_DEBEZIUM_PASSWORD}",
		// Unique among replicas of this MySQL server.
		"database.server.id":    "184054",
		"topic.prefix":          TopicPrefix,
		"database.include.list": schema.Database,
		"table.include.list":    strings.Join(tables, ","),
		"snapshot.mode":         "initial",
		// Emit DATETIME/DATE as Connect logical types so Avro carries
		// timestamp-millis/date and Tableflow materialises real timestamps.
		"time.precision.mode": "connect",
		// Keep the default of skipping truncates: Tableflow does not
		// support them.
		"skipped.operations":     "t",
		"include.schema.changes": "false",

		"schema.history.internal.kafka.bootstrap.servers":    s.KafkaBootstrap,
		"schema.history.internal.kafka.topic":                historyTopic(s.Generation),
		"schema.history.internal.producer.security.protocol": "SASL_SSL",
		"schema.history.internal.producer.sasl.mechanism":    "PLAIN",
		"schema.history.internal.producer.sasl.jaas.config":  jaas,
		"schema.history.internal.consumer.security.protocol": "SASL_SSL",
		"schema.history.internal.consumer.sasl.mechanism":    "PLAIN",
		"schema.history.internal.consumer.sasl.jaas.config":  jaas,

		// pims.pims.patients -> patients
		"transforms":                   "route",
		"transforms.route.type":        "org.apache.kafka.connect.transforms.RegexRouter",
		"transforms.route.regex":       fmt.Sprintf(`%s\.%s\.(.*)`, TopicPrefix, schema.Database),
		"transforms.route.replacement": "$1",
	}, nil
}

// Client talks to the Kafka Connect REST API.
type Client struct {
	name    string
	baseURL string
	http    *http.Client
	log     *slog.Logger
	// pollInterval controls status polling; overridden in tests.
	pollInterval time.Duration
}

// NewClient returns a client that manages the connector for generation via
// the Connect REST API at baseURL, e.g. http://127.0.0.1:8083. The API is
// only published on localhost.
func NewClient(baseURL, generation string, log *slog.Logger) *Client {
	return &Client{
		name:         Name(generation),
		baseURL:      strings.TrimRight(baseURL, "/"),
		http:         &http.Client{Timeout: 15 * time.Second},
		log:          log.With("component", "connector"),
		pollInterval: 3 * time.Second,
	}
}

// Register creates or updates the connector. PUT /connectors/{name}/config
// is idempotent, so this is safe to run on every setup. Connectors left
// over from earlier MySQL generations are deleted first.
func (c *Client) Register(ctx context.Context, cfg map[string]string) error {
	body, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode connector config: %w", err)
	}
	p := retry.Default
	p.OnRetry = func(attempt int, err error, wait time.Duration) {
		c.log.Info("Waiting for Kafka Connect", "attempt", attempt, "retry_in", wait.Round(time.Millisecond), "error", err)
	}
	err = retry.Do(ctx, p, func(ctx context.Context) error {
		if err := c.deleteStale(ctx); err != nil {
			return err
		}
		_, err := c.do(ctx, http.MethodPut, c.path("/config"), body)
		return err
	})
	if err != nil {
		return fmt.Errorf("register connector %s: %w", c.name, err)
	}
	c.log.Info("Registered Debezium connector", "connector", c.name)
	return nil
}

// deleteStale removes PIMS connectors from earlier MySQL generations,
// which would otherwise keep failing against the recreated database.
func (c *Client) deleteStale(ctx context.Context) error {
	b, err := c.do(ctx, http.MethodGet, "/connectors", nil)
	if err != nil {
		return fmt.Errorf("list connectors: %w", err)
	}
	var names []string
	if err := json.Unmarshal(b, &names); err != nil {
		return retry.Permanent(fmt.Errorf("decode connector list: %w", err))
	}
	for _, n := range names {
		if n == c.name || !strings.HasPrefix(n, NamePrefix) {
			continue
		}
		c.log.Info("Deleting connector from an earlier database", "connector", n)
		if _, err := c.do(ctx, http.MethodDelete, "/connectors/"+url.PathEscape(n), nil); err != nil {
			return fmt.Errorf("delete stale connector %s: %w", n, err)
		}
	}
	return nil
}

func (c *Client) path(suffix string) string {
	return "/connectors/" + url.PathEscape(c.name) + suffix
}

// Status is the subset of GET /connectors/{name}/status that PIMS uses.
type Status struct {
	Connector struct {
		State string `json:"state"`
		Trace string `json:"trace"`
	} `json:"connector"`
	Tasks []struct {
		ID    int    `json:"id"`
		State string `json:"state"`
		Trace string `json:"trace"`
	} `json:"tasks"`
}

// Running reports whether the connector and all its tasks are RUNNING.
func (s Status) Running() bool {
	if s.Connector.State != "RUNNING" || len(s.Tasks) == 0 {
		return false
	}
	for _, t := range s.Tasks {
		if t.State != "RUNNING" {
			return false
		}
	}
	return true
}

// failure returns a description of the first FAILED component, if any.
func (s Status) failure() string {
	if s.Connector.State == "FAILED" {
		return "connector: " + firstLine(s.Connector.Trace)
	}
	for _, t := range s.Tasks {
		if t.State == "FAILED" {
			return fmt.Sprintf("task %d: %s", t.ID, firstLine(t.Trace))
		}
	}
	return ""
}

// Status fetches the connector status.
func (c *Client) Status(ctx context.Context) (Status, error) {
	var st Status
	b, err := c.do(ctx, http.MethodGet, c.path("/status"), nil)
	if err != nil {
		return st, fmt.Errorf("get connector status: %w", err)
	}
	if err := json.Unmarshal(b, &st); err != nil {
		return st, fmt.Errorf("decode connector status: %w", err)
	}
	return st, nil
}

// WaitRunning polls until the connector and its task are RUNNING, a
// component FAILS, or ctx is done.
func (c *Client) WaitRunning(ctx context.Context) error {
	t := time.NewTicker(c.pollInterval)
	defer t.Stop()
	last := ""
	for {
		st, err := c.Status(ctx)
		switch {
		case err != nil:
			c.log.Info("Waiting for connector status", "error", err)
		case st.Running():
			c.log.Info("Debezium connector is running", "connector", c.name)
			return nil
		case st.failure() != "":
			return fmt.Errorf("%w: %s", ErrConnectorFailed, st.failure())
		case st.Connector.State != last:
			last = st.Connector.State
			c.log.Info("Debezium connector starting", "state", last, "tasks", len(st.Tasks))
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for connector %s: %w", c.name, ctx.Err())
		case <-t.C:
		}
	}
}

// Delete removes the connector. A missing connector is not an error.
func (c *Client) Delete(ctx context.Context) error {
	_, err := c.do(ctx, http.MethodDelete, c.path(""), nil)
	var he *HTTPError
	if errors.As(err, &he) && he.StatusCode == http.StatusNotFound {
		return nil
	}
	if err != nil {
		return fmt.Errorf("delete connector %s: %w", c.name, err)
	}
	return nil
}

// HTTPError is a non-2xx response from Kafka Connect or Schema Registry.
type HTTPError struct {
	Method     string
	Path       string
	StatusCode int
	Body       string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("%s %s: HTTP %d: %s", e.Method, e.Path, e.StatusCode, e.Body)
}

// retryable reports whether a status code is worth retrying: server
// errors, rate limiting, and 409 (Connect returns it during rebalances).
func retryable(code int) bool {
	return code >= 500 || code == http.StatusTooManyRequests || code == http.StatusConflict
}

// do sends a request and returns the body of a 2xx response. 4xx responses
// other than 409/429 are marked permanent so retry.Do stops immediately.
func (c *Client) do(ctx context.Context, method, path string, body []byte) ([]byte, error) {
	return doJSON(ctx, c.http, method, c.baseURL, path, body, nil)
}

func doJSON(ctx context.Context, hc *http.Client, method, base, path string, body []byte, auth func(*http.Request)) ([]byte, error) {
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, r)
	if err != nil {
		return nil, retry.Permanent(fmt.Errorf("build request %s %s: %w", method, path, err))
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if auth != nil {
		auth(req)
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("read %s %s response: %w", method, path, err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		herr := &HTTPError{Method: method, Path: path, StatusCode: resp.StatusCode, Body: firstLine(string(b))}
		if retryable(resp.StatusCode) {
			return nil, herr
		}
		return nil, retry.Permanent(herr)
	}
	return b, nil
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	const max = 300
	if len(s) > max {
		s = s[:max] + "..."
	}
	return s
}
