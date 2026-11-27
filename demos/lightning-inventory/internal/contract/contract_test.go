// Package contract holds the tests that keep Terraform, the Go code, the schema and the
// design doc saying the same thing. Each is a check across two languages: a name that drifts
// fails a task at start up in the cloud, minutes into a launch, so it is checked here.
package contract

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"lightninginv/internal/config"
	"lightninginv/internal/worker"
)

var envName = regexp.MustCompile(`"(LIGHTNING_[A-Z0-9_]+)"`)

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func namesIn(s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range envName.FindAllStringSubmatch(s, -1) {
		out[m[1]] = true
	}
	return out
}

// block returns the text of `name = [ ... ]` in ecs.tf.
func block(t *testing.T, src, name string) string {
	t.Helper()
	start := strings.Index(src, "  "+name+" = [")
	if start < 0 {
		t.Fatalf("tf/ecs.tf has no %s", name)
	}
	end := strings.Index(src[start:], "\n  ]\n")
	if end < 0 {
		t.Fatalf("tf/ecs.tf: %s is not closed", name)
	}
	return src[start : start+end]
}

// What a role reads is what Load asks the environment for when the role is set; what it
// requires is what Load complains is missing from an empty environment.
func readAndRequired(t *testing.T, role string) (read, required map[string]bool) {
	t.Helper()
	read, required = map[string]bool{}, map[string]bool{}
	_, err := config.Load(func(n string) string {
		read[n] = true
		if n == "LIGHTNING_ROLE" {
			return role
		}
		return ""
	})
	if err == nil {
		t.Fatalf("%s: Load accepted an empty environment", role)
	}
	for _, m := range regexp.MustCompile(`(LIGHTNING_[A-Z0-9_]+) is not set`).FindAllStringSubmatch(err.Error(), -1) {
		required[m[1]] = true
	}
	return read, required
}

func keys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Terraform gives each role exactly what that role needs: every variable it requires, and
// none it does not read (a producer must not be handed the Global key).
func TestTerraformSetsExactlyWhatEachRoleReads(t *testing.T) {
	src := readFile(t, "../../tf/ecs.tf")
	for _, role := range []string{config.RoleControl, config.RoleProducer, config.RoleQuery} {
		given := namesIn(block(t, src, role+"_env") + block(t, src, role+"_secrets"))
		read, required := readAndRequired(t, role)
		if len(required) < 5 {
			t.Fatalf("%s: found only %v required names: the parse is wrong", role, keys(required))
		}
		for name := range required {
			if !given[name] {
				t.Errorf("%s: tf/ecs.tf does not set %s, which the role requires", role, name)
			}
		}
		for name := range given {
			if !read[name] {
				t.Errorf("%s: tf/ecs.tf sets %s, which the role does not read: a credential a role does not use must not be given to it", role, name)
			}
		}
	}
	// Every name in config.go is one of the three roles'.
	for name := range namesIn(readFile(t, "../config/config.go")) {
		found := false
		for _, role := range []string{config.RoleControl, config.RoleProducer, config.RoleQuery} {
			if read, _ := readAndRequired(t, role); read[name] {
				found = true
			}
		}
		if !found && name != "LIGHTNING_ROLE" {
			t.Errorf("%s is named in config.go but no role reads it", name)
		}
	}
}

// The design doc's environment table names every variable.
func TestDesignDocListsEveryVariable(t *testing.T) {
	doc := readFile(t, "../../docs/specs/launchpad.md")
	for name := range namesIn(readFile(t, "../config/config.go")) {
		if !strings.Contains(doc, name) {
			t.Errorf("docs/specs/launchpad.md does not mention %s", name)
		}
	}
}

// Terraform registers the schema the producer encodes with. If they differ, consumers (the
// Lightning Table) read columns the producer does not write.
func TestRegisteredSchemaIsTheProducersSchema(t *testing.T) {
	var a, b any
	if err := json.Unmarshal([]byte(readFile(t, "../../tf/inventory-value.avsc")), &a); err != nil {
		t.Fatalf("tf/inventory-value.avsc is not JSON: %v", err)
	}
	if err := json.Unmarshal([]byte(worker.SchemaJSON), &b); err != nil {
		t.Fatal(err)
	}
	ja, _ := json.Marshal(a)
	jb, _ := json.Marshal(b)
	if string(ja) != string(jb) {
		t.Errorf("tf/inventory-value.avsc differs from worker.SchemaJSON.\ngo: %s\ntf: %s", jb, ja)
	}
	if _, err := worker.NewCodec(1); err != nil {
		t.Fatal(err)
	}
}

// The query text names columns the schema has, and the record schema has exactly the three
// the brief names.
func TestSchemaColumns(t *testing.T) {
	var s struct {
		Fields []struct {
			Name string
			Type any
		}
	}
	if err := json.Unmarshal([]byte(worker.SchemaJSON), &s); err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range s.Fields {
		got = append(got, f.Name)
		if _, isUnion := f.Type.([]any); isUnion {
			t.Errorf("field %s is nullable: the table's columns are meant to be NOT NULL", f.Name)
		}
	}
	if strings.Join(got, ",") != "sku,location_id,quantity" {
		t.Errorf("the record is %v, want sku, location_id, quantity", got)
	}
	q := readFile(t, "../lightning/queries.go")
	for _, col := range got {
		if !strings.Contains(q, col) {
			t.Errorf("queries.go never names the column %s", col)
		}
	}
}

// Terraform and the app name the topic once each, and the same.
func TestTopicNamesAgree(t *testing.T) {
	m := regexp.MustCompile(`topic\s*=\s*"([^"]+)"`).FindStringSubmatch(readFile(t, "../../tf/confluent.tf"))
	if m == nil || m[1] != config.DefaultTopic {
		t.Fatalf("tf/confluent.tf names the topic %v, the app defaults to %q", m, config.DefaultTopic)
	}
	if !regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_]*$`).MatchString(m[1]) {
		t.Errorf("topic %q is not a valid Lightning Table topic name", m[1])
	}
	ecs := readFile(t, "../../tf/ecs.tf")
	if strings.Count(ecs, `name = "LIGHTNING_TOPIC", value = local.topic`) != 3 {
		t.Error("every role must take the topic from local.topic")
	}
	if !strings.Contains(readFile(t, "../../tf/confluent.tf"), `subject_name  = "${local.topic}-value"`) {
		t.Error("the value schema must be registered as <topic>-value")
	}
}

// The browser-facing bounds are the brief's, and Terraform's partition count too.
func TestBounds(t *testing.T) {
	if worker.MaxEventsPerSec != 200 || worker.DefaultEventsPerSec != 10 || worker.MaxQueriesPerSec != 1000 || worker.DefaultQueriesPerSec != 50 {
		t.Error("rate bounds changed: update the design doc, CLAUDE.md and the brief's numbers together")
	}
	if worker.MaxLocationsPerQuery != 2000 {
		t.Error("a by_locations query takes up to 2000 locations")
	}
	if !regexp.MustCompile(`variable "partitions"[^}]*default\s*=\s*6`).MatchString(readFile(t, "../../tf/variables.tf")) {
		t.Error("the topic has 6 partitions")
	}
}

// Terraform must not declare a backend: Launchpad supplies it.
func TestNoBackend(t *testing.T) {
	files, _ := filepath.Glob("../../tf/*.tf")
	if len(files) == 0 {
		t.Fatal("no tf files")
	}
	for _, f := range files {
		if regexp.MustCompile(`backend\s+"`).MatchString(readFile(t, f)) {
			t.Errorf("%s declares a backend, but Launchpad supplies the S3 backend", f)
		}
	}
}

// code returns src without its comment lines, so a comment may name what code may not.
func code(src string) string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if t := strings.TrimSpace(line); strings.HasPrefix(t, "#") || strings.HasPrefix(t, "//") {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// goSources returns every non-test Go file of the app, by path.
func goSources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir("../..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == "node_modules" || n == ".terraform" || n == ".git" || n == "dist" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			out[path] = readFile(t, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) < 10 {
		t.Fatalf("found only %d Go files: the walk is wrong", len(out))
	}
	return out
}

// Terraform owns the Lightning Table (confluent_rtce_topic), so there is nothing for the app to
// undo: no shutdown hook in Terraform and no /shutdown in the app, and the app has no way to
// enable or disable a table. (The owner's decision of 2026-11-27, replacing the earlier
// "the app enables it" one.)
func TestTableIsTerraformsAndThereIsNoHook(t *testing.T) {
	files, _ := filepath.Glob("../../tf/*.tf")
	var all string
	for _, f := range files {
		all += code(readFile(t, f)) + "\n"
	}
	if !regexp.MustCompile(`(?m)^resource "confluent_rtce_topic" "\w+"`).MatchString(all) {
		t.Error("tf/ declares no confluent_rtce_topic: Terraform enables the Lightning Table")
	}
	conf := code(readFile(t, "../../tf/confluent.tf"))
	rtce := regexp.MustCompile(`(?s)resource "confluent_rtce_topic".*?\n}\n`).FindString(conf)
	for _, want := range []string{"confluent_kafka_topic.inventory.topic_name", "confluent_schema.inventory_value", "var.aws_region", `cloud       = "AWS"`} {
		if !strings.Contains(rtce, want) {
			t.Errorf("the confluent_rtce_topic block does not use %s", want)
		}
	}
	if strings.Contains(rtce, "credentials") {
		t.Error("the table is created with the provider's own credentials, not a key of the app")
	}
	// the workers wait for the table, so on destroy they stop before it goes
	if !strings.Contains(code(readFile(t, "../../tf/ecs.tf")), "confluent_rtce_topic.inventory") {
		t.Error("the worker services must depend on the table, so destroy stops them first")
	}
	out := code(readFile(t, "../../tf/outputs.tf"))
	if !strings.Contains(out, `output "launchpad_url"`) {
		t.Error("tf/outputs.tf lacks launchpad_url")
	}
	for _, f := range append(files, "../../demo.json") {
		if strings.Contains(code(readFile(t, f)), "launchpad_shutdown") {
			t.Errorf("%s names launchpad_shutdown: Terraform owns everything, so there is no hook", f)
		}
	}
	if strings.Contains(all, "/shutdown") {
		t.Error("a Terraform file names /shutdown")
	}
	for path, src := range goSources(t) {
		c := code(src)
		if strings.Contains(c, "/shutdown") || strings.Contains(c, "launchpad_shutdown") {
			t.Errorf("%s has a shutdown hook: there is none", path)
		}
	}
	// The Lightning client only reads the table's status.
	cl := code(readFile(t, "../lightning/client.go"))
	if strings.Contains(cl, "http.MethodDelete") || strings.Contains(cl, "func (t *Tables) Enable") || strings.Contains(cl, "func (t *Tables) Disable") {
		t.Error("internal/lightning can enable or disable the table: Terraform does, and the app only reads")
	}
	for _, line := range strings.Split(cl, "\n") {
		if strings.Contains(line, "rtce-topics") && !strings.Contains(line, "MethodGet") {
			t.Errorf("a call to rtce-topics that is not a GET: %s", strings.TrimSpace(line))
		}
	}
	if !strings.Contains(all, `variable "confluent_cloud_api_key"`) || !strings.Contains(all, `variable "confluent_cloud_api_secret"`) {
		t.Error("declare both Confluent variables")
	}
}

// No task's service account holds an administrator role, and the keys Terraform uses to create
// the topic and the schema never reach a container.
func TestNoTaskHoldsAnAdministratorKey(t *testing.T) {
	conf := code(readFile(t, "../../tf/confluent.tf"))
	if n := strings.Count(conf, `"EnvironmentAdmin"`); n != 1 {
		t.Errorf("EnvironmentAdmin is bound %d times: only the provisioner, whose keys no task holds, may have it", n)
	}
	for _, admin := range []string{"CloudClusterAdmin", "OrganizationAdmin", "ResourceOwner"} {
		if strings.Contains(conf, `"`+admin+`"`) {
			t.Errorf("tf/confluent.tf binds %s", admin)
		}
	}
	prov := regexp.MustCompile(`(?s)resource "confluent_role_binding" "provisioner" \{.*?\n}\n`).FindString(conf)
	if !strings.Contains(prov, "confluent_service_account.provisioner.id") || !strings.Contains(prov, "EnvironmentAdmin") {
		t.Error("the one EnvironmentAdmin binding must be the provisioner's")
	}
	// the provisioner's keys are used by Terraform only: not in Secrets Manager, not in a task
	// definition, not in an output (which Launchpad stores and shows)
	for _, f := range []string{"../../tf/secrets.tf", "../../tf/ecs.tf", "../../tf/outputs.tf"} {
		if strings.Contains(code(readFile(t, f)), "provisioner") {
			t.Errorf("%s uses the provisioner's keys: they are for Terraform only", f)
		}
	}
	// each key belongs to the account of its role
	for key, sa := range map[string]string{"producer_kafka": "producer", "query_global": "query", "control_cloud": "control"} {
		blk := regexp.MustCompile(`(?s)resource "confluent_api_key" "` + key + `" \{.*?\n}\n`).FindString(conf)
		if !strings.Contains(blk, "confluent_service_account."+sa+".id") {
			t.Errorf("the %s key is not owned by the %s service account", key, sa)
		}
	}
	// what each account may do
	for binding, want := range map[string]string{
		"producer_write":     `"DeveloperWrite"`,
		"query_read":         `"DeveloperRead"`,
		"query_schema_read":  `"DeveloperRead"`,
		"control_cluster_read": `"DeveloperRead"`,
		"control_metrics":    `"MetricsViewer"`,
	} {
		blk := regexp.MustCompile(`(?s)resource "confluent_role_binding" "` + binding + `" \{.*?\n}\n`).FindString(conf)
		if !strings.Contains(blk, want) {
			t.Errorf("role binding %s is not %s", binding, want)
		}
	}
}

// The app's only AWS permissions are the two CloudWatch ones, each as narrow as AWS allows.
func TestOnlyCloudWatchPermissions(t *testing.T) {
	ecs := readFile(t, "../../tf/ecs.tf")
	var actions []string
	for _, m := range regexp.MustCompile(`"([a-z0-9]+:[A-Za-z*]+)"`).FindAllStringSubmatch(ecs, -1) {
		actions = append(actions, m[1])
	}
	allowed := map[string]bool{"cloudwatch:PutMetricData": true, "cloudwatch:GetMetricData": true, "secretsmanager:GetSecretValue": true, "sts:AssumeRole": true, "cloudwatch:namespace": true}
	for _, a := range actions {
		if !allowed[a] {
			t.Errorf("tf/ecs.tf grants or names %s: the app's AWS permissions are PutMetricData (query) and GetMetricData (control) only", a)
		}
	}
	if !strings.Contains(ecs, `Condition = { StringEquals = { "cloudwatch:namespace" = local.metrics_namespace } }`) {
		t.Error("PutMetricData must be restricted to the demo's namespace")
	}
	if strings.Contains(ecs, `"*:*"`) || strings.Contains(ecs, "AdministratorAccess") {
		t.Error("wildcard permissions")
	}
}

// Nothing in the tree may hold a credential.
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
		if b, err := os.ReadFile(path); err == nil && key.Match(b) {
			t.Errorf("%s contains what looks like an AWS access key id", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// The demo's tags name the products it shows, and the image is built where Terraform looks.
func TestDemoJSONAndImage(t *testing.T) {
	var d struct {
		Name, Description string
		Tags              []string
	}
	if err := json.Unmarshal([]byte(readFile(t, "../../demo.json")), &d); err != nil {
		t.Fatal(err)
	}
	if len(d.Name) == 0 || len(d.Name) > 80 || len(d.Description) > 500 {
		t.Error("demo.json name or description is out of bounds")
	}
	if strings.Join(d.Tags, ",") != "Apache Kafka,Lightning Tables" {
		t.Errorf("tags %v", d.Tags)
	}
	wf := readFile(t, "../../../../.github/workflows/lightning-inventory-image.yml")
	for _, want := range []string{"context: demos/lightning-inventory", "file: demos/lightning-inventory/docker/Dockerfile", "linux/amd64"} {
		if !strings.Contains(wf, want) {
			t.Errorf("the image workflow lacks %q", want)
		}
	}
	if !strings.Contains(readFile(t, "../../tf/ecs.tf"), "/lightning-inventory:") {
		t.Error("tf/ecs.tf must run the lightning-inventory image")
	}
	// No path filter: Launchpad deploys the image tagged with the commit it downloaded.
	if strings.Contains(wf, "paths:") {
		t.Error("the image workflow must not have a path filter")
	}
}
