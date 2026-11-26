// Package demoif checks the interface a demo's Terraform must implement. See docs/specs/001_design.md.
package demoif

import (
	"regexp"
	"sort"
	"strings"
)

const requiredOutput = "launchpad_url"

var (
	requiredVariables  = []string{"aws_region", "launchpad_tags"}
	confluentVariables = []string{"confluent_cloud_api_key", "confluent_cloud_api_secret"}

	variablePattern = regexp.MustCompile(`(?m)^\s*variable\s+"([^"]+)"`)
	outputPattern   = regexp.MustCompile(`(?m)^\s*output\s+"([^"]+)"`)
	backendPattern  = regexp.MustCompile(`\bbackend\s+"[^"]+"\s*\{`)
	blockComment    = regexp.MustCompile(`(?s)/\*.*?\*/`)
	lineComment     = regexp.MustCompile(`(?m)(#|//).*$`)
)

// Result is what Check found.
type Result struct {
	Problems       []string
	NeedsConfluent bool
}

// Valid reports whether the demo follows the interface.
func (r Result) Valid() bool { return len(r.Problems) == 0 }

func code(text string) string {
	return lineComment.ReplaceAllString(blockComment.ReplaceAllString(text, ""), "")
}

func names(pattern *regexp.Regexp, text string) map[string]bool {
	found := map[string]bool{}
	for _, m := range pattern.FindAllStringSubmatch(text, -1) {
		found[m[1]] = true
	}
	return found
}

// Check checks the top-level .tf files of a demo's tf directory, by file name.
func Check(tfFiles map[string]string) Result {
	// Problems is never nil, so it is a JSON array and a DynamoDB list.
	result := Result{Problems: []string{}}
	if len(tfFiles) == 0 {
		result.Problems = append(result.Problems, "tf has no .tf files")
		return result
	}
	files := make([]string, 0, len(tfFiles))
	for name := range tfFiles {
		files = append(files, name)
	}
	sort.Strings(files)
	parts := make([]string, len(files))
	for i, name := range files {
		parts[i] = code(tfFiles[name])
	}
	text := strings.Join(parts, "\n")

	variables := names(variablePattern, text)
	outputs := names(outputPattern, text)
	for _, name := range requiredVariables {
		if !variables[name] {
			result.Problems = append(result.Problems, `missing variable "`+name+`"`)
		}
	}
	if !outputs[requiredOutput] {
		result.Problems = append(result.Problems, `missing output "`+requiredOutput+`"`)
	}
	if backendPattern.MatchString(text) {
		result.Problems = append(result.Problems, "declares a backend, but Launchpad supplies the S3 backend")
	}
	declared := 0
	for _, name := range confluentVariables {
		if variables[name] {
			declared++
		}
	}
	if declared > 0 && declared < len(confluentVariables) {
		result.Problems = append(result.Problems, "declare both confluent_cloud_api_key and confluent_cloud_api_secret, or neither")
	}
	result.NeedsConfluent = declared == len(confluentVariables)
	return result
}
