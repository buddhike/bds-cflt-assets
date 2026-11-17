package sim

import "time"

// Clinic opening hours on the simulated clock (UTC).
const (
	openHour  = 8
	closeHour = 17
	// slot is the appointment booking granularity.
	slot = 15 * time.Minute
)

// isOpen reports whether t falls within clinic hours.
func isOpen(t time.Time) bool {
	if wd := t.Weekday(); wd == time.Saturday || wd == time.Sunday {
		return false
	}
	return t.Hour() >= openHour && t.Hour() < closeHour
}

// nextOpen returns t if the clinic is open then, otherwise the next
// opening time.
func nextOpen(t time.Time) time.Time {
	t = t.UTC()
	if isOpen(t) {
		return t
	}
	day := time.Date(t.Year(), t.Month(), t.Day(), openHour, 0, 0, 0, time.UTC)
	if t.Hour() >= closeHour || !isBusinessDay(t) {
		day = day.AddDate(0, 0, 1)
	}
	for !isBusinessDay(day) {
		day = day.AddDate(0, 0, 1)
	}
	if t.Before(day) {
		return day
	}
	return t
}

func isBusinessDay(t time.Time) bool {
	wd := t.Weekday()
	return wd != time.Saturday && wd != time.Sunday
}

// advance moves the simulated clock forward by d, skipping nights and
// weekends when the clinic is closed.
func advance(t time.Time, d time.Duration) time.Time {
	return nextOpen(t.Add(d))
}

// dateKey identifies a calendar day, used for the one-appointment-per-day
// rule.
func dateKey(t time.Time) string { return t.UTC().Format(time.DateOnly) }
