package schema

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
)

func TestStatementsSplitsSchema(t *testing.T) {
	stmts := Statements()
	if len(stmts) != len(Tables) {
		t.Fatalf("got %d statements, want %d", len(stmts), len(Tables))
	}
	for i, table := range Tables {
		if !strings.HasPrefix(stmts[i], "CREATE TABLE IF NOT EXISTS "+table) {
			t.Errorf("statement %d = %.40q, want CREATE TABLE %s first", i, stmts[i], table)
		}
		for _, col := range []string{"created_at", "updated_at", "PRIMARY KEY"} {
			if !strings.Contains(stmts[i], col) {
				t.Errorf("table %s is missing %s", table, col)
			}
		}
	}
}

func TestDSNIsUTCAndHidesPasswordFromLogs(t *testing.T) {
	c := Conn{Host: "127.0.0.1", Port: 33306, User: "root", Password: "pw"}
	cfg, err := mysql.ParseDSN(c.DSN())
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.ParseTime || cfg.Loc != time.UTC || cfg.DBName != Database {
		t.Fatalf("unexpected DSN config: %+v", cfg)
	}
	if v := c.LogValue().String(); strings.Contains(v, "pw") {
		t.Fatalf("LogValue leaked password: %s", v)
	}
}

// TestInstallAgainstMySQL runs the installer twice against a real MySQL,
// for example one run with `docker run` (see docs/CONTRIBUTING.md), to prove it is idempotent and
// that the same-day rule is enforced. Set PIMS_TEST_MYSQL_ROOT_PASSWORD and
// optionally PIMS_TEST_MYSQL_PORT (default 33306).
func TestInstallAgainstMySQL(t *testing.T) {
	pw := os.Getenv("PIMS_TEST_MYSQL_ROOT_PASSWORD")
	if pw == "" {
		t.Skip("set PIMS_TEST_MYSQL_ROOT_PASSWORD to run")
	}
	port := 33306
	if p := os.Getenv("PIMS_TEST_MYSQL_PORT"); p != "" {
		port, _ = strconv.Atoi(p)
	}
	ctx := context.Background()
	log := slog.New(slog.DiscardHandler)
	db, err := Open(ctx, Conn{Host: "127.0.0.1", Port: port, User: "root", Password: pw}, log)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	for i := range 2 {
		if err := Install(ctx, db, "dbz-test-password", log); err != nil {
			t.Fatalf("install #%d: %v", i+1, err)
		}
	}

	mrn := fmt.Sprintf("T%d", time.Now().UnixNano()%1e12)
	res, err := db.ExecContext(ctx, `INSERT INTO patients (mrn, first_name, last_name, date_of_birth, sex) VALUES (?, 'Test', 'Patient', '1980-01-01', 'F')`, mrn)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := res.LastInsertId()
	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM appointments WHERE patient_id = ?`, pid)
		_, _ = db.ExecContext(ctx, `DELETE FROM patients WHERE id = ?`, pid)
	})

	book := func(at string, provider string) error {
		_, err := db.ExecContext(ctx, `INSERT INTO appointments (patient_id, provider, department, scheduled_at, appointment_date, duration_minutes)
			VALUES (?, ?, 'General Practice', ?, DATE(?), 15)`, pid, provider, at, at)
		return err
	}
	if err := book("2030-01-07 09:00:00", "Dr Test A "+mrn); err != nil {
		t.Fatal(err)
	}
	err = book("2030-01-07 14:00:00", "Dr Test B "+mrn)
	var me *mysql.MySQLError
	if !errors.As(err, &me) || me.Number != 1062 {
		t.Fatalf("second same-day appointment: err = %v, want duplicate key (1062)", err)
	}

	var created, updated sql.NullTime
	if err := db.QueryRowContext(ctx, `SELECT created_at, updated_at FROM patients WHERE id = ?`, pid).Scan(&created, &updated); err != nil {
		t.Fatal(err)
	}
	if !created.Valid || time.Since(created.Time).Abs() > time.Minute {
		t.Fatalf("created_at = %v, want close to now in UTC", created.Time)
	}
}
