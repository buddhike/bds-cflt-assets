// Package cw publishes the query latencies to CloudWatch and reads them back.
//
// A query task does not publish percentiles. It publishes, for every second, the
// distribution of the latencies it saw (CloudWatch's Values and Counts), and CloudWatch
// computes p50, p95 and p99 over the samples of every task together. Averaging the
// percentiles of several tasks would be wrong once the service scales out.
package cw

import (
	"math"
	"sort"
	"time"
)

const (
	// growth is the width of a latency bucket: a value is rounded up to the next
	// 5 % step, so a percentile is off by at most that.
	growth = 1.05
	// floorMs is the smallest latency told apart.
	floorMs = 0.1
	// MaxValues is CloudWatch's limit of values per metric in one PutMetricData call.
	MaxValues = 150
)

var logGrowth = math.Log(growth)

// Histogram counts latencies in 5 % log-scale buckets.
type Histogram struct {
	counts map[int]int64
	n      int64
}

// Observe adds one latency.
func (h *Histogram) Observe(d time.Duration) {
	ms := float64(d) / float64(time.Millisecond)
	idx := 0
	if ms > floorMs {
		idx = int(math.Ceil(math.Log(ms/floorMs) / logGrowth))
	}
	if h.counts == nil {
		h.counts = map[int]int64{}
	}
	h.counts[idx]++
	h.n++
}

// Count is how many latencies were observed.
func (h *Histogram) Count() int64 { return h.n }

func bucketValue(idx int) float64 { return floorMs * math.Pow(growth, float64(idx)) }

// Values returns parallel Values and Counts for PutMetricData, at most MaxValues of
// them: when more buckets are used, neighbours are merged (weighted by count).
func (h *Histogram) Values() (values, counts []float64) {
	keys := make([]int, 0, len(h.counts))
	for k := range h.counts {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		values = append(values, bucketValue(k))
		counts = append(counts, float64(h.counts[k]))
	}
	for len(values) > MaxValues {
		mv, mc := values[:0:0], counts[:0:0]
		for i := 0; i < len(values); i += 2 {
			if i+1 == len(values) {
				mv, mc = append(mv, values[i]), append(mc, counts[i])
				break
			}
			c := counts[i] + counts[i+1]
			mv = append(mv, (values[i]*counts[i]+values[i+1]*counts[i+1])/c)
			mc = append(mc, c)
		}
		values, counts = mv, mc
	}
	return values, counts
}
