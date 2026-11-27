package config

import (
	"fmt"
	"strings"
	"testing"
)

const tok = "0123456789012345678901234567890123"

func env(role string, extra map[string]string) func(string) string {
	base := map[string]string{
		"LIGHTNING_ROLE": role, "LIGHTNING_AWS_REGION": "us-east-1", "LIGHTNING_DEMO_NAME": "d-1",
		"LIGHTNING_TOPIC": "inventory_positions", "LIGHTNING_WORKER_TOKEN": tok,
		"LIGHTNING_ENVIRONMENT_ID": "env-1", "LIGHTNING_KAFKA_CLUSTER_ID": "lkc-1",
		"LIGHTNING_METRICS_NAMESPACE": "LightningInventory/d-1",
		"LIGHTNING_GLOBAL_API_KEY":    "k", "LIGHTNING_GLOBAL_API_SECRET": "s",
		"LIGHTNING_ACCESS_TOKEN": tok, "LIGHTNING_STATUS_API_KEY": "st", "LIGHTNING_STATUS_API_SECRET": "sts", "LIGHTNING_CLOUD_API_KEY": "m", "LIGHTNING_CLOUD_API_SECRET": "ms",
		"LIGHTNING_KAFKA_BOOTSTRAP": "b:9092", "LIGHTNING_KAFKA_API_KEY": "k", "LIGHTNING_KAFKA_API_SECRET": "s",
		"LIGHTNING_VALUE_SCHEMA_ID": "100001", "LIGHTNING_CONTROL_URL": "http://alb",
	}
	for k, v := range extra {
		base[k] = v
	}
	return func(n string) string { return base[n] }
}

func TestEachRole(t *testing.T) {
	for _, r := range []string{RoleControl, RoleProducer, RoleQuery} {
		c, err := Load(env(r, nil))
		if err != nil {
			t.Fatalf("%s: %v", r, err)
		}
		if c.Role != r {
			t.Fatal("role")
		}
	}
}

func TestRoleDoesNotReadOthersSecrets(t *testing.T) {
	c, _ := Load(env(RoleProducer, nil))
	if c.GlobalAPIKey != "" || c.StatusAPIKey != "" || c.StatusAPISecret != "" || c.CloudAPIKey != "" || c.AccessToken != "" {
		t.Fatal("a producer must not hold the Global key, the status key, the Cloud key or the access token")
	}
	c, _ = Load(env(RoleQuery, nil))
	if c.KafkaAPIKey != "" || c.AccessToken != "" || c.StatusAPIKey != "" || c.StatusAPISecret != "" || c.CloudAPIKey != "" {
		t.Fatal("a query worker must not hold the Kafka key, the status key, the Cloud key or the access token")
	}
	c, _ = Load(env(RoleControl, nil))
	if c.GlobalAPIKey != "" || c.KafkaAPIKey != "" || c.StatusAPIKey == "" || c.StatusAPISecret == "" || c.CloudAPIKey == "" {
		t.Fatal("the control task holds the status key (CloudClusterAdmin; the app only GETs) and the read-only Cloud key, not the query Global or Kafka key")
	}
}

func TestProblemsAreAllReported(t *testing.T) {
	_, err := Load(func(n string) string {
		if n == "LIGHTNING_ROLE" {
			return RoleProducer
		}
		return ""
	})
	if err == nil || !strings.Contains(err.Error(), "LIGHTNING_KAFKA_BOOTSTRAP is not set") || !strings.Contains(err.Error(), "LIGHTNING_VALUE_SCHEMA_ID is not set") {
		t.Fatalf("got %v", err)
	}
	if _, err := Load(func(string) string { return "" }); err == nil {
		t.Fatal("no role accepted")
	}
}

func TestControlNeedsTheStatusKey(t *testing.T) {
	_, err := Load(env(RoleControl, map[string]string{"LIGHTNING_STATUS_API_KEY": "", "LIGHTNING_STATUS_API_SECRET": ""}))
	if err == nil || !strings.Contains(err.Error(), "LIGHTNING_STATUS_API_KEY is not set") || !strings.Contains(err.Error(), "LIGHTNING_STATUS_API_SECRET is not set") {
		t.Fatalf("got %v", err)
	}
}

func TestBounds(t *testing.T) {
	for name, extra := range map[string]map[string]string{
		"miss":    {"LIGHTNING_MISS_RATIO": "0.5"},
		"density": {"LIGHTNING_LOCATIONS_PER_SKU": "1000"},
		"token":   {"LIGHTNING_WORKER_TOKEN": "short"},
		"scheme":  {"LIGHTNING_CONTROL_URL": "ftp://alb"},
		"nohost":  {"LIGHTNING_CONTROL_URL": "http://"},
		"userinf": {"LIGHTNING_CONTROL_URL": "http://u:p@alb"},
		"path":    {"LIGHTNING_CONTROL_URL": "http://alb/internal"},
	} {
		if _, err := Load(env(RoleQuery, extra)); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
}

func TestRedaction(t *testing.T) {
	c, _ := Load(env(RoleControl, map[string]string{"LIGHTNING_CLOUD_API_SECRET": "supersecretvalue", "LIGHTNING_STATUS_API_SECRET": "statussecretvalue"}))
	for _, s := range []string{fmt.Sprint(c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c)} {
		if strings.Contains(s, "supersecretvalue") || strings.Contains(s, "statussecretvalue") || strings.Contains(s, tok) {
			t.Fatalf("secret leaked: %s", s)
		}
	}
}
