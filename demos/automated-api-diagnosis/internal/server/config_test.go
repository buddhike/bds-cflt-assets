package server

import (
	"fmt"
	"strings"
	"testing"
)

func fullEnv() map[string]string {
	return map[string]string{
		"APIDIAG_ACCESS_TOKEN":               strings.Repeat("t", 32),
		"APIDIAG_AWS_REGION":                 "us-east-1",
		"APIDIAG_DEMO_NAME":                  "automated-api-diagnosis-ab12cd",
		"APIDIAG_ENVIRONMENT_ID":             "env-1",
		"APIDIAG_KAFKA_CLUSTER_ID":           "lkc-1",
		"APIDIAG_KAFKA_BOOTSTRAP":            "pkc-1.us-east-1.aws.confluent.cloud:9092",
		"APIDIAG_KAFKA_API_KEY":              "kk",
		"APIDIAG_KAFKA_API_SECRET":           "ks-secret",
		"APIDIAG_SCHEMA_REGISTRY_URL":        "https://psrc-1.us-east-1.aws.confluent.cloud",
		"APIDIAG_SCHEMA_REGISTRY_API_KEY":    "sk",
		"APIDIAG_SCHEMA_REGISTRY_API_SECRET": "ss-secret",
	}
}

func getenv(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestLoadConfigDefaults(t *testing.T) {
	c, err := LoadConfig(getenv(fullEnv()))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":8080" || c.Topic != DefaultTopic || c.AIReports || !c.AutoStart {
		t.Errorf("unexpected defaults: %+v", c)
	}
}

func TestLoadConfigOptions(t *testing.T) {
	env := fullEnv()
	env["APIDIAG_AI_REPORTS"] = "true"
	env["APIDIAG_AUTOSTART"] = "false"
	env["APIDIAG_TOPIC"] = "logs"
	c, err := LoadConfig(getenv(env))
	if err != nil {
		t.Fatal(err)
	}
	if !c.AIReports || c.AutoStart || c.Topic != "logs" {
		t.Errorf("options not read: %+v", c)
	}
}

func TestLoadConfigReportsEveryProblem(t *testing.T) {
	env := fullEnv()
	delete(env, "APIDIAG_KAFKA_BOOTSTRAP")
	delete(env, "APIDIAG_ENVIRONMENT_ID")
	env["APIDIAG_AI_REPORTS"] = "maybe"
	_, err := LoadConfig(getenv(env))
	if err == nil {
		t.Fatal("accepted a broken environment")
	}
	for _, want := range []string{"APIDIAG_KAFKA_BOOTSTRAP is not set", "APIDIAG_ENVIRONMENT_ID is not set", "APIDIAG_AI_REPORTS must be true or false"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q: %v", want, err)
		}
	}
}

func TestLoadConfigRejectsShortToken(t *testing.T) {
	env := fullEnv()
	env["APIDIAG_ACCESS_TOKEN"] = "short"
	if _, err := LoadConfig(getenv(env)); err == nil || !strings.Contains(err.Error(), "at least") {
		t.Fatalf("a short token was accepted: %v", err)
	}
}

func TestConfigNeverPrintsSecrets(t *testing.T) {
	c, err := LoadConfig(getenv(fullEnv()))
	if err != nil {
		t.Fatal(err)
	}
	for _, out := range []string{fmt.Sprint(c), fmt.Sprintf("%v", c), fmt.Sprintf("%+v", c), fmt.Sprintf("%#v", c), c.LogValue().String()} {
		for _, secret := range []string{c.AccessToken, c.KafkaAPISecret, c.SchemaRegistryAPISecret} {
			if strings.Contains(out, secret) {
				t.Errorf("%q leaked in %q", secret, out)
			}
		}
	}
}
