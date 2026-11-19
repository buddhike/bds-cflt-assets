package latency

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

var tables = []string{"patients", "appointments", "visits"}

// fakeAPI serves canned query responses and records the requests it gets.
type fakeAPI struct {
	mu       sync.Mutex
	requests []recorded
	// respond returns the status and body for a metric.
	respond func(metric string) (int, string)
}

type recorded struct {
	Path        string
	User, Pass  string
	ContentType string
	UserAgent   string
	Body        queryRequest
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var rec recorded
	rec.Path = r.URL.Path
	rec.User, rec.Pass, _ = r.BasicAuth()
	rec.ContentType = r.Header.Get("Content-Type")
	rec.UserAgent = r.Header.Get("User-Agent")
	b, _ := io.ReadAll(r.Body)
	_ = json.Unmarshal(b, &rec.Body)
	f.mu.Lock()
	f.requests = append(f.requests, rec)
	f.mu.Unlock()

	if len(rec.Body.Aggregations) == 0 {
		// Fail the test through the client rather than panic in the handler.
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"errors":[{"detail":"the test server got a query without aggregations"}]}`)
		return
	}
	status, body := http.StatusOK, `{"data":[]}`
	if f.respond != nil {
		status, body = f.respond(rec.Body.Aggregations[0].Metric)
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (f *fakeAPI) calls() []recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]recorded(nil), f.requests...)
}

func newClient(t *testing.T, f *fakeAPI) *MetricsAPI {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	m, err := NewMetricsAPI(srv.URL, "lkc-abc123", "KEY", "SECRET", tables)
	if err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return time.Date(2026, 11, 19, 10, 7, 42, 0, time.UTC) }
	return m
}

func TestNewMetricsAPIValidates(t *testing.T) {
	for name, tc := range map[string]struct {
		url, cluster, key, secret string
		tables                    []string
		wantErr                   string
	}{
		"https":              {"https://api.telemetry.confluent.cloud", "lkc-abc123", "k", "s", tables, ""},
		"loopback http":      {"http://127.0.0.1:8080", "lkc-abc123", "k", "s", tables, ""},
		"remote http":        {"http://api.telemetry.confluent.cloud", "lkc-abc123", "k", "s", tables, "must use https"},
		"bad cluster":        {DefaultEndpoint, "env-1", "k", "s", tables, "invalid kafka cluster id"},
		"empty cluster":      {DefaultEndpoint, "", "k", "s", tables, "invalid kafka cluster id"},
		"missing key":        {DefaultEndpoint, "lkc-abc123", "", "s", tables, "api key and secret are required"},
		"missing secret":     {DefaultEndpoint, "lkc-abc123", "k", "", tables, "api key and secret are required"},
		"no tables":          {DefaultEndpoint, "lkc-abc123", "k", "s", nil, "at least one table"},
		"unparseable":        {"://", "lkc-abc123", "k", "s", tables, "parse metrics api url"},
		"cluster has slash":  {DefaultEndpoint, "lkc-a/../b", "k", "s", tables, "invalid kafka cluster id"},
		"uppercase cluster":  {DefaultEndpoint, "LKC-ABC", "k", "s", tables, "invalid kafka cluster id"},
		"cluster with space": {DefaultEndpoint, "lkc-abc 123", "k", "s", tables, "invalid kafka cluster id"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := NewMetricsAPI(tc.url, tc.cluster, tc.key, tc.secret, tc.tables)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Fatalf("err = %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Fatalf("err = %v, want it to contain %q", err, tc.wantErr)
			}
		})
	}
}

func TestQueryAsksForEachMetricByTopic(t *testing.T) {
	f := &fakeAPI{}
	m := newClient(t, f)
	if _, err := m.Query(context.Background()); err != nil {
		t.Fatal(err)
	}

	calls := f.calls()
	if len(calls) != 2 {
		t.Fatalf("requests = %d, want one per metric", len(calls))
	}
	for i, metric := range []string{EventMetric, ProcessingMetric} {
		c := calls[i]
		if c.Path != "/v2/metrics/cloud/query" {
			t.Errorf("path = %q", c.Path)
		}
		if c.User != "KEY" || c.Pass != "SECRET" {
			t.Errorf("basic auth = %q:%q", c.User, c.Pass)
		}
		if c.ContentType != "application/json" || c.UserAgent != userAgent {
			t.Errorf("headers: content type %q, user agent %q", c.ContentType, c.UserAgent)
		}
		b := c.Body
		if len(b.Aggregations) != 1 || b.Aggregations[0].Metric != metric {
			t.Errorf("aggregations = %+v, want only %s", b.Aggregations, metric)
		}
		if b.Filter != (fieldFilter{Field: "resource.kafka.id", Op: "EQ", Value: "lkc-abc123"}) {
			t.Errorf("filter = %+v", b.Filter)
		}
		if len(b.GroupBy) != 1 || b.GroupBy[0] != "metric.topic" {
			t.Errorf("group_by = %v", b.GroupBy)
		}
		if b.Granularity != "PT1M" {
			t.Errorf("granularity = %q", b.Granularity)
		}
		// The window is five hours, the most the API allows at PT1M, and ends
		// at the start of the current minute, whose value is still being
		// collected.
		if want := []string{"2026-11-19T05:07:00Z/2026-11-19T10:07:00Z"}; len(b.Intervals) != 1 || b.Intervals[0] != want[0] {
			t.Errorf("intervals = %v, want %v", b.Intervals, want)
		}
		if b.Limit != maxGroups {
			t.Errorf("limit = %d", b.Limit)
		}
	}
}

func TestQueryReturnsSamplesInTimeThenTableOrder(t *testing.T) {
	f := &fakeAPI{respond: func(metric string) (int, string) {
		// Out of order on purpose, with a topic PIMS does not chart, a null
		// value and a timestamp with an offset.
		if metric == EventMetric {
			return 200, `{"data":[
				{"timestamp":"2026-11-19T10:02:00Z","value":3000,"metric.topic":"visits"},
				{"timestamp":"2026-11-19T10:01:00Z","value":2000,"metric.topic":"visits"},
				{"timestamp":"2026-11-19T10:01:00Z","value":1000.5,"metric.topic":"patients"},
				{"timestamp":"2026-11-19T10:01:00Z","value":1500,"metric.topic":"appointments"},
				{"timestamp":"2026-11-19T10:01:00Z","value":9,"metric.topic":"some_other_topic"},
				{"timestamp":"2026-11-19T10:03:00Z","value":null,"metric.topic":"patients"},
				{"timestamp":"2026-11-19T21:04:00+11:00","value":4000,"metric.topic":"patients"}
			]}`
		}
		return 200, `{"data":[{"timestamp":"2026-11-19T10:01:00Z","value":42,"metric.topic":"patients"}]}`
	}}
	m := newClient(t, f)

	res, err := m.Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	at := func(min int) time.Time { return time.Date(2026, 11, 19, 10, min, 0, 0, time.UTC) }
	want := []Sample{
		{"patients", at(1), 1000.5},
		{"appointments", at(1), 1500},
		{"visits", at(1), 2000},
		{"visits", at(2), 3000},
		{"patients", at(4), 4000},
	}
	if len(res.Event) != len(want) {
		t.Fatalf("event = %+v, want %+v", res.Event, want)
	}
	for i, w := range want {
		if g := res.Event[i]; g.Table != w.Table || !g.Time.Equal(w.Time) || g.Ms != w.Ms {
			t.Errorf("event[%d] = %+v, want %+v", i, g, w)
		}
	}
	if len(res.Processing) != 1 || res.Processing[0].Ms != 42 {
		t.Errorf("processing = %+v", res.Processing)
	}
	if len(res.Unmatched) != 1 || res.Unmatched[0] != "some_other_topic" {
		t.Errorf("unmatched = %v, want the one topic that is not charted", res.Unmatched)
	}
	if !res.ObservedAt.Equal(m.now()) || res.Error != "" {
		t.Errorf("observedAt = %v, error = %q", res.ObservedAt, res.Error)
	}
}

func TestQueryWithNoDataYet(t *testing.T) {
	// Tableflow has not reported anything for the first minutes: that is a
	// valid answer, not an error.
	m := newClient(t, &fakeAPI{})
	res, err := m.Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Event) != 0 || len(res.Processing) != 0 {
		t.Fatalf("result = %+v", res)
	}
}

func TestQueryReportsAPIErrors(t *testing.T) {
	for name, tc := range map[string]struct {
		status   int
		body     string
		contains []string
	}{
		"json detail": {400, `{"errors":[{"status":"400","detail":"Unknown metric io.confluent.tableflow/nope"}]}`,
			[]string{"400 Bad Request", "Unknown metric io.confluent.tableflow/nope"}},
		"json title only": {500, `{"errors":[{"title":"Internal Server Error"}]}`,
			[]string{"500 Internal Server Error", "Internal Server Error"}},
		"plain body": {502, "bad gateway\nsecond line", []string{"502 Bad Gateway: bad gateway"}},
		"unauthorised": {401, `{"errors":[{"detail":"Unauthorized"}]}`,
			[]string{"401 Unauthorized", "Cloud resource management key", "MetricsViewer"}},
		"forbidden":    {403, `{"errors":[{"detail":"Forbidden"}]}`, []string{"403 Forbidden", "MetricsViewer"}},
		"rate limited": {429, `{"errors":[{"detail":"Too many requests"}]}`, []string{"429 Too Many Requests", "Too many requests"}},
	} {
		t.Run(name, func(t *testing.T) {
			m := newClient(t, &fakeAPI{respond: func(string) (int, string) { return tc.status, tc.body }})
			_, err := m.Query(context.Background())
			var apiErr *APIError
			if !errors.As(err, &apiErr) || apiErr.StatusCode != tc.status {
				t.Fatalf("err = %v, want an APIError with status %d", err, tc.status)
			}
			for _, s := range tc.contains {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("err = %q, want it to contain %q", err, s)
				}
			}
			if strings.Contains(err.Error(), "SECRET") {
				t.Errorf("err leaks the API secret: %q", err)
			}
		})
	}
}

func TestQueryNamesTheMetricThatFailed(t *testing.T) {
	m := newClient(t, &fakeAPI{respond: func(metric string) (int, string) {
		if metric == ProcessingMetric {
			return 400, `{"errors":[{"detail":"no such metric"}]}`
		}
		return 200, `{"data":[{"timestamp":"2026-11-19T10:01:00Z","value":1,"metric.topic":"patients"}]}`
	}})
	res, err := m.Query(context.Background())
	if err == nil || !strings.Contains(err.Error(), "processing freshness") {
		t.Fatalf("err = %v, want it to name the processing metric", err)
	}
	if len(res.Event) != 0 || len(res.Processing) != 0 {
		t.Fatalf("a failed poll must return no samples, got %+v", res)
	}
}

func TestQueryRejectsUndecodableAndPartialResponses(t *testing.T) {
	for name, body := range map[string]string{
		"not json":      `<html>`,
		"bad timestamp": `{"data":[{"timestamp":"yesterday","value":1,"metric.topic":"patients"}]}`,
		"more pages":    `{"data":[],"meta":{"pagination":{"next_page_token":"abc"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			m := newClient(t, &fakeAPI{respond: func(string) (int, string) { return 200, body }})
			if _, err := m.Query(context.Background()); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestQueryStopsWhenContextIsCancelled(t *testing.T) {
	m := newClient(t, &fakeAPI{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Query(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// seqQuerier returns canned outcomes, then blocks.
type seqQuerier struct {
	mu      sync.Mutex
	results []Result
	errs    []error
	calls   int
}

func (s *seqQuerier) Query(ctx context.Context) (Result, error) {
	s.mu.Lock()
	i := s.calls
	s.calls++
	s.mu.Unlock()
	if i >= len(s.results) {
		<-ctx.Done()
		return Result{}, ctx.Err()
	}
	return s.results[i], s.errs[i]
}

func TestMonitorReportsEveryResultAndFailure(t *testing.T) {
	ok := Result{Event: []Sample{{Table: "patients", Ms: 1}}}
	q := &seqQuerier{
		results: []Result{ok, {}, ok},
		errs:    []error{nil, errors.New("metrics api returned 401 Unauthorized"), nil},
	}
	m := NewMonitor(q, time.Millisecond, slog.New(slog.DiscardHandler))
	fixed := time.Date(2026, 11, 19, 10, 0, 0, 0, time.UTC)
	m.now = func() time.Time { return fixed }

	got := make(chan Result, 3)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx, func(r Result) { got <- r }) }()

	var rs []Result
	for range 3 {
		select {
		case r := <-got:
			rs = append(rs, r)
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for results")
		}
	}
	cancel()
	<-done

	if len(rs[0].Event) != 1 || rs[0].Error != "" {
		t.Errorf("first = %+v", rs[0])
	}
	if !strings.Contains(rs[1].Error, "401 Unauthorized") || len(rs[1].Event) != 0 || !rs[1].ObservedAt.Equal(fixed) {
		t.Errorf("failed poll = %+v, want its error, no samples and the observed time", rs[1])
	}
	if len(rs[2].Event) != 1 || rs[2].Error != "" {
		t.Errorf("recovered = %+v", rs[2])
	}
}

func TestMonitorStopsWithoutReportingACancelledPoll(t *testing.T) {
	q := &seqQuerier{}
	m := NewMonitor(q, time.Hour, slog.New(slog.DiscardHandler))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	reported := make(chan Result, 1)
	go func() { defer close(done); m.Run(ctx, func(r Result) { reported <- r }) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	select {
	case r := <-reported:
		t.Fatalf("a cancelled poll was reported: %+v", r)
	default:
	}
}

func TestNewMonitorDefaultsTheInterval(t *testing.T) {
	if m := NewMonitor(&seqQuerier{}, 0, slog.New(slog.DiscardHandler)); m.interval != DefaultInterval {
		t.Fatalf("interval = %v, want %v", m.interval, DefaultInterval)
	}
}

func TestQuerySkipsSamplesWithoutATimestamp(t *testing.T) {
	// A null timestamp decodes to the zero time, which would be charted at
	// year 1 and squash everything else.
	m := newClient(t, &fakeAPI{respond: func(string) (int, string) {
		return 200, `{"data":[
			{"timestamp":null,"value":5,"metric.topic":"patients"},
			{"value":6,"metric.topic":"patients"},
			{"timestamp":"2026-11-19T10:01:00Z","value":7,"metric.topic":"patients"}
		]}`
	}})
	res, err := m.Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Event) != 1 || res.Event[0].Ms != 7 {
		t.Fatalf("event = %+v, want only the sample with a timestamp", res.Event)
	}
}

func TestQueryDoesNotFollowRedirects(t *testing.T) {
	// Go would send the Authorization header on to a same-host redirect.
	var hits int
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if r.Header.Get("Authorization") != "" {
			t.Errorf("the redirect target received credentials")
		}
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/elsewhere", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()

	m, err := NewMetricsAPI(redirector.URL, "lkc-abc123", "KEY", "SECRET", tables)
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.Query(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("err = %v, want an APIError for the redirect", err)
	}
	if hits != 0 {
		t.Fatalf("the redirect target was called %d times", hits)
	}
}

func TestQueryRejectsAnOversizedResponse(t *testing.T) {
	m := newClient(t, &fakeAPI{respond: func(string) (int, string) {
		return 200, `{"data":[],"padding":"` + strings.Repeat("x", maxResponseBytes) + `"}`
	}})
	_, err := m.Query(context.Background())
	if err == nil || !strings.Contains(err.Error(), "larger than") {
		t.Fatalf("err = %v, want it to say the response is too large", err)
	}
}

func TestQueryTimesOut(t *testing.T) {
	// The handler never answers until released. The server only notices a
	// client that hung up once it has read the request body, so it cannot be
	// relied on to unblock itself; release it before closing the server.
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer srv.Close()
	defer close(release)
	m, err := NewMetricsAPI(srv.URL, "lkc-abc123", "KEY", "SECRET", tables)
	if err != nil {
		t.Fatal(err)
	}
	m.http.Timeout = 50 * time.Millisecond

	start := time.Now()
	if _, err := m.Query(context.Background()); err == nil {
		t.Fatal("want a timeout error")
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Fatalf("Query took %v, want it to give up at the client timeout", d)
	}
}

func TestClientDoesNotPrintItsCredentials(t *testing.T) {
	m, err := NewMetricsAPI(DefaultEndpoint, "lkc-abc123", "THE-KEY", "THE-SECRET", tables)
	if err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	slog.New(slog.NewTextHandler(&logs, nil)).Info("Client", "client", m)
	out := fmt.Sprintf("%v|%+v|%#v|%s|%v|%+v|%#v|%s", m, m, m, m, *m, *m, *m, logs.String())
	for _, secret := range []string{"THE-SECRET", "THE-KEY"} {
		if strings.Contains(out, secret) {
			t.Errorf("output contains %q: %s", secret, out)
		}
	}
	if !strings.Contains(out, "lkc-abc123") || !strings.Contains(out, DefaultEndpoint) {
		t.Errorf("output should still identify the client: %s", out)
	}
}

func TestQueryReportsRowsWithoutATopicLabel(t *testing.T) {
	// If the API named the label differently, every row would arrive without
	// a topic and nothing would be charted. It must be visible as such.
	m := newClient(t, &fakeAPI{respond: func(string) (int, string) {
		return 200, `{"data":[{"timestamp":"2026-11-19T10:01:00Z","value":7,"metric.source_topic":"patients"}]}`
	}})
	res, err := m.Query(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Event) != 0 || len(res.Unmatched) != 1 || res.Unmatched[0] != "" {
		t.Fatalf("result = %+v, want no samples and one row with an empty topic", res)
	}
}

func TestWindowIsWithinWhatThePT1MGranularityAllows(t *testing.T) {
	// The Metrics API allows at most five hours at one-minute granularity.
	if Window > 5*time.Hour || Window%time.Minute != 0 {
		t.Fatalf("Window = %v", Window)
	}
}

func TestMonitorLogsWhatEachPollReturned(t *testing.T) {
	at := time.Date(2026, 11, 19, 10, 5, 0, 0, time.UTC)
	q := &seqQuerier{
		results: []Result{{
			Event:      []Sample{{Table: "patients", Time: at.Add(-time.Minute)}, {Table: "visits", Time: at}},
			Processing: []Sample{{Table: "patients", Time: at.Add(-2 * time.Minute)}},
			Unmatched:  []string{"other"},
		}},
		errs: []error{nil},
	}
	var logs bytes.Buffer
	m := NewMonitor(q, time.Hour, slog.New(slog.NewTextHandler(&logs, nil)))

	got := make(chan Result, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx, func(r Result) { got <- r }) }()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
	cancel()
	<-done

	out := logs.String()
	for _, want := range []string{
		"Queried Tableflow freshness metrics",
		"event_samples=2", "newest_event=2026-11-19T10:05:00Z",
		"processing_samples=1", "newest_processing=2026-11-19T10:03:00Z",
		"other_topics=[other]",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q does not contain %q", out, want)
		}
	}
	if !strings.Contains(out, "level=INFO") {
		t.Errorf("a poll should be logged at info so it shows in the activity log: %q", out)
	}
}

func TestNewestIsNoneWithoutSamples(t *testing.T) {
	if got := newest(nil); got != "none" {
		t.Fatalf("newest(nil) = %q", got)
	}
}
