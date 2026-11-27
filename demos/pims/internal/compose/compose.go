// Package compose holds the types the pipeline and the DuckDB exec path share
// from the time the demo's local services ran under Docker Compose: the MySQL
// credentials and Debezium generation, and the error of a command that failed
// inside a service. The server runs no Compose project; the sibling containers
// of the ECS task replace it.
//
// The package directory also holds the Kafka Connect image, built from
// project/connect/Dockerfile by the image workflow.
package compose

import (
	"errors"
	"fmt"
	"log/slog"
)

// MySQLSecrets are the passwords for the demo database and the identity of
// this database instance. Launchpad supplies the passwords through the task's
// environment.
type MySQLSecrets struct {
	RootPassword     string `json:"rootPassword"`
	AppPassword      string `json:"appPassword"`
	DebeziumPassword string `json:"debeziumPassword"`
	// Generation identifies this database instance. Debezium state in
	// Confluent Cloud is keyed by it, so a recreated database starts a
	// fresh snapshot instead of resuming stale binlog offsets.
	Generation string `json:"generation"`
}

// LogValue keeps passwords out of logs.
func (s MySQLSecrets) LogValue() slog.Value {
	return slog.GroupValue(slog.String("generation", s.Generation), slog.String("passwords", "[REDACTED]"))
}

// ErrOutputTooLarge is returned when a command writes more to stdout than the
// caller allowed.
var ErrOutputTooLarge = errors.New("command output is too large")

// ExecError is a command that failed inside a service. Its text names the
// service and the exit status but not the command: callers may wrap the
// program in another such as timeout, which would then read as the cause of
// the failure.
type ExecError struct {
	Service string
	// Err is how the command ended, for example an exit status.
	Err error
	// Stderr is what the command wrote to stderr, scrubbed of secrets and
	// truncated.
	Stderr string
}

func (e *ExecError) Error() string {
	return fmt.Sprintf("exec in %s: %v: %s", e.Service, e.Err, e.Stderr)
}

func (e *ExecError) Unwrap() error { return e.Err }
