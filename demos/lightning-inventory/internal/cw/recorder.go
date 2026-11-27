package cw

import (
	"sync"
	"time"
)

// Second is what happened in one second of one scenario.
type Second struct {
	At      time.Time
	Latency Histogram
	Errors  int64
	Dropped int64
}

// Recorder collects results by the second they finished in, so that a slow publish never
// moves a sample into the wrong second.
type Recorder struct {
	mu   sync.Mutex
	secs map[string]map[int64]*Second // scenario -> unix second
}

// NewRecorder returns an empty Recorder.
func NewRecorder() *Recorder { return &Recorder{secs: map[string]map[int64]*Second{}} }

func (r *Recorder) at(scenario string, now time.Time) *Second {
	m := r.secs[scenario]
	if m == nil {
		m = map[int64]*Second{}
		r.secs[scenario] = m
	}
	s := m[now.Unix()]
	if s == nil {
		s = &Second{At: now.Truncate(time.Second)}
		m[now.Unix()] = s
	}
	return s
}

// Success records a latency that finished at now.
func (r *Recorder) Success(scenario string, now time.Time, d time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.at(scenario, now).Latency.Observe(d)
}

// Error records a failed query.
func (r *Recorder) Error(scenario string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.at(scenario, now).Errors++
}

// Drop records a query that was not started because too many were in flight.
func (r *Recorder) Drop(scenario string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.at(scenario, now).Dropped++
}

// Drain returns, per scenario, the seconds that are over (before now's second).
func (r *Recorder) Drain(now time.Time) map[string][]*Second {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string][]*Second{}
	for sc, m := range r.secs {
		for sec, s := range m {
			if sec < now.Unix() {
				out[sc] = append(out[sc], s)
				delete(m, sec)
			}
		}
	}
	return out
}
