package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"lightninginv/internal/catalog"
	"lightninginv/internal/confluentmetrics"
	"lightninginv/internal/cw"
	"lightninginv/internal/lightning"
	"lightninginv/internal/protocol"
	"lightninginv/internal/worker"
)

const (
	access = "access-token-access-token-1234"
	wtok   = "worker-token-worker-token-1234"
)

// fakeTables answers Status from fields the test sets. It has no way to change the table: the
// Tables interface only reads.
type fakeTables struct {
	mu    sync.Mutex
	phase lightning.Phase
	msg   string
	err   error
	calls int
}

func (f *fakeTables) Status(context.Context) (lightning.Phase, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return "", "", f.err
	}
	return f.phase, f.msg, nil
}

func (f *fakeTables) called() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

func (f *fakeTables) set(p lightning.Phase, err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.phase, f.err = p, err
}

type fakeLatency struct {
	calls atomic.Int64
	// gate, when set, holds every Read until it is closed.
	gate chan struct{}
	// deadline is true when the last Read's context had a deadline.
	deadline atomic.Bool
	panics   bool
	// failWith, when set, is what every Read returns.
	failWith error
}

func (f *fakeLatency) Read(ctx context.Context, _ time.Time, _ time.Duration) (map[string]*cw.Series, error) {
	f.calls.Add(1)
	_, has := ctx.Deadline()
	f.deadline.Store(has)
	if f.panics {
		panic("boom")
	}
	if f.failWith != nil {
		return nil, f.failWith
	}
	if f.gate != nil {
		<-f.gate
	}
	return map[string]*cw.Series{"by_sku": {Times: []int64{1}}}, nil
}

func newSvc(t *testing.T) (*Service, *fakeTables, *fakeLatency, *time.Time) {
	t.Helper()
	ft := &fakeTables{phase: lightning.PhaseActive}
	fl := &fakeLatency{}
	clock := time.Unix(1_000_000, 0)
	s := New(Options{DemoName: "d", Region: "us-east-1", Topic: "inventory_positions", Namespace: "ns",
		AccessToken: access, WorkerToken: wtok, Tables: ft, Latency: fl, Now: func() time.Time { return clock }})
	return s, ft, fl, &clock
}

func hb(id, role string) protocol.Heartbeat { return protocol.Heartbeat{ID: id, Role: role} }

func TestRatesAreBoundedByTheServer(t *testing.T) {
	s, _, _, _ := newSvc(t)
	s.setTable(lightning.PhaseActive, "", "")
	for _, r := range []int{0, -1, 201, 100000} {
		if err := s.ChangeEvents(LoadChange{Rate: &r}); !errors.Is(err, ErrInvalid) {
			t.Errorf("events rate %d: %v", r, err)
		}
	}
	for _, r := range []int{0, 1001, 5000} {
		if err := s.ChangeQueries(LoadChange{Rate: &r}); !errors.Is(err, ErrInvalid) {
			t.Errorf("queries rate %d: %v", r, err)
		}
	}
	for _, r := range []int{1, 200} {
		if err := s.ChangeEvents(LoadChange{Rate: &r}); err != nil {
			t.Errorf("events rate %d: %v", r, err)
		}
	}
	r := 1000
	if err := s.ChangeQueries(LoadChange{Rate: &r}); err != nil {
		t.Error(err)
	}
	st := s.State()
	if st.Events.Rate != 200 || st.Queries.Rate != 1000 || st.EventsBounds.Max != 200 || st.QueriesBounds.Max != 1000 {
		t.Fatalf("%+v", st)
	}
}

func TestDefaultsAndNothingAutostarts(t *testing.T) {
	s, _, _, _ := newSvc(t)
	st := s.State()
	if st.Events.Running || st.Queries.Running || st.Events.Rate != 10 || st.Queries.Rate != 50 {
		t.Fatalf("%+v", st)
	}
	p, err := s.Heartbeat(hb("p1", "producer"))
	if err != nil || p.Run {
		t.Fatalf("a producer was told to run before the operator clicked start: %+v", p)
	}
	p, _ = s.Heartbeat(hb("q1", "query"))
	if p.Run {
		t.Fatal("a query worker was told to run before the operator clicked start")
	}
}

func TestQueriesNeedAnActiveTable(t *testing.T) {
	s, _, _, _ := newSvc(t)
	on := true
	if err := s.ChangeQueries(LoadChange{Running: &on}); !errors.Is(err, ErrConflict) {
		t.Fatal("queries started with no active table:", err)
	}
	s.setTable(lightning.PhaseActive, "", "")
	if err := s.ChangeQueries(LoadChange{Running: &on}); err != nil {
		t.Fatal(err)
	}
}

func TestRateIsSplitBetweenWorkers(t *testing.T) {
	s, _, _, clock := newSvc(t)
	s.setTable(lightning.PhaseActive, "", "")
	on, r := true, 100
	_ = s.ChangeQueries(LoadChange{Running: &on, Rate: &r})
	s.Heartbeat(hb("a", "query"))
	p, _ := s.Heartbeat(hb("b", "query"))
	if p.Workers != 2 || p.Rate != 50 || !p.Run {
		t.Fatalf("%+v", p)
	}
	// a worker that went away is no longer counted
	*clock = clock.Add(10 * time.Second)
	p, _ = s.Heartbeat(hb("b", "query"))
	if p.Workers != 1 || p.Rate != 100 {
		t.Fatalf("%+v", p)
	}
	// producers are counted apart from query workers
	e := 10
	_ = s.ChangeEvents(LoadChange{Running: &on, Rate: &e})
	p, _ = s.Heartbeat(hb("x", "producer"))
	if p.Workers != 1 || p.Rate != 10 {
		t.Fatalf("%+v", p)
	}
}

func TestHeartbeatValidation(t *testing.T) {
	s, _, _, _ := newSvc(t)
	for _, h := range []protocol.Heartbeat{{}, {ID: "x"}, {ID: "x", Role: "control"}, {ID: strings.Repeat("a", 100), Role: "query"}} {
		if _, err := s.Heartbeat(h); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v accepted", h)
		}
	}
}

func TestLiveRatesFromReports(t *testing.T) {
	s, _, _, clock := newSvc(t)
	for i := 0; i < 5; i++ {
		s.Heartbeat(protocol.Heartbeat{ID: "q", Role: "query", Queries: map[string]protocol.QueryStats{
			"by_sku": {OK: 50, Errors: 5, Rows: 500}, "nonsense": {OK: 5}}})
		*clock = clock.Add(time.Second)
	}
	st := s.State()
	if st.Queries.ByScenario["by_sku"].OKPerSec != 25 || st.Queries.ByScenario["by_sku"].AvgRows != 10 || st.Queries.ErrorsPerSec != 2.5 {
		t.Fatalf("%+v", st.Queries)
	}
}

func TestSeedLeasesShardsAndSurvivesALostWorker(t *testing.T) {
	s, _, _, clock := newSvc(t)
	if err := s.StartSeed(); !errors.Is(err, ErrConflict) {
		t.Fatal("seed started with no table or producer:", err)
	}
	s.setTable(lightning.PhaseActive, "", "")
	s.Heartbeat(hb("p1", "producer"))
	s.Heartbeat(hb("p2", "producer"))
	if err := s.StartSeed(); err != nil {
		t.Fatal(err)
	}
	if err := s.StartSeed(); !errors.Is(err, ErrConflict) {
		t.Fatal("seed started twice:", err)
	}
	p1, _ := s.Heartbeat(hb("p1", "producer"))
	p2, _ := s.Heartbeat(hb("p2", "producer"))
	if len(p1.SeedShards)+len(p2.SeedShards) != catalog.Shards || len(p1.SeedShards) == 0 || len(p2.SeedShards) == 0 {
		t.Fatalf("shards %d + %d", len(p1.SeedShards), len(p2.SeedShards))
	}
	if p1.SeedRate != 12500 || p2.SeedRate != 12500 {
		t.Fatalf("seed rate %v %v: the fixed rate must be split", p1.SeedRate, p2.SeedRate)
	}
	// p2 reports a shard done; it is then not leased again
	done := p2.SeedShards[0]
	if p1.SeedEpoch != 1 || p2.SeedEpoch != 1 {
		t.Fatalf("epochs %d %d: the first load is epoch 1", p1.SeedEpoch, p2.SeedEpoch)
	}
	h := hb("p2", "producer")
	h.SeedEpoch = 1
	h.SeedDone = []int{done, p1.SeedShards[0]} // a shard that p2 does not own is ignored
	h.Seeded = 1234
	p2, _ = s.Heartbeat(h)
	for _, sh := range p2.SeedShards {
		if sh == done {
			t.Fatal("a finished shard is leased again")
		}
	}
	if st := s.State().Catalogue; st.ShardsDone != 1 || st.Records != 1234 || st.Status != "loading" {
		t.Fatalf("%+v", st)
	}
	// p1 vanishes: its shards go to p2
	*clock = clock.Add(10 * time.Second)
	p2, _ = s.Heartbeat(hb("p2", "producer"))
	if len(p2.SeedShards) != catalog.Shards-1 {
		t.Fatalf("p2 has %d shards after p1 vanished", len(p2.SeedShards))
	}
	if p2.SeedRate != 25000 {
		t.Fatalf("rate %v", p2.SeedRate)
	}
	// all done
	h = hb("p2", "producer")
	h.SeedEpoch = 1
	h.SeedDone = p2.SeedShards
	s.Heartbeat(h)
	h.SeedDone = []int{done}
	for i := 0; i < catalog.Shards; i++ {
		h.SeedDone = append(h.SeedDone, i)
	}
	s.Heartbeat(h)
	if st := s.State().Catalogue; st.Status != "loaded" {
		t.Fatalf("%+v", st)
	}
}

func newHandler(t *testing.T, s *Service) http.Handler {
	assets := fstest.MapFS{"index.html": {Data: []byte("<html>ui</html>")}, "assets/a.js": {Data: []byte("x")}}
	return s.Handler(fs.FS(assets))
}

func do(h http.Handler, method, path string, hdr map[string]string, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func cookieHdr() map[string]string {
	return map[string]string{"Cookie": CookieName + "=" + cookieValue(access)}
}

func TestAccessGate(t *testing.T) {
	s, _, _, _ := newSvc(t)
	h := newHandler(t, s)
	if rec := do(h, "GET", "/healthz", nil, ""); rec.Code != 200 {
		t.Fatal("healthz", rec.Code)
	}
	for _, p := range []string{"/", "/api/state", "/api/latency", "/assets/a.js"} {
		if rec := do(h, "GET", p, nil, ""); rec.Code != 401 {
			t.Errorf("%s without a token: %d", p, rec.Code)
		}
	}
	if rec := do(h, "GET", "/?access=wrong", nil, ""); rec.Code != 401 {
		t.Error("wrong token accepted")
	}
	rec := do(h, "GET", "/?access="+access+"&x=1", map[string]string{"CloudFront-Forwarded-Proto": "https"}, "")
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/?x=1" {
		t.Fatalf("redirect: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	c := rec.Result().Cookies()[0]
	if !c.HttpOnly || !c.Secure || strings.Contains(c.Value, access) {
		t.Fatalf("cookie %+v", c)
	}
	if rec := do(h, "GET", "/api/state", cookieHdr(), ""); rec.Code != 200 {
		t.Fatal("with cookie", rec.Code)
	}
	// a state change needs the header
	if rec := do(h, "POST", "/api/events", cookieHdr(), `{"rate":20}`); rec.Code != 403 {
		t.Fatal("no CSRF header:", rec.Code)
	}
	hdr := cookieHdr()
	hdr[CSRFHeader] = "1"
	if rec := do(h, "POST", "/api/events", hdr, `{"rate":20}`); rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if rec := do(h, "POST", "/api/events", hdr, `{"rate":201}`); rec.Code != 400 {
		t.Fatal("rate 201:", rec.Code)
	}
	if rec := do(h, "POST", "/api/events", hdr, `{"rate":`); rec.Code != 400 {
		t.Fatal("bad json:", rec.Code)
	}
	if rec := do(h, "GET", "/api/nothing", cookieHdr(), ""); rec.Code != 404 {
		t.Fatal(rec.Code)
	}
	// the browser cannot set anything but running and rate: a body that tries is refused whole
	if rec := do(h, "POST", "/api/events", hdr, `{"rate":30,"topic":"x","endpoint":"http://evil","skus":99999}`); rec.Code != 400 {
		t.Fatal("extra fields accepted:", rec.Code)
	}
	if st := s.State(); st.Topic != "inventory_positions" || st.Events.Rate != 20 {
		t.Fatalf("%+v", st)
	}
}

func TestHeartbeatAuth(t *testing.T) {
	s, _, _, _ := newSvc(t)
	h := newHandler(t, s)
	body := `{"id":"w1","role":"query"}`
	if rec := do(h, "POST", protocol.HeartbeatPath, nil, body); rec.Code != 401 {
		t.Fatal("no token:", rec.Code)
	}
	if rec := do(h, "POST", protocol.HeartbeatPath, map[string]string{"Authorization": "Bearer " + access}, body); rec.Code != 401 {
		t.Fatal("the access token must not work for workers:", rec.Code)
	}
	if rec := do(h, "POST", protocol.HeartbeatPath, cookieHdr(), body); rec.Code != 401 {
		t.Fatal("the cookie must not work for workers:", rec.Code)
	}
	if rec := do(h, "POST", protocol.HeartbeatPath, map[string]string{"Authorization": "Bearer " + wtok, "X-Amz-Cf-Id": "x"}, body); rec.Code != 401 {
		t.Fatal("a request that came through CloudFront must be refused:", rec.Code)
	}
	rec := do(h, "POST", protocol.HeartbeatPath, map[string]string{"Authorization": "Bearer " + wtok}, body)
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var p protocol.Plan
	if json.Unmarshal(rec.Body.Bytes(), &p) != nil || p.Workers != 1 {
		t.Fatalf("%s", rec.Body.String())
	}
	if rec := do(h, "POST", protocol.HeartbeatPath, map[string]string{"Authorization": "Bearer " + wtok}, `{"id":"","role":"query"}`); rec.Code != 400 {
		t.Fatal(rec.Code)
	}
}

func TestLatencyIsCachedAndRangesAreFixed(t *testing.T) {
	s, _, fl, clock := newSvc(t)
	h := newHandler(t, s)
	for i := 0; i < 3; i++ {
		if rec := do(h, "GET", "/api/latency?minutes=15", cookieHdr(), ""); rec.Code != 200 {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	if fl.calls.Load() != 1 {
		t.Fatalf("%d CloudWatch reads for three requests", fl.calls.Load())
	}
	if !fl.deadline.Load() {
		t.Fatal("the CloudWatch read has no timeout")
	}
	*clock = clock.Add(3 * time.Second)
	do(h, "GET", "/api/latency?minutes=15", cookieHdr(), "")
	if fl.calls.Load() != 1 {
		t.Fatal("the cache expired after 3 s: it lasts longer than that")
	}
	*clock = clock.Add(3 * time.Second)
	do(h, "GET", "/api/latency?minutes=15", cookieHdr(), "")
	if fl.calls.Load() != 2 {
		t.Fatal("cache never expires")
	}
	if rec := do(h, "GET", "/api/latency?minutes=100000", cookieHdr(), ""); rec.Code != 400 {
		t.Fatal("arbitrary range accepted:", rec.Code)
	}
}

// Requests that arrive while a CloudWatch read is in flight wait for it: one read serves them all.
func TestLatencyReadsAreCoalesced(t *testing.T) {
	s, _, fl, _ := newSvc(t)
	fl.gate = make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.Latency(context.Background(), 5)
			results <- err
		}()
	}
	deadline := time.Now().Add(5 * time.Second)
	for fl.calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // let the others queue behind the read
	close(fl.gate)
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if n := fl.calls.Load(); n != 1 {
		t.Fatalf("%d CloudWatch reads for 8 concurrent requests", n)
	}
}

// One viewer closing the page must not fail the read the others are waiting for, and a panic in
// the read must release them with an error, not hang them.
func TestLatencyReadSurvivesACancelledRequestAndAPanic(t *testing.T) {
	s, _, fl, _ := newSvc(t)
	fl.gate = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { _, err := s.Latency(ctx, 15); errc <- err }()
	for fl.calls.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	waiter := make(chan error, 1)
	go func() { _, err := s.Latency(context.Background(), 15); waiter <- err }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	close(fl.gate)
	if err := <-waiter; err != nil {
		t.Fatalf("the waiting request failed because another was cancelled: %v", err)
	}
	<-errc

	s2, _, fl2, _ := newSvc(t)
	fl2.panics = true
	if _, err := s2.Latency(context.Background(), 5); err == nil {
		t.Fatal("a panic in the read was not an error")
	}
	// and the next request is not stuck behind the dead call
	if _, err := s2.Latency(context.Background(), 5); err == nil {
		t.Fatal("expected the same error")
	}
}

func TestRateBelowTheWorkerCountStillAddsUp(t *testing.T) {
	s, _, _, _ := newSvc(t)
	on, r := true, 1
	if err := s.ChangeEvents(LoadChange{Running: &on, Rate: &r}); err != nil {
		t.Fatal(err)
	}
	s.Heartbeat(hb("a", "producer"))
	pa, _ := s.Heartbeat(hb("a", "producer"))
	pb, _ := s.Heartbeat(hb("b", "producer"))
	if pa.Workers != 2 && pb.Workers != 2 {
		t.Fatalf("%+v %+v", pa, pb)
	}
	if pb.Rate != 0.5 {
		t.Fatalf("1 per second over 2 producers gives %v each, want 0.5 (the worker's pacer handles a rate under 1)", pb.Rate)
	}
}

func TestHeartbeatCountsAreBounded(t *testing.T) {
	s, _, _, _ := newSvc(t)
	for _, h := range []protocol.Heartbeat{
		{ID: "x", Role: "producer", Produced: -1},
		{ID: "x", Role: "producer", Seeded: protocol.MaxCount + 1},
		{ID: "x", Role: "query", Queries: map[string]protocol.QueryStats{"by_sku": {OK: 1 << 40}}},
	} {
		if _, err := s.Heartbeat(h); !errors.Is(err, ErrInvalid) {
			t.Errorf("%+v accepted", h)
		}
	}
	if st := s.State(); st.Events.PerSec != 0 {
		t.Fatalf("%+v", st.Events)
	}
}

func TestJSONBodiesAreStrict(t *testing.T) {
	s, _, _, _ := newSvc(t)
	h := newHandler(t, s)
	hdr := cookieHdr()
	hdr[CSRFHeader] = "1"
	for name, body := range map[string]string{
		"unknown field": `{"rate":20,"topic":"x"}`,
		"trailing data": `{"rate":20} {"rate":30}`,
		"trailing junk": `{"rate":20}x`,
	} {
		if rec := do(h, "POST", "/api/events", hdr, body); rec.Code != 400 {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	if st := s.State(); st.Events.Rate != 10 {
		t.Fatalf("a refused body changed the rate: %d", st.Events.Rate)
	}
	if rec := do(h, "POST", "/api/events", hdr, `{"rate":20}`+"\n"); rec.Code != 200 {
		t.Fatalf("a trailing newline is fine: %d", rec.Code)
	}
	wk := map[string]string{"Authorization": "Bearer " + wtok}
	// Lenient about unknown fields on this endpoint only (a rolling deploy), not about the rest.
	if rec := do(h, "POST", protocol.HeartbeatPath, wk, `{"id":"w","role":"query","extra":1}`); rec.Code != 200 {
		t.Errorf("heartbeat with an unknown field: %d, want 200", rec.Code)
	}
	if rec := do(h, "POST", protocol.HeartbeatPath, wk, `{"id":"w","role":"query"}{"id":"v","role":"query"}`); rec.Code != 400 {
		t.Errorf("heartbeat with trailing data: %d", rec.Code)
	}
	if rec := do(h, "POST", protocol.HeartbeatPath, wk, `{"id":"w","role":"query","produced":-5}`); rec.Code != 400 {
		t.Errorf("heartbeat with a negative count: %d", rec.Code)
	}
}

// There is no shutdown hook: Terraform owns everything, so nothing answers at /shutdown.
func TestThereIsNoShutdownEndpoint(t *testing.T) {
	s, _, _, _ := newSvc(t)
	h := newHandler(t, s)
	on := true
	_ = s.ChangeEvents(LoadChange{Running: &on})
	for _, hdr := range []map[string]string{
		nil,
		{"Authorization": "Bearer " + access},
		{"Authorization": "Bearer " + wtok},
	} {
		if rec := do(h, "POST", "/shutdown", hdr, ""); rec.Code != 401 {
			t.Errorf("POST /shutdown with %v: %d, want 401 from the gate", hdr, rec.Code)
		}
	}
	if !s.State().Events.Running {
		t.Fatal("a request to /shutdown stopped the load")
	}
}

func TestPanickingHandlerIsA500(t *testing.T) {
	h := recoverer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }))
	rec := do(h, "GET", "/x", nil, "")
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "boom") {
		t.Fatalf("%d %s", rec.Code, rec.Body.String())
	}
}

func TestPollTable(t *testing.T) {
	s, ft, _, _ := newSvc(t)
	ctx := context.Background()

	// No table yet (Terraform has not made it): no phase, no error, ask again soon.
	ft.set("", lightning.ErrNotFound)
	if wait, failed := s.pollTable(ctx, 0); failed || wait != pollSettling || s.State().Table.Phase != "" || s.State().Table.Error != "" {
		t.Fatalf("wait %v failed %v %+v", wait, failed, s.State().Table)
	}
	// being made
	ft.set(lightning.PhaseProvisioning, nil)
	if wait, failed := s.pollTable(ctx, 0); failed || wait != pollSettling || s.State().Table.Phase != "PROVISIONING" {
		t.Fatalf("wait %v %+v", wait, s.State().Table)
	}
	on := true
	if err := s.ChangeQueries(LoadChange{Running: &on}); !errors.Is(err, ErrConflict) {
		t.Fatal("queries started before the table was ACTIVE:", err)
	}
	if err := s.StartSeed(); !errors.Is(err, ErrConflict) {
		t.Fatal("catalogue load started before the table was ACTIVE:", err)
	}
	// ACTIVE: watch it slowly, and the loads are allowed
	ft.set(lightning.PhaseActive, nil)
	if wait, _ := s.pollTable(ctx, 0); wait != pollActive || s.State().Table.Phase != "ACTIVE" {
		t.Fatalf("wait %v", wait)
	}
	if err := s.ChangeQueries(LoadChange{Running: &on}); err != nil {
		t.Fatal(err)
	}
}

func TestPollTableBacksOffOnErrors(t *testing.T) {
	s, ft, _, _ := newSvc(t)
	ctx := context.Background()

	// a server error: doubling from the minimum to the maximum
	ft.set("", &lightning.StatusError{Status: 503})
	var wait time.Duration
	var prev time.Duration
	var waits []time.Duration
	for i := 0; i < 10; i++ {
		var failed bool
		wait, failed = s.pollTable(ctx, prev)
		if !failed {
			t.Fatal("an error was not a failure")
		}
		waits = append(waits, wait)
		prev = wait
	}
	if waits[0] != pollErrorMin || waits[1] != 2*pollErrorMin || waits[len(waits)-1] != pollErrorMax {
		t.Fatalf("%v", waits)
	}
	if s.State().Table.Error == "" {
		t.Fatal("the UI is not told")
	}

	// a rate limit goes to the top: Confluent is asking for less
	ft.set("", lightning.ErrRateLimited)
	if wait, failed := s.pollTable(ctx, 0); !failed || wait != pollErrorMax {
		t.Errorf("429: wait %v", wait)
	}
	// a refused key or role (401, 403, any 4xx) doubles from 10 s but stops at a minute: right
	// after an apply it is often only a key that has not taken effect yet
	for _, code := range []int{401, 403, 404 + 5} {
		ft.set("", &lightning.StatusError{Status: code})
		var prev time.Duration
		var waits []time.Duration
		for i := 0; i < 6; i++ {
			prev, _ = s.pollTable(ctx, prev)
			waits = append(waits, prev)
		}
		if waits[0] != pollErrorMin || waits[1] != 2*pollErrorMin || waits[5] != pollClientErrorMax {
			t.Errorf("%d: %v", code, waits)
		}
	}
	// and a success clears the error
	ft.set(lightning.PhaseActive, nil)
	if _, failed := s.pollTable(ctx, pollErrorMax); failed || s.State().Table.Error != "" {
		t.Fatalf("%+v", s.State().Table)
	}
}

// Run polls until its context ends and never writes: the fake has no way to.
func TestRunStopsWithItsContext(t *testing.T) {
	s, ft, _, _ := newSvc(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	for ft.called() == 0 {
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
}

// A worker that cannot reach control stops before control gives its share to the others, or
// the total would exceed the operator's rate.
func TestWorkersStopBeforeTheirShareIsReassigned(t *testing.T) {
	// Control last heard a worker at T. The poll that control answered completes for the worker
	// at most PollTimeout later (or it counts as failed and the worker's last good poll is
	// earlier), so the worker stops by T + PollTimeout + StaleAfter. Control reassigns at
	// T + aliveFor.
	if worker.PollTimeout+worker.StaleAfter >= aliveFor {
		t.Fatalf("a worker may run until %v after control last heard it, but control reassigns its share after %v", worker.PollTimeout+worker.StaleAfter, aliveFor)
	}
}

// Start a catalogue load, finish it, start it again: the second is a load of its own, and a
// report that belongs to the first cannot mark its shards done.
func TestSecondCatalogueLoadStartsFromScratch(t *testing.T) {
	s, _, _, _ := newSvc(t)
	s.setTable(lightning.PhaseActive, "", "")
	s.Heartbeat(hb("p1", "producer"))
	if err := s.StartSeed(); err != nil {
		t.Fatal(err)
	}
	p, _ := s.Heartbeat(hb("p1", "producer"))
	if p.SeedEpoch != 1 || len(p.SeedShards) != catalog.Shards {
		t.Fatalf("%+v", p)
	}
	all := make([]int, catalog.Shards)
	for i := range all {
		all[i] = i
	}
	h := hb("p1", "producer")
	h.SeedEpoch, h.SeedDone, h.Seeded = 1, all, 500
	s.Heartbeat(h)
	if st := s.State().Catalogue; st.Status != "loaded" || st.Records != 500 {
		t.Fatalf("%+v", st)
	}

	if err := s.StartSeed(); err != nil {
		t.Fatal("a second load after the first finished:", err)
	}
	if st := s.State().Catalogue; st.Status != "loading" || st.ShardsDone != 0 || st.Records != 0 {
		t.Fatalf("the second load did not start from scratch: %+v", st)
	}
	// the producer has not seen epoch 2 yet and repeats its old report: ignored
	s.Heartbeat(h)
	if st := s.State().Catalogue; st.ShardsDone != 0 || st.Records != 0 {
		t.Fatalf("a report of the first load counted in the second: %+v", st)
	}
	p, _ = s.Heartbeat(hb("p1", "producer"))
	if p.SeedEpoch != 2 || len(p.SeedShards) != catalog.Shards {
		t.Fatalf("%+v", p)
	}
	h.SeedEpoch, h.Seeded = 2, 700
	s.Heartbeat(h)
	if st := s.State().Catalogue; st.Status != "loaded" || st.Records != 700 {
		t.Fatalf("%+v", st)
	}
}

func TestLatencyErrorsAreNotRetriedAtOnce(t *testing.T) {
	s, _, fl, clock := newSvc(t)
	fl.failWith = errors.New("throttled")
	for i := 0; i < 3; i++ {
		if _, err := s.Latency(context.Background(), 5); err == nil {
			t.Fatal("no error")
		}
	}
	if fl.calls.Load() != 1 {
		t.Fatalf("%d CloudWatch calls for 3 requests during an error", fl.calls.Load())
	}
	*clock = clock.Add(latencyErrorTTL + time.Second)
	fl.failWith = nil
	if _, err := s.Latency(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	if fl.calls.Load() != 2 {
		t.Fatal("not retried after the error expired")
	}
}

func TestUsageWait(t *testing.T) {
	if usageWait(time.Minute, nil) != usageEvery {
		t.Error("success")
	}
	if usageWait(time.Minute, confluentmetrics.ErrRateLimited) != usageRateLimited {
		t.Error("429")
	}
	w := time.Duration(0)
	for i := 0; i < 8; i++ {
		w = usageWait(w, errors.New("boom"))
	}
	if w != usageErrorMax {
		t.Errorf("%v", w)
	}
}

// A panic after the response has started must not append an error body to it: the connection
// is aborted instead.
func TestRecovererDoesNotWriteAfterTheResponseStarted(t *testing.T) {
	h := recoverer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("partial"))
		panic("boom")
	}))
	rec := httptest.NewRecorder()
	var aborted any
	func() {
		defer func() { aborted = recover() }()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/x", nil))
	}()
	if aborted != http.ErrAbortHandler {
		t.Fatalf("recovered %v, want the connection aborted", aborted)
	}
	if rec.Body.String() != "partial" {
		t.Fatalf("body %q: something was written after the response started", rec.Body.String())
	}
}
