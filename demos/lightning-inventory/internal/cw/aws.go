package cw

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"

	"lightninginv/internal/protocol"
)

// Metric names and the dimension.
const (
	MetricLatency     = "Latency"
	MetricErrors      = "Errors"
	MetricDropped     = "Dropped"
	DimensionScenario = "Scenario"
)

// API is the part of the CloudWatch client the demo uses.
type API interface {
	PutMetricData(ctx context.Context, in *cloudwatch.PutMetricDataInput, opts ...func(*cloudwatch.Options)) (*cloudwatch.PutMetricDataOutput, error)
	GetMetricData(ctx context.Context, in *cloudwatch.GetMetricDataInput, opts ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error)
}

func dim(scenario string) []types.Dimension {
	return []types.Dimension{{Name: aws.String(DimensionScenario), Value: aws.String(scenario)}}
}

// Publisher sends seconds to CloudWatch as high-resolution metrics.
type Publisher struct {
	API       API
	Namespace string
	// Timeout bounds each PutMetricData call; DefaultPublishTimeout when zero.
	Timeout time.Duration
}

// DefaultPublishTimeout is how long one PutMetricData call may take.
const DefaultPublishTimeout = 5 * time.Second

// maxPerCall keeps one PutMetricData well under CloudWatch's 1000 metrics and 1 MB.
const maxPerCall = 200

// unit is the metric data of one second of one scenario. A call carries whole units, so a
// second is either sent or not, never half: sending a second's latency twice would double
// its sample count.
type unit struct {
	scenario string
	sec      *Second
	data     []types.MetricDatum
}

func units(secs map[string][]*Second) []unit {
	scenarios := make([]string, 0, len(secs))
	for sc := range secs {
		scenarios = append(scenarios, sc)
	}
	sort.Strings(scenarios)
	var out []unit
	for _, sc := range scenarios {
		for _, s := range secs[sc] {
			ts := aws.Time(s.At)
			var data []types.MetricDatum
			if s.Latency.Count() > 0 {
				v, c := s.Latency.Values()
				data = append(data, types.MetricDatum{
					MetricName: aws.String(MetricLatency), Dimensions: dim(sc), Timestamp: ts,
					Values: v, Counts: c, Unit: types.StandardUnitMilliseconds, StorageResolution: aws.Int32(1),
				})
			}
			if s.Errors > 0 {
				data = append(data, types.MetricDatum{
					MetricName: aws.String(MetricErrors), Dimensions: dim(sc), Timestamp: ts,
					Value: aws.Float64(float64(s.Errors)), Unit: types.StandardUnitCount, StorageResolution: aws.Int32(1),
				})
			}
			if s.Dropped > 0 {
				data = append(data, types.MetricDatum{
					MetricName: aws.String(MetricDropped), Dimensions: dim(sc), Timestamp: ts,
					Value: aws.Float64(float64(s.Dropped)), Unit: types.StandardUnitCount, StorageResolution: aws.Int32(1),
				})
			}
			if len(data) > 0 {
				out = append(out, unit{sc, s, data})
			}
		}
	}
	return out
}

// Publish sends every second in batches, each call under its own timeout. Latency goes as
// Values and Counts with StorageResolution 1, so CloudWatch can answer percentile statistics
// at a 1 s period. When a call fails, the seconds of that call and of every call after it
// are returned as unpublished, for the caller to try again; the seconds before it were sent.
// (A call that timed out may have reached CloudWatch all the same: its retry would then count
// its samples twice, which is accepted, since the alternative is to lose them.)
func (p *Publisher) Publish(ctx context.Context, secs map[string][]*Second) (unpublished map[string][]*Second, err error) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = DefaultPublishTimeout
	}
	us := units(secs)
	for len(us) > 0 {
		var batch []types.MetricDatum
		n := 0
		for n < len(us) && (n == 0 || len(batch)+len(us[n].data) <= maxPerCall) {
			batch = append(batch, us[n].data...)
			n++
		}
		cctx, cancel := context.WithTimeout(ctx, timeout)
		_, err := p.API.PutMetricData(cctx, &cloudwatch.PutMetricDataInput{Namespace: aws.String(p.Namespace), MetricData: batch})
		cancel()
		if err != nil {
			left := map[string][]*Second{}
			for _, u := range us {
				left[u.scenario] = append(left[u.scenario], u.sec)
			}
			return left, err
		}
		us = us[n:]
	}
	return nil, nil
}

// Series is one scenario's chart data, one entry per second that has any.
type Series struct {
	Times   []int64   `json:"times"` // unix seconds
	P50     []float64 `json:"p50"`
	P95     []float64 `json:"p95"`
	P99     []float64 `json:"p99"`
	Count   []float64 `json:"count"`
	Errors  []float64 `json:"errors"`
	Dropped []float64 `json:"dropped"`
	// Truncated is true when CloudWatch had more pages than the Reader reads: the newest
	// data is kept and the oldest is missing.
	Truncated bool `json:"truncated,omitempty"`
}

// maxPages bounds the GetMetricData pages one Read follows. 5 minutes is 300 points of 6
// statistics for 2 scenarios; an hour is 3600, about 43,000 points: 1 page holds 100,800.
const maxPages = 20

// Reader reads the series back.
type Reader struct {
	API       API
	Namespace string
}

// Read returns each scenario's series for the last window, at 1 second resolution.
func (r *Reader) Read(ctx context.Context, now time.Time, window time.Duration) (map[string]*Series, error) {
	type q struct {
		id, metric, stat, scenario string
	}
	var qs []q
	var queries []types.MetricDataQuery
	for i, sc := range protocol.Scenarios {
		for _, s := range []struct{ key, metric, stat string }{
			{"p50", MetricLatency, "p50"}, {"p95", MetricLatency, "p95"}, {"p99", MetricLatency, "p99"},
			{"count", MetricLatency, "SampleCount"}, {"errors", MetricErrors, "Sum"}, {"dropped", MetricDropped, "Sum"},
		} {
			id := fmt.Sprintf("s%d_%s", i, s.key)
			qs = append(qs, q{id, s.metric, s.key, sc})
			queries = append(queries, types.MetricDataQuery{
				Id: aws.String(id), ReturnData: aws.Bool(true),
				MetricStat: &types.MetricStat{
					Metric: &types.Metric{Namespace: aws.String(r.Namespace), MetricName: aws.String(s.metric), Dimensions: dim(sc)},
					Period: aws.Int32(1), Stat: aws.String(s.stat),
				},
			})
		}
	}
	byID := map[string]q{}
	for _, x := range qs {
		byID[x.id] = x
	}
	// scenario -> second -> stat -> value
	cells := map[string]map[int64]map[string]float64{}
	in := &cloudwatch.GetMetricDataInput{
		StartTime: aws.Time(now.Add(-window).Truncate(time.Second)), EndTime: aws.Time(now.Truncate(time.Second).Add(time.Second)),
		MetricDataQueries: queries,
		// Newest first, so that if the pages ever run out it is the oldest data that is
		// missing, not the latest.
		ScanBy: types.ScanByTimestampDescending,
	}
	truncated := false
	for page := 0; ; page++ {
		if page == maxPages {
			truncated = true
			slog.Warn("CloudWatch returned more pages than the chart reads; the oldest points are left out", "pages", maxPages)
			break
		}
		out, err := r.API.GetMetricData(ctx, in)
		if err != nil {
			return nil, err
		}
		for _, res := range out.MetricDataResults {
			x, ok := byID[aws.ToString(res.Id)]
			if !ok {
				continue
			}
			for i, ts := range res.Timestamps {
				if i >= len(res.Values) {
					break
				}
				m := cells[x.scenario]
				if m == nil {
					m = map[int64]map[string]float64{}
					cells[x.scenario] = m
				}
				row := m[ts.Unix()]
				if row == nil {
					row = map[string]float64{}
					m[ts.Unix()] = row
				}
				row[x.stat] = res.Values[i]
			}
		}
		if out.NextToken == nil {
			break
		}
		in.NextToken = out.NextToken
	}
	result := map[string]*Series{}
	for _, sc := range protocol.Scenarios {
		s := &Series{Truncated: truncated}
		secs := make([]int64, 0, len(cells[sc]))
		for t := range cells[sc] {
			secs = append(secs, t)
		}
		sort.Slice(secs, func(i, j int) bool { return secs[i] < secs[j] })
		for _, t := range secs {
			row := cells[sc][t]
			s.Times = append(s.Times, t)
			s.P50 = append(s.P50, row["p50"])
			s.P95 = append(s.P95, row["p95"])
			s.P99 = append(s.P99, row["p99"])
			s.Count = append(s.Count, row["count"])
			s.Errors = append(s.Errors, row["errors"])
			s.Dropped = append(s.Dropped, row["dropped"])
		}
		result[sc] = s
	}
	return result, nil
}
