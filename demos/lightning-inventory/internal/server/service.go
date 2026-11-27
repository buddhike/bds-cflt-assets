// Package server is the control service: the web UI and its API, the access gate, the
// read-only view of the Lightning Table (Terraform makes and removes it), and the plan the
// producer and query tasks poll for.
package server

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"lightninginv/internal/catalog"
	"lightninginv/internal/confluentmetrics"
	"lightninginv/internal/cw"
	"lightninginv/internal/lightning"
	"lightninginv/internal/protocol"
	"lightninginv/internal/worker"
)

// Errors the HTTP layer maps to a status.
var (
	// ErrInvalid is a request the server will not apply: out of bounds.
	ErrInvalid = errors.New("invalid request")
	// ErrConflict is a request that is fine but not now.
	ErrConflict = errors.New("not possible now")
)

// aliveFor is how long after its last heartbeat a worker counts. It must stay longer than
// worker.StaleAfter: a worker cut off from control stops itself before its share is reassigned.
const aliveFor = 5 * time.Second

// Tables reads the Lightning Table of the topic. Terraform makes it, so the app only looks.
type Tables interface {
	Status(ctx context.Context) (phase lightning.Phase, message string, err error)
}

// LatencyReader reads the charts' data (CloudWatch).
type LatencyReader interface {
	Read(ctx context.Context, now time.Time, window time.Duration) (map[string]*cw.Series, error)
}

// UsageReader reads Confluent's own metrics for the table.
type UsageReader interface {
	Usage(ctx context.Context, now time.Time) (confluentmetrics.Usage, error)
}

// Options are what a Service needs besides its config.
type Options struct {
	DemoName, Region, Topic, Namespace string
	AccessToken, WorkerToken           string
	Tables                             Tables
	Latency                            LatencyReader
	Usage                              UsageReader
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

type load struct {
	running bool
	rate    int
}

type workerInfo struct {
	role string
	seen time.Time
}

type shard struct {
	owner string
	done  bool
}

// bucket is one second of what the workers reported.
type bucket struct {
	produced, produceErrs int64
	queries               map[string]protocol.QueryStats
}

// Service holds the state of the demo in memory.
type Service struct {
	o Options

	mu      sync.Mutex
	events  load
	queries load
	workers map[string]*workerInfo
	buckets map[int64]*bucket
	seed    struct {
		epoch   int // loads started so far; reports of an older one are ignored
		active  bool
		shards  [catalog.Shards]shard
		loaded  int64
		errors  int64
		started time.Time
	}
	table struct {
		phase   lightning.Phase
		message string
		err     string
	}
	usage    confluentmetrics.Usage
	usageErr string
	latCache map[time.Duration]latEntry
	latFly   map[time.Duration]*latCall
	latErr   map[time.Duration]latErrEntry
}

type latEntry struct {
	at     time.Time
	series map[string]*cw.Series
}

type latErrEntry struct {
	at  time.Time
	err error
}

// latCall is a CloudWatch read in flight, which requests for the same range wait for.
type latCall struct {
	done   chan struct{}
	series map[string]*cw.Series
	err    error
}

// New returns a Service with both loads stopped at their default rates.
func New(o Options) *Service {
	if o.Now == nil {
		o.Now = time.Now
	}
	return &Service{
		o:        o,
		events:   load{rate: worker.DefaultEventsPerSec},
		queries:  load{rate: worker.DefaultQueriesPerSec},
		workers:  map[string]*workerInfo{},
		buckets:  map[int64]*bucket{},
		latCache: map[time.Duration]latEntry{},
		latFly:   map[time.Duration]*latCall{},
		latErr:   map[time.Duration]latErrEntry{},
	}
}

func (s *Service) aliveLocked(role string, now time.Time) []string {
	var ids []string
	for id, w := range s.workers {
		if w.role == role && now.Sub(w.seen) <= aliveFor {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

// Heartbeat takes a worker's report and answers with its plan.
func (s *Service) Heartbeat(hb protocol.Heartbeat) (protocol.Plan, error) {
	if hb.ID == "" || len(hb.ID) > 64 || (hb.Role != "producer" && hb.Role != "query") || !hb.Valid() {
		return protocol.Plan{}, ErrInvalid
	}
	now := s.o.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.workers[hb.ID] = &workerInfo{role: hb.Role, seen: now}
	for id, w := range s.workers {
		if now.Sub(w.seen) > time.Minute {
			delete(s.workers, id)
		}
	}
	b := s.bucketLocked(now)
	b.produced += hb.Produced
	b.produceErrs += hb.ProduceErrors
	for sc, st := range hb.Queries {
		if sc != protocol.ScenarioBySKU && sc != protocol.ScenarioByLocations {
			continue
		}
		if b.queries == nil {
			b.queries = map[string]protocol.QueryStats{}
		}
		cur := b.queries[sc]
		cur.OK += st.OK
		cur.Errors += st.Errors
		cur.Dropped += st.Dropped
		cur.Rows += st.Rows
		b.queries[sc] = cur
	}

	switch hb.Role {
	case "producer":
		// Only a report of the current load counts: one from an earlier load (a producer
		// that has not yet seen the new epoch) must not mark this load's shards done.
		if s.seed.active && hb.SeedEpoch == s.seed.epoch {
			s.seed.loaded += hb.Seeded
			s.seed.errors += hb.SeedErrors
			for _, sh := range hb.SeedDone {
				if sh >= 0 && sh < catalog.Shards && s.seed.shards[sh].owner == hb.ID {
					s.seed.shards[sh].done = true
				}
			}
		}
		n := len(s.aliveLocked("producer", now))
		plan := protocol.Plan{Run: s.events.running, Rate: float64(s.events.rate) / float64(max(n, 1)), Workers: n}
		if s.seed.active {
			s.assignSeedLocked(now)
			var mine []int
			for i, sh := range s.seed.shards {
				if !sh.done && sh.owner == hb.ID {
					mine = append(mine, i)
				}
			}
			plan.SeedShards = mine
			plan.SeedEpoch = s.seed.epoch
			plan.SeedRate = float64(worker.SeedRecordsPerSec) / float64(max(s.seedProducersLocked(), 1))
		}
		return plan, nil
	default:
		n := len(s.aliveLocked("query", now))
		return protocol.Plan{Run: s.queries.running, Rate: float64(s.queries.rate) / float64(max(n, 1)), Workers: n}, nil
	}
}

func (s *Service) bucketLocked(now time.Time) *bucket {
	sec := now.Unix()
	b := s.buckets[sec]
	if b == nil {
		b = &bucket{}
		s.buckets[sec] = b
		for k := range s.buckets {
			if k < sec-30 {
				delete(s.buckets, k)
			}
		}
	}
	return b
}

// assignSeedLocked leases every shard that is not done to an alive producer: a shard
// whose owner has gone is given to the producer that owns the fewest.
func (s *Service) assignSeedLocked(now time.Time) {
	alive := s.aliveLocked("producer", now)
	if len(alive) == 0 {
		return
	}
	owned := map[string]int{}
	isAlive := map[string]bool{}
	for _, id := range alive {
		isAlive[id] = true
	}
	for _, sh := range s.seed.shards {
		if !sh.done && isAlive[sh.owner] {
			owned[sh.owner]++
		}
	}
	for i := range s.seed.shards {
		sh := &s.seed.shards[i]
		if sh.done || isAlive[sh.owner] {
			continue
		}
		best := alive[0]
		for _, id := range alive {
			if owned[id] < owned[best] {
				best = id
			}
		}
		sh.owner = best
		owned[best]++
	}
}

// seedProducersLocked counts the producers that still have a shard to load.
func (s *Service) seedProducersLocked() int {
	owners := map[string]bool{}
	for _, sh := range s.seed.shards {
		if !sh.done && sh.owner != "" {
			owners[sh.owner] = true
		}
	}
	return len(owners)
}

// LoadChange is what the browser may ask of a load: run or stop it, and its rate.
type LoadChange struct {
	Running *bool `json:"running"`
	Rate    *int  `json:"rate"`
}

func (s *Service) change(l *load, c LoadChange, maxRate int, needTable bool) error {
	if c.Rate != nil && (*c.Rate < 1 || *c.Rate > maxRate) {
		return fmt.Errorf("%w: the rate must be from 1 to %d per second", ErrInvalid, maxRate)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.Running != nil && *c.Running && needTable && s.table.phase != lightning.PhaseActive {
		return fmt.Errorf("%w: the Lightning Table is not active yet", ErrConflict)
	}
	if c.Rate != nil {
		l.rate = *c.Rate
	}
	if c.Running != nil {
		l.running = *c.Running
	}
	return nil
}

// ChangeEvents starts, stops or re-rates the live events (1..200 per second).
func (s *Service) ChangeEvents(c LoadChange) error {
	return s.change(&s.events, c, worker.MaxEventsPerSec, false)
}

// ChangeQueries starts, stops or re-rates the queries (1..1000 per second, in total).
func (s *Service) ChangeQueries(c LoadChange) error {
	return s.change(&s.queries, c, worker.MaxQueriesPerSec, true)
}

// StartSeed begins the one-off load of the catalogue.
func (s *Service) StartSeed() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	switch {
	case s.table.phase != lightning.PhaseActive:
		return fmt.Errorf("%w: load the catalogue when the Lightning Table is active", ErrConflict)
	case s.seed.active && !s.seedFinishedLocked():
		return fmt.Errorf("%w: the catalogue is already loading", ErrConflict)
	case len(s.aliveLocked("producer", s.o.Now())) == 0:
		return fmt.Errorf("%w: no producer task is running", ErrConflict)
	}
	s.seed.epoch++
	s.seed.active = true
	s.seed.shards = [catalog.Shards]shard{}
	s.seed.loaded, s.seed.errors = 0, 0
	s.seed.started = s.o.Now()
	return nil
}

func (s *Service) seedFinishedLocked() bool {
	for _, sh := range s.seed.shards {
		if !sh.done {
			return false
		}
	}
	return true
}
