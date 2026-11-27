package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"lightninginv/internal/catalog"
	"lightninginv/internal/cw"
	"lightninginv/internal/lightning"
	"lightninginv/internal/protocol"
)

const (
	// MaxInFlight is how many queries one task has outstanding. Past it a query is not
	// started and is counted as dropped, so the task's own saturation is visible and does not
	// pass for the table being slow.
	MaxInFlight = 512
	// QueryTimeout ends a query that has not answered.
	QueryTimeout = 30 * time.Second
	// MaxLocationsPerQuery is the most locations in one by_locations query.
	MaxLocationsPerQuery = 2000
)

// QueryFunc runs one SQL statement and returns how many rows came back.
type QueryFunc func(ctx context.Context, sql string) (int, error)

// Publisher is where one-second windows go (CloudWatch). Publish returns the seconds it
// could not send, for the caller to try again, and the error that stopped it.
type Publisher interface {
	Publish(ctx context.Context, secs map[string][]*cw.Second) (unpublished map[string][]*cw.Second, err error)
}

// maxBacklogSeconds bounds, per scenario, the seconds kept for a retry while CloudWatch
// cannot be reached. The oldest go first: the chart's newest data matters most.
const maxBacklogSeconds = 120

// outcomeLogEvery is how often the failures of the queries are summarised in the log.
const outcomeLogEvery = 10 * time.Second

// Querier is the load generator of one task.
type Querier struct {
	Ctl       *Control
	Query     QueryFunc
	Rec       *cw.Recorder
	Pub       Publisher
	Topic     string
	Catalog   catalog.Catalog
	MissRatio float64
	Seed      uint64
	// Now is the clock; time.Now in production.
	Now func() time.Time

	sem      chan struct{}
	wg       sync.WaitGroup
	stats    map[string]*scenarioStats
	outcomes [numOutcomes]atomic.Int64
	// sample is the latest HTTP error answer, for the log: what Confluent said, which
	// scenario and how long the statement was. Confluent may quote a fragment of the
	// statement back (at most maxDetail characters), which holds only catalogue ids.
	sample atomic.Pointer[string]
	// droppedSecs counts latency seconds lost for good: the backlog was full.
	droppedSecs atomic.Int64
	pubMu       sync.Mutex              // serialises flushOnce, which owns backlog
	backlog     map[string][]*cw.Second // seconds that could not be published yet
}

type scenarioStats struct{ ok, errs, dropped, rows atomic.Int64 }

// Outcome is how a failed query ended, for the log and the counters. The charts only count
// Errors; this says why.
type Outcome int

// The outcomes a failed query is sorted into.
const (
	OutcomeOther Outcome = iota
	OutcomeRateLimited
	OutcomeAsync
	OutcomeNotFound
	OutcomeClientError
	OutcomeServerError
	OutcomeTimeout
	OutcomeNetwork
	numOutcomes
)

var outcomeNames = [numOutcomes]string{"other", "rate_limited_429", "async_202", "not_found_404", "client_error_4xx", "server_error_5xx", "timeout", "network"}

// Classify sorts a query error into an Outcome.
func Classify(err error) Outcome {
	var se *lightning.StatusError
	var ne net.Error
	switch {
	case errors.Is(err, lightning.ErrRateLimited):
		return OutcomeRateLimited
	case errors.Is(err, lightning.ErrAsync):
		return OutcomeAsync
	case errors.Is(err, lightning.ErrNotFound):
		return OutcomeNotFound
	case errors.As(err, &se):
		if se.Status >= 500 {
			return OutcomeServerError
		}
		return OutcomeClientError
	case errors.Is(err, context.DeadlineExceeded):
		return OutcomeTimeout
	case errors.As(err, &ne):
		if ne.Timeout() {
			return OutcomeTimeout
		}
		return OutcomeNetwork
	}
	return OutcomeOther
}

func (q *Querier) init() {
	if q.Now == nil {
		q.Now = time.Now
	}
	q.sem = make(chan struct{}, MaxInFlight)
	q.stats = map[string]*scenarioStats{}
	q.backlog = map[string][]*cw.Second{}
	for _, s := range protocol.Scenarios {
		q.stats[s] = &scenarioStats{}
	}
}

func (q *Querier) heartbeat() protocol.Heartbeat {
	hb := protocol.Heartbeat{Role: "query", Queries: map[string]protocol.QueryStats{}}
	for sc, s := range q.stats {
		hb.Queries[sc] = protocol.QueryStats{OK: s.ok.Swap(0), Errors: s.errs.Swap(0), Dropped: s.dropped.Swap(0), Rows: s.rows.Swap(0)}
	}
	return hb
}

// Run blocks until ctx ends.
func (q *Querier) Run(ctx context.Context) {
	q.init()
	go q.Ctl.Loop(ctx, q.heartbeat)
	go q.publishLoop(ctx)
	go q.outcomeLoop(ctx)

	s := catalog.NewSampler(q.Catalog, q.MissRatio, q.Seed)
	Pace(ctx, func() float64 {
		pl := q.Ctl.Plan()
		if !pl.Run {
			return 0
		}
		return min(pl.Rate, MaxQueriesPerSec)
	}, func() { q.fire(ctx, s) })
	q.wg.Wait()
	q.flush(context.Background())
}

// fire starts one query: by_sku or by_locations with equal probability.
func (q *Querier) fire(ctx context.Context, s *catalog.Sampler) {
	sku, _ := s.QuerySKU()
	scenario, sql := protocol.ScenarioBySKU, lightning.SKUQuery(q.Topic, sku)
	if s.SetSize(2) == 1 {
		scenario = protocol.ScenarioByLocations
		sql = lightning.LocationsQuery(q.Topic, sku, s.LocationSet(s.SetSize(MaxLocationsPerQuery)))
	}
	select {
	case q.sem <- struct{}{}:
	default:
		q.stats[scenario].dropped.Add(1)
		q.Rec.Drop(scenario, q.Now())
		return
	}
	q.wg.Add(1)
	go func() {
		defer q.wg.Done()
		defer func() { <-q.sem }()
		q.run(ctx, scenario, sql)
	}()
}

func (q *Querier) run(ctx context.Context, scenario, sql string) {
	qctx, cancel := context.WithTimeout(ctx, QueryTimeout)
	defer cancel()
	start := q.Now()
	rows, err := q.Query(qctx, sql)
	end := q.Now()
	st := q.stats[scenario]
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return // shutting down, not a failed query
		}
		st.errs.Add(1)
		o := Classify(err)
		q.outcomes[o].Add(1)
		if o == OutcomeClientError || o == OutcomeServerError || o == OutcomeOther {
			m := fmt.Sprintf("%s, statement of %d bytes: %v", scenario, len(sql), err)
			q.sample.Store(&m)
		}
		q.Rec.Error(scenario, end)
		return
	}
	st.ok.Add(1)
	st.rows.Add(int64(rows))
	q.Rec.Success(scenario, end, end.Sub(start))
}

// outcomeLoop logs, every few seconds and only when there were any, why queries failed:
// a 429 is the table's rate limit, a 202 an answer the demo does not follow, a timeout or a
// 5xx the table or the network. One line per interval, however many queries failed.
func (q *Querier) outcomeLoop(ctx context.Context) {
	t := time.NewTicker(outcomeLogEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		var attrs []any
		for i := range q.outcomes {
			if n := q.outcomes[i].Swap(0); n > 0 {
				attrs = append(attrs, outcomeNames[i], n)
			}
		}
		if len(attrs) > 0 {
			if m := q.sample.Swap(nil); m != nil {
				attrs = append(attrs, "sample", *m)
			}
			slog.Warn("Queries failed in the last interval", append(attrs, "interval", outcomeLogEvery.String())...)
		}
	}
}

func (q *Querier) publishLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := q.flushOnce(ctx, q.Now()); err != nil {
				if failures++; failures == 1 || failures%60 == 0 {
					slog.Warn("Could not publish the latency metrics", "error", err, "failures", failures, "seconds_dropped_in_total", q.droppedSecs.Load())
				}
			} else {
				if failures > 0 {
					slog.Info("Publishing the latency metrics again", "failed_for", failures)
				}
				failures = 0
			}
		}
	}
}

// flushOnce publishes the finished seconds, and with them any that an earlier attempt could
// not send. Seconds that still cannot be sent are kept, up to maxBacklogSeconds per scenario.
func (q *Querier) flushOnce(ctx context.Context, now time.Time) error {
	q.pubMu.Lock()
	defer q.pubMu.Unlock()
	secs := q.Rec.Drain(now)
	for sc, list := range q.backlog {
		secs[sc] = append(list, secs[sc]...)
	}
	q.backlog = map[string][]*cw.Second{}
	if len(secs) == 0 {
		return nil
	}
	// Oldest first (Drain's order is a map's), so a trimmed backlog loses the oldest.
	for _, list := range secs {
		sort.Slice(list, func(i, j int) bool { return list[i].At.Before(list[j].At) })
	}
	left, err := q.Pub.Publish(ctx, secs)
	if err != nil {
		dropped := 0
		for sc, list := range left {
			if over := len(list) - maxBacklogSeconds; over > 0 {
				list, dropped = list[over:], dropped+over
			}
			q.backlog[sc] = list
		}
		q.droppedSecs.Add(int64(dropped))
	}
	return err
}

// flush sends everything left, including the current second and any backlog, at shutdown.
func (q *Querier) flush(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := q.flushOnce(ctx, q.Now().Add(2*time.Second)); err != nil {
		slog.Warn("Could not publish the last latency metrics at shutdown", "error", err)
	}
}
