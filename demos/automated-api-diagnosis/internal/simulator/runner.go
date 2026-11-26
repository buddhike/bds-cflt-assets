package simulator

import (
	"context"
	"sync/atomic"
	"time"
)

// apiRunner drives one simulated API: a ticker whose interval tracks the
// API's configured rate, producing one event per tick.
type apiRunner struct {
	name     string
	cfg      atomic.Pointer[APIConfig]
	updateCh chan struct{}
	stopCh   chan struct{}
	board    *OutcomeBoard
	producer Sink
	onError  func(apiName string, err error)
	counters apiCounters
}

func newAPIRunner(cfg APIConfig, board *OutcomeBoard, producer Sink, onError func(string, error)) *apiRunner {
	r := &apiRunner{
		name:     cfg.Name,
		updateCh: make(chan struct{}, 1),
		stopCh:   make(chan struct{}),
		board:    board,
		producer: producer,
		onError:  onError,
	}
	r.cfg.Store(&cfg)
	return r
}

// UpdateConfig swaps in a new config; the running loop picks it up and
// resets its ticker immediately rather than waiting out the old interval.
func (r *apiRunner) UpdateConfig(cfg APIConfig) {
	r.cfg.Store(&cfg)
	select {
	case r.updateCh <- struct{}{}:
	default:
	}
}

func (r *apiRunner) Stop() {
	close(r.stopCh)
}

// minRate is the slowest an API produces, in events a second. A smaller rate would
// overflow the ticker's interval, and time.NewTicker panics on that.
const minRate = 0.01

func tickInterval(rate float64) time.Duration {
	if rate < minRate {
		rate = 0.1
	}
	return time.Duration(float64(time.Second) / rate)
}

func (r *apiRunner) run(ctx context.Context) {
	cfg := *r.cfg.Load()
	ticker := time.NewTicker(tickInterval(cfg.Rate))
	defer ticker.Stop()

	for {
		select {
		case <-r.stopCh:
			return
		case <-ctx.Done():
			return
		case <-r.updateCh:
			cfg = *r.cfg.Load()
			ticker.Reset(tickInterval(cfg.Rate))
		case now := <-ticker.C:
			cfg = *r.cfg.Load()
			deps := r.board.Get(cfg.DependsOn)
			ev, outcome := buildEvent(cfg, deps, now)
			r.board.Set(cfg.Name, outcome)
			r.counters.record(outcome.Status, outcome.DurationMs)

			// r.name, not cfg.Name: the delivery callback runs on another goroutine, and
			// cfg is reassigned on the next tick.
			r.producer.Produce(ctx, r.name, ev, func(err error) {
				if err != nil && r.onError != nil {
					r.onError(r.name, err)
				}
			})
		}
	}
}
