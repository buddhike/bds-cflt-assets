// Package config reads the settings the functions get from their environment.
package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Settings are read once at start-up.
type Settings struct {
	TableName      string
	KMSKeyID       string
	StateBucket    string
	Region         string
	SuperUserEmail string // the one person who can sign in, lower case
	DemosRepo      string // owner/name
	DemosPath      string // directory in the repository that holds the demos
	DemosRef       string // branch or tag the demos are read from
	GitHubToken    string
	RunnerFunction string
	SignInSecret   string // ARN of the secret that holds the phone number codes go to
	SessionHours   int
}

// FromEnv builds Settings from an environment lookup such as os.Getenv.
// TABLE_NAME, KMS_KEY_ID and SUPER_USER_EMAIL are required.
func FromEnv(getenv func(string) string) (Settings, error) {
	get := func(key, fallback string) string {
		if v := getenv(key); v != "" {
			return v
		}
		return fallback
	}
	var missing []string
	require := func(key string) string {
		v := getenv(key)
		if v == "" {
			missing = append(missing, key)
		}
		return v
	}

	s := Settings{
		TableName:      require("TABLE_NAME"),
		KMSKeyID:       require("KMS_KEY_ID"),
		StateBucket:    getenv("STATE_BUCKET"),
		Region:         get("AWS_REGION", "us-east-1"),
		SuperUserEmail: strings.ToLower(strings.TrimSpace(require("SUPER_USER_EMAIL"))),
		DemosRepo:      get("DEMOS_REPO", "buddhike/bds-cflt-assets"),
		DemosPath:      strings.Trim(get("DEMOS_PATH", "demos"), "/"),
		DemosRef:       get("DEMOS_REF", "main"),
		GitHubToken:    getenv("GITHUB_TOKEN"),
		RunnerFunction: getenv("RUNNER_FUNCTION"),
		SignInSecret:   getenv("SIGN_IN_SECRET"),
		SessionHours:   12,
	}
	if len(missing) > 0 {
		return Settings{}, fmt.Errorf("missing environment variables: %s", strings.Join(missing, ", "))
	}
	if v := getenv("SESSION_HOURS"); v != "" {
		hours, err := strconv.Atoi(v)
		if err != nil || hours <= 0 {
			return Settings{}, fmt.Errorf("SESSION_HOURS must be a positive whole number, got %q", v)
		}
		s.SessionHours = hours
	}
	return s, nil
}
