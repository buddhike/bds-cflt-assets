package cw

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch"
	"github.com/aws/aws-sdk-go-v2/service/cloudwatch/types"
)

func TestHistogramKeepsPercentilesWithin5Percent(t *testing.T) {
	var h Histogram
	for i := 1; i <= 1000; i++ {
		h.Observe(time.Duration(i) * time.Millisecond)
	}
	v, c := h.Values()
	var total float64
	for _, x := range c {
		total += x
	}
	if total != 1000 || len(v) > MaxValues {
		t.Fatalf("total %v values %d", total, len(v))
	}
	// the median of 1..1000 ms is about 500
	var run float64
	for i := range v {
		run += c[i]
		if run >= 500 {
			if math.Abs(v[i]-500)/500 > 0.06 {
				t.Fatalf("median %v", v[i])
			}
			break
		}
	}
}

func TestHistogramNeverExceedsTheLimit(t *testing.T) {
	var h Histogram
	// 0.1 ms to ~100 s on a fine grid: far more than 150 buckets.
	for ms := 0.1; ms < 100_000; ms *= 1.01 {
		h.Observe(time.Duration(ms * float64(time.Millisecond)))
	}
	v, c := h.Values()
	if len(v) > MaxValues || len(v) != len(c) {
		t.Fatalf("%d values", len(v))
	}
	var total float64
	for _, x := range c {
		total += x
	}
	if int64(total) != h.Count() {
		t.Fatal("merging lost samples")
	}
}

func TestRecorderDrainsOnlyFinishedSeconds(t *testing.T) {
	r := NewRecorder()
	t0 := time.Unix(1000, 500_000_000)
	r.Success("by_sku", t0, 10*time.Millisecond)
	r.Error("by_sku", t0)
	r.Drop("by_locations", t0.Add(time.Second))
	got := r.Drain(t0.Add(time.Second))
	if len(got["by_sku"]) != 1 || got["by_sku"][0].Errors != 1 || got["by_sku"][0].Latency.Count() != 1 {
		t.Fatalf("%+v", got)
	}
	if len(got["by_locations"]) != 0 {
		t.Fatal("the current second was drained")
	}
	if len(r.Drain(t0.Add(time.Second))["by_sku"]) != 0 {
		t.Fatal("drained twice")
	}
}

type fakeAPI struct {
	puts []*cloudwatch.PutMetricDataInput
	get  *cloudwatch.GetMetricDataOutput
	// failFrom makes every PutMetricData call from this one on (1-based) fail; 0 never.
	failFrom int
	// hang makes PutMetricData wait for its context to end.
	hang  bool
	calls int
	gets  int
}

func (f *fakeAPI) PutMetricData(ctx context.Context, in *cloudwatch.PutMetricDataInput, _ ...func(*cloudwatch.Options)) (*cloudwatch.PutMetricDataOutput, error) {
	f.calls++
	if f.hang {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if f.failFrom > 0 && f.calls >= f.failFrom {
		return nil, errors.New("throttled")
	}
	f.puts = append(f.puts, in)
	return &cloudwatch.PutMetricDataOutput{}, nil
}
func (f *fakeAPI) GetMetricData(context.Context, *cloudwatch.GetMetricDataInput, ...func(*cloudwatch.Options)) (*cloudwatch.GetMetricDataOutput, error) {
	f.gets++
	return f.get, nil
}

func TestPublishIsHighResolutionWithValuesAndCounts(t *testing.T) {
	api := &fakeAPI{}
	p := &Publisher{API: api, Namespace: "LightningInventory/x"}
	s := &Second{At: time.Unix(1000, 0), Errors: 2}
	s.Latency.Observe(5 * time.Millisecond)
	if left, err := p.Publish(context.Background(), map[string][]*Second{"by_sku": {s}}); err != nil || len(left) != 0 {
		t.Fatal(err, left)
	}
	in := api.puts[0]
	if aws.ToString(in.Namespace) != "LightningInventory/x" || len(in.MetricData) != 2 {
		t.Fatalf("%+v", in)
	}
	lat := in.MetricData[0]
	if aws.ToString(lat.MetricName) != MetricLatency || aws.ToInt32(lat.StorageResolution) != 1 || len(lat.Values) != 1 || lat.Counts[0] != 1 {
		t.Fatalf("%+v", lat)
	}
	if aws.ToString(lat.Dimensions[0].Name) != "Scenario" || aws.ToString(lat.Dimensions[0].Value) != "by_sku" {
		t.Fatal("dimension")
	}
}

func TestPublishSplitsLargeBatches(t *testing.T) {
	api := &fakeAPI{}
	p := &Publisher{API: api, Namespace: "n"}
	var list []*Second
	for i := 0; i < 450; i++ {
		s := &Second{At: time.Unix(int64(1000+i), 0)}
		s.Latency.Observe(time.Millisecond)
		list = append(list, s)
	}
	if _, err := p.Publish(context.Background(), map[string][]*Second{"by_sku": list}); err != nil {
		t.Fatal(err)
	}
	if len(api.puts) != 3 {
		t.Fatalf("%d calls", len(api.puts))
	}
}

func TestReadBuildsSeries(t *testing.T) {
	t1, t2 := time.Unix(1000, 0), time.Unix(1001, 0)
	api := &fakeAPI{get: &cloudwatch.GetMetricDataOutput{MetricDataResults: []types.MetricDataResult{
		{Id: aws.String("s0_p50"), Timestamps: []time.Time{t1, t2}, Values: []float64{10, 11}},
		{Id: aws.String("s0_p99"), Timestamps: []time.Time{t1}, Values: []float64{30}},
		{Id: aws.String("s1_count"), Timestamps: []time.Time{t2}, Values: []float64{7}},
	}}}
	r := &Reader{API: api, Namespace: "n"}
	out, err := r.Read(context.Background(), time.Unix(1002, 0), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	a := out["by_sku"]
	if len(a.Times) != 2 || a.P50[1] != 11 || a.P99[0] != 30 || a.P99[1] != 0 {
		t.Fatalf("%+v", a)
	}
	if b := out["by_locations"]; len(b.Times) != 1 || b.Count[0] != 7 {
		t.Fatalf("%+v", b)
	}
}

// A failed call hands back the seconds of that call and the ones after it, and only those: the
// seconds of the calls that got through must not be sent again, or CloudWatch counts them twice.
func TestPublishReturnsTheSecondsOfAFailedCallAndAfter(t *testing.T) {
	api := &fakeAPI{failFrom: 2}
	p := &Publisher{API: api, Namespace: "n"}
	var list []*Second
	for i := 0; i < 450; i++ { // 1 datum each, 200 per call: 3 calls
		s := &Second{At: time.Unix(int64(1000+i), 0)}
		s.Latency.Observe(time.Millisecond)
		list = append(list, s)
	}
	left, err := p.Publish(context.Background(), map[string][]*Second{"by_sku": list})
	if err == nil {
		t.Fatal("no error although the second call failed")
	}
	if len(api.puts) != 1 || len(api.puts[0].MetricData) != 200 {
		t.Fatalf("%d calls got through", len(api.puts))
	}
	got := left["by_sku"]
	if len(got) != 250 || got[0] != list[200] || got[249] != list[449] {
		t.Fatalf("%d seconds returned, the first of the failed call is %v", len(got), got[0] == list[200])
	}
}

func TestPublishNeverSplitsASecondAcrossCalls(t *testing.T) {
	api := &fakeAPI{}
	p := &Publisher{API: api, Namespace: "n"}
	var list []*Second
	for i := 0; i < 100; i++ { // 3 datums each (latency, errors, dropped): 66 fit in 200
		s := &Second{At: time.Unix(int64(1000+i), 0), Errors: 1, Dropped: 1}
		s.Latency.Observe(time.Millisecond)
		list = append(list, s)
	}
	if _, err := p.Publish(context.Background(), map[string][]*Second{"by_sku": list}); err != nil {
		t.Fatal(err)
	}
	for _, in := range api.puts {
		if len(in.MetricData)%3 != 0 || len(in.MetricData) > 200 {
			t.Fatalf("a call carries %d datums", len(in.MetricData))
		}
	}
}

func TestPublishTimesOutEachCall(t *testing.T) {
	api := &fakeAPI{hang: true}
	p := &Publisher{API: api, Namespace: "n", Timeout: 50 * time.Millisecond}
	s := &Second{At: time.Unix(1000, 0)}
	s.Latency.Observe(time.Millisecond)
	start := time.Now()
	left, err := p.Publish(context.Background(), map[string][]*Second{"by_sku": {s}})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 2*time.Second {
		t.Fatalf("err %v after %v", err, time.Since(start))
	}
	if len(left["by_sku"]) != 1 {
		t.Fatal("the second that was not sent is not returned")
	}
}

// More pages than the reader follows: it stops, says so, and the data it has is returned.
func TestReadMarksTruncationAfterTooManyPages(t *testing.T) {
	api := &fakeAPI{get: &cloudwatch.GetMetricDataOutput{
		NextToken: aws.String("more"),
		MetricDataResults: []types.MetricDataResult{
			{Id: aws.String("s0_p50"), Timestamps: []time.Time{time.Unix(1000, 0)}, Values: []float64{10}},
		},
	}}
	r := &Reader{API: api, Namespace: "n"}
	out, err := r.Read(context.Background(), time.Unix(1002, 0), 5*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if api.gets != maxPages {
		t.Fatalf("%d pages read, want %d", api.gets, maxPages)
	}
	if !out["by_sku"].Truncated || !out["by_locations"].Truncated || len(out["by_sku"].Times) != 1 {
		t.Fatalf("%+v", out["by_sku"])
	}
	// a result that ends in time is not marked
	api = &fakeAPI{get: &cloudwatch.GetMetricDataOutput{}}
	out, _ = (&Reader{API: api, Namespace: "n"}).Read(context.Background(), time.Unix(1002, 0), 5*time.Minute)
	if out["by_sku"].Truncated {
		t.Fatal("marked truncated")
	}
}
