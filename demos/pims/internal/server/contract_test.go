package server

import (
	"os"
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

func TestTerraformPassesTheTableflowParameterThatItCreates(t *testing.T) {
	ecs, _ := os.ReadFile("../../tf/ecs.tf")
	tflow, _ := os.ReadFile("../../tf/tableflow.tf")
	if !strings.Contains(string(ecs), `name = "PIMS_TABLEFLOW_PARAM", value = local.tableflow_param_name`) {
		t.Error("the app is not told the parameter name")
	}
	if !strings.Contains(string(tflow), "name  = local.tableflow_param_name") || !strings.Contains(string(tflow), `value = "true"`) {
		t.Error("tableflow.tf does not set that parameter to true")
	}
}
