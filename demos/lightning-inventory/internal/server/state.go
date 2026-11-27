package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"lightninginv/internal/catalog"
	"lightninginv/internal/confluentmetrics"
	"lightninginv/internal/cw"
	"lightninginv/internal/protocol"
	"lightninginv/internal/worker"
)

// Bounds tells the UI what it may ask for. The server enforces them regardless.
type Bounds struct {
	Min     int `json:"min"`
	Max     int `json:"max"`
	Default int `json:"default"`
}

// ScenarioRates are the last seconds' rates of one scenario.
type ScenarioRates struct {
	OKPerSec      float64 `json:"ok_per_sec"`
	ErrorsPerSec  float64 `json:"errors_per_sec"`
	DroppedPerSec float64 `json:"dropped_per_sec"`
	AvgRows       float64 `json:"avg_rows"`
}

// LoadView is one load as the page shows it.
type LoadView struct {
	Running      bool                     `json:"running"`
	Rate         int                      `json:"rate"`
	Workers      int                      `json:"workers"`
	PerSec       float64                  `json:"per_sec"`
	ErrorsPerSec float64                  `json:"errors_per_sec"`
	ByScenario   map[string]ScenarioRates `json:"by_scenario,omitempty"`
}

// CatalogueView is the one-off load of the catalogue.
type CatalogueView struct {
	Status      string `json:"status"` // not_loaded, loading, loaded
	ShardsDone  int    `json:"shards_done"`
	ShardsTotal int    `json:"shards_total"`
	Records     int64  `json:"records"`
	Errors      int64  `json:"errors"`
	SKUs        int    `json:"skus"`
	Locations   int    `json:"locations"`
}

// TableView is the Lightning Table's state.
type TableView struct {
	Phase   string `json:"phase"` // "" when none yet
	Message string `json:"message,omitempty"`
	Error   string `json:"error,omitempty"`
}

// UsageView is Confluent's own metrics.
type UsageView struct {
	confluentmetrics.Usage
	Error string `json:"error,omitempty"`
}

// State is what GET /api/state returns.
type State struct {
	Demo          string        `json:"demo"`
	Region        string        `json:"region"`
	Topic         string        `json:"topic"`
	Namespace     string        `json:"namespace"`
	Table         TableView     `json:"table"`
	Catalogue     CatalogueView `json:"catalogue"`
	Events        LoadView      `json:"events"`
	Queries       LoadView      `json:"queries"`
	Usage         UsageView     `json:"usage"`
	EventsBounds  Bounds        `json:"events_bounds"`
	QueriesBounds Bounds        `json:"queries_bounds"`
}

// rateWindow is how many finished seconds the live rates average over.
const rateWindow = 10

// State returns a snapshot.
func (s *Service) State() State {
	now := s.o.Now()
	s.mu.Lock()
	defer s.mu.Unlock()

	var prod, prodErr int64
	qs := map[string]protocol.QueryStats{}
	for sec, b := range s.buckets {
		if sec >= now.Unix() || sec < now.Unix()-rateWindow {
			continue
		}
		prod += b.produced
		prodErr += b.produceErrs
		for sc, st := range b.queries {
			cur := qs[sc]
			cur.OK += st.OK
			cur.Errors += st.Errors
			cur.Dropped += st.Dropped
			cur.Rows += st.Rows
			qs[sc] = cur
		}
	}
	per := func(n int64) float64 { return float64(n) / rateWindow }

	st := State{
		Demo: s.o.DemoName, Region: s.o.Region, Topic: s.o.Topic, Namespace: s.o.Namespace,
		Table:         TableView{Phase: string(s.table.phase), Message: s.table.message, Error: s.table.err},
		Events:        LoadView{Running: s.events.running, Rate: s.events.rate, Workers: len(s.aliveLocked("producer", now)), PerSec: per(prod), ErrorsPerSec: per(prodErr)},
		Queries:       LoadView{Running: s.queries.running, Rate: s.queries.rate, Workers: len(s.aliveLocked("query", now)), ByScenario: map[string]ScenarioRates{}},
		Usage:         UsageView{Usage: s.usage, Error: s.usageErr},
		EventsBounds:  Bounds{Min: 1, Max: worker.MaxEventsPerSec, Default: worker.DefaultEventsPerSec},
		QueriesBounds: Bounds{Min: 1, Max: worker.MaxQueriesPerSec, Default: worker.DefaultQueriesPerSec},
	}
	for _, sc := range protocol.Scenarios {
		r := qs[sc]
		v := ScenarioRates{OKPerSec: per(r.OK), ErrorsPerSec: per(r.Errors), DroppedPerSec: per(r.Dropped)}
		if r.OK > 0 {
			v.AvgRows = float64(r.Rows) / float64(r.OK)
		}
		st.Queries.ByScenario[sc] = v
		st.Queries.PerSec += v.OKPerSec
		st.Queries.ErrorsPerSec += v.ErrorsPerSec + v.DroppedPerSec
	}

	done := 0
	for _, sh := range s.seed.shards {
		if sh.done {
			done++
		}
	}
	status := "not_loaded"
	if s.seed.active {
		status = "loading"
		if done == catalog.Shards {
			status = "loaded"
		}
	}
	st.Catalogue = CatalogueView{Status: status, ShardsDone: done, ShardsTotal: catalog.Shards, Records: s.seed.loaded, Errors: s.seed.errors, SKUs: catalog.SKUs, Locations: catalog.Locations}
	return st
}

// LatencyWindows are the chart ranges the browser may pick, in minutes.
var LatencyWindows = map[int]time.Duration{5: 5 * time.Minute, 15: 15 * time.Minute, 60: time.Hour}

// latencyTTL is how long a CloudWatch read is reused, however many pages are open. The
// charts are at 1 s resolution, but a point shows in CloudWatch seconds late anyway.
const latencyTTL = 5 * time.Second

// latencyErrorTTL is how long a failed read is remembered and returned instead of asking
// CloudWatch again.
const latencyErrorTTL = 3 * time.Second

// latencyTimeout bounds one CloudWatch read, so a slow answer does not hold requests open.
const latencyTimeout = 10 * time.Second

// Latency returns the chart data for a range. A read from CloudWatch is reused for
// latencyTTL, and requests that arrive while one is in flight wait for it instead of starting
// their own: however many pages are open there is at most one read per range at a time.
func (s *Service) Latency(ctx context.Context, minutes int) (map[string]*cw.Series, error) {
	win, ok := LatencyWindows[minutes]
	if !ok {
		return nil, fmt.Errorf("%w: the range is 5, 15 or 60 minutes", ErrInvalid)
	}
	if s.o.Latency == nil {
		return nil, fmt.Errorf("%w: no metrics source", ErrConflict)
	}
	now := s.o.Now()
	s.mu.Lock()
	if e, ok := s.latCache[win]; ok && now.Sub(e.at) < latencyTTL {
		s.mu.Unlock()
		return e.series, nil
	}
	// A read that just failed is not repeated for a moment: with CloudWatch throttling, every
	// open page asking again would only make it worse.
	if e, ok := s.latErr[win]; ok && now.Sub(e.at) < latencyErrorTTL {
		s.mu.Unlock()
		return nil, e.err
	}
	call, flying := s.latFly[win]
	if !flying {
		call = &latCall{done: make(chan struct{})}
		s.latFly[win] = call
	}
	s.mu.Unlock()

	if flying {
		select {
		case <-call.done:
			return call.series, call.err
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	// The read belongs to everyone waiting, so it is not tied to this request's context
	// (a closed tab must not fail the others), only to its own timeout.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), latencyTimeout)
	defer cancel()
	call.series, call.err = s.readLatency(rctx, now, win)
	s.mu.Lock()
	delete(s.latFly, win)
	if call.err == nil {
		s.latCache[win] = latEntry{at: now, series: call.series}
		delete(s.latErr, win)
	} else {
		s.latErr[win] = latErrEntry{at: now, err: call.err}
	}
	s.mu.Unlock()
	close(call.done)
	return call.series, call.err
}

// readLatency is Read with a panic turned into an error, so the requests waiting on it are
// always released.
func (s *Service) readLatency(ctx context.Context, now time.Time, win time.Duration) (series map[string]*cw.Series, err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("Reading the latency metrics panicked", "panic", fmt.Sprint(r))
			series, err = nil, errors.New("reading the latency metrics failed")
		}
	}()
	return s.o.Latency.Read(ctx, now, win)
}
