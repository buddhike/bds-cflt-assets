package server

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var envName = regexp.MustCompile(`"(PIMS_[A-Z0-9_]+)"`)

func namesIn(s string) map[string]bool {
	out := map[string]bool{}
	for _, m := range envName.FindAllStringSubmatch(s, -1) {
		out[m[1]] = true
	}
	return out
}

// The environment Terraform gives the app (tf/ecs.tf) and the one LoadConfig reads
// are two lists in two languages. A name that drifts apart fails a task at start
// up, in the cloud, minutes into a launch, so check it here.
func TestTerraformSetsExactlyWhatTheServerReads(t *testing.T) {
	tf, err := os.ReadFile("../../tf/ecs.tf")
	if err != nil {
		t.Fatal(err)
	}
	src := string(tf)
	start := strings.Index(src, "app_env = [")
	end := strings.Index(src, "\n  ]\n}")
	if start < 0 || end < start {
		t.Fatal("could not find app_env and app_secrets in tf/ecs.tf")
	}
	given := namesIn(src[start:end])

	code, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}
	read := namesIn(string(code))

	// Everything required must be given.
	_, loadErr := LoadConfig(func(string) string { return "" })
	if loadErr == nil {
		t.Fatal("LoadConfig accepted an empty environment")
	}
	var required []string
	for _, m := range regexp.MustCompile(`(PIMS_[A-Z0-9_]+) is not set`).FindAllStringSubmatch(loadErr.Error(), -1) {
		required = append(required, m[1])
	}
	for _, name := range required {
		if !given[name] {
			t.Errorf("tf/ecs.tf does not set %s, which the server requires", name)
		}
	}
	// And nothing is given that the server ignores, which would be a typo.
	for name := range given {
		if !read[name] {
			t.Errorf("tf/ecs.tf sets %s, which the server does not read", name)
		}
	}
	if len(required) < 15 {
		t.Errorf("found only %d required names (%v): the parse is wrong", len(required), required)
	}
}

// Tableflow is the app's to switch on and off. A confluent_tableflow_topic in Terraform
// would fight it: the app's POST would find the topic enabled and Terraform would lose track
// of what the app disabled at shutdown.
func TestTerraformDoesNotManageTableflowTopics(t *testing.T) {
	files, err := filepath.Glob("../../tf/*.tf")
	if err != nil || len(files) == 0 {
		t.Fatalf("no Terraform files found: %v", err)
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), `resource "confluent_tableflow_topic"`) {
			t.Errorf("%s manages Tableflow topics, which the app enables itself", f)
		}
	}
}
