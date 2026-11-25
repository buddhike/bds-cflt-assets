package server

import (
	"fmt"
	"strings"
	"testing"
)

func fullEnv() map[string]string {
	return map[string]string{
		"PIMS_ACCESS_TOKEN":               "0123456789abcdef0123456789abcdef",
		"PIMS_AWS_REGION":                 "us-east-1",
		"PIMS_CC_API_KEY":                 "CCKEY",
		"PIMS_CC_API_SECRET":              "ccsecret-value",
		"PIMS_ORGANIZATION_ID":            "org-1",
		"PIMS_ENVIRONMENT_ID":             "env-1",
		"PIMS_KAFKA_CLUSTER_ID":           "lkc-1",
		"PIMS_KAFKA_BOOTSTRAP":            "pkc-1.us-east-1.aws.confluent.cloud:9092",
		"PIMS_KAFKA_API_KEY":              "KK",
		"PIMS_KAFKA_API_SECRET":           "kafka-secret-value",
		"PIMS_SCHEMA_REGISTRY_URL":        "https://psrc-1.us-east-1.aws.confluent.cloud",
		"PIMS_SCHEMA_REGISTRY_API_KEY":    "SK",
		"PIMS_SCHEMA_REGISTRY_API_SECRET": "sr-secret-value",
		"PIMS_TABLEFLOW_API_KEY":          "TK",
		"PIMS_TABLEFLOW_API_SECRET":       "tf-secret-value",
		"PIMS_TABLEFLOW_CATALOG_ENDPOINT": "https://tableflow.us-east-1.aws.confluent.cloud/iceberg/catalog/organizations/o/environments/e",
		"PIMS_MYSQL_ROOT_PASSWORD":        "root-password-value",
		"PIMS_MYSQL_APP_PASSWORD":         "app-password-value",
		"PIMS_MYSQL_DEBEZIUM_PASSWORD":    "dbz-password-value",
		"PIMS_AGENT_TOKEN":                "agent-token-value",
		"PIMS_TABLEFLOW_PARAM":            "/launchpad/pims/tableflow",
	}
}

func load(env map[string]string) (Config, error) {
	return LoadConfig(func(k string) string { return env[k] })
}

func TestLoadConfigDefaultsAndOutputs(t *testing.T) {
	c, err := load(fullEnv())
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" || c.MySQLHost != "127.0.0.1" || c.MySQLPort != 3306 {
		t.Errorf("defaults: %+v", c)
	}
	if c.ConnectURL != "http://127.0.0.1:8083" || c.AgentURL != "http://127.0.0.1:8081" {
		t.Errorf("urls: %s %s", c.ConnectURL, c.AgentURL)
	}
	if got := strings.Join(c.Outputs.Topics, ","); got != "patients,appointments,visits" {
		t.Errorf("topics default = %s", got)
	}
	if c.Outputs.KafkaClusterID != "lkc-1" || c.Outputs.TableflowEnabled {
		t.Errorf("outputs: %+v", c.Outputs)
	}
}

func TestLoadConfigOverrides(t *testing.T) {
	env := fullEnv()
	env["PIMS_LISTEN"], env["PIMS_MYSQL_ADDR"], env["PIMS_TOPICS"] = ":9000", "10.0.0.1:3307", " a , b,,c "
	c, err := load(env)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":9000" || c.MySQLHost != "10.0.0.1" || c.MySQLPort != 3307 || strings.Join(c.Outputs.Topics, ",") != "a,b,c" {
		t.Errorf("%+v", c)
	}
}

func TestLoadConfigReportsEveryProblemAtOnce(t *testing.T) {
	_, err := load(map[string]string{"PIMS_ACCESS_TOKEN": "short", "PIMS_MYSQL_ADDR": "nope"})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, want := range []string{"PIMS_AWS_REGION is not set", "PIMS_KAFKA_BOOTSTRAP is not set", "PIMS_AGENT_TOKEN is not set", "at least 24 characters", `"nope" is not host:port`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

func TestConfigNeverPrintsSecrets(t *testing.T) {
	c, err := load(fullEnv())
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{fmt.Sprint(c), fmt.Sprintf("%v %+v %#v", c, c, c), c.LogValue().String()} {
		for k, v := range fullEnv() {
			if strings.Contains(k, "SECRET") || strings.Contains(k, "PASSWORD") || strings.Contains(k, "TOKEN") {
				if strings.Contains(s, v) {
					t.Errorf("%s leaked into %q", k, s)
				}
			}
		}
	}
}
