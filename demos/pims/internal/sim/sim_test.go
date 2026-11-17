package sim

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"

	"pims/internal/schema"
)

// fakeStore keeps rows in memory and enforces the same uniqueness rules
// as the MySQL schema, so tests can check the simulator never relies on
// the database to catch rule violations.
type fakeStore struct {
	mu           sync.Mutex
	nextID       int64
	patients     map[int64]Patient
	appointments map[int64]*fakeAppt
	visits       map[int64]*fakeVisit
	patientDay   map[string]bool
	providerSlot map[string]bool
	conflicts    int
	failAfter    int // fail every call once this many calls were made (0 = never)
	calls        int
}

type fakeAppt struct {
	Appointment
	Status string
}

type fakeVisit struct {
	Visit
	Status   string
	CheckOut time.Time
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		patients:     map[int64]Patient{},
		appointments: map[int64]*fakeAppt{},
		visits:       map[int64]*fakeVisit{},
		patientDay:   map[string]bool{},
		providerSlot: map[string]bool{},
	}
}

func (f *fakeStore) call() error {
	f.calls++
	if f.failAfter > 0 && f.calls > f.failAfter {
		return errors.New("connection refused")
	}
	return nil
}

func (f *fakeStore) id() int64 { f.nextID++; return f.nextID }

func (f *fakeStore) Load(context.Context, time.Time) (Snapshot, error) { return Snapshot{}, nil }

func (f *fakeStore) InsertPatient(_ context.Context, p Patient) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call(); err != nil {
		return 0, err
	}
	id := f.id()
	f.patients[id] = p
	return id, nil
}

func (f *fakeStore) UpdatePatientContact(_ context.Context, id int64, _ ContactChange) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.patients[id]; !ok {
		return fmt.Errorf("no patient %d", id)
	}
	return f.call()
}

func (f *fakeStore) InsertAppointment(_ context.Context, a Appointment) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call(); err != nil {
		return 0, err
	}
	dayKey := fmt.Sprintf("%d/%s", a.PatientID, dateKey(a.ScheduledAt))
	slotKey := a.Provider + "/" + a.ScheduledAt.String()
	if f.patientDay[dayKey] || f.providerSlot[slotKey] {
		f.conflicts++
		return 0, ErrConflict
	}
	f.patientDay[dayKey], f.providerSlot[slotKey] = true, true
	a.ID = f.id()
	f.appointments[a.ID] = &fakeAppt{Appointment: a, Status: StatusScheduled}
	return a.ID, nil
}

func (f *fakeStore) SetAppointmentStatus(_ context.Context, id int64, status string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call(); err != nil {
		return err
	}
	f.appointments[id].Status = status
	return nil
}

func (f *fakeStore) CheckIn(_ context.Context, v Visit) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call(); err != nil {
		return 0, err
	}
	v.ID = f.id()
	f.visits[v.ID] = &fakeVisit{Visit: v, Status: "IN_PROGRESS"}
	if v.AppointmentID != nil {
		f.appointments[*v.AppointmentID].Status = StatusCheckedIn
	}
	return v.ID, nil
}

func (f *fakeStore) CompleteVisit(_ context.Context, v Visit, out time.Time, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.call(); err != nil {
		return err
	}
	fv := f.visits[v.ID]
	fv.Status, fv.CheckOut = "COMPLETED", out
	if v.AppointmentID != nil {
		f.appointments[*v.AppointmentID].Status = StatusCompleted
	}
	return nil
}

func newTestSim(store Store) *Simulator {
	s := New(store, slog.New(slog.DiscardHandler), 42)
	start := time.Date(2030, 3, 4, 7, 0, 0, 0, time.UTC) // a Monday, before opening
	s.m = newModel(start)
	return s
}

// TestSimulationInvariants runs many events and checks the domain rules
// hold, without the store ever reporting a conflict.
func TestSimulationInvariants(t *testing.T) {
	store := newFakeStore()
	s := newTestSim(store)
	ctx := context.Background()

	for i := range 20000 {
		kind, err := s.step(ctx)
		if err != nil {
			t.Fatalf("step %d (%s): %v", i, kind, err)
		}
		s.record(kind, err)
	}

	if store.conflicts != 0 {
		t.Errorf("store reported %d conflicts; the model should prevent them", store.conflicts)
	}

	st := s.Stats()
	for _, k := range []Kind{KindRegister, KindBook, KindCheckIn, KindComplete, KindWalkIn, KindNoShow, KindCancel, KindUpdate} {
		if st.ByKind[k] == 0 {
			t.Errorf("no %s events in 20000 steps", k)
		}
	}

	visitsByAppt := map[int64]int{}
	for _, v := range store.visits {
		if !isOpen(v.CheckIn) {
			t.Errorf("visit %d checked in outside clinic hours at %v", v.ID, v.CheckIn)
		}
		if v.Status == "COMPLETED" && v.CheckOut.Before(v.CheckIn) {
			t.Errorf("visit %d checked out before check-in", v.ID)
		}
		if v.AppointmentID != nil {
			visitsByAppt[*v.AppointmentID]++
			a := store.appointments[*v.AppointmentID]
			if v.CheckIn.Before(a.ScheduledAt) {
				t.Errorf("visit %d checked in before its appointment time", v.ID)
			}
			if a.Status != StatusCheckedIn && a.Status != StatusCompleted {
				t.Errorf("appointment %d has a visit but status %s", a.ID, a.Status)
			}
		}
	}
	for id, n := range visitsByAppt {
		if n > 1 {
			t.Errorf("appointment %d has %d visits", id, n)
		}
	}
	byProvider := map[string][]*fakeAppt{}
	for _, a := range store.appointments {
		if !isOpen(a.ScheduledAt) {
			t.Errorf("appointment %d booked outside clinic hours at %v", a.ID, a.ScheduledAt)
		}
		end := a.ScheduledAt.Add(a.Duration)
		if end.Hour()*60+end.Minute() > closeHour*60 {
			t.Errorf("appointment %d ends after closing at %v", a.ID, end)
		}
		byProvider[a.Provider] = append(byProvider[a.Provider], a)
	}
	// A provider never has overlapping appointments.
	for prov, appts := range byProvider {
		for i, x := range appts {
			for _, y := range appts[i+1:] {
				if x.ScheduledAt.Before(y.ScheduledAt.Add(y.Duration)) && y.ScheduledAt.Before(x.ScheduledAt.Add(x.Duration)) {
					t.Errorf("%s double booked: %v (%v) overlaps %v (%v)", prov, x.ScheduledAt, x.Duration, y.ScheduledAt, y.Duration)
				}
			}
		}
	}

	// A patient is never in two visits at once.
	open := map[int64]int{}
	for _, v := range store.visits {
		if v.Status == "IN_PROGRESS" {
			open[v.PatientID]++
		}
	}
	for pid, n := range open {
		if n > 1 {
			t.Errorf("patient %d has %d visits in progress", pid, n)
		}
	}
}

func TestSimulatorStopsAfterRepeatedErrors(t *testing.T) {
	store := newFakeStore()
	store.failAfter = 1
	s := New(store, slog.New(slog.DiscardHandler), 1)

	if err := s.Start(context.Background(), MaxRate); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for s.Stats().Running {
		select {
		case <-deadline:
			s.Stop()
			t.Fatal("simulation kept running despite repeated errors")
		case <-time.After(10 * time.Millisecond):
		}
	}
	st := s.Stats()
	if st.Errors < maxConsecutiveErrors || st.LastError == "" {
		t.Fatalf("stats = %+v, want at least %d errors recorded", st, maxConsecutiveErrors)
	}
}

func TestStartTwiceAndStop(t *testing.T) {
	s := New(newFakeStore(), slog.New(slog.DiscardHandler), 1)
	ctx := context.Background()
	if err := s.Start(ctx, 50); err != nil {
		t.Fatal(err)
	}
	if err := s.Start(ctx, 50); !errors.Is(err, ErrAlreadyRunning) {
		t.Fatalf("second Start() = %v, want ErrAlreadyRunning", err)
	}
	s.Stop()
	if s.Stats().Running {
		t.Fatal("still running after Stop")
	}
	// It can be started again after stopping.
	if err := s.Start(ctx, 50); err != nil {
		t.Fatalf("restart: %v", err)
	}
	s.Stop()
}

func TestSetRateClamps(t *testing.T) {
	s := New(newFakeStore(), slog.New(slog.DiscardHandler), 1)
	s.SetRate(1e6)
	if got := s.Stats().Rate; got != MaxRate {
		t.Fatalf("rate = %v, want %v", got, MaxRate)
	}
	s.SetRate(0)
	if got := s.Stats().Rate; got != MinRate {
		t.Fatalf("rate = %v, want %v", got, MinRate)
	}
}

// TestMySQLStoreEndToEnd drives the simulator against a real MySQL with
// the PIMS schema, then restarts it from the database. Set
// PIMS_TEST_MYSQL_ROOT_PASSWORD (and PIMS_TEST_MYSQL_PORT) to run.
func TestMySQLStoreEndToEnd(t *testing.T) {
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
	db, err := schema.Open(ctx, schema.Conn{Host: "127.0.0.1", Port: port, User: "root", Password: pw}, log)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := schema.Install(ctx, db, "dbz", log); err != nil {
		t.Fatal(err)
	}

	store := NewMySQLStore(db)
	for run := range 2 {
		s := New(store, log, uint64(run+1))
		if err := s.Start(ctx, MaxRate); err != nil {
			t.Fatal(err)
		}
		time.Sleep(3 * time.Second)
		s.Stop()
		st := s.Stats()
		if st.Errors != 0 {
			t.Fatalf("run %d: %d errors, last: %s", run, st.Errors, st.LastError)
		}
		if st.Events < 50 {
			t.Fatalf("run %d: only %d events", run, st.Events)
		}
		t.Logf("run %d: %d events, %d conflicts, by kind %v", run, st.Events, st.Conflicts, st.ByKind)
	}

	var dupDays int
	err = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (SELECT patient_id, appointment_date FROM appointments GROUP BY 1, 2 HAVING COUNT(*) > 1) d`).Scan(&dupDays)
	if err != nil {
		t.Fatal(err)
	}
	if dupDays != 0 {
		t.Fatalf("%d patients have more than one appointment on a day", dupDays)
	}
	var badVisits int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM visits WHERE updated_at < created_at OR (check_out_at IS NOT NULL AND check_out_at < check_in_at)`).Scan(&badVisits); err != nil {
		t.Fatal(err)
	}
	if badVisits != 0 {
		t.Fatalf("%d visits have inconsistent timestamps", badVisits)
	}
}

// conflictingStore reports a conflict for every check-in, as happens when
// an earlier check-in committed but its context timed out.
type conflictingStore struct{ *fakeStore }

func (c conflictingStore) CheckIn(context.Context, Visit) (int64, error) { return 0, ErrConflict }

func TestCheckInConflictDoesNotBlockLaterAppointments(t *testing.T) {
	s := newTestSim(conflictingStore{newFakeStore()})
	m := s.m
	m.now = time.Date(2030, 3, 4, 10, 0, 0, 0, time.UTC)
	m.patients = []int64{1, 2}
	m.insertScheduled(Appointment{ID: 10, PatientID: 1, ScheduledAt: m.now.Add(-5 * time.Minute)})
	m.insertScheduled(Appointment{ID: 11, PatientID: 2, ScheduledAt: m.now.Add(-time.Minute)})

	if err := s.checkIn(context.Background(), m); !errors.Is(err, ErrConflict) {
		t.Fatalf("checkIn() = %v, want conflict", err)
	}
	if i := m.due(); i < 0 || m.scheduled[i].ID != 11 {
		t.Fatalf("after a conflict the next due appointment should be 11, got index %d (%v)", i, m.scheduled)
	}
}

func TestRestoreDoesNotJumpClockForFutureBookings(t *testing.T) {
	now := time.Date(2030, 3, 4, 10, 0, 0, 0, time.UTC)
	m := newModel(now)
	m.restore(Snapshot{
		Patients: []int64{1},
		// A cancelled appointment a week out must not move the clock.
		Booked: []Appointment{{ID: 1, PatientID: 1, Provider: "Dr X", ScheduledAt: now.AddDate(0, 0, 7), Duration: slot}},
		Latest: now.Add(-time.Hour),
	})
	if !m.now.Equal(now) {
		t.Fatalf("clock = %v, want %v", m.now, now)
	}
	if m.canBook(1, "Dr X", now.AddDate(0, 0, 7), slot) {
		t.Fatal("restored booking should still block the slot")
	}
}

func TestCanBookRejectsOverlapAndLateFinish(t *testing.T) {
	m := newModel(time.Date(2030, 3, 4, 8, 0, 0, 0, time.UTC))
	at := time.Date(2030, 3, 5, 10, 0, 0, 0, time.UTC)
	m.book(Appointment{PatientID: 1, Provider: "Dr X", ScheduledAt: at, Duration: 30 * time.Minute})

	if m.canBook(2, "Dr X", at.Add(15*time.Minute), slot) {
		t.Fatal("10:15 overlaps a 30 minute appointment at 10:00")
	}
	if !m.canBook(2, "Dr X", at.Add(30*time.Minute), slot) {
		t.Fatal("10:30 is free")
	}
	if m.canBook(2, "Dr Y", time.Date(2030, 3, 5, 16, 45, 0, 0, time.UTC), 30*time.Minute) {
		t.Fatal("a 30 minute appointment at 16:45 runs past closing")
	}
}

// slowLoadStore blocks in Load until the context is cancelled.
type slowLoadStore struct{ *fakeStore }

func (slowLoadStore) Load(ctx context.Context, _ time.Time) (Snapshot, error) {
	<-ctx.Done()
	return Snapshot{}, ctx.Err()
}

func TestStopDuringLoadCancelsStart(t *testing.T) {
	s := New(slowLoadStore{newFakeStore()}, slog.New(slog.DiscardHandler), 1)
	errc := make(chan error, 1)
	go func() { errc <- s.Start(context.Background(), 10) }()

	// Wait until Start has registered its cancel func.
	for {
		s.mu.Lock()
		ready := s.cancel != nil
		s.mu.Unlock()
		if ready {
			break
		}
		time.Sleep(time.Millisecond)
	}
	s.Stop()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Start() = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Start did not return after Stop")
	}
	if s.Stats().Running {
		t.Fatal("simulation running after Stop during load")
	}
}

func TestRestartResetsCounters(t *testing.T) {
	s := New(newFakeStore(), slog.New(slog.DiscardHandler), 1)
	ctx := context.Background()
	if err := s.Start(ctx, MaxRate); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	s.Stop()
	if s.Stats().Events == 0 {
		t.Fatal("expected events in first run")
	}
	if err := s.Start(ctx, MinRate); err != nil {
		t.Fatal(err)
	}
	defer s.Stop()
	if got := s.Stats().Events; got > 1 {
		t.Fatalf("events = %d right after restart, want counters reset", got)
	}
}
