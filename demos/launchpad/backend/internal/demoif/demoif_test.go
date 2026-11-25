package demoif

import (
	"slices"
	"testing"

	"launchpad/internal/testenv"
)

func TestGoodDemo(t *testing.T) {
	r := Check(map[string]string{"main.tf": testenv.GoodTF})
	if !r.Valid() || r.NeedsConfluent {
		t.Fatalf("%+v", r)
	}
}

func TestConfluentDemoDeclaresBothVariables(t *testing.T) {
	r := Check(map[string]string{"main.tf": testenv.CloudTF})
	if !r.Valid() || !r.NeedsConfluent {
		t.Fatalf("%+v", r)
	}
}

func TestAllProblemsAreReported(t *testing.T) {
	r := Check(map[string]string{"main.tf": "terraform { backend \"s3\" {} }\nvariable \"confluent_cloud_api_key\" {}"})
	if r.Valid() {
		t.Fatal("must not be valid")
	}
	for _, want := range []string{`missing variable "aws_region"`, `missing variable "launchpad_tags"`, `missing output "launchpad_url"`} {
		if !slices.Contains(r.Problems, want) {
			t.Errorf("no problem %q in %v", want, r.Problems)
		}
	}
	if len(r.Problems) != 5 {
		t.Errorf("want 5 problems, got %v", r.Problems)
	}
}

func TestCommentedOutDeclarationsDoNotCount(t *testing.T) {
	tf := "# variable \"aws_region\" {}\n// variable \"launchpad_tags\" {}\n/* output \"launchpad_url\" {} */"
	if got := len(Check(map[string]string{"main.tf": tf}).Problems); got != 3 {
		t.Fatalf("want 3 problems, got %d", got)
	}
}

func TestNoFiles(t *testing.T) {
	r := Check(nil)
	if len(r.Problems) != 1 || r.Problems[0] != "tf has no .tf files" {
		t.Fatalf("%+v", r)
	}
}

func TestDeclarationsMaySpreadOverFiles(t *testing.T) {
	r := Check(map[string]string{
		"variables.tf": "variable \"aws_region\" {}\nvariable \"launchpad_tags\" {}",
		"outputs.tf":   "output \"launchpad_url\" {}",
	})
	if !r.Valid() {
		t.Fatalf("%+v", r)
	}
}

func TestProblemsIsAnEmptyListWhenValid(t *testing.T) {
	if r := Check(map[string]string{"main.tf": testenv.GoodTF}); r.Problems == nil {
		t.Fatal("Problems must be an empty list, not nil, so it stores and encodes as a list")
	}
}
