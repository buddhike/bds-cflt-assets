package simulator

import (
	"strings"
	"testing"
	"time"

	"github.com/heetch/avro"
)

func TestPickStatusDeterministic(t *testing.T) {
	cases := []struct {
		name        string
		cfg         APIConfig
		wantAlways  int
	}{
		{"always 500", APIConfig{Pct500: 100}, 500},
		{"always 403", APIConfig{Pct403: 100}, 403},
		{"always 404", APIConfig{Pct404: 100}, 404},
		{"always 200", APIConfig{}, 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for i := 0; i < 100; i++ {
				if got := pickStatus(c.cfg); got != c.wantAlways {
					t.Fatalf("pickStatus() = %d, want %d", got, c.wantAlways)
				}
			}
		})
	}
}

func TestPickStatusDistribution(t *testing.T) {
	cfg := APIConfig{Pct500: 10, Pct403: 20, Pct404: 5}
	counts := map[int]int{}
	const n = 20000
	for i := 0; i < n; i++ {
		counts[pickStatus(cfg)]++
	}

	check := func(status int, wantPct float64) {
		gotPct := float64(counts[status]) / n * 100
		if gotPct < wantPct-3 || gotPct > wantPct+3 {
			t.Errorf("status %d: got %.1f%%, want ~%.1f%%", status, gotPct, wantPct)
		}
	}
	check(500, 10)
	check(403, 20)
	check(404, 5)
	check(200, 65)
}

func TestJitterLatencyBounds(t *testing.T) {
	for i := 0; i < 1000; i++ {
		d := jitterLatency(100)
		if d < 1 {
			t.Fatalf("jitterLatency(100) = %d, want >= 1", d)
		}
		if d < 40 || d > 160 {
			t.Errorf("jitterLatency(100) = %d, wildly outside expected jitter range", d)
		}
	}
	if d := jitterLatency(0); d < 1 {
		t.Fatalf("jitterLatency(0) = %d, want >= 1 (clamped)", d)
	}
}

func TestResolveCascadePropagatesFailure(t *testing.T) {
	deps := []Outcome{{Status: 500, DurationMs: 300, Failed: true}}
	cascadedCount := 0
	const n = 2000
	for i := 0; i < n; i++ {
		cascaded, latency := resolveCascade(deps)
		if cascaded {
			cascadedCount++
			if latency != 300 {
				t.Fatalf("resolveCascade latency = %d, want 300", latency)
			}
		}
	}
	pct := float64(cascadedCount) / n * 100
	wantPct := cascadeFactor * 100
	if pct < wantPct-5 || pct > wantPct+5 {
		t.Errorf("cascade rate = %.1f%%, want ~%.1f%%", pct, wantPct)
	}
}

func TestResolveCascadeGatewayErrorPropagatesAsNoResponse(t *testing.T) {
	deps := []Outcome{{Status: 502, DurationMs: -1, Failed: true}}
	sawCascade := false
	for i := 0; i < 2000 && !sawCascade; i++ {
		cascaded, latency := resolveCascade(deps)
		if cascaded {
			sawCascade = true
			if latency != -1 {
				t.Fatalf("resolveCascade latency = %d, want -1", latency)
			}
		}
	}
	if !sawCascade {
		t.Fatal("expected at least one cascade in 2000 trials at cascadeFactor=0.9")
	}
}

func TestResolveCascadeIgnoresHealthyDeps(t *testing.T) {
	deps := []Outcome{{Status: 200, DurationMs: 10, Failed: false}}
	for i := 0; i < 100; i++ {
		if cascaded, _ := resolveCascade(deps); cascaded {
			t.Fatal("resolveCascade should never cascade from a healthy dependency")
		}
	}
}

func TestBuildEventGatewayError(t *testing.T) {
	cfg := APIConfig{Name: "TEST_API", Path: "/test", GatewayError: true}
	ev, outcome := buildEvent(cfg, nil, time.Now())

	if ev.Status != "502" || ev.ResponseStatus != "502" {
		t.Errorf("status = %q/%q, want 502/502", ev.Status, ev.ResponseStatus)
	}
	if ev.ResponseDuration != "-1" || ev.RequestTime != "-1" {
		t.Errorf("duration = %q/%q, want -1/-1", ev.ResponseDuration, ev.RequestTime)
	}
	if !outcome.Failed || outcome.DurationMs != -1 {
		t.Errorf("outcome = %+v, want Failed=true DurationMs=-1", outcome)
	}
	if ev.APIName != "TEST_API" || ev.Path != "/test" {
		t.Errorf("event did not carry through cfg.Name/Path: %+v", ev)
	}
}

// TestBuildEventAvroRoundTrip round-trips a KongLogEvent through
// github.com/heetch/avro (the library confluent-kafka-go's Avro serde uses
// internally) exactly the way the real serializer/deserializer do: schema
// comes from KongLogEvent's AvroRecord method (timestamp-millis), not
// reflection. A mismatch here (e.g. a struct tag not matching the schema)
// would fail to marshal/unmarshal, or silently drop data.
func TestBuildEventAvroRoundTrip(t *testing.T) {
	cfg := APIConfig{Name: "TEST_API", Path: "/test", LatencyMs: 50}
	ev, _ := buildEvent(cfg, nil, time.Now())

	data, writerType, err := avro.Marshal(ev)
	if err != nil {
		t.Fatalf("avro.Marshal: %v", err)
	}

	var got KongLogEvent
	if _, err := avro.Unmarshal(data, &got, writerType); err != nil {
		t.Fatalf("avro.Unmarshal: %v", err)
	}

	if got.Timestamp != ev.Timestamp {
		t.Errorf("Timestamp round-trip mismatch: got %v, want %v", got.Timestamp, ev.Timestamp)
	}
	if !strings.Contains(writerType.String(), `"logicalType":"timestamp-millis"`) {
		t.Errorf("expected schema to declare timestamp-millis, got: %s", writerType.String())
	}
	if got.APIName != ev.APIName || got.Path != ev.Path || got.Status != ev.Status ||
		got.RequestMethod != ev.RequestMethod || got.ResponseStatus != ev.ResponseStatus ||
		got.ResponseDuration != ev.ResponseDuration || got.AuthType != ev.AuthType ||
		got.ID != ev.ID || got.Index != ev.Index {
		t.Errorf("round-tripped event mismatch:\n got  %+v\n want %+v", got, ev)
	}
}
