// Package sim simulates a busy outpatient clinic: patient registrations,
// appointment bookings, check-ins, walk-ins, no-shows, cancellations and
// visit completions, at an operator-controlled rate.
package sim

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Rate limits accepted by SetRate, in events per second.
const (
	MinRate = 0.2
	MaxRate = 200
)

// Each event advances the simulated clinic clock by this much, so a
// simulated day passes in a few minutes at typical rates and booked
// appointments soon become due.
const stepPerEvent = 2 * time.Minute

// maxConsecutiveErrors stops the simulation when the database keeps
// failing (for example MySQL was stopped) instead of spinning.
const maxConsecutiveErrors = 20

// ErrConflict is returned by a Store when a write violates a uniqueness
// rule. The simulator treats it as a lost race and moves on.
var ErrConflict = errors.New("conflicting row")

// ErrAlreadyRunning is returned by Start when a simulation is running.
var ErrAlreadyRunning = errors.New("simulation already running")

// Kind identifies a simulated event.
type Kind string

// Event kinds.
const (
	KindRegister Kind = "register_patient"
	KindUpdate   Kind = "update_patient"
	KindBook     Kind = "book_appointment"
	KindCancel   Kind = "cancel_appointment"
	KindNoShow   Kind = "no_show"
	KindCheckIn  Kind = "check_in"
	KindWalkIn   Kind = "walk_in"
	KindComplete Kind = "complete_visit"
)

// Store persists simulated events. Implementations must return an error
// wrapping ErrConflict for uniqueness violations.
type Store interface {
	Load(ctx context.Context, from time.Time) (Snapshot, error)
	InsertPatient(ctx context.Context, p Patient) (int64, error)
	UpdatePatientContact(ctx context.Context, id int64, c ContactChange) error
	InsertAppointment(ctx context.Context, a Appointment) (int64, error)
	SetAppointmentStatus(ctx context.Context, id int64, status string) error
	// CheckIn inserts the visit and, when it is for an appointment, marks
	// the appointment CHECKED_IN in the same transaction.
	CheckIn(ctx context.Context, v Visit) (int64, error)
	// CompleteVisit closes the visit and, when it is for an appointment,
	// marks the appointment COMPLETED in the same transaction.
	CompleteVisit(ctx context.Context, v Visit, checkOut time.Time, diagnosis string) error
}

// Stats summarise a simulation run for the UI.
type Stats struct {
	Running     bool           `json:"running"`
	Rate        float64        `json:"rate"`
	Events      int64          `json:"events"`
	ByKind      map[Kind]int64 `json:"byKind"`
	Conflicts   int64          `json:"conflicts"`
	Errors      int64          `json:"errors"`
	LastError   string         `json:"lastError,omitempty"`
	SimulatedAt time.Time      `json:"simulatedAt"`
	Patients    int            `json:"patients"`
	Scheduled   int            `json:"scheduled"`
	InClinic    int            `json:"inClinic"`
}

// Simulator drives events into a Store.
type Simulator struct {
	store Store
	log   *slog.Logger
	gen   *generator
	opTO  time.Duration

	limiter *rate.Limiter

	mu      sync.Mutex
	m       *model
	stats   Stats
	cancel  context.CancelFunc
	done    chan struct{}
	started bool
}

// New returns a Simulator. seed makes runs reproducible.
func New(store Store, log *slog.Logger, seed uint64) *Simulator {
	return &Simulator{
		store:   store,
		log:     log.With("component", "simulator"),
		gen:     newGenerator(seed),
		opTO:    5 * time.Second,
		limiter: rate.NewLimiter(rate.Limit(1), 1),
		stats:   Stats{Rate: 1, ByKind: make(map[Kind]int64)},
	}
}

// Start resumes from the database state and generates events at rate
// events per second until Stop is called or ctx is cancelled.
func (s *Simulator) Start(ctx context.Context, eventsPerSec float64) error {
	// The run context exists before loading so a Stop issued while Load is
	// in progress cancels it rather than racing with the goroutine start.
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		stop()
		return ErrAlreadyRunning
	}
	s.started = true
	s.cancel = stop
	s.done = done
	// Counters are per run; only the rate carries over.
	s.stats = Stats{Rate: s.stats.Rate, ByKind: make(map[Kind]int64)}
	s.m = nil
	s.mu.Unlock()

	m := newModel(time.Now().UTC())
	lctx, cancel := context.WithTimeout(runCtx, 30*time.Second)
	snap, err := s.store.Load(lctx, m.now)
	cancel()
	if err != nil {
		s.mu.Lock()
		s.started = false
		s.cancel = nil
		s.stats.LastError = err.Error()
		s.mu.Unlock()
		stop()
		close(done)
		return fmt.Errorf("load simulation state: %w", err)
	}
	m.restore(snap)

	s.mu.Lock()
	s.m = m
	s.stats.Running = true
	s.mu.Unlock()
	s.SetRate(eventsPerSec)

	s.log.Info("Simulation started", "rate", eventsPerSec, "patients", len(snap.Patients), "scheduled", len(snap.Scheduled), "simulated_at", m.now)
	go s.run(runCtx)
	return nil
}

// SetRate changes the event rate, clamped to [MinRate, MaxRate].
func (s *Simulator) SetRate(eventsPerSec float64) {
	r := min(max(eventsPerSec, MinRate), MaxRate)
	s.limiter.SetLimit(rate.Limit(r))
	s.mu.Lock()
	s.stats.Rate = r
	s.mu.Unlock()
}

// Stop halts the simulation and waits for the in-flight event.
func (s *Simulator) Stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	<-done
}

// Stats returns a snapshot of the run's counters.
func (s *Simulator) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stats
	st.ByKind = make(map[Kind]int64, len(s.stats.ByKind))
	for k, v := range s.stats.ByKind {
		st.ByKind[k] = v
	}
	if s.m != nil {
		st.SimulatedAt = s.m.now
		st.Patients = len(s.m.patients)
		st.Scheduled = len(s.m.scheduled)
		st.InClinic = len(s.m.open)
	}
	return st
}

func (s *Simulator) run(ctx context.Context) {
	defer func() {
		s.mu.Lock()
		s.stats.Running = false
		s.started = false
		s.cancel = nil
		close(s.done)
		s.mu.Unlock()
		s.log.Info("Simulation stopped")
	}()

	consecutive := 0
	for {
		if err := s.limiter.Wait(ctx); err != nil {
			return // cancelled
		}
		kind, err := s.step(ctx)
		if ctx.Err() != nil {
			return
		}
		s.record(kind, err)
		if err == nil || errors.Is(err, ErrConflict) {
			consecutive = 0
			continue
		}
		consecutive++
		s.log.Warn("Simulated event failed", "kind", kind, "error", err, "consecutive", consecutive)
		if consecutive >= maxConsecutiveErrors {
			s.log.Error("Stopping simulation after repeated database errors", "error", err)
			return
		}
	}
}

func (s *Simulator) record(kind Kind, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case err == nil:
		if kind != "" {
			s.stats.Events++
			s.stats.ByKind[kind]++
		}
	case errors.Is(err, ErrConflict):
		s.stats.Conflicts++
	default:
		s.stats.Errors++
		s.stats.LastError = err.Error()
	}
}

// step advances the clock and performs one event. It returns an empty
// kind when nothing was feasible this tick.
func (s *Simulator) step(ctx context.Context) (Kind, error) {
	ctx, cancel := context.WithTimeout(ctx, s.opTO)
	defer cancel()

	// Concurrency contract: only the run goroutine mutates the model, and
	// always while holding mu. Event handlers may read the model without the
	// lock because no other goroutine writes it; Stats reads it under mu.
	// Any new writer outside the run goroutine must take mu.
	s.mu.Lock()
	m := s.m
	m.tick(stepPerEvent)
	kind := s.choose(m)
	s.mu.Unlock()

	var err error
	switch kind {
	case KindRegister:
		err = s.register(ctx, m)
	case KindUpdate:
		err = s.updatePatient(ctx, m)
	case KindBook:
		err = s.bookAppointment(ctx, m)
	case KindCancel:
		err = s.cancelAppointment(ctx, m)
	case KindNoShow:
		err = s.noShow(ctx, m)
	case KindCheckIn:
		err = s.checkIn(ctx, m)
	case KindWalkIn:
		err = s.walkIn(ctx, m)
	case KindComplete:
		err = s.completeVisit(ctx, m)
	}
	return kind, err
}

// choose picks the next event with weights that depend on clinic state:
// due appointments and finished visits are handled promptly, new
// registrations dominate while the patient list is small.
func (s *Simulator) choose(m *model) Kind {
	type option struct {
		kind   Kind
		weight float64
	}
	opts := []option{{KindRegister, 3}}
	if len(m.patients) < 25 {
		opts[0].weight = 20
	}
	if len(m.patients) > 0 {
		opts = append(opts, option{KindBook, 5}, option{KindUpdate, 0.5})
		if isOpen(m.now) {
			opts = append(opts, option{KindWalkIn, 1})
		}
	}
	if m.due() >= 0 {
		opts = append(opts, option{KindCheckIn, 12})
	}
	if m.overdue() >= 0 {
		opts = append(opts, option{KindNoShow, 4})
	}
	if m.finished() >= 0 {
		opts = append(opts, option{KindComplete, 10})
	}
	if len(m.scheduled) > 0 {
		opts = append(opts, option{KindCancel, 0.4})
	}

	total := 0.0
	for _, o := range opts {
		total += o.weight
	}
	r := s.gen.rng.Float64() * total
	for _, o := range opts {
		if r < o.weight {
			return o.kind
		}
		r -= o.weight
	}
	return opts[len(opts)-1].kind
}

func (s *Simulator) register(ctx context.Context, m *model) error {
	s.mu.Lock()
	m.mrnSeq++
	seq := m.mrnSeq
	s.mu.Unlock()
	p := s.gen.patient(m.now, seq)
	id, err := s.store.InsertPatient(ctx, p)
	if err != nil {
		return fmt.Errorf("register patient: %w", err)
	}
	s.mu.Lock()
	m.patients = append(m.patients, id)
	s.mu.Unlock()
	return nil
}

func (s *Simulator) updatePatient(ctx context.Context, m *model) error {
	id := m.patients[s.gen.rng.IntN(len(m.patients))]
	if err := s.store.UpdatePatientContact(ctx, id, s.gen.contactChange()); err != nil {
		return fmt.Errorf("update patient %d: %w", id, err)
	}
	return nil
}

// bookAppointment books a future slot for a random patient, respecting
// one appointment per patient per day and one patient per provider slot.
func (s *Simulator) bookAppointment(ctx context.Context, m *model) error {
	for range 10 {
		pid := m.patients[s.gen.rng.IntN(len(m.patients))]
		prov := providers[s.gen.rng.IntN(len(providers))]
		dur := s.gen.appointmentDuration(prov.Department)
		at := s.candidateSlot(m.now, dur)
		if !m.canBook(pid, prov.Name, at, dur) {
			continue
		}
		a := Appointment{
			PatientID:   pid,
			Provider:    prov.Name,
			Department:  prov.Department,
			ScheduledAt: at,
			Duration:    dur,
			Reason:      s.gen.pick(appointmentReasons[prov.Department]),
		}
		id, err := s.store.InsertAppointment(ctx, a)
		if err != nil {
			return fmt.Errorf("book appointment: %w", err)
		}
		a.ID = id
		s.mu.Lock()
		m.book(a)
		m.insertScheduled(a)
		s.mu.Unlock()
		return nil
	}
	return nil // fully booked candidates this tick; try again later
}

// candidateSlot picks a slot between later today and two weeks out,
// favouring the next few days as real clinics do. The slot leaves room for
// the appointment to finish by closing time.
func (s *Simulator) candidateSlot(now time.Time, dur time.Duration) time.Time {
	days := min(int(s.gen.rng.ExpFloat64()*3), 14)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, days)
	slots := int(((closeHour-openHour)*time.Hour - dur) / slot)
	return day.Add(openHour*time.Hour + time.Duration(s.gen.rng.IntN(slots+1))*slot)
}

// forget drops an appointment from the model after a conflict: the
// database already holds a newer state for it (for example a check-in
// that committed before its context timed out), so retrying would only
// conflict again and block later appointments.
func (s *Simulator) forget(m *model, id int64, err error) {
	if errors.Is(err, ErrConflict) {
		s.mu.Lock()
		m.removeScheduled(id)
		s.mu.Unlock()
	}
}

func (s *Simulator) cancelAppointment(ctx context.Context, m *model) error {
	i := m.future(s.gen.rng.IntN)
	if i < 0 {
		return nil
	}
	a := m.scheduled[i]
	if err := s.store.SetAppointmentStatus(ctx, a.ID, StatusCancelled); err != nil {
		s.forget(m, a.ID, err)
		return fmt.Errorf("cancel appointment %d: %w", a.ID, err)
	}
	// The day and slot stay taken: the cancelled row still holds them in
	// the unique indexes.
	s.mu.Lock()
	m.removeScheduled(a.ID)
	s.mu.Unlock()
	return nil
}

func (s *Simulator) noShow(ctx context.Context, m *model) error {
	i := m.overdue()
	if i < 0 {
		return nil
	}
	a := m.scheduled[i]
	if err := s.store.SetAppointmentStatus(ctx, a.ID, StatusNoShow); err != nil {
		s.forget(m, a.ID, err)
		return fmt.Errorf("mark appointment %d no-show: %w", a.ID, err)
	}
	s.mu.Lock()
	m.removeScheduled(a.ID)
	s.mu.Unlock()
	return nil
}

func (s *Simulator) checkIn(ctx context.Context, m *model) error {
	i := m.due()
	if i < 0 {
		return nil
	}
	a := m.scheduled[i]
	apptID := a.ID
	v := Visit{
		PatientID:     a.PatientID,
		AppointmentID: &apptID,
		Type:          VisitScheduled,
		Provider:      a.Provider,
		Department:    a.Department,
		Reason:        a.Reason,
		CheckIn:       m.now.Truncate(time.Second),
		expectedOut:   m.now.Add(a.Duration + time.Duration(s.gen.rng.IntN(15))*time.Minute),
	}
	id, err := s.store.CheckIn(ctx, v)
	if err != nil {
		s.forget(m, a.ID, err)
		return fmt.Errorf("check in appointment %d: %w", a.ID, err)
	}
	v.ID = id
	s.mu.Lock()
	m.removeScheduled(a.ID)
	m.open = append(m.open, v)
	m.busy[v.PatientID] = true
	s.mu.Unlock()
	return nil
}

func (s *Simulator) walkIn(ctx context.Context, m *model) error {
	pid := m.idlePatient(s.gen.rng.IntN)
	if pid < 0 {
		return nil
	}
	v := Visit{PatientID: pid, Type: VisitWalkIn, Provider: providers[s.gen.rng.IntN(3)].Name, Department: "General Practice", Reason: s.gen.pick(walkInReasons)}
	if s.gen.chance(0.2) {
		v.Type, v.Provider, v.Department, v.Reason = VisitEmergency, emergencyProvider.Name, emergencyProvider.Department, s.gen.pick(emergencyReasons)
	}
	v.CheckIn = m.now.Truncate(time.Second)
	v.expectedOut = m.now.Add(time.Duration(20+s.gen.rng.IntN(70)) * time.Minute)

	id, err := s.store.CheckIn(ctx, v)
	if err != nil {
		return fmt.Errorf("walk-in visit: %w", err)
	}
	v.ID = id
	s.mu.Lock()
	m.open = append(m.open, v)
	m.busy[pid] = true
	s.mu.Unlock()
	return nil
}

func (s *Simulator) completeVisit(ctx context.Context, m *model) error {
	i := m.finished()
	if i < 0 {
		return nil
	}
	v := m.open[i]
	checkOut := m.now.Truncate(time.Second)
	if err := s.store.CompleteVisit(ctx, v, checkOut, s.gen.pick(diagnoses)); err != nil {
		return fmt.Errorf("complete visit %d: %w", v.ID, err)
	}
	s.mu.Lock()
	m.open = append(m.open[:i], m.open[i+1:]...)
	delete(m.busy, v.PatientID)
	s.mu.Unlock()
	return nil
}
