package config

import (
	"strings"
	"testing"
)

func env(values map[string]string) func(string) string {
	return func(k string) string { return values[k] }
}

var required = map[string]string{"TABLE_NAME": "t", "KMS_KEY_ID": "k", "SUPER_USER_EMAIL": "  Boss@Confluent.io "}

func with(extra map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range required {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func TestDefaults(t *testing.T) {
	s, err := FromEnv(env(required))
	if err != nil {
		t.Fatal(err)
	}
	if s.SuperUserEmail != "boss@confluent.io" || s.DemosPath != "demos" || s.DemosRef != "main" || s.DemosRepo != "buddhike/bds-cflt-assets" ||
		s.Region != "us-east-1" || s.SessionHours != 12 || s.StateBucket != "" || s.SignInSecret != "" {
		t.Fatalf("%+v", s)
	}
}

func TestValuesAreRead(t *testing.T) {
	s, err := FromEnv(env(with(map[string]string{
		"STATE_BUCKET": "b", "AWS_REGION": "ap-southeast-2", "DEMOS_REPO": "o/r", "DEMOS_PATH": "/x/y/", "DEMOS_REF": "dev",
		"GITHUB_TOKEN": "tok", "RUNNER_FUNCTION": "fn", "SIGN_IN_SECRET": "arn", "SESSION_HOURS": "3",
	})))
	if err != nil {
		t.Fatal(err)
	}
	if s.DemosPath != "x/y" || s.Region != "ap-southeast-2" || s.SessionHours != 3 || s.RunnerFunction != "fn" || s.GitHubToken != "tok" || s.StateBucket != "b" {
		t.Fatalf("%+v", s)
	}
}

func TestMissingRequiredVariablesAreAllNamed(t *testing.T) {
	_, err := FromEnv(env(map[string]string{}))
	if err == nil {
		t.Fatal("want an error")
	}
	for _, name := range []string{"TABLE_NAME", "KMS_KEY_ID", "SUPER_USER_EMAIL"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("%q does not name %s", err, name)
		}
	}
}

func TestABadSessionLengthIsAnError(t *testing.T) {
	for _, v := range []string{"abc", "0", "-2", "1.5"} {
		if _, err := FromEnv(env(with(map[string]string{"SESSION_HOURS": v}))); err == nil {
			t.Errorf("SESSION_HOURS=%q was accepted", v)
		}
	}
}
