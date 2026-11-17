package sim

import (
	"slices"
	"time"
)

// Appointment statuses, matching the schema's CHECK constraint.
const (
	StatusScheduled = "SCHEDULED"
	StatusCheckedIn = "CHECKED_IN"
	StatusCompleted = "COMPLETED"
	StatusCancelled = "CANCELLED"
	StatusNoShow    = "NO_SHOW"
)

// Visit types, matching the schema's CHECK constraint.
const (
	VisitScheduled = "SCHEDULED"
	VisitWalkIn    = "WALK_IN"
	VisitEmergency = "EMERGENCY"
)

// Patient is a new registration.
type Patient struct {
	MRN         string
	FirstName   string
	LastName    string
	DateOfBirth time.Time
	Sex         string
	Phone       string
	Email       string
	Address     string
	City        string
	State       string
	Postcode    string
}

// ContactChange is a patient updating their phone number and address.
type ContactChange struct {
	Phone    string
	Address  string
	City     string
	State    string
	Postcode string
}

// Appointment is a booked appointment.
type Appointment struct {
	ID          int64
	PatientID   int64
	Provider    string
	Department  string
	ScheduledAt time.Time
	Duration    time.Duration
	Reason      string
}

// Visit is a patient in the clinic, with or without an appointment.
type Visit struct {
	ID            int64
	PatientID     int64
	AppointmentID *int64
	Type          string
	Provider      string
	Department    string
	Reason        string
	CheckIn       time.Time
	// expectedOut is when the visit is due to finish; not persisted.
	expectedOut time.Time
}

// Snapshot is the state needed to resume a simulation from the database.
type Snapshot struct {
	Patients []int64
	// Booked holds every appointment on or after the snapshot date, in any
	// status, so the per-day and per-slot rules stay correct on resume.
	Booked []Appointment
	// Scheduled holds the appointments still awaiting check-in.
	Scheduled  []Appointment
	OpenVisits []Visit
	// Latest is the most recent business time recorded, so the simulated
	// clock never runs backwards across restarts.
	Latest time.Time
}

// model is the simulator's in-memory view of the clinic. It decides what
// can happen next and enforces the domain rules before anything is
// written, so the database constraints are only a backstop.
type model struct {
	now time.Time

	patients []int64
	// patientDays[patient][day] is set when the patient has an appointment
	// that day. A patient cannot have more than one appointment per day.
	patientDays map[int64]map[string]bool
	// providerSlots[provider][time] is set when the provider is booked.
	providerSlots map[string]map[time.Time]bool
	// scheduled is ordered by ScheduledAt.
	scheduled []Appointment
	open      []Visit
	// busy marks patients currently in the clinic, who cannot check in to
	// a second visit at the same time.
	busy map[int64]bool

	lastPrunedDay string
}

func newModel(now time.Time) *model {
	return &model{
		now:           nextOpen(now),
		patientDays:   make(map[int64]map[string]bool),
		providerSlots: make(map[string]map[time.Time]bool),
		busy:          make(map[int64]bool),
	}
}

// restore loads a snapshot. The clock starts at the later of now and the
// latest recorded business time.
func (m *model) restore(s Snapshot) {
	if s.Latest.After(m.now) {
		m.now = nextOpen(s.Latest)
	}
	m.patients = append(m.patients[:0], s.Patients...)
	for _, a := range s.Booked {
		m.book(a)
	}
	m.scheduled = m.scheduled[:0]
	for _, a := range s.Scheduled {
		m.insertScheduled(a)
	}
	for _, v := range s.OpenVisits {
		v.expectedOut = v.CheckIn.Add(20 * time.Minute)
		m.open = append(m.open, v)
		m.busy[v.PatientID] = true
	}
}

// tick advances the clock by one event's worth of business time.
func (m *model) tick(step time.Duration) {
	m.now = advance(m.now, step)
	if day := dateKey(m.now); day != m.lastPrunedDay {
		m.pruneBefore(day)
		m.lastPrunedDay = day
	}
}

// pruneBefore drops bookings on past days. They can no longer conflict
// with new bookings, which are always in the future.
func (m *model) pruneBefore(day string) {
	for pid, days := range m.patientDays {
		for d := range days {
			if d < day {
				delete(days, d)
			}
		}
		if len(days) == 0 {
			delete(m.patientDays, pid)
		}
	}
	for _, slots := range m.providerSlots {
		for t := range slots {
			if dateKey(t) < day {
				delete(slots, t)
			}
		}
	}
}

// canBook reports whether the appointment satisfies the domain rules.
func (m *model) canBook(patientID int64, prov string, at time.Time) bool {
	if !at.After(m.now) || !isOpen(at) {
		return false
	}
	if m.patientDays[patientID][dateKey(at)] {
		return false
	}
	return !m.providerSlots[prov][at]
}

// book records an appointment against the per-day and per-slot rules.
func (m *model) book(a Appointment) {
	days := m.patientDays[a.PatientID]
	if days == nil {
		days = make(map[string]bool)
		m.patientDays[a.PatientID] = days
	}
	days[dateKey(a.ScheduledAt)] = true
	slots := m.providerSlots[a.Provider]
	if slots == nil {
		slots = make(map[time.Time]bool)
		m.providerSlots[a.Provider] = slots
	}
	slots[a.ScheduledAt] = true
}

func (m *model) insertScheduled(a Appointment) {
	i, _ := slices.BinarySearchFunc(m.scheduled, a.ScheduledAt, func(x Appointment, t time.Time) int {
		return x.ScheduledAt.Compare(t)
	})
	m.scheduled = slices.Insert(m.scheduled, i, a)
}

func (m *model) removeScheduled(id int64) {
	m.scheduled = slices.DeleteFunc(m.scheduled, func(a Appointment) bool { return a.ID == id })
}

// due returns the index of the earliest appointment whose time has come
// and whose patient is not already in the clinic, or -1.
func (m *model) due() int {
	for i, a := range m.scheduled {
		if a.ScheduledAt.After(m.now) {
			return -1
		}
		if !m.busy[a.PatientID] {
			return i
		}
	}
	return -1
}

// overdue returns the index of an appointment more than 30 minutes late,
// which becomes a no-show, or -1.
func (m *model) overdue() int {
	if len(m.scheduled) > 0 && m.now.Sub(m.scheduled[0].ScheduledAt) > 30*time.Minute {
		return 0
	}
	return -1
}

// finished returns the index of an open visit whose expected end has
// passed, or -1.
func (m *model) finished() int {
	for i, v := range m.open {
		if !v.expectedOut.After(m.now) {
			return i
		}
	}
	return -1
}

// future returns the index of a random appointment that has not happened
// yet, for cancellation, or -1.
func (m *model) future(pick func(n int) int) int {
	start, _ := slices.BinarySearchFunc(m.scheduled, m.now, func(x Appointment, t time.Time) int {
		if x.ScheduledAt.After(t) {
			return 1
		}
		return -1
	})
	if start >= len(m.scheduled) {
		return -1
	}
	return start + pick(len(m.scheduled)-start)
}

// idlePatient returns a random patient who is not in the clinic, or -1.
func (m *model) idlePatient(pick func(n int) int) int64 {
	if len(m.patients) == 0 {
		return -1
	}
	for range 5 {
		if p := m.patients[pick(len(m.patients))]; !m.busy[p] {
			return p
		}
	}
	return -1
}
