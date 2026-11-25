// Package server runs PIMS as a web service, for the ECS task Launchpad deploys:
// the same setup, simulator and charts as the desktop app, with a browser as the
// window. Terraform provisions what the desktop app provisions itself, and hands
// the result over in environment variables.
package server

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strconv"
	"strings"

	"pims/internal/schema"
	"pims/internal/terraform"
)

// MinTokenLength is the shortest access token accepted.
const MinTokenLength = 24

// Config is everything the server is told at start-up.
type Config struct {
	Listen      string
	AccessToken string
	AWSRegion   string
	// CloudAPIKey and CloudAPISecret are the launching user's Confluent Cloud API key,
	// which the Metrics API needs. The demo's own service account may not call it.
	CloudAPIKey    string
	CloudAPISecret string
	// Outputs is what Terraform provisioned, in the shape the desktop app reads from its own state.
	Outputs terraform.Outputs

	MySQLHost             string
	MySQLPort             int
	MySQLRootPassword     string
	MySQLAppPassword      string
	MySQLDebeziumPassword string

	ConnectURL     string
	AgentURL       string
	AgentToken     string
	TableflowParam string
}

// LogValue keeps secrets out of logs.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("listen", c.Listen),
		slog.String("aws_region", c.AWSRegion),
		slog.Attr{Key: "outputs", Value: c.Outputs.LogValue()}, // resolved here: a lazily resolved value prints its fields raw
		slog.String("mysql", net.JoinHostPort(c.MySQLHost, strconv.Itoa(c.MySQLPort))),
		slog.String("connect", c.ConnectURL),
		slog.String("agent", c.AgentURL),
		slog.String("tableflow_param", c.TableflowParam),
	)
}

// String redacts secrets so a Config can never leak through %v.
func (c Config) String() string { return fmt.Sprintf("server.Config{%v}", c.LogValue()) }

// GoString redacts secrets for %#v.
func (c Config) GoString() string { return c.String() }

// LoadConfig reads the configuration from the environment and reports every
// problem at once, so a task that cannot start says everything that is wrong.
func LoadConfig(getenv func(string) string) (Config, error) {
	var problems []string
	req := func(name string) string {
		v := strings.TrimSpace(getenv(name))
		if v == "" {
			problems = append(problems, name+" is not set")
		}
		return v
	}
	opt := func(name, def string) string {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			return v
		}
		return def
	}

	c := Config{
		Listen:                opt("PIMS_LISTEN", ":8080"),
		AccessToken:           req("PIMS_ACCESS_TOKEN"),
		AWSRegion:             req("PIMS_AWS_REGION"),
		CloudAPIKey:           req("PIMS_CC_API_KEY"),
		CloudAPISecret:        req("PIMS_CC_API_SECRET"),
		MySQLRootPassword:     req("PIMS_MYSQL_ROOT_PASSWORD"),
		MySQLAppPassword:      req("PIMS_MYSQL_APP_PASSWORD"),
		MySQLDebeziumPassword: req("PIMS_MYSQL_DEBEZIUM_PASSWORD"),
		ConnectURL:            opt("PIMS_CONNECT_URL", "http://127.0.0.1:8083"),
		AgentURL:              opt("PIMS_DUCKDB_AGENT_URL", "http://127.0.0.1:8081"),
		AgentToken:            req("PIMS_AGENT_TOKEN"),
		TableflowParam:        req("PIMS_TABLEFLOW_PARAM"),
	}
	if c.AccessToken != "" && len(c.AccessToken) < MinTokenLength {
		problems = append(problems, fmt.Sprintf("PIMS_ACCESS_TOKEN must be at least %d characters", MinTokenLength))
	}

	addr := opt("PIMS_MYSQL_ADDR", "127.0.0.1:3306")
	host, port, err := net.SplitHostPort(addr)
	if p, perr := strconv.Atoi(port); err != nil || perr != nil || p <= 0 || p > 65535 {
		problems = append(problems, fmt.Sprintf("PIMS_MYSQL_ADDR %q is not host:port", addr))
	} else {
		c.MySQLHost, c.MySQLPort = host, p
	}

	topics := schema.Tables
	if v := strings.TrimSpace(getenv("PIMS_TOPICS")); v != "" {
		topics = nil
		for _, t := range strings.Split(v, ",") {
			if t = strings.TrimSpace(t); t != "" {
				topics = append(topics, t)
			}
		}
	}
	c.Outputs = terraform.Outputs{
		AWSRegion:                c.AWSRegion,
		OrganizationID:           req("PIMS_ORGANIZATION_ID"),
		EnvironmentID:            req("PIMS_ENVIRONMENT_ID"),
		KafkaClusterID:           req("PIMS_KAFKA_CLUSTER_ID"),
		KafkaBootstrap:           req("PIMS_KAFKA_BOOTSTRAP"),
		KafkaAPIKey:              req("PIMS_KAFKA_API_KEY"),
		KafkaAPISecret:           req("PIMS_KAFKA_API_SECRET"),
		SchemaRegistryURL:        req("PIMS_SCHEMA_REGISTRY_URL"),
		SchemaRegistryAPIKey:     req("PIMS_SCHEMA_REGISTRY_API_KEY"),
		SchemaRegistryAPISecret:  req("PIMS_SCHEMA_REGISTRY_API_SECRET"),
		TableflowAPIKey:          req("PIMS_TABLEFLOW_API_KEY"),
		TableflowAPISecret:       req("PIMS_TABLEFLOW_API_SECRET"),
		TableflowCatalogEndpoint: req("PIMS_TABLEFLOW_CATALOG_ENDPOINT"),
		Topics:                   topics,
	}

	if len(problems) > 0 {
		return Config{}, errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}
	return c, nil
}

// OSEnv is LoadConfig's getenv for the real environment.
func OSEnv(name string) string { return os.Getenv(name) }
