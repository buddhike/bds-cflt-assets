// Package config reads the environment Terraform gives each ECS task. One image runs
// three roles, and each role is given (and requires) only what it uses, so a worker never
// holds a credential it does not need.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"strings"

	"lightninginv/internal/catalog"
)

// Roles of the one binary.
const (
	RoleControl  = "control"
	RoleProducer = "producer"
	RoleQuery    = "query"
)

// MinTokenLength is the shortest access or worker token accepted.
const MinTokenLength = 24

// DefaultTopic is the topic the demo writes to. Lightning Table topic names allow only
// letters, digits and underscores. tf/confluent.tf names the same topic.
const DefaultTopic = "inventory_positions"

// MaxMissRatio bounds the share of queries on keys that do not exist.
const MaxMissRatio = 0.2

// Config is everything a task is told at start-up.
type Config struct {
	Role   string
	Listen string

	AWSRegion string
	DemoName  string

	EnvironmentID  string
	KafkaClusterID string
	Topic          string

	KafkaBootstrap  string
	ValueSchemaID   int
	KafkaAPIKey     string
	KafkaAPISecret  string
	LocationsPerSKU int
	MissRatio       float64

	MetricsNamespace string

	ControlURL  string
	AccessToken string
	WorkerToken string

	// GlobalAPIKey is the query tasks' key. It is read-only (DeveloperRead).
	GlobalAPIKey    string
	GlobalAPISecret string

	// CloudAPIKey is the control task's key: Confluent's usage metrics and the table's
	// status, read-only.
	CloudAPIKey    string
	CloudAPISecret string
}

// LogValue keeps secrets out of logs.
func (c Config) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("role", c.Role),
		slog.String("aws_region", c.AWSRegion),
		slog.String("demo", c.DemoName),
		slog.String("environment", c.EnvironmentID),
		slog.String("kafka_cluster", c.KafkaClusterID),
		slog.String("topic", c.Topic),
		slog.String("metrics_namespace", c.MetricsNamespace),
		slog.Int("locations_per_sku", c.LocationsPerSKU),
		slog.Float64("miss_ratio", c.MissRatio),
	)
}

// String redacts secrets so a Config can never leak through %v.
func (c Config) String() string { return fmt.Sprintf("config.Config{%v}", c.LogValue()) }

// GoString redacts secrets for %#v.
func (c Config) GoString() string { return c.String() }

// Load reads the configuration for the role in LIGHTNING_ROLE and reports every problem
// at once, so a task that cannot start says everything that is wrong.
func Load(getenv func(string) string) (Config, error) {
	var problems []string
	role := strings.TrimSpace(getenv("LIGHTNING_ROLE"))
	switch role {
	case RoleControl, RoleProducer, RoleQuery:
	default:
		return Config{}, errors.New("invalid configuration: LIGHTNING_ROLE is not set to control, producer or query")
	}
	is := func(roles ...string) bool {
		for _, r := range roles {
			if r == role {
				return true
			}
		}
		return false
	}
	// req reads a variable the role needs; others are not read at all.
	req := func(name string, roles ...string) string {
		if !is(roles...) {
			return ""
		}
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
	intv := func(name string, def int, roles ...string) int {
		if !is(roles...) {
			return 0
		}
		v := strings.TrimSpace(getenv(name))
		if v == "" {
			return def
		}
		n, err := strconv.Atoi(v)
		if err != nil {
			problems = append(problems, name+" must be a whole number")
			return def
		}
		return n
	}
	both := []string{RoleProducer, RoleQuery}
	all := []string{RoleControl, RoleProducer, RoleQuery}
	ctlQ := []string{RoleControl, RoleQuery}

	c := Config{
		Role:      role,
		AWSRegion: req("LIGHTNING_AWS_REGION", all...),
		DemoName:  req("LIGHTNING_DEMO_NAME", all...),
		Topic:     req("LIGHTNING_TOPIC", all...),
		Listen:    opt("LIGHTNING_LISTEN", ":8080"),

		EnvironmentID:  req("LIGHTNING_ENVIRONMENT_ID", ctlQ...),
		KafkaClusterID: req("LIGHTNING_KAFKA_CLUSTER_ID", ctlQ...),

		KafkaBootstrap:  req("LIGHTNING_KAFKA_BOOTSTRAP", RoleProducer),
		KafkaAPIKey:     req("LIGHTNING_KAFKA_API_KEY", RoleProducer),
		KafkaAPISecret:  req("LIGHTNING_KAFKA_API_SECRET", RoleProducer),
		ValueSchemaID:   0,
		LocationsPerSKU: intv("LIGHTNING_LOCATIONS_PER_SKU", catalog.DefaultLocationsPerSKU, both...),

		MetricsNamespace: req("LIGHTNING_METRICS_NAMESPACE", ctlQ...),

		ControlURL:  req("LIGHTNING_CONTROL_URL", both...),
		AccessToken: req("LIGHTNING_ACCESS_TOKEN", RoleControl),
		WorkerToken: req("LIGHTNING_WORKER_TOKEN", all...),

		GlobalAPIKey:    req("LIGHTNING_GLOBAL_API_KEY", RoleQuery),
		GlobalAPISecret: req("LIGHTNING_GLOBAL_API_SECRET", RoleQuery),

		CloudAPIKey:    req("LIGHTNING_CLOUD_API_KEY", RoleControl),
		CloudAPISecret: req("LIGHTNING_CLOUD_API_SECRET", RoleControl),
	}
	if is(RoleProducer) {
		c.ValueSchemaID = intv("LIGHTNING_VALUE_SCHEMA_ID", 0, RoleProducer)
		if c.ValueSchemaID <= 0 {
			problems = append(problems, "LIGHTNING_VALUE_SCHEMA_ID is not set")
		}
	}
	if is(RoleQuery) {
		c.MissRatio = 0.02
		if v := strings.TrimSpace(getenv("LIGHTNING_MISS_RATIO")); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f < 0 || f > MaxMissRatio {
				problems = append(problems, fmt.Sprintf("LIGHTNING_MISS_RATIO must be a number from 0 to %v", MaxMissRatio))
			} else {
				c.MissRatio = f
			}
		}
	}
	if is(both...) {
		if err := checkControlURL(c.ControlURL); err != nil {
			problems = append(problems, "LIGHTNING_CONTROL_URL: "+err.Error())
		}
		if _, err := catalog.New(c.LocationsPerSKU); err != nil {
			problems = append(problems, "LIGHTNING_LOCATIONS_PER_SKU: "+err.Error())
		}
	}
	for name, tok := range map[string]string{"LIGHTNING_ACCESS_TOKEN": c.AccessToken, "LIGHTNING_WORKER_TOKEN": c.WorkerToken} {
		if tok != "" && len(tok) < MinTokenLength {
			problems = append(problems, fmt.Sprintf("%s must be at least %d characters", name, MinTokenLength))
		}
	}
	if len(problems) > 0 {
		return Config{}, errors.New("invalid configuration: " + strings.Join(problems, "; "))
	}
	return c, nil
}

// checkControlURL accepts only an http or https address with a host and nothing else of a
// URL: the workers send their token to it.
func checkControlURL(raw string) error {
	if raw == "" {
		return nil // already reported as not set
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("is not a URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("must start with http:// or https://")
	}
	if u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("must be a scheme and a host only")
	}
	return nil
}

// OSEnv is Load's getenv for the real environment.
func OSEnv(name string) string { return os.Getenv(name) }
