package sim

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/go-sql-driver/mysql"
)

// mysqlDuplicateEntry is ER_DUP_ENTRY.
const mysqlDuplicateEntry = 1062

// MySQLStore persists simulated events to the PIMS schema.
type MySQLStore struct {
	db *sql.DB
}

// NewMySQLStore returns a Store backed by db.
func NewMySQLStore(db *sql.DB) *MySQLStore { return &MySQLStore{db: db} }

func classify(err error) error {
	var me *mysql.MySQLError
	if errors.As(err, &me) && me.Number == mysqlDuplicateEntry {
		return fmt.Errorf("%w: %v", ErrConflict, err)
	}
	return err
}

func nullString(s string) sql.NullString { return sql.NullString{String: s, Valid: s != ""} }

// InsertPatient implements Store.
func (s *MySQLStore) InsertPatient(ctx context.Context, p Patient) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO patients (mrn, first_name, last_name, date_of_birth, sex, phone, email, address_line, city, state, postcode)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		p.MRN, p.FirstName, p.LastName, p.DateOfBirth, p.Sex,
		nullString(p.Phone), nullString(p.Email), nullString(p.Address), nullString(p.City), nullString(p.State), nullString(p.Postcode))
	if err != nil {
		return 0, classify(err)
	}
	return res.LastInsertId()
}

// UpdatePatientContact implements Store.
func (s *MySQLStore) UpdatePatientContact(ctx context.Context, id int64, c ContactChange) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE patients SET phone = ?, address_line = ?, city = ?, state = ?, postcode = ? WHERE id = ?`,
		nullString(c.Phone), nullString(c.Address), nullString(c.City), nullString(c.State), nullString(c.Postcode), id)
	return classify(err)
}

// InsertAppointment implements Store.
func (s *MySQLStore) InsertAppointment(ctx context.Context, a Appointment) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
		INSERT INTO appointments (patient_id, provider, department, scheduled_at, appointment_date, duration_minutes, reason, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.PatientID, a.Provider, a.Department, a.ScheduledAt, dateKey(a.ScheduledAt), int(a.Duration/time.Minute), nullString(a.Reason), StatusScheduled)
	if err != nil {
		return 0, classify(err)
	}
	return res.LastInsertId()
}

// SetAppointmentStatus implements Store.
func (s *MySQLStore) SetAppointmentStatus(ctx context.Context, id int64, status string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE appointments SET status = ? WHERE id = ?`, status, id)
	return classify(err)
}

// CheckIn implements Store.
func (s *MySQLStore) CheckIn(ctx context.Context, v Visit) (int64, error) {
	var id int64
	err := s.tx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO visits (patient_id, appointment_id, visit_type, provider, department, reason, status, check_in_at)
			VALUES (?, ?, ?, ?, ?, ?, 'IN_PROGRESS', ?)`,
			v.PatientID, v.AppointmentID, v.Type, v.Provider, v.Department, nullString(v.Reason), v.CheckIn)
		if err != nil {
			return err
		}
		if id, err = res.LastInsertId(); err != nil {
			return err
		}
		if v.AppointmentID != nil {
			_, err = tx.ExecContext(ctx, `UPDATE appointments SET status = ? WHERE id = ?`, StatusCheckedIn, *v.AppointmentID)
		}
		return err
	})
	return id, err
}

// CompleteVisit implements Store.
func (s *MySQLStore) CompleteVisit(ctx context.Context, v Visit, checkOut time.Time, diagnosis string) error {
	return s.tx(ctx, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE visits SET status = 'COMPLETED', check_out_at = ?, diagnosis_code = ? WHERE id = ?`,
			checkOut, diagnosis, v.ID)
		if err != nil {
			return err
		}
		if v.AppointmentID != nil {
			_, err = tx.ExecContext(ctx, `UPDATE appointments SET status = ? WHERE id = ?`, StatusCompleted, *v.AppointmentID)
		}
		return err
	})
}

func (s *MySQLStore) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return classify(err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// Load implements Store. It reads only what the model needs: patient ids,
// bookings from the given day onwards, appointments awaiting check-in and
// visits still in progress.
func (s *MySQLStore) Load(ctx context.Context, from time.Time) (Snapshot, error) {
	var snap Snapshot

	rows, err := s.db.QueryContext(ctx, `SELECT id FROM patients`)
	if err != nil {
		return snap, fmt.Errorf("load patients: %w", err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return snap, fmt.Errorf("scan patient: %w", err)
		}
		snap.Patients = append(snap.Patients, id)
	}
	if err := closeRows(rows); err != nil {
		return snap, fmt.Errorf("load patients: %w", err)
	}

	rows, err = s.db.QueryContext(ctx, `
		SELECT id, patient_id, provider, department, scheduled_at, duration_minutes, COALESCE(reason, ''), status
		FROM appointments WHERE appointment_date >= ? OR status = ?
		ORDER BY scheduled_at`, dateKey(from), StatusScheduled)
	if err != nil {
		return snap, fmt.Errorf("load appointments: %w", err)
	}
	for rows.Next() {
		var a Appointment
		var minutes int
		var status string
		if err := rows.Scan(&a.ID, &a.PatientID, &a.Provider, &a.Department, &a.ScheduledAt, &minutes, &a.Reason, &status); err != nil {
			rows.Close()
			return snap, fmt.Errorf("scan appointment: %w", err)
		}
		a.Duration = time.Duration(minutes) * time.Minute
		snap.Booked = append(snap.Booked, a)
		if status == StatusScheduled {
			snap.Scheduled = append(snap.Scheduled, a)
		}
		if a.ScheduledAt.After(snap.Latest) && status != StatusScheduled {
			snap.Latest = a.ScheduledAt
		}
	}
	if err := closeRows(rows); err != nil {
		return snap, fmt.Errorf("load appointments: %w", err)
	}

	rows, err = s.db.QueryContext(ctx, `
		SELECT id, patient_id, appointment_id, visit_type, provider, department, COALESCE(reason, ''), check_in_at
		FROM visits WHERE status = 'IN_PROGRESS'`)
	if err != nil {
		return snap, fmt.Errorf("load visits: %w", err)
	}
	for rows.Next() {
		var v Visit
		var appt sql.NullInt64
		if err := rows.Scan(&v.ID, &v.PatientID, &appt, &v.Type, &v.Provider, &v.Department, &v.Reason, &v.CheckIn); err != nil {
			rows.Close()
			return snap, fmt.Errorf("scan visit: %w", err)
		}
		if appt.Valid {
			id := appt.Int64
			v.AppointmentID = &id
		}
		snap.OpenVisits = append(snap.OpenVisits, v)
		if v.CheckIn.After(snap.Latest) {
			snap.Latest = v.CheckIn
		}
	}
	if err := closeRows(rows); err != nil {
		return snap, fmt.Errorf("load visits: %w", err)
	}

	var latest sql.NullTime
	if err := s.db.QueryRowContext(ctx, `SELECT MAX(COALESCE(check_out_at, check_in_at)) FROM visits`).Scan(&latest); err != nil {
		return snap, fmt.Errorf("load latest visit time: %w", err)
	}
	if latest.Valid && latest.Time.After(snap.Latest) {
		snap.Latest = latest.Time
	}
	return snap, nil
}

func closeRows(rows *sql.Rows) error {
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	return rows.Close()
}
