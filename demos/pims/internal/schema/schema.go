// Package schema installs the PIMS tables and the Debezium replication user
// in MySQL.
package schema

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"

	"pims/internal/retry"
)

// Database is the MySQL schema PIMS uses.
const Database = "pims"

// Tables lists the captured tables in dependency order.
var Tables = []string{"patients", "appointments", "visits"}

// DebeziumUser is the MySQL account the Debezium connector logs in as.
const DebeziumUser = "debezium"

//go:embed schema.sql
var schemaSQL string

// Conn describes how to reach MySQL.
type Conn struct {
	Host     string
	Port     int
	User     string
	Password string
}

// LogValue omits the password.
func (c Conn) LogValue() slog.Value {
	return slog.GroupValue(slog.String("host", c.Host), slog.Int("port", c.Port), slog.String("user", c.User))
}

// DSN returns a go-sql-driver DSN that parses DATETIME columns as UTC
// time.Time values.
func (c Conn) DSN() string {
	cfg := mysql.NewConfig()
	cfg.User = c.User
	cfg.Passwd = c.Password
	cfg.Net = "tcp"
	cfg.Addr = net.JoinHostPort(c.Host, strconv.Itoa(c.Port))
	cfg.DBName = Database
	cfg.ParseTime = true
	cfg.Loc = time.UTC
	cfg.Timeout = 5 * time.Second
	cfg.ReadTimeout = 30 * time.Second
	cfg.WriteTimeout = 30 * time.Second
	// Client-side interpolation lets CREATE USER take a bound password.
	cfg.InterpolateParams = true
	cfg.Params = map[string]string{"time_zone": "'+00:00'"}
	return cfg.FormatDSN()
}

// driverLogger routes go-sql-driver's internal messages (for example
// "unexpected EOF" while MySQL is starting) through slog.
type driverLogger struct{ log *slog.Logger }

func (d driverLogger) Print(v ...any) {
	d.log.Debug("MySQL driver message", "message", fmt.Sprint(v...))
}

// Open opens a connection pool and waits until MySQL accepts connections.
func Open(ctx context.Context, c Conn, log *slog.Logger) (*sql.DB, error) {
	// The driver logger is process-wide; the last caller's logger wins,
	// which is fine because PIMS uses a single logger.
	_ = mysql.SetLogger(driverLogger{log: log.With("component", "mysql")})

	db, err := sql.Open("mysql", c.DSN())
	if err != nil {
		return nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(8)
	db.SetConnMaxLifetime(30 * time.Minute)

	p := retry.Default
	p.OnRetry = func(attempt int, err error, wait time.Duration) {
		log.Info("Waiting for MySQL", "attempt", attempt, "retry_in", wait.Round(time.Millisecond), "error", err)
	}
	err = retry.Do(ctx, p, func(ctx context.Context) error {
		pctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return db.PingContext(pctx)
	})
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("connect to mysql at %s:%d: %w", c.Host, c.Port, err)
	}
	return db, nil
}

// Install creates the PIMS tables and the Debezium user. It must run as a
// user allowed to create accounts (root). It is safe to run repeatedly.
func Install(ctx context.Context, db *sql.DB, debeziumPassword string, log *slog.Logger) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	for _, stmt := range Statements() {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("install schema: %w (statement: %.60s...)", err, stmt)
		}
	}
	log.Info("Installed PIMS tables", "database", Database, "tables", Tables)

	userStmts := []struct {
		query string
		args  []any
	}{
		{"CREATE USER IF NOT EXISTS 'debezium'@'%' IDENTIFIED BY ?", []any{debeziumPassword}},
		// Keep the password in sync if the user already existed.
		{"ALTER USER 'debezium'@'%' IDENTIFIED BY ?", []any{debeziumPassword}},
		// The privileges Debezium documents for the MySQL connector.
		{"GRANT SELECT, RELOAD, SHOW DATABASES, REPLICATION SLAVE, REPLICATION CLIENT, LOCK TABLES ON *.* TO 'debezium'@'%'", nil},
	}
	for _, s := range userStmts {
		if _, err := db.ExecContext(ctx, s.query, s.args...); err != nil {
			// Never include args in the error: they contain the password.
			return fmt.Errorf("configure debezium user: %w", err)
		}
	}
	log.Info("Configured Debezium replication user", "user", DebeziumUser)
	return nil
}

// Statements splits the embedded schema into individual statements,
// dropping comments.
func Statements() []string {
	var b strings.Builder
	for _, line := range strings.Split(schemaSQL, "\n") {
		if t := strings.TrimSpace(line); t == "" || strings.HasPrefix(t, "--") {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	var out []string
	for _, s := range strings.Split(b.String(), ";") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}
