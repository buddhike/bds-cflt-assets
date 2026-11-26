package simulator

import (
	"context"
	"sync"
	"time"
)

// statsInterval is how often live per-API stats are broadcast while running.
const statsInterval = 500 * time.Millisecond

// Sink is where the engine sends the events it generates. *Producer is the real
// one, which writes to Kafka; tests supply their own.
type Sink interface {
	Produce(ctx context.Context, key string, ev KongLogEvent, onErr func(error))
}

// Engine owns the shared event sink and one apiRunner per simulated API.
type Engine struct {
	mu       sync.Mutex
	producer Sink
	board    *OutcomeBoard
	runners  map[string]*apiRunner
	cancel   context.CancelFunc
	running  bool
	onError  func(apiName string, err error)
	onStats  func(apiName string, stats APIStats)
}

// NewEngine builds an Engine bound to producer, usually a *Producer. onError (optional) is
// called whenever a Kafka delivery fails, and onStats (optional) roughly
// every statsInterval with each API's recent activity - both from
// arbitrary goroutines.
func NewEngine(producer Sink, onError func(apiName string, err error), onStats func(apiName string, stats APIStats)) *Engine {
	return &Engine{
		producer: producer,
		board:    newOutcomeBoard(),
		runners:  make(map[string]*apiRunner),
		onError:  onError,
		onStats:  onStats,
	}
}

// Start launches one goroutine per API in apis, plus a stats broadcaster.
// No-op if already running.
func (e *Engine) Start(apis []APIConfig) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.running {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	for _, cfg := range apis {
		r := newAPIRunner(cfg, e.board, e.producer, e.onError)
		e.runners[cfg.Name] = r
		go r.run(ctx)
	}
	go e.broadcastStats(ctx)
	e.running = true
}

// Stop halts every runner and the stats broadcaster. No-op if not running.
func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.running {
		return
	}
	e.cancel()
	e.runners = make(map[string]*apiRunner)
	e.running = false
}

// UpdateConfig applies a live config change to one running API. Reports
// false if that API isn't currently running (e.g. engine is stopped).
func (e *Engine) UpdateConfig(cfg APIConfig) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	r, ok := e.runners[cfg.Name]
	if !ok {
		return false
	}
	r.UpdateConfig(cfg)
	return true
}

func (e *Engine) IsRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

func (e *Engine) broadcastStats(ctx context.Context) {
	if e.onStats == nil {
		return
	}
	ticker := time.NewTicker(statsInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, r := range e.runnersSnapshot() {
				snap := r.counters.snapshotAndReset()
				statusCounts := make(map[string]int, len(snap.statusCounts))
				for k, v := range snap.statusCounts {
					statusCounts[k] = int(v)
				}
				e.onStats(r.name, APIStats{
					EventsPerSec:   float64(snap.total) / statsInterval.Seconds(),
					StatusCounts:   statusCounts,
					LastDurationMs: int(snap.lastDuration),
				})
			}
		}
	}
}

func (e *Engine) runnersSnapshot() []*apiRunner {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]*apiRunner, 0, len(e.runners))
	for _, r := range e.runners {
		out = append(out, r)
	}
	return out
}
