// Package latency reports how fresh the Tableflow Iceberg tables are, using
// the two freshness metrics Tableflow publishes through the Confluent Cloud
// Metrics API:
//
//   - io.confluent.tableflow/event_freshness_latency_milliseconds: the age of
//     the most recently materialised record, from the source record's
//     timestamp to when Tableflow wrote it to the table.
//   - io.confluent.tableflow/processing_freshness_latency_milliseconds: the
//     time from Tableflow picking a record up to writing it to the table.
//
// Both are gauges that Tableflow updates when it commits to a table, so the
// values only move on a commit. PIMS reports them as Tableflow does and does
// not derive anything from the data itself.
package latency

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	// DefaultEndpoint is the Confluent Cloud Metrics API.
	DefaultEndpoint = "https://api.telemetry.confluent.cloud"

	// EventMetric is the age of the newest materialised record.
	EventMetric = "io.confluent.tableflow/event_freshness_latency_milliseconds"
	// ProcessingMetric is Tableflow's own processing time.
	ProcessingMetric = "io.confluent.tableflow/processing_freshness_latency_milliseconds"

	queryPath = "/v2/metrics/cloud/query"

	// Window is how far back each poll reads: the longest the Metrics API
	// allows at one-minute granularity. That is 300 samples per table per
	// metric. The UI text and the docs say "5 hours"; change them with it.
	Window = 5 * time.Hour
	// DefaultInterval is used when NewMonitor is given a non-positive
	// interval. The metrics have one-minute granularity and arrive about
	// three minutes late, so polling faster would only repeat answers.
	DefaultInterval = time.Minute

	// userAgent identifies PIMS to the API.
	userAgent = "pims/0.1"
	// maxResponseBytes bounds what is read from the API.
	maxResponseBytes = 4 << 20
	// requestTimeout bounds one call to the API.
	requestTimeout = 20 * time.Second
	// maxGroups is the most groups (topics) one query may return. It is far
	// above the number of tables PIMS has.
	maxGroups = 1000
)

var clusterPattern = regexp.MustCompile(`^lkc-[a-z0-9]+$`)

// Sample is one value of a metric for one table: Tableflow's reading for the
// minute starting at Time.
type Sample struct {
	Table string    `json:"table"`
	Time  time.Time `json:"time"`
	// Ms is the latency in milliseconds.
	Ms float64 `json:"ms"`
}

// Result is the outcome of one poll: the last Window of both metrics.
type Result struct {
	// Event and Processing are the samples of each metric, oldest first.
	// Tables Tableflow has not reported on yet have none.
	Event      []Sample `json:"event"`
	Processing []Sample `json:"processing"`
	// Unmatched are the topics the API returned that PIMS does not chart,
	// sorted. They are logged, not sent to the UI: a poll that charts nothing
	// although the API answered would otherwise look the same as one that
	// got no answer.
	Unmatched []string `json:"-"`
	// ObservedAt is when PIMS ran the query. It is absent before the first
	// poll. The Monitor sets it on a failed poll too, but the app keeps the
	// last good one, so on screen it says when the samples were read.
	ObservedAt time.Time `json:"observedAt,omitzero"`
	// Error is why the poll failed, or empty. The Monitor reports a failed
	// poll as a Result with no samples; the app keeps the last good samples
	// beside the error.
	Error string `json:"error,omitempty"`
}

// APIError is an unsuccessful response from the Metrics API.
type APIError struct {
	StatusCode int
	// Detail is the API's own explanation, when it gave one.
	Detail string
}

func (e *APIError) Error() string {
	msg := fmt.Sprintf("metrics api returned %d %s", e.StatusCode, http.StatusText(e.StatusCode))
	if e.Detail != "" {
		msg += ": " + e.Detail
	}
	switch e.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		// The usual causes are a key that is not a Cloud resource management
		// key, or an owner without access to metrics.
		msg += " (the Confluent Cloud API key must be a Cloud resource management key whose owner can use the Metrics API, for example with the MetricsViewer role)"
	}
	return msg
}

// MetricsAPI reads Tableflow's freshness metrics for one Kafka cluster. It
// is safe for concurrent use.
//
// It does not retry. Each poll reads a whole window, so a failed poll loses
// nothing, and the Monitor tries again at the next interval.
type MetricsAPI struct {
	baseURL   string
	clusterID string
	apiKey    string
	apiSecret string
	// tables are the topics to report, in display order. The API returns
	// every topic in the cluster that has the metric.
	tables []string
	http   *http.Client
	now    func() time.Time
}

// NewMetricsAPI returns a client for the Metrics API at baseURL. clusterID is
// the Kafka cluster whose Tableflow topics are read, and tables the topics to
// report. The key and secret must be a Cloud resource management API key:
// the Metrics API rejects keys scoped to a Kafka cluster. They are sent only
// over HTTPS, except to a loopback address so tests can use httptest.
func NewMetricsAPI(baseURL, clusterID, apiKey, apiSecret string, tables []string) (*MetricsAPI, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse metrics api url: %w", err)
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname())) {
		return nil, fmt.Errorf("metrics api url must use https, got %q", u.Scheme)
	}
	if !clusterPattern.MatchString(clusterID) {
		return nil, fmt.Errorf("invalid kafka cluster id %q", clusterID)
	}
	if apiKey == "" || apiSecret == "" {
		return nil, errors.New("confluent cloud api key and secret are required")
	}
	if len(tables) == 0 {
		return nil, errors.New("at least one table is required")
	}
	return &MetricsAPI{
		baseURL:   strings.TrimRight(baseURL, "/"),
		clusterID: clusterID,
		apiKey:    apiKey,
		apiSecret: apiSecret,
		tables:    slices.Clone(tables),
		http: &http.Client{
			Timeout: requestTimeout,
			// Never follow a redirect. Go would send the Authorization header
			// on to a same-host redirect, even from https to http. The
			// redirect is returned as the response and reported as an error.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		now: time.Now,
	}, nil
}

// String omits the credentials so the client is safe to print.
func (m MetricsAPI) String() string {
	return fmt.Sprintf("MetricsAPI{%s cluster:%s}", m.baseURL, m.clusterID)
}

// GoString omits the credentials for %#v.
func (m MetricsAPI) GoString() string { return m.String() }

// LogValue omits the credentials when the client is passed to slog.
func (m MetricsAPI) LogValue() slog.Value {
	return slog.GroupValue(slog.String("endpoint", m.baseURL), slog.String("cluster_id", m.clusterID))
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Query reads both metrics for the last Window, up to the start of the
// current minute, whose value is still being collected.
func (m *MetricsAPI) Query(ctx context.Context) (Result, error) {
	end := m.now().UTC().Truncate(time.Minute)
	start := end.Add(-Window)
	event, evOther, err := m.series(ctx, EventMetric, start, end)
	if err != nil {
		return Result{}, fmt.Errorf("event freshness: %w", err)
	}
	processing, procOther, err := m.series(ctx, ProcessingMetric, start, end)
	if err != nil {
		return Result{}, fmt.Errorf("processing freshness: %w", err)
	}
	unmatched := slices.Compact(slices.Sorted(slices.Values(slices.Concat(evOther, procOther))))
	return Result{Event: event, Processing: processing, Unmatched: unmatched, ObservedAt: m.now().UTC()}, nil
}

type queryRequest struct {
	Aggregations []aggregation `json:"aggregations"`
	Filter       fieldFilter   `json:"filter"`
	GroupBy      []string      `json:"group_by"`
	Granularity  string        `json:"granularity"`
	Intervals    []string      `json:"intervals"`
	Limit        int           `json:"limit"`
}

// aggregation names a metric only. The API picks the aggregation the metric
// needs, which for these gauges is MAX across destinations.
type aggregation struct {
	Metric string `json:"metric"`
}

type fieldFilter struct {
	Field string `json:"field"`
	Op    string `json:"op"`
	Value string `json:"value"`
}

type queryResponse struct {
	Data []struct {
		Timestamp time.Time `json:"timestamp"`
		Value     *float64  `json:"value"`
		Topic     string    `json:"metric.topic"`
	} `json:"data"`
	Meta struct {
		Pagination struct {
			NextPageToken string `json:"next_page_token"`
		} `json:"pagination"`
	} `json:"meta"`
}

// series reads one metric, one sample per table per minute in [start, end),
// and the topics it returned that are not charted.
func (m *MetricsAPI) series(ctx context.Context, metric string, start, end time.Time) ([]Sample, []string, error) {
	body, err := json.Marshal(queryRequest{
		Aggregations: []aggregation{{Metric: metric}},
		Filter:       fieldFilter{Field: "resource.kafka.id", Op: "EQ", Value: m.clusterID},
		GroupBy:      []string{"metric.topic"},
		Granularity:  "PT1M",
		Intervals:    []string{start.Format(time.RFC3339) + "/" + end.Format(time.RFC3339)},
		Limit:        maxGroups,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("encode query: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, m.baseURL+queryPath, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("build request: %w", err)
	}
	req.SetBasicAuth(m.apiKey, m.apiSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", userAgent)

	resp, err := m.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("POST %s: %w", queryPath, err)
	}
	defer resp.Body.Close()
	// One byte more than the limit tells a body that is too large from one
	// that is exactly the limit, rather than failing to decode a cut-off one.
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, nil, &APIError{StatusCode: resp.StatusCode, Detail: errorDetail(b)}
	}
	if len(b) > maxResponseBytes {
		return nil, nil, fmt.Errorf("response is larger than %d bytes", maxResponseBytes)
	}
	var r queryResponse
	if err := json.Unmarshal(b, &r); err != nil {
		return nil, nil, fmt.Errorf("decode response: %w", err)
	}
	if r.Meta.Pagination.NextPageToken != "" {
		// Not expected with a handful of topics, and PIMS does not read
		// further pages. Failing is better than charting a partial answer as
		// if it were whole.
		return nil, nil, errors.New("response has further pages, which PIMS does not read")
	}
	samples, other := m.samples(r)
	return samples, other, nil
}

// samples keeps the values for the configured tables, ordered by time and
// then by the tables' display order, and returns the other topics it saw,
// sorted. Minutes the API has no value for are absent or null and are
// skipped, as is anything without a timestamp, which would otherwise be
// charted at year 1.
func (m *MetricsAPI) samples(r queryResponse) (out []Sample, other []string) {
	for _, d := range r.Data {
		if !slices.Contains(m.tables, d.Topic) {
			other = append(other, d.Topic)
			continue
		}
		if d.Value == nil || d.Timestamp.IsZero() {
			continue
		}
		out = append(out, Sample{Table: d.Topic, Time: d.Timestamp.UTC(), Ms: *d.Value})
	}
	slices.SortStableFunc(out, func(a, b Sample) int {
		if c := a.Time.Compare(b.Time); c != 0 {
			return c
		}
		return slices.Index(m.tables, a.Table) - slices.Index(m.tables, b.Table)
	})
	return out, slices.Compact(slices.Sorted(slices.Values(other)))
}

// errorDetail extracts the API's explanation from an error body, which looks
// like {"errors":[{"detail":"..."}]}, and falls back to the body's first line.
func errorDetail(body []byte) string {
	var e struct {
		Errors []struct {
			Detail string `json:"detail"`
			Title  string `json:"title"`
		} `json:"errors"`
	}
	if json.Unmarshal(body, &e) == nil {
		var parts []string
		for _, x := range e.Errors {
			switch {
			case x.Detail != "":
				parts = append(parts, x.Detail)
			case x.Title != "":
				parts = append(parts, x.Title)
			}
		}
		if len(parts) > 0 {
			return truncate(strings.Join(parts, "; "))
		}
	}
	s := strings.TrimSpace(string(body))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncate(s)
}

func truncate(s string) string {
	const max = 300
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// Querier is satisfied by MetricsAPI; tests substitute a fake.
type Querier interface {
	Query(ctx context.Context) (Result, error)
}

// Monitor polls a Querier and reports each result.
type Monitor struct {
	q        Querier
	interval time.Duration
	log      *slog.Logger
	now      func() time.Time
}

// NewMonitor returns a Monitor that polls every interval.
func NewMonitor(q Querier, interval time.Duration, log *slog.Logger) *Monitor {
	if interval <= 0 {
		interval = DefaultInterval
	}
	return &Monitor{q: q, interval: interval, log: log.With("component", "latency"), now: time.Now}
}

// Run polls until ctx is cancelled, calling onResult with every result. A
// failed poll is reported as a Result with Error set, so the UI can say why
// the numbers stopped moving.
func (m *Monitor) Run(ctx context.Context, onResult func(Result)) {
	t := time.NewTicker(m.interval)
	defer t.Stop()
	for {
		res, err := m.q.Query(ctx)
		switch {
		case ctx.Err() != nil:
			return
		case err != nil:
			m.log.Warn("Tableflow metrics query failed", "error", err)
			onResult(Result{Error: err.Error(), ObservedAt: m.now().UTC()})
		default:
			m.log.Info("Queried Tableflow freshness metrics",
				"event_samples", len(res.Event), "newest_event", newest(res.Event),
				"processing_samples", len(res.Processing), "newest_processing", newest(res.Processing),
				"other_topics", res.Unmatched)
			onResult(res)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// newest is the time of the latest sample, or "none".
func newest(samples []Sample) string {
	var t time.Time
	for _, s := range samples {
		if s.Time.After(t) {
			t = s.Time
		}
	}
	if t.IsZero() {
		return "none"
	}
	return t.Format(time.RFC3339)
}
