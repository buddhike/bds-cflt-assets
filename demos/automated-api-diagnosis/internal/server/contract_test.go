package server

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"apidiag/internal/simulator"
)

var envName = regexp.MustCompile(`"(APIDIAG_[A-Z0-9_]+)"`)

func namesIn(s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range envName.FindAllStringSubmatch(s, -1) {
		out[m[1]] = true
	}
	return out
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The environment Terraform gives the app (tf/ecs.tf) and the one LoadConfig reads are
// two lists in two languages. A name that drifts apart fails a task at start up, in the
// cloud, minutes into a launch, so check it here.
func TestTerraformSetsExactlyWhatTheServerReads(t *testing.T) {
	src := readFile(t, "../../tf/ecs.tf")
	start := strings.Index(src, "app_env = [")
	end := strings.Index(src, "\n  ]\n}")
	if start < 0 || end < start {
		t.Fatal("could not find app_env and app_secrets in tf/ecs.tf")
	}
	given := namesIn(src[start:end])
	read := namesIn(readFile(t, "config.go"))

	_, loadErr := LoadConfig(func(string) string { return "" })
	if loadErr == nil {
		t.Fatal("LoadConfig accepted an empty environment")
	}
	var required []string
	for _, m := range regexp.MustCompile(`(APIDIAG_[A-Z0-9_]+) is not set`).FindAllStringSubmatch(loadErr.Error(), -1) {
		required = append(required, m[1])
	}
	for _, name := range required {
		if !given[name] {
			t.Errorf("tf/ecs.tf does not set %s, which the server requires", name)
		}
	}
	for name := range given {
		if !read[name] {
			t.Errorf("tf/ecs.tf sets %s, which the server does not read", name)
		}
	}
	if len(required) < 10 {
		t.Errorf("found only %d required names (%v): the parse is wrong", len(required), required)
	}
}

// Terraform registers the schema the simulator's producer would register itself. If they
// differ, the producer's registration fails the compatibility check, or Flink reads
// columns the app does not write.
func TestRegisteredSchemaIsTheProducersSchema(t *testing.T) {
	want, err := simulator.AvroSchemaJSON()
	if err != nil {
		t.Fatal(err)
	}
	var a, b any
	if err := json.Unmarshal([]byte(readFile(t, "../../tf/gateway-logs-value.avsc")), &a); err != nil {
		t.Fatalf("tf/gateway-logs-value.avsc is not JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(want), &b); err != nil {
		t.Fatal(err)
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Errorf("tf/gateway-logs-value.avsc differs from simulator.AvroSchemaJSON().\nregenerate it from:\n%s", want)
	}
}

// Four places name the topics: the server, Terraform and the Flink statements.
func TestTopicNamesAgree(t *testing.T) {
	m := regexp.MustCompile(`topic\s*=\s*"([^"]+)"`).FindStringSubmatch(readFile(t, "../../tf/confluent.tf"))
	if m == nil || m[1] != DefaultTopic {
		t.Fatalf("tf/confluent.tf names the topic %v, the server defaults to %q", m, DefaultTopic)
	}
	for _, f := range []string{"alter_watermark.sql", "detect_anomalies.sql"} {
		if !strings.Contains(readFile(t, "../../tf/flink/"+f), "`"+DefaultTopic+"`") {
			t.Errorf("tf/flink/%s does not read `%s`", f, DefaultTopic)
		}
	}
	if !strings.Contains(readFile(t, "../../tf/flink/detect_anomalies.sql"), "INSERT INTO "+UnhealthyEventsTopic) {
		t.Errorf("the detector does not write %s, which the monitor reads", UnhealthyEventsTopic)
	}
	if !strings.Contains(readFile(t, "../../tf/flink/create_anomaly_reports.sql"), "CREATE TABLE "+AnomalyReportsTopic) {
		t.Errorf("no table %s, which the reports tab reads", AnomalyReportsTopic)
	}
	if !strings.Contains(readFile(t, "../../tf/flink/create_unhealthy_events.sql"), "CREATE TABLE "+UnhealthyEventsTopic) {
		t.Errorf("no table %s, which the monitor reads", UnhealthyEventsTopic)
	}
}

// The app reads api_anomaly_ai_reports into non-pointer fields, so every column must be
// NOT NULL: a nullable one is an Avro union the decoder cannot read into a plain string.
func TestReportTableColumnsAreNotNull(t *testing.T) {
	sql := readFile(t, "../../tf/flink/create_anomaly_reports.sql")
	for _, col := range []string{"api_id STRING", "http_status STRING", "detected_at TIMESTAMP_LTZ(3)", "agent_status STRING", "report STRING"} {
		if !strings.Contains(sql, col+" NOT NULL") {
			t.Errorf("%s must be NOT NULL", col)
		}
	}
}

// Nothing the example may leave behind in the tree should run by itself, and Terraform must
// not declare a backend: Launchpad supplies it.
func TestNoBackendAndTheAIExampleIsNotLive(t *testing.T) {
	for _, f := range []string{"versions.tf", "confluent.tf", "flink.tf", "ecs.tf", "network.tf", "cloudfront.tf", "secrets.tf", "outputs.tf", "variables.tf"} {
		if regexp.MustCompile(`backend\s+"`).MatchString(readFile(t, "../../tf/"+f)) {
			t.Errorf("tf/%s declares a backend, but Launchpad supplies the S3 backend", f)
		}
	}
	if _, err := os.Stat("../../tf/ai.tf"); err == nil {
		t.Log("tf/ai.tf exists: the AI agent is deployed. Check variable ai_agent_deployed is true.")
		if !regexp.MustCompile(`(?s)variable "ai_agent_deployed".*?default\s*=\s*true`).MatchString(readFile(t, "../../tf/variables.tf")) {
			t.Error("tf/ai.tf exists, but variable ai_agent_deployed still defaults to false, so the tab will say no agent is deployed")
		}
	}
}
