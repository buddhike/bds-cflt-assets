package server

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
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

// Terraform must not declare a backend: Launchpad supplies it.
func TestNoBackend(t *testing.T) {
	for _, f := range []string{"versions.tf", "confluent.tf", "flink.tf", "ai.tf", "ecs.tf", "network.tf", "cloudfront.tf", "secrets.tf", "outputs.tf", "variables.tf"} {
		if regexp.MustCompile(`backend\s+"`).MatchString(readFile(t, "../../tf/"+f)) {
			t.Errorf("tf/%s declares a backend, but Launchpad supplies the S3 backend", f)
		}
	}
}

// The agent's Bedrock key is made by Terraform and travels only from aws_iam_access_key to
// confluent_flink_connection. Nothing in the tree may hold a key, and no output or Flink
// statement may carry one.
func TestNoCredentialInTheTree(t *testing.T) {
	key := regexp.MustCompile(`\b(AKIA|ASIA)[0-9A-Z]{16}\b|(?i)aws_secret_access_key\s*[=:]\s*\S{30,}`)
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == "node_modules" || n == ".terraform" || n == "dist" || n == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".jar") {
			return nil
		}
		if b, err := os.ReadFile(path); err == nil && key.Match(b) {
			t.Errorf("%s contains what looks like an AWS access key id", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	ai := readFile(t, "../../tf/ai.tf")
	if !regexp.MustCompile(`aws_access_key\s*=\s*aws_iam_access_key\.flink_llm\.id`).MatchString(ai) ||
		!regexp.MustCompile(`aws_secret_key\s*=\s*aws_iam_access_key\.flink_llm\.secret`).MatchString(ai) {
		t.Error("the Flink connection must take its key from aws_iam_access_key.flink_llm, which Terraform makes")
	}
	tfs, _ := filepath.Glob("../../tf/*.tf")
	for _, f := range tfs {
		if filepath.Base(f) == "ai.tf" {
			continue
		}
		src := readFile(t, f)
		for _, bad := range []string{"flink_llm", "aws_iam_access_key", "aws_secret_key", "aws_access_key"} {
			if strings.Contains(src, bad) {
				t.Errorf("%s mentions %s: the Bedrock key must reach only the Flink connection in ai.tf", f, bad)
			}
		}
	}
	sqls, _ := filepath.Glob("../../tf/flink/*")
	for _, f := range sqls {
		if strings.Contains(strings.ToLower(readFile(t, f)), "aws-secret-key") {
			t.Errorf("%s holds a credential: it belongs in the Flink connection", f)
		}
	}
}

// The report statement reads a table, calls a function and runs an agent that other
// statements create, and Terraform only orders them if it says so.
func TestReportStatementWaitsForWhatItUses(t *testing.T) {
	ai := readFile(t, "../../tf/ai.tf")
	m := regexp.MustCompile(`(?s)resource "confluent_flink_statement" "ai_reports" \{.*?depends_on = \[(.*?)\]`).FindStringSubmatch(ai)
	if m == nil {
		t.Fatal("ai.tf has no ai_reports statement with depends_on")
	}
	for _, dep := range []string{"ai_agent", "emit_after_gap", "create_anomalies", "create_anomaly_reports"} {
		if !strings.Contains(m[1], "confluent_flink_statement."+dep) {
			t.Errorf("ai_reports must depend on %s", dep)
		}
	}
	sql := readFile(t, "../../tf/flink/ai_reports.sql")
	for _, want := range []string{"INSERT INTO `" + AnomalyReportsTopic + "`", "EmitAfterGap", "TABLE api_anomalies", "anomaly_investigation_agent"} {
		if !strings.Contains(sql, want) {
			t.Errorf("ai_reports.sql lacks %q", want)
		}
	}
	if !strings.Contains(readFile(t, "../../tf/flink/ai_agent.sql"), "CREATE AGENT anomaly_investigation_agent") {
		t.Error("ai_agent.sql does not create the agent ai_reports.sql runs")
	}
	if !strings.Contains(readFile(t, "../../tf/flink/create_anomalies.sql"), "CREATE TABLE api_anomalies") {
		t.Error("no table api_anomalies, which the report statement reads")
	}
}

// The function is registered by class name from a jar that a workflow builds from
// udf/emit-after-gap, and nothing else checks that the SQL and the source agree.
func TestEmitAfterGapIsWhatTheSQLRegisters(t *testing.T) {
	if !strings.Contains(readFile(t, "../../tf/flink/create_emit_after_gap.sql.tftpl"), "'org.example.EmitAfterGap'") {
		t.Error("the CREATE FUNCTION statement does not name org.example.EmitAfterGap")
	}
	src := readFile(t, "../../udf/emit-after-gap/src/main/java/org/example/EmitAfterGap.java")
	if !strings.Contains(src, "package org.example;") || !strings.Contains(src, "public class EmitAfterGap extends ProcessTableFunction") {
		t.Error("udf/emit-after-gap no longer defines org.example.EmitAfterGap, which the CREATE FUNCTION statement names")
	}
}

// Terraform and the release workflow must name the jar's release with one definition, or a
// launch asks for a release the workflow never makes. No jar may be committed either: the
// release is the only source of it.
func TestEmitAfterGapReleaseIsNamedByOneModule(t *testing.T) {
	ai := readFile(t, "../../tf/ai.tf")
	if !strings.Contains(ai, `source     = "./modules/udf-release"`) || !strings.Contains(ai, "module.udf_release.tag") {
		t.Error("tf/ai.tf must take the release tag from modules/udf-release")
	}
	if !strings.Contains(ai, "/releases/download/${module.udf_release.tag}/emit-after-gap.jar") {
		t.Error("tf/ai.tf must download emit-after-gap.jar from the release the module names")
	}
	wf := readFile(t, "../../../../.github/workflows/automated-api-diagnosis-udf.yml")
	if !strings.Contains(wf, "demos/automated-api-diagnosis/tf/modules/udf-release") || !strings.Contains(wf, `gh release create "$TAG"`) {
		t.Error("the udf workflow must name its release with modules/udf-release")
	}
	jars, _ := filepath.Glob("../../tf/udf/*.jar")
	if len(jars) > 0 {
		t.Errorf("a jar is committed (%v): the workflow publishes it as a release instead", jars)
	}
}
