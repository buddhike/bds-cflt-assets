// Package server runs the API diagnosis demo as a web service, for the ECS task
// Launchpad deploys: the simulator, the health monitor and the AI report viewer, with a
// browser UI over HTTP. Terraform provisions the Confluent Cloud resources and hands
// them over in environment variables.
package server

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// MinTokenLength is the shortest access token accepted.
const MinTokenLength = 24

// DefaultTopic is the topic the simulator writes the gateway logs to. The Flink
// statements in tf/flink read it by this name.
const DefaultTopic = "gateway-logs"

// Config is everything the server is told at start-up.
type Config struct {
	Listen      string
	AccessToken string
	AWSRegion   string

	// DemoName, EnvironmentID and KafkaClusterID say which Confluent Cloud resources the
	// demo runs on, for the page header. They are not secrets.
	DemoName       string
	EnvironmentID  string
	KafkaClusterID string

	KafkaBootstrap          string
	KafkaAPIKey             string
	KafkaAPISecret          string
	SchemaRegistryURL       string
	SchemaRegistryAPIKey    string
	SchemaRegistryAPISecret string

	// Topic is where the gateway logs go.
	Topic string
	// AIReports is whether Terraform deployed an AI agent that writes the reports. Without
	// one the reports tab says so, instead of waiting for reports that will not come.
	AIReports bool
	// AutoStart starts the simulation when the server starts, so the anomaly detector has a
	// baseline by the time anyone opens the page.
	AutoStart bool
}

// LogValue keeps secrets out of logs.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("listen", c.Listen),
		slog.String("aws_region", c.AWSRegion),
		slog.String("demo", c.DemoName),
		slog.String("environment", c.EnvironmentID),
		slog.String("kafka_cluster", c.KafkaClusterID),
		slog.String("bootstrap", c.KafkaBootstrap),
		slog.String("topic", c.Topic),
		slog.Bool("ai_reports", c.AIReports),
		slog.Bool("auto_start", c.AutoStart),
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
	flag := func(name string, def bool) bool {
		v := strings.TrimSpace(getenv(name))
		if v == "" {
			return def
		}
		b, err := strconv.ParseBool(v)
		if err != nil {
			problems = append(problems, name+" must be true or false")
			return def
		}
		return b
	}

	c := Config{
		Listen:                  opt("APIDIAG_LISTEN", ":8080"),
		AccessToken:             req("APIDIAG_ACCESS_TOKEN"),
		AWSRegion:               req("APIDIAG_AWS_REGION"),
		DemoName:                req("APIDIAG_DEMO_NAME"),
		EnvironmentID:           req("APIDIAG_ENVIRONMENT_ID"),
		KafkaClusterID:          req("APIDIAG_KAFKA_CLUSTER_ID"),
		KafkaBootstrap:          req("APIDIAG_KAFKA_BOOTSTRAP"),
		KafkaAPIKey:             req("APIDIAG_KAFKA_API_KEY"),
		KafkaAPISecret:          req("APIDIAG_KAFKA_API_SECRET"),
		SchemaRegistryURL:       req("APIDIAG_SCHEMA_REGISTRY_URL"),
		SchemaRegistryAPIKey:    req("APIDIAG_SCHEMA_REGISTRY_API_KEY"),
		SchemaRegistryAPISecret: req("APIDIAG_SCHEMA_REGISTRY_API_SECRET"),
		Topic:                   opt("APIDIAG_TOPIC", DefaultTopic),
		AIReports:               flag("APIDIAG_AI_REPORTS", false),
		AutoStart:               flag("APIDIAG_AUTOSTART", true),
	}
	if c.AccessToken != "" && len(c.AccessToken) < MinTokenLength {
		problems = append(problems, fmt.Sprintf("APIDIAG_ACCESS_TOKEN must be at least %d characters", MinTokenLength))
	}

	if len(problems) > 0 {
		return Config{}, errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}
	return c, nil
}

// OSEnv is LoadConfig's getenv for the real environment.
func OSEnv(name string) string { return os.Getenv(name) }
