package simulator

import (
	"fmt"
	"math/rand"
	"strconv"
	"time"

	"github.com/google/uuid"
)

// cascadeFactor is the probability a dependent API's request fails when one
// of its dependencies' last outcome was itself a failure. Kept high so a
// cascading failure is clearly visible in the log stream for demo purposes,
// rather than lost in noise.
const cascadeFactor = 0.9

var clientIPPool = []string{
	"203.0.113.10", "203.0.113.11", "203.0.113.12", "203.0.113.20", "203.0.113.30",
}

// buildEvent produces one simulated log event and the resulting Outcome
// (published afterwards so dependent APIs can react to it on their next
// tick). Pure function, no I/O, so it's straightforward to unit test.
func buildEvent(cfg APIConfig, deps []Outcome, now time.Time) (KongLogEvent, Outcome) {
	status, durationMs := resolveStatusAndLatency(cfg, deps)
	failed := durationMs < 0 || status >= 500

	statusStr := strconv.Itoa(status)
	durationStr := strconv.Itoa(durationMs)

	ev := KongLogEvent{
		Timestamp:        now.UnixMilli(),
		CorrelationID:    uuid.NewString(),
		Env:              "demo",
		EventType:        "kong",
		InstanceType:     "internal",
		ClientIP:         clientIPPool[rand.Intn(len(clientIPPool))],
		APIName:          cfg.Name,
		BkHost:           kongHost(cfg.Name),
		Path:             cfg.Path,
		Status:           statusStr,
		RequestMethod:    "GET",
		RequestTime:      durationStr,
		ConsumerUsername: cfg.Name,
		ResponseStatus:   statusStr,
		ResponseDuration: durationStr,
		AuthType:         "JWT",
		AuthSubject:      cfg.Name,
		ID:               uuid.NewString(),
		Type:             " - ",
		Index:            "gateway-kong-" + now.Format("2006-01-02"),
	}

	return ev, Outcome{Status: status, DurationMs: durationMs, Failed: failed, At: now}
}

// resolveStatusAndLatency decides this tick's status code and latency,
// applying (in priority order): the API's own gateway-error toggle, a
// cascaded failure from a failing dependency, then normal weighted status
// selection with jittered latency.
func resolveStatusAndLatency(cfg APIConfig, deps []Outcome) (status int, durationMs int) {
	if cfg.GatewayError {
		return 502, -1
	}

	cascaded, depLatency := resolveCascade(deps)
	if cascaded {
		if depLatency < 0 {
			return 500, -1
		}
		return 500, jitterLatency(cfg.LatencyMs) + depLatency
	}

	return pickStatus(cfg), jitterLatency(cfg.LatencyMs)
}

// resolveCascade rolls, for each failing dependency, whether that failure
// propagates to this API. If any does, it reports the largest dependency
// latency observed (or -1 if any cascaded dependency itself had no response).
func resolveCascade(deps []Outcome) (cascaded bool, latency int) {
	for _, dep := range deps {
		if !dep.Failed {
			continue
		}
		if rand.Float64() >= cascadeFactor {
			continue
		}
		cascaded = true
		if dep.DurationMs < 0 {
			return true, -1
		}
		if dep.DurationMs > latency {
			latency = dep.DurationMs
		}
	}
	return cascaded, latency
}

func pickStatus(cfg APIConfig) int {
	r := rand.Float64() * 100
	switch {
	case r < cfg.Pct500:
		return 500
	case r < cfg.Pct500+cfg.Pct403:
		return 403
	case r < cfg.Pct500+cfg.Pct403+cfg.Pct404:
		return 404
	default:
		return 200
	}
}

func jitterLatency(baseMs int) int {
	if baseMs <= 0 {
		baseMs = 1
	}
	jitter := rand.NormFloat64() * float64(baseMs) * 0.15
	d := int(float64(baseMs) + jitter)
	if d < 1 {
		d = 1
	}
	return d
}

func kongHost(apiName string) string {
	return fmt.Sprintf("api-gateway-%s", apiName)
}
