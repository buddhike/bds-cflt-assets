package compose

import (
	"bytes"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestMySQLSecretsNeverLogPasswords(t *testing.T) {
	s := MySQLSecrets{RootPassword: "rootpw", AppPassword: "apppw", DebeziumPassword: "dbzpw", Generation: "g1"}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("Secrets", "secrets", s)
	got := buf.String()
	for _, pw := range []string{"rootpw", "apppw", "dbzpw"} {
		if strings.Contains(got, pw) {
			t.Fatalf("log leaked %q: %s", pw, got)
		}
	}
	if !strings.Contains(got, "g1") {
		t.Fatalf("log = %s, want the generation", got)
	}
}

// A failure must not blame the first word of the command: callers wrap the
// program in others such as timeout, which would read as the cause.
func TestExecErrorNamesTheServiceAndStatus(t *testing.T) {
	err := &ExecError{Service: "duckdb", Err: errors.New("exit status 3"), Stderr: "Parser Error"}
	if want := "exec in duckdb: exit status 3: Parser Error"; err.Error() != want {
		t.Fatalf("error = %q, want %q", err, want)
	}
	if !errors.Is(err, err.Err) {
		t.Fatal("ExecError must unwrap to how the command ended")
	}
}
