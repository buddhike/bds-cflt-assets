package terraform

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

func TestOutputsNeverLogSecrets(t *testing.T) {
	o := Outputs{
		EnvironmentID:           "env-1",
		KafkaAPISecret:          "kafka-secret",
		SchemaRegistryAPISecret: "sr-secret",
		TableflowAPISecret:      "tf-secret",
		Topics:                  []string{"patients"},
	}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("Outputs", "outputs", o)
	got := buf.String()
	for _, secret := range []string{"kafka-secret", "sr-secret", "tf-secret"} {
		if strings.Contains(got, secret) {
			t.Fatalf("log leaked %q: %s", secret, got)
		}
	}
	if !strings.Contains(got, "env-1") {
		t.Fatalf("log = %s, want the environment id", got)
	}
}
