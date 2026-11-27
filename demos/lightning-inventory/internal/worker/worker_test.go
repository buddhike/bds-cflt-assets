package worker

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"lightninginv/internal/catalog"
	"lightninginv/internal/cw"
	"lightninginv/internal/lightning"
	"lightninginv/internal/protocol"
)

func TestPaceRate(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var n atomic.Int64
	Pace(ctx, func() float64 { return 200 }, func() { n.Add(1) })
	if got := n.Load(); got < 100 || got > 260 {
		t.Fatalf("fired %d in 1 s at 200/s", got)
	}
}

// A per-worker rate under 1 per second (the operator's total split over more tasks than it has
// calls per second) must still fire: with credit capped at the rate it never reached one.
func TestPaceBelowOnePerSecond(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var n atomic.Int64
	Pace(ctx, func() float64 { return 0.5 }, func() { n.Add(1) })
	if got := n.Load(); got < 2 || got > 3 {
		t.Fatalf("fired %d times in 5 s at 0.5/s, want about 2", got)
	}
}

func TestPaceAtOnePerSecond(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	var n atomic.Int64
	Pace(ctx, func() float64 { return 1 }, func() { n.Add(1) })
	if got := n.Load(); got < 3 || got > 5 {
		t.Fatalf("fired %d times in 4 s at 1/s", got)
	}
}

// After an idle spell the pacer does not repay the time it was idle as a burst: at most a
// second of the rate, plus the one call that makes rates below 1 work.
func TestPaceDoesNotBurstAfterIdle(t *testing.T) {
	t.Parallel()
	var rate atomic.Int64 // tenths per second
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	var n atomic.Int64
	go func() { time.Sleep(1200 * time.Millisecond); rate.Store(1000) }()
	Pace(ctx, func() float64 { return float64(rate.Load()) / 10 }, func() { n.Add(1) })
	if got := n.Load(); got > 120 {
		t.Fatalf("fired %d in 0.3 s at 100/s after an idle spell", got)
	}
}

func TestPaceStopsAtZero(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	var n atomic.Int64
	Pace(ctx, func() float64 { return 0 }, func() { n.Add(1) })
	if n.Load() != 0 {
		t.Fatal("fired at rate 0")
	}
}

func TestCodecWireFormat(t *testing.T) {
	c, err := NewCodec(100042)
	if err != nil {
		t.Fatal(err)
	}
	b, err := c.Encode(Position{SKU: "SKU-0000001", LocationID: "LOC-0002", Quantity: 7})
	if err != nil {
		t.Fatal(err)
	}
	if b[0] != 0 || binary.BigEndian.Uint32(b[1:5]) != 100042 {
		t.Fatalf("header % x", b[:5])
	}
	if !strings.Contains(string(b[5:]), "SKU-0000001") {
		t.Fatal("payload")
	}
	if _, err := NewCodec(0); err == nil {
		t.Fatal("schema id 0 accepted")
	}
}

// controlStub answers every heartbeat with plan.
func controlStub(t *testing.T, plan func() protocol.Plan, got chan<- protocol.Heartbeat) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" || r.URL.Path != protocol.HeartbeatPath {
			w.WriteHeader(401)
			return
		}
		var hb protocol.Heartbeat
		_ = json.NewDecoder(r.Body).Decode(&hb)
		select {
		case got <- hb:
		default:
		}
		_ = json.NewEncoder(w).Encode(plan())
	}))
}

func TestControlPlanGoesStale(t *testing.T) {
	srv := controlStub(t, func() protocol.Plan { return protocol.Plan{Run: true, Rate: 5, Workers: 1} }, nil)
	c := NewControl(srv.URL, "tok")
	if c.Plan().Run {
		t.Fatal("ran before hearing from control")
	}
	if err := c.Poll(context.Background(), protocol.Heartbeat{}); err != nil {
		t.Fatal(err)
	}
	if p := c.Plan(); !p.Run || p.Rate != 5 {
		t.Fatalf("%+v", p)
	}
	c.setPlan(c.latest.Load().plan, time.Now().Add(-time.Minute))
	if c.Plan().Run {
		t.Fatal("kept running on a stale plan: a load nobody can stop")
	}
	srv.Close()
	if err := c.Poll(context.Background(), protocol.Heartbeat{}); err == nil {
		t.Fatal("no error from a closed server")
	}
	bad := NewControl(httptest.NewServer(http.NotFoundHandler()).URL, "tok")
	if err := bad.Poll(context.Background(), protocol.Heartbeat{}); err == nil {
		t.Fatal("404 accepted")
	}
}

type pendingRec struct {
	ctx  context.Context
	done func(error)
	over bool // delivered or failed already
}

type fakeSender struct {
	mu   sync.Mutex
	keys []string
	fail bool
	// hook, when set, is called with the 1-based number of each record and decides its outcome.
	hook func(n int) error
	// linger models the Kafka client's buffer: a record is not acknowledged by Send but by
	// Flush, and a record whose context has been cancelled by then fails with that error, as
	// franz-go fails buffered records of a cancelled context.
	linger  bool
	pending []*pendingRec
	acked   int // records acknowledged without error
	lost    int // records failed with a cancelled context
	// flushErr is what Flush returns, when set.
	flushErr error
	// flushWaits makes Flush wait until a buffered record's context ends, as it would if the
	// client could not deliver and the shard were abandoned meanwhile.
	flushWaits bool
}

func (f *fakeSender) Send(ctx context.Context, key string, _ []byte, done func(error)) {
	f.mu.Lock()
	f.keys = append(f.keys, key)
	n := len(f.keys)
	if f.linger {
		rec := &pendingRec{ctx: ctx, done: done}
		f.pending = append(f.pending, rec)
		f.mu.Unlock()
		// A record whose context ends while it is still buffered fails at once with that
		// error, as the Kafka client fails it, whether or not anyone flushes.
		context.AfterFunc(ctx, func() { f.finish(rec) })
		return
	}
	f.mu.Unlock()
	if f.hook != nil {
		done(f.hook(n))
		return
	}
	if f.fail {
		done(context.DeadlineExceeded)
		return
	}
	done(nil)
}

// finish settles a buffered record: failed if its context has ended, delivered if not.
func (f *fakeSender) finish(r *pendingRec) {
	f.mu.Lock()
	if r.over {
		f.mu.Unlock()
		return
	}
	r.over = true
	err := r.ctx.Err()
	if err != nil {
		f.lost++
	} else {
		f.acked++
	}
	f.mu.Unlock()
	r.done(err)
}

func (f *fakeSender) Flush(ctx context.Context) error {
	f.mu.Lock()
	pend := f.pending
	f.pending = nil
	waits, err := f.flushWaits, f.flushErr
	f.mu.Unlock()
	if waits && len(pend) > 0 {
		select {
		case <-pend[0].ctx.Done():
		case <-ctx.Done():
		}
	}
	for _, r := range pend {
		f.finish(r)
	}
	return err
}
func (f *fakeSender) n() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.keys)
}

func newProducer(t *testing.T, plan func() protocol.Plan) (*Producer, *fakeSender, chan protocol.Heartbeat, func()) {
	got := make(chan protocol.Heartbeat, 100)
	srv := controlStub(t, plan, got)
	codec, _ := NewCodec(1)
	cat, _ := catalog.New(1)
	fs := &fakeSender{}
	ctl := NewControl(srv.URL, "tok")
	return &Producer{Ctl: ctl, Sender: fs, Codec: codec, Catalog: cat, Seed: 1}, fs, got, srv.Close
}

func TestProducerRunsOnlyWhenToldAndKeysArePairs(t *testing.T) {
	var run atomic.Bool
	p, fs, _, closeSrv := newProducer(t, func() protocol.Plan { return protocol.Plan{Run: run.Load(), Rate: 100, Workers: 1} })
	defer closeSrv()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()
	time.Sleep(2500 * time.Millisecond)
	if fs.n() != 0 {
		t.Fatalf("produced %d before it was started", fs.n())
	}
	run.Store(true)
	time.Sleep(3500 * time.Millisecond) // one poll to learn the plan, then it runs
	run.Store(false)
	cancel()
	<-done
	if fs.n() < 100 {
		t.Fatalf("produced only %d", fs.n())
	}
	for _, k := range fs.keys {
		parts := strings.Split(k, "|")
		if len(parts) != 2 || !strings.HasPrefix(parts[0], "SKU-") || !strings.HasPrefix(parts[1], "LOC-") {
			t.Fatalf("key %q is not sku|location", k)
		}
	}
}

func TestProducerNeverExceedsTheEventBound(t *testing.T) {
	p, fs, _, closeSrv := newProducer(t, func() protocol.Plan { return protocol.Plan{Run: true, Rate: 1_000_000, Workers: 1} })
	defer closeSrv()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	p.Run(ctx)
	// about 2 s of running at 200/s
	if n := fs.n(); n > 2*MaxEventsPerSec+100 {
		t.Fatalf("produced %d: the server bound was not applied", n)
	}
}

func TestProducerLoadsALeasedShard(t *testing.T) {
	p, fs, got, closeSrv := newProducer(t, func() protocol.Plan {
		return protocol.Plan{SeedEpoch: 1, SeedShards: []int{3}, SeedRate: SeedRecordsPerSec, Workers: 1}
	})
	defer closeSrv()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	go p.Run(ctx)
	deadline := time.After(18 * time.Second)
	for {
		select {
		case hb := <-got:
			for _, s := range hb.SeedDone {
				if s == 3 {
					cat, _ := catalog.New(1)
					want := 0
					for i := 3; i < catalog.SKUs; i += catalog.Shards {
						want += cat.Count(i)
					}
					if fs.n() != want {
						t.Fatalf("shard 3 has %d records, sent %d", want, fs.n())
					}
					for _, k := range fs.keys[:100] {
						var sku int
						for _, c := range k[4:11] {
							sku = sku*10 + int(c-'0')
						}
						if catalog.ShardOf(sku) != 3 {
							t.Fatalf("%s is not in shard 3", k)
						}
					}
					return
				}
			}
		case <-deadline:
			t.Fatalf("shard not finished, sent %d", fs.n())
		}
	}
}

type fakePub struct {
	mu    sync.Mutex
	secs  map[string][]*cw.Second
	fail  bool
	calls int
}

func (f *fakePub) Publish(_ context.Context, s map[string][]*cw.Second) (map[string][]*cw.Second, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return s, errors.New("cloudwatch is down")
	}
	f.calls++
	for k, v := range s {
		f.secs[k] = append(f.secs[k], v...)
	}
	return nil, nil
}

func TestQuerierMeasuresAndBounds(t *testing.T) {
	var run atomic.Bool
	run.Store(true)
	got := make(chan protocol.Heartbeat, 100)
	srv := controlStub(t, func() protocol.Plan { return protocol.Plan{Run: run.Load(), Rate: 5000, Workers: 1} }, got)
	defer srv.Close()
	cat, _ := catalog.New(10)
	var calls atomic.Int64
	var sawBy, sawLoc atomic.Int64
	q := &Querier{
		Ctl: NewControl(srv.URL, "tok"), Rec: cw.NewRecorder(), Pub: &fakePub{secs: map[string][]*cw.Second{}},
		Topic: "inventory_positions", Catalog: cat, MissRatio: 0.1, Seed: 7,
		Query: func(ctx context.Context, sql string) (int, error) {
			n := calls.Add(1) // the number is taken here: concurrent queries must not share one
			if strings.Contains(sql, "IN (") {
				sawLoc.Add(1)
			} else {
				sawBy.Add(1)
			}
			time.Sleep(2 * time.Millisecond)
			if n%10 == 0 {
				return 0, context.DeadlineExceeded
			}
			return 3, nil
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	q.Run(ctx)
	// the plan arrives after the first poll (1 s), so about 3 s at the 1000/s bound
	if n := calls.Load(); n < 100 || n > 3500 {
		t.Fatalf("%d queries: want a few thousand at most (bound 1000/s)", n)
	}
	if sawBy.Load() == 0 || sawLoc.Load() == 0 {
		t.Fatal("both scenarios must run")
	}
	pub := q.Pub.(*fakePub)
	var lat, errs int64
	for _, list := range pub.secs {
		for _, s := range list {
			lat += s.Latency.Count()
			errs += s.Errors
		}
	}
	if lat == 0 || errs == 0 {
		t.Fatalf("published %d latencies and %d errors", lat, errs)
	}
}

func TestQuerierDropsWhenSaturated(t *testing.T) {
	cat, _ := catalog.New(10)
	srv := controlStub(t, func() protocol.Plan { return protocol.Plan{Run: true, Rate: 1000, Workers: 1} }, nil)
	defer srv.Close()
	block := make(chan struct{})
	q := &Querier{
		Ctl: NewControl(srv.URL, "tok"), Rec: cw.NewRecorder(), Pub: &fakePub{secs: map[string][]*cw.Second{}},
		Topic: "t", Catalog: cat, Seed: 1,
		Query: func(ctx context.Context, sql string) (int, error) { <-block; return 0, nil },
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	go func() { <-ctx.Done(); close(block) }()
	q.Run(ctx)
	// Drops are counted for the heartbeat (which zeroes them) and for CloudWatch; the final
	// flush has published every second of the second kind.
	var dropped int64
	for _, list := range q.Pub.(*fakePub).secs {
		for _, s := range list {
			dropped += s.Dropped
		}
	}
	if dropped == 0 {
		t.Fatal("nothing dropped although every query hangs")
	}
	if len(q.sem) != 0 {
		t.Fatal("in-flight slots leaked")
	}
}

// The counter a heartbeat zeroes is not the one that decides whether a shard loaded: a shard
// with a failed record is not done even if a heartbeat went out between the failure and the end.
func TestShardWithFailedRecordIsNotDoneAcrossAHeartbeat(t *testing.T) {
	cat, _ := catalog.New(1)
	codec, _ := NewCodec(1)
	fs := &fakeSender{}
	ctl := NewControl("http://unused.invalid", "tok")
	ctl.setPlan(protocol.Plan{SeedEpoch: 1, SeedShards: []int{3}, SeedRate: SeedRecordsPerSec, Workers: 1}, time.Now())
	p := &Producer{Ctl: ctl, Sender: fs, Codec: codec, Catalog: cat, Seed: 1, seedEpoch: 1}
	var reported protocol.Heartbeat
	fs.hook = func(n int) error {
		switch n {
		case 1:
			return context.DeadlineExceeded // a record that was not delivered
		case 2:
			reported = p.heartbeat() // the heartbeat takes the error count and zeroes it
		}
		return nil
	}
	_, err := p.loadShard(context.Background(), 3, 1)
	if err == nil || !strings.Contains(err.Error(), "1 records") {
		t.Fatalf("the shard was reported done despite a failed record: %v", err)
	}
	if reported.SeedErrors != 0 {
		t.Fatalf("a heartbeat in mid-shard reported %d errors: they are reported when the shard ends", reported.SeedErrors)
	}
	if hb := p.heartbeat(); hb.SeedErrors != 1 || len(hb.SeedDone) != 0 {
		t.Fatalf("after the failed shard: %+v", hb)
	}

	// the same shard with no failures is done, and its records are returned once
	fs2 := &fakeSender{}
	p.Sender = fs2
	n, err := p.loadShard(context.Background(), 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(shardRecords(cat, 3)) {
		t.Fatalf("delivered %d, want %d", n, shardRecords(cat, 3))
	}
}

func TestLiveErrorsAreCountedForTheHeartbeat(t *testing.T) {
	cat, _ := catalog.New(1)
	codec, _ := NewCodec(1)
	p := &Producer{Sender: &fakeSender{fail: true}, Codec: codec, Catalog: cat}
	p.send(context.Background(), "SKU-0000001", "LOC-0001", 1, &p.produced, &p.produceErrs, nil, nil)
	if hb := p.heartbeat(); hb.ProduceErrors != 1 || hb.Produced != 0 {
		t.Fatalf("%+v", hb)
	}
}

// When a heartbeat does not get through, its counts ride on the next one.
func TestFailedPollKeepsItsCounts(t *testing.T) {
	var calls atomic.Int64
	got := make(chan protocol.Heartbeat, 50)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		if n <= 2 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		var hb protocol.Heartbeat
		_ = json.NewDecoder(r.Body).Decode(&hb)
		got <- hb
		_ = json.NewEncoder(w).Encode(protocol.Plan{})
	}))
	defer srv.Close()
	c := NewControl(srv.URL, "tok")
	c.Every = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var n atomic.Int64
	go c.Loop(ctx, func() protocol.Heartbeat {
		n.Add(1)
		return protocol.Heartbeat{Role: "query", Produced: 1, SeedDone: []int{4}, Queries: map[string]protocol.QueryStats{"by_sku": {OK: 2, Rows: 10}}}
	})
	select {
	case hb := <-got:
		// two failed polls, then this one: three collections, nothing lost
		if hb.Produced != 3 || hb.Queries["by_sku"].OK != 6 || hb.Queries["by_sku"].Rows != 30 {
			t.Fatalf("the counts of the failed polls were lost: %+v", hb)
		}
		if len(hb.SeedDone) != 1 {
			t.Fatalf("seed_done is a set: %v", hb.SeedDone)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no heartbeat got through")
	}
	// after a delivery the next heartbeat carries only its own counts
	select {
	case hb := <-got:
		if hb.Produced != 1 {
			t.Fatalf("counts were sent twice: %+v", hb)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no second heartbeat")
	}
}

func TestHeartbeatMergeAndBounds(t *testing.T) {
	a := protocol.Heartbeat{Produced: 1, SeedDone: []int{1, 2}, Queries: map[string]protocol.QueryStats{"by_sku": {OK: 1}}}
	a.Merge(protocol.Heartbeat{Produced: 2, SeedDone: []int{2, 3}, Queries: map[string]protocol.QueryStats{"by_sku": {OK: 4, Errors: 1}, "by_locations": {OK: 1}}})
	if a.Produced != 3 || len(a.SeedDone) != 3 || a.Queries["by_sku"].OK != 5 || a.Queries["by_locations"].OK != 1 {
		t.Fatalf("%+v", a)
	}
	if !a.Valid() {
		t.Fatal("a normal heartbeat is invalid")
	}
	// the seed fields belong to one load: a newer load replaces an older one's, and an older
	// one's are dropped when merged into a newer
	n := protocol.Heartbeat{SeedEpoch: 1, SeedErrors: 5, SeedDone: []int{1}, SeedRecords: map[int]int64{1: 10}}
	n.Merge(protocol.Heartbeat{SeedEpoch: 2, SeedErrors: 7, SeedDone: []int{2}, SeedRecords: map[int]int64{2: 20}})
	if n.SeedEpoch != 2 || n.SeedErrors != 7 || len(n.SeedDone) != 1 || n.SeedDone[0] != 2 || len(n.SeedRecords) != 1 || n.SeedRecords[2] != 20 {
		t.Fatalf("%+v", n)
	}
	n.Merge(protocol.Heartbeat{SeedEpoch: 1, SeedErrors: 100, SeedDone: []int{9}, SeedRecords: map[int]int64{9: 1}})
	if n.SeedErrors != 7 || len(n.SeedDone) != 1 || len(n.SeedRecords) != 1 {
		t.Fatalf("an older load's counts were merged into a newer: %+v", n)
	}
	if (protocol.Heartbeat{SeedEpoch: -1}).Valid() {
		t.Fatal("a negative epoch is valid")
	}
	for _, bad := range []protocol.Heartbeat{
		{Produced: -1}, {Produced: protocol.MaxCount + 1}, {SeedErrors: 1 << 40}, {SeedRecords: map[int]int64{1: -1}},
		{Queries: map[string]protocol.QueryStats{"by_sku": {OK: -5}}},
		{Queries: map[string]protocol.QueryStats{"by_sku": {Rows: protocol.MaxRows + 1}}},
		{SeedDone: make([]int, 65)},
	} {
		if bad.Valid() {
			t.Errorf("accepted %+v", bad)
		}
	}
}

func TestClassify(t *testing.T) {
	cases := map[Outcome]error{
		OutcomeRateLimited: lightning.ErrRateLimited,
		OutcomeAsync:       lightning.ErrAsync,
		OutcomeNotFound:    lightning.ErrNotFound,
		OutcomeServerError: &lightning.StatusError{Status: 503},
		OutcomeClientError: &lightning.StatusError{Status: 403},
		OutcomeTimeout:     context.DeadlineExceeded,
		OutcomeOther:       errors.New("the response is not the documented JSON"),
	}
	for want, err := range cases {
		if got := Classify(fmt.Errorf("wrapped: %w", err)); got != want {
			t.Errorf("%v: got %v, want %v", err, outcomeNames[got], outcomeNames[want])
		}
	}
}

// CloudWatch failing: the seconds are kept and go out with the next flush, once; the backlog
// is bounded.
func TestPublishFailureIsRetriedFromABoundedBacklog(t *testing.T) {
	pub := &fakePub{secs: map[string][]*cw.Second{}, fail: true}
	now := time.Unix(10_000, 0)
	q := &Querier{Rec: cw.NewRecorder(), Pub: pub, Now: func() time.Time { return now }}
	q.init()
	q.Rec.Success("by_sku", now, 5*time.Millisecond)
	now = now.Add(2 * time.Second)
	if err := q.flushOnce(context.Background(), now); err == nil {
		t.Fatal("no error from a failing publisher")
	}
	if len(q.backlog["by_sku"]) != 1 {
		t.Fatalf("backlog %v", q.backlog)
	}
	// a new second arrives while it is down
	q.Rec.Success("by_sku", now, 6*time.Millisecond)
	now = now.Add(2 * time.Second)
	_ = q.flushOnce(context.Background(), now)
	if len(q.backlog["by_sku"]) != 2 {
		t.Fatalf("backlog %d", len(q.backlog["by_sku"]))
	}
	// it comes back: both seconds are published, once
	pub.mu.Lock()
	pub.fail = false
	pub.mu.Unlock()
	now = now.Add(time.Second)
	if err := q.flushOnce(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(pub.secs["by_sku"]) != 2 || len(q.backlog["by_sku"]) != 0 {
		t.Fatalf("published %d, backlog %d", len(pub.secs["by_sku"]), len(q.backlog["by_sku"]))
	}
	if err := q.flushOnce(context.Background(), now.Add(time.Second)); err != nil || pub.calls != 1 {
		t.Fatalf("published again: calls %d, err %v", pub.calls, err)
	}

	// a long outage does not grow without bound, and the newest seconds are the ones kept
	pub.mu.Lock()
	pub.fail = true
	pub.mu.Unlock()
	base := now
	for i := 0; i < maxBacklogSeconds+50; i++ {
		at := base.Add(time.Duration(i) * time.Second)
		q.Rec.Success("by_sku", at, time.Millisecond)
		_ = q.flushOnce(context.Background(), at.Add(time.Second))
	}
	b := q.backlog["by_sku"]
	if len(b) != maxBacklogSeconds {
		t.Fatalf("backlog %d, want %d", len(b), maxBacklogSeconds)
	}
	if want := base.Add(time.Duration(maxBacklogSeconds+49) * time.Second).Unix(); b[len(b)-1].At.Unix() != want {
		t.Fatalf("the newest second was dropped: %d, want %d", b[len(b)-1].At.Unix(), want)
	}
}

// keepFresh stores plan on ctl and keeps it from going stale, as a control that answers would.
func keepFresh(ctx context.Context, ctl *Control, plan func() protocol.Plan) {
	set := func() {
		pl := plan()
		ctl.setPlan(pl, time.Now())
	}
	set()
	go func() {
		t := time.NewTicker(20 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				set()
			}
		}
	}()
}

func shardRecords(cat catalog.Catalog, shard int) int {
	want := 0
	for i := shard; i < catalog.SKUs; i += catalog.Shards {
		want += cat.Count(i)
	}
	return want
}

// The last records of a shard are still buffered in the Kafka client when the pacer is done.
// They must be delivered by the flush, not failed because the shard's context was cancelled to
// stop the pacer: that failure was ignored and the shard reported done with records lost.
func TestShardTailIsDeliveredNotCancelled(t *testing.T) {
	cat, _ := catalog.New(1)
	codec, _ := NewCodec(1)
	fs := &fakeSender{linger: true}
	ctl := NewControl("http://unused.invalid", "tok")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	keepFresh(ctx, ctl, func() protocol.Plan {
		return protocol.Plan{SeedEpoch: 1, SeedShards: []int{3}, SeedRate: SeedRecordsPerSec}
	})
	p := &Producer{Ctl: ctl, Sender: fs, Codec: codec, Catalog: cat, Seed: 1, seedEpoch: 1}
	n, err := p.loadShard(ctx, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	want := shardRecords(cat, 3)
	if fs.lost != 0 || fs.acked != want || n != int64(want) {
		t.Fatalf("%d of %d records delivered (returned %d), %d lost to a cancelled context", fs.acked, want, n, fs.lost)
	}
}

// A shard that is abandoned (the lease moves away) fails, and its records are not counted.
func TestAbandonedShardIsNotDoneAndNotCounted(t *testing.T) {
	cat, _ := catalog.New(1)
	codec, _ := NewCodec(1)
	fs := &fakeSender{linger: true}
	ctl := NewControl("http://unused.invalid", "tok")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var taken atomic.Bool
	keepFresh(ctx, ctl, func() protocol.Plan {
		if taken.Load() {
			return protocol.Plan{SeedEpoch: 1, SeedRate: 2000} // the shard is somebody else's now; the rate is still announced
		}
		return protocol.Plan{SeedEpoch: 1, SeedShards: []int{3}, SeedRate: 2000}
	})
	p := &Producer{Ctl: ctl, Sender: fs, Codec: codec, Catalog: cat, Seed: 1, seedEpoch: 1}
	go func() { time.Sleep(300 * time.Millisecond); taken.Store(true) }()
	n, err := p.loadShard(ctx, 3, 1)
	if !errors.Is(err, errLeaseLost) || n != 0 {
		t.Fatalf("err %v, %d records", err, n)
	}
	// Nothing is produced for a shard that is no longer ours, not even until the next lease
	// check a second later: at 2000/s that would be ~2600 records by now, not ~600.
	if sent := fs.n(); sent > 1200 {
		t.Fatalf("%d records were produced although the lease was gone after 0.3 s", sent)
	}
	// The buffered records of the abandoned shard fail as cancelled (the fake does, as the
	// client does); that is not a produce error to report.
	deadline := time.Now().Add(3 * time.Second)
	for {
		fs.mu.Lock()
		lost := fs.lost
		fs.mu.Unlock()
		if lost > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	fs.mu.Lock()
	lost := fs.lost
	fs.mu.Unlock()
	if lost == 0 {
		t.Fatal("the abandoned shard's pending records were never failed: the test does not exercise the cancelled path")
	}
	if hb := p.heartbeat(); hb.SeedErrors != 0 || len(hb.SeedDone) != 0 {
		t.Fatalf("cancelled records counted as errors: %+v", hb)
	}
}

// A second load of the catalogue must produce the records again: the shards a producer
// finished in the first belong to it, and a new epoch makes it forget them.
func TestSecondCatalogueLoadProducesAgain(t *testing.T) {
	cat, _ := catalog.New(1)
	codec, _ := NewCodec(1)
	fs := &fakeSender{}
	ctl := NewControl("http://unused.invalid", "tok")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var epoch atomic.Int64
	epoch.Store(1)
	keepFresh(ctx, ctl, func() protocol.Plan {
		return protocol.Plan{SeedEpoch: int(epoch.Load()), SeedShards: []int{3}, SeedRate: SeedRecordsPerSec}
	})
	p := &Producer{Ctl: ctl, Sender: fs, Codec: codec, Catalog: cat, Seed: 1}
	go p.seedLoop(ctx)
	want := shardRecords(cat, 3)
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out waiting for %s (sent %d)", what, fs.n())
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	done := func(e int) func() bool {
		return func() bool {
			hb := p.heartbeat()
			return hb.SeedEpoch == e && len(hb.SeedDone) == 1
		}
	}
	waitFor("the first load", done(1))
	if fs.n() != want {
		t.Fatalf("first load sent %d, want %d", fs.n(), want)
	}
	epoch.Store(2)
	waitFor("the second load", done(2))
	if fs.n() != 2*want {
		t.Fatalf("second load sent %d records in total, want %d: the load was a silent no-op", fs.n(), 2*want)
	}
	// and the report of the second load carries its own epoch, which control matches
	if hb := p.heartbeat(); hb.SeedEpoch != 2 {
		t.Fatalf("%+v", hb)
	}
}

// The live events still buffered when the task is told to stop are delivered by the final
// flush, not failed as cancelled.
func TestLiveEventTailIsDeliveredAtStop(t *testing.T) {
	p, fs, _, closeSrv := newProducer(t, func() protocol.Plan { return protocol.Plan{Run: true, Rate: 100, Workers: 1} })
	defer closeSrv()
	fs.linger = true
	ctx, cancel := context.WithTimeout(context.Background(), 2500*time.Millisecond)
	defer cancel()
	p.Run(ctx)
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if len(fs.keys) == 0 {
		t.Fatal("nothing produced")
	}
	if fs.lost != 0 || fs.acked != len(fs.keys) {
		t.Fatalf("%d of %d delivered, %d lost to the cancelled context", fs.acked, len(fs.keys), fs.lost)
	}
}

func TestSeedLoopBacksOffAndRecovers(t *testing.T) {
	ctl := NewControl("http://unused.invalid", "tok")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	keepFresh(ctx, ctl, func() protocol.Plan {
		return protocol.Plan{SeedEpoch: 1, SeedShards: []int{3}, SeedRate: 1000}
	})
	var mu sync.Mutex
	var at []time.Time
	p := &Producer{Ctl: ctl, SeedBackoffMin: 20 * time.Millisecond, SeedBackoffMax: 60 * time.Millisecond}
	p.loadFn = func(context.Context, int, int) (int64, error) {
		mu.Lock()
		defer mu.Unlock()
		at = append(at, time.Now())
		if len(at) <= 4 {
			return 0, errors.New("kafka is down")
		}
		return 42, nil
	}
	go p.seedLoop(ctx)
	deadline := time.Now().Add(5 * time.Second)
	for {
		p.mu.Lock()
		ok := p.seedDone[3]
		p.mu.Unlock()
		if ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the shard was never marked done after the cluster came back")
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(at) != 5 {
		t.Fatalf("%d attempts", len(at))
	}
	// waits of 20, 40, 60 (capped), 60 ms: each attempt is not sooner than its wait
	for i, min := range []time.Duration{20, 40, 60, 60} {
		if gap := at[i+1].Sub(at[i]); gap < min*time.Millisecond-2*time.Millisecond {
			t.Errorf("attempt %d came %v after the failure, want at least %v", i+2, gap, min*time.Millisecond)
		}
	}
	if got := nextBackoff(0, time.Second, 30*time.Second); got != time.Second {
		t.Fatal(got)
	}
	if got := nextBackoff(20*time.Second, time.Second, 30*time.Second); got != 30*time.Second {
		t.Fatal(got)
	}
}

// Counts that could not be delivered for longer than the hold are dropped, not sent in one
// late burst into a second they do not belong to.
func TestPollCountsExpireAfterTheHold(t *testing.T) {
	start := time.Now()
	got := make(chan protocol.Heartbeat, 50)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if time.Since(start) < 400*time.Millisecond {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		var hb protocol.Heartbeat
		_ = json.NewDecoder(r.Body).Decode(&hb)
		got <- hb
		_ = json.NewEncoder(w).Encode(protocol.Plan{})
	}))
	defer srv.Close()
	c := NewControl(srv.URL, "tok")
	c.Every, c.Hold = 10*time.Millisecond, 60*time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var collected atomic.Int64
	go c.Loop(ctx, func() protocol.Heartbeat { collected.Add(1); return protocol.Heartbeat{Role: "query", Produced: 1} })
	select {
	case hb := <-got:
		// about 40 collections failed; only those of the last ~60 ms may have been kept
		if collected.Load() < 20 || hb.Produced > 15 {
			t.Fatalf("delivered %d counts after %d collections: old counts were not dropped", hb.Produced, collected.Load())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing delivered")
	}
}

func TestClassifyNetworkErrors(t *testing.T) {
	if got := Classify(&net.DNSError{Err: "i/o timeout", IsTimeout: true}); got != OutcomeTimeout {
		t.Errorf("a network timeout is %v", outcomeNames[got])
	}
	if got := Classify(&net.OpError{Op: "dial", Err: errors.New("connection refused")}); got != OutcomeNetwork {
		t.Errorf("a refused connection is %v", outcomeNames[got])
	}
}

// When the rate is lowered the credit saved up at the old one goes with it: no burst.
func TestPaceRateLowered(t *testing.T) {
	t.Parallel()
	var rate atomic.Int64 // tenths per second
	rate.Store(10_000)    // 1000/s
	ctx, cancel := context.WithTimeout(context.Background(), 1600*time.Millisecond)
	defer cancel()
	var n, atLower atomic.Int64
	go func() {
		time.Sleep(600 * time.Millisecond)
		rate.Store(20) // 2/s
		// a tick already under way still fires at the old rate: count from after it
		time.Sleep(100 * time.Millisecond)
		atLower.Store(n.Load())
	}()
	Pace(ctx, func() float64 { return float64(rate.Load()) / 10 }, func() { n.Add(1) })
	if after := n.Load() - atLower.Load(); after > 12 {
		t.Fatalf("%d calls in 0.9 s after the rate dropped to 2/s", after)
	}
}

func planOf(epoch int, shards ...int) protocol.Plan {
	return protocol.Plan{SeedEpoch: epoch, SeedShards: shards, SeedRate: 1000}
}

// Control's epochs are in memory: a restarted control may announce a lower number than the one
// this producer holds. The producer must follow it, not wait for a larger one for ever.
func TestProducerFollowsAnEpochThatDecreases(t *testing.T) {
	ctl := NewControl("http://unused.invalid", "tok")
	p := &Producer{Ctl: ctl}
	p.initSeed()
	ctl.setPlan(planOf(5, 7), time.Now())
	if sh, e, ok := p.nextShard(); !ok || sh != 7 || e != 5 {
		t.Fatalf("%v %v %v", sh, e, ok)
	}
	p.seedDone[7] = true
	// control restarts and starts a load of its own: epoch 2, which is lower
	ctl.setPlan(planOf(2, 9), time.Now())
	sh, e, ok := p.nextShard()
	if !ok || sh != 9 || e != 2 {
		t.Fatalf("the producer ignored a plan with a lower epoch: %v %v %v", sh, e, ok)
	}
	if hb := p.heartbeat(); hb.SeedEpoch != 2 || len(hb.SeedDone) != 0 {
		t.Fatalf("what the producer finished in the old load was kept: %+v", hb)
	}
	// epoch 0 is no load (or a plan gone stale): it changes nothing
	ctl.setPlan(protocol.Plan{}, time.Now())
	if _, _, ok := p.nextShard(); ok {
		t.Fatal("a shard from a plan with no load")
	}
	if p.seedEpoch != 2 {
		t.Fatalf("epoch 0 reset the producer's epoch to %d", p.seedEpoch)
	}
}

// What a producer counted in one load is not reported under the next one's epoch.
func TestCountersDoNotCrossEpochs(t *testing.T) {
	ctl := NewControl("http://unused.invalid", "tok")
	p := &Producer{Ctl: ctl}
	p.initSeed()
	ctl.setPlan(planOf(1, 3), time.Now())
	p.nextShard()
	p.seedErrs.Add(5)
	p.seedDone[3], p.seedRecs[3] = true, 100
	ctl.setPlan(planOf(2, 3), time.Now())
	p.nextShard()
	hb := p.heartbeat()
	if hb.SeedEpoch != 2 || hb.SeedErrors != 0 || len(hb.SeedDone) != 0 || len(hb.SeedRecords) != 0 {
		t.Fatalf("the old load's counts are reported under the new epoch: %+v", hb)
	}
	// and a shard of the old load that ends late adds nothing to the new one
	fs := &fakeSender{fail: true}
	codec, _ := NewCodec(1)
	cat, _ := catalog.New(1)
	late := &Producer{Ctl: ctl, Sender: fs, Codec: codec, Catalog: cat, seedEpoch: 2}
	late.initSeed()
	ctl.setPlan(planOf(1, 3), time.Now()) // this shard was loaded for epoch 1
	ctx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	_, _ = late.loadShard(ctx, 3, 1)
	if hb := late.heartbeat(); hb.SeedErrors != 0 {
		t.Fatalf("an old load's failures were counted in the new one: %+v", hb)
	}
}

func TestLostLeaseDoesNotBackOff(t *testing.T) {
	ctl := NewControl("http://unused.invalid", "tok")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	keepFresh(ctx, ctl, func() protocol.Plan { return planOf(1, 3) })
	var mu sync.Mutex
	var at []time.Time
	p := &Producer{Ctl: ctl, SeedBackoffMin: 300 * time.Millisecond, SeedBackoffMax: time.Second}
	p.loadFn = func(context.Context, int, int) (int64, error) {
		mu.Lock()
		defer mu.Unlock()
		at = append(at, time.Now())
		if len(at) <= 3 {
			return 0, errLeaseLost
		}
		return 1, nil
	}
	go p.seedLoop(ctx)
	deadline := time.Now().Add(3 * time.Second)
	for {
		mu.Lock()
		n := len(at)
		mu.Unlock()
		if n >= 4 || time.Now().After(deadline) {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(at) < 4 || at[3].Sub(at[0]) > 250*time.Millisecond {
		t.Fatalf("a lost lease made the task wait: %d attempts over %v", len(at), at[len(at)-1].Sub(at[0]))
	}
}

// A flush that fails is an error of the load, shown in the UI, not only a line in the log.
func TestFlushFailureIsCounted(t *testing.T) {
	cat, _ := catalog.New(1)
	codec, _ := NewCodec(1)
	fs := &fakeSender{flushErr: errors.New("broker unreachable")}
	ctl := NewControl("http://unused.invalid", "tok")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	keepFresh(ctx, ctl, func() protocol.Plan {
		return protocol.Plan{SeedEpoch: 1, SeedShards: []int{3}, SeedRate: SeedRecordsPerSec}
	})
	p := &Producer{Ctl: ctl, Sender: fs, Codec: codec, Catalog: cat, seedEpoch: 1}
	p.initSeed()
	if _, err := p.loadShard(ctx, 3, 1); err == nil {
		t.Fatal("a failed flush was not an error")
	}
	if hb := p.heartbeat(); hb.SeedErrors < 1 || len(hb.SeedDone) != 0 {
		t.Fatalf("%+v", hb)
	}
}

// The shard is taken away while its flush is running (up to 30 s): the producer notices, and
// does not report it done.
func TestLeaseIsCheckedDuringTheFlush(t *testing.T) {
	cat, _ := catalog.New(1)
	codec, _ := NewCodec(1)
	fs := &fakeSender{linger: true, flushWaits: true}
	ctl := NewControl("http://unused.invalid", "tok")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var taken atomic.Bool
	keepFresh(ctx, ctl, func() protocol.Plan {
		if taken.Load() {
			return protocol.Plan{SeedEpoch: 1}
		}
		return protocol.Plan{SeedEpoch: 1, SeedShards: []int{3}, SeedRate: SeedRecordsPerSec}
	})
	p := &Producer{Ctl: ctl, Sender: fs, Codec: codec, Catalog: cat, seedEpoch: 1}
	p.initSeed()
	go func() { time.Sleep(1200 * time.Millisecond); taken.Store(true) }() // after the pacer is done (0.6 s)
	start := time.Now()
	_, err := p.loadShard(ctx, 3, 1)
	if !errors.Is(err, errLeaseLost) {
		t.Fatalf("err %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %v: the flush was not cut short", time.Since(start))
	}
}

// The plan's age is measured on the monotonic clock, so a step of the wall clock cannot make
// a fresh plan stale or keep an old one.
func TestPlanAgeIsMonotonic(t *testing.T) {
	srv := controlStub(t, func() protocol.Plan { return protocol.Plan{Run: true} }, nil)
	defer srv.Close()
	c := NewControl(srv.URL, "tok")
	if err := c.Poll(context.Background(), protocol.Heartbeat{}); err != nil {
		t.Fatal(err)
	}
	if at := c.latest.Load().at; !strings.Contains(at.String(), "m=") {
		t.Fatalf("the plan's arrival time %q has no monotonic reading", at.String())
	}
}

// At stop the client is closed under the events still being produced: that is not a failed
// record.
func TestClientClosedAtStopIsNotAProduceError(t *testing.T) {
	cat, _ := catalog.New(1)
	codec, _ := NewCodec(1)
	fs := &fakeSender{hook: func(int) error { return kgo.ErrClientClosed }}
	p := &Producer{Sender: fs, Codec: codec, Catalog: cat}
	p.send(context.Background(), "SKU-0000001", "LOC-0001", 1, &p.produced, &p.produceErrs, nil, nil)
	if hb := p.heartbeat(); hb.ProduceErrors != 0 {
		t.Fatalf("ErrClientClosed counted as %d produce errors", hb.ProduceErrors)
	}
}
