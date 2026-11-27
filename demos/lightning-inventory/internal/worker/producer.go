package worker

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
	"github.com/twmb/franz-go/pkg/sasl/plain"

	"lightninginv/internal/catalog"
	"lightninginv/internal/protocol"
)

// MaxEventsPerSec and MaxQueriesPerSec bound what the operator may ask for. They are the
// server's, and the browser cannot raise them: the cluster is billed.
const (
	MaxEventsPerSec      = 200
	DefaultEventsPerSec  = 10
	MaxQueriesPerSec     = 1000
	DefaultQueriesPerSec = 50
	// SeedRecordsPerSec is the fixed rate, across all producer tasks, at which the one-off
	// load of the catalogue runs.
	SeedRecordsPerSec = 25_000
	// RecordDeliveryTimeout is how long a record may take to be delivered before it counts
	// as an error.
	RecordDeliveryTimeout = 15 * time.Second
	// MaxBufferedRecords is the producer's send buffer, in records.
	MaxBufferedRecords = 20_000
)

// Sender delivers one record. The real one is a Kafka producer.
type Sender interface {
	Send(ctx context.Context, key string, value []byte, done func(error))
	Flush(ctx context.Context) error
}

// KafkaSender produces to one topic.
type KafkaSender struct {
	cl *kgo.Client
}

// NewKafkaSender connects to Confluent Cloud with the cluster's API key. The default
// partitioner hashes the key like the Java client, so a pair always lands in one partition.
func NewKafkaSender(bootstrap, key, secret, topic string) (*KafkaSender, error) {
	cl, err := kgo.NewClient(
		kgo.SeedBrokers(bootstrap),
		kgo.DialTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12}),
		kgo.SASL(plain.Auth{User: key, Pass: secret}.AsMechanism()),
		kgo.DefaultProduceTopic(topic),
		kgo.ProducerLinger(5*time.Millisecond),
		kgo.ProducerBatchCompression(kgo.Lz4Compression()),
		// A slow or unreachable cluster must show as ProduceErrors, not as records that wait
		// for ever: a record not delivered in this time fails, and a request that has no
		// answer in this time is retried or fails with it.
		kgo.RecordDeliveryTimeout(RecordDeliveryTimeout),
		kgo.ProduceRequestTimeout(10*time.Second),
		// A full buffer blocks the pacer. About a second of the catalogue load (25,000/s
		// between the tasks), so a stall is short and the backlog it hides is small.
		kgo.MaxBufferedRecords(MaxBufferedRecords),
	)
	if err != nil {
		return nil, fmt.Errorf("could not create the Kafka client: %w", err)
	}
	return &KafkaSender{cl: cl}, nil
}

// Send queues a record; done is called with the outcome.
func (k *KafkaSender) Send(ctx context.Context, key string, value []byte, done func(error)) {
	k.cl.Produce(ctx, &kgo.Record{Key: []byte(key), Value: value}, func(_ *kgo.Record, err error) { done(err) })
}

// Flush waits for queued records.
func (k *KafkaSender) Flush(ctx context.Context) error { return k.cl.Flush(ctx) }

// Close closes the client.
func (k *KafkaSender) Close() { k.cl.Close() }

// Producer runs the live events and the load of the catalogue on one task.
type Producer struct {
	Ctl     *Control
	Sender  Sender
	Codec   *Codec
	Catalog catalog.Catalog
	Seed    uint64

	produced, produceErrs, seedErrs atomic.Int64

	// SeedBackoffMin and SeedBackoffMax bound the wait after a shard that did not load;
	// seedBackoffMin and seedBackoffMax when zero.
	SeedBackoffMin, SeedBackoffMax time.Duration

	mu        sync.Mutex
	seedEpoch int           // the load seedDone belongs to
	seedDone  map[int]bool  // shards finished in seedEpoch
	seedRecs  map[int]int64 // records of each finished shard
	// loadFn loads one shard of an epoch; loadShard unless a test replaces it.
	loadFn func(ctx context.Context, shard, epoch int) (records int64, err error)
}

// send encodes and queues one record. ok and bad are the counters the heartbeat reports
// (it zeroes them). shardOK and shardBad, when set, are the counts of one shard: they are
// never zeroed, so a shard that lost a record is not reported done whatever the heartbeat did.
// A record whose context is cancelled before it was delivered is a failure of the shard:
// whoever cancels a shard's context while records are still buffered has abandoned the shard
// and does not report it done. For the live events (no shard) it is the task stopping, which
// the heartbeat does not count.
func (p *Producer) send(ctx context.Context, sku, loc string, qty int, ok, bad, shardOK, shardBad *atomic.Int64) {
	fail := func(canceled bool) {
		if shardBad != nil {
			shardBad.Add(1)
		}
		if !canceled {
			bad.Add(1)
		}
	}
	val, err := p.Codec.Encode(Position{SKU: sku, LocationID: loc, Quantity: int32(qty)})
	if err != nil {
		fail(false)
		return
	}
	p.Sender.Send(ctx, catalog.Key(sku, loc), val, func(err error) {
		if err != nil {
			// Cancelled, or the client closed at stop: the task or the shard ending, not a
			// failed record, for the heartbeat's count. (The shard's own count includes it:
			// a shard whose records were cancelled is not done.)
			fail(errors.Is(err, context.Canceled) || errors.Is(err, kgo.ErrClientClosed))
			return
		}
		if shardOK != nil {
			shardOK.Add(1)
		} else {
			ok.Add(1)
		}
	})
}

// initSeed makes the maps of the load's progress, if they are not there.
func (p *Producer) initSeed() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.seedDone == nil {
		p.seedDone, p.seedRecs = map[int]bool{}, map[int]int64{}
	}
}

func (p *Producer) heartbeat() protocol.Heartbeat {
	// Under the lock so that the seed counts and the epoch they belong to are read together.
	p.mu.Lock()
	defer p.mu.Unlock()
	hb := protocol.Heartbeat{
		Role: "producer", Produced: p.produced.Swap(0), ProduceErrors: p.produceErrs.Swap(0),
		SeedErrors: p.seedErrs.Swap(0), SeedEpoch: p.seedEpoch,
	}
	for s := range p.seedDone {
		hb.SeedDone = append(hb.SeedDone, s)
		if hb.SeedRecords == nil {
			hb.SeedRecords = map[int]int64{}
		}
		hb.SeedRecords[s] = p.seedRecs[s]
	}
	return hb
}

// stopGrace is how long the records of the live events may take to be delivered after the
// task is told to stop.
const stopGrace = 5 * time.Second

// Run blocks until ctx ends.
func (p *Producer) Run(ctx context.Context) {
	p.initSeed()
	go p.Ctl.Loop(ctx, p.heartbeat)
	go p.seedLoop(ctx)

	// The records carry a context that outlives ctx, so the ones still buffered when the task
	// is told to stop are delivered by the final flush, not failed as cancelled. It is cancelled
	// after the flush, and as a backstop shortly after ctx ends.
	rctx, rcancel := context.WithCancel(context.WithoutCancel(ctx))
	defer rcancel()
	stopBackstop := context.AfterFunc(ctx, func() { time.AfterFunc(stopGrace+time.Second, rcancel) })
	defer stopBackstop()

	s := catalog.NewSampler(p.Catalog, 0, p.Seed)
	Pace(ctx, func() float64 {
		pl := p.Ctl.Plan()
		if !pl.Run {
			return 0
		}
		return min(pl.Rate, MaxEventsPerSec)
	}, func() {
		sku, loc, q := s.Event()
		p.send(rctx, sku, loc, q, &p.produced, &p.produceErrs, nil, nil)
	})
	fctx, cancel := context.WithTimeout(context.Background(), stopGrace)
	defer cancel()
	_ = p.Sender.Flush(fctx)
}

// Default bounds of the wait after a shard that did not load.
const (
	seedBackoffMin = time.Second
	seedBackoffMax = 30 * time.Second
)

// nextBackoff doubles prev within [lo, hi].
func nextBackoff(prev, lo, hi time.Duration) time.Duration {
	return min(max(prev*2, lo), hi)
}

// seedLoop loads the shards control leases to this task, one at a time, at this task's
// share of the loading rate. A shard that fails is logged and tried again after a growing
// wait, so a broken cluster is neither retried in a tight loop nor silent.
func (p *Producer) seedLoop(ctx context.Context) {
	lo, hi := p.SeedBackoffMin, p.SeedBackoffMax
	if lo <= 0 {
		lo = seedBackoffMin
	}
	if hi <= 0 {
		hi = seedBackoffMax
	}
	p.initSeed()
	load := p.loadFn
	if load == nil {
		load = p.loadShard
	}
	backoff := time.Duration(0)
	for ctx.Err() == nil {
		shard, epoch, ok := p.nextShard()
		if !ok {
			if !sleep(ctx, time.Second) {
				return
			}
			continue
		}
		n, err := load(ctx, shard, epoch)
		if err == nil {
			p.mu.Lock()
			// Only if the load is still the one this shard was loaded for.
			if p.seedEpoch == epoch {
				p.seedDone[shard] = true
				p.seedRecs[shard] = n
			}
			p.mu.Unlock()
			slog.Info("Loaded a catalogue shard", "shard", shard, "records", n)
			backoff = 0
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, errLeaseLost) {
			// Control gave the shard away (or started another load): nothing is wrong, and
			// the next shard, if any, is not made to wait for it.
			slog.Info("A catalogue shard is no longer this task's", "shard", shard)
			backoff = 0
			continue
		}
		backoff = nextBackoff(backoff, lo, hi)
		slog.Warn("A catalogue shard did not load; it will be tried again", "shard", shard, "error", err, "retry_in", backoff.String())
		if !sleep(ctx, backoff) {
			return
		}
	}
}

// sleep waits d, and reports false if ctx ended first.
func sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// nextShard is a shard control leases to this task and has not finished in the current load.
// A plan with a different, non-zero SeedEpoch is another load of the catalogue: what this task
// had finished and counted belongs to the old one, so it is forgotten, and the shards are
// loaded again. Any different epoch counts, not only a larger one: control's epochs restart
// from its start time when it is restarted. Epoch 0 is no load at all (or a plan gone stale),
// and changes nothing.
func (p *Producer) nextShard() (shard, epoch int, ok bool) {
	pl := p.Ctl.Plan()
	p.mu.Lock()
	defer p.mu.Unlock()
	if pl.SeedEpoch == 0 {
		return 0, 0, false
	}
	if pl.SeedEpoch != p.seedEpoch {
		p.seedEpoch = pl.SeedEpoch
		p.seedDone, p.seedRecs = map[int]bool{}, map[int]int64{}
		// The old load's error count must not be reported under the new epoch (and counted in
		// its Errors).
		p.seedErrs.Store(0)
	}
	for _, s := range pl.SeedShards {
		if !p.seedDone[s] {
			return s, p.seedEpoch, true
		}
	}
	return 0, 0, false
}

// holdsShard says whether control still leases the shard of this load to this task.
func (p *Producer) holdsShard(shard, epoch int) bool {
	pl := p.Ctl.Plan()
	if pl.SeedEpoch != epoch {
		return false
	}
	for _, s := range pl.SeedShards {
		if s == shard {
			return true
		}
	}
	return false
}

// errLeaseLost is the end of a shard that control gave to another task or whose load was
// replaced by a new one.
var errLeaseLost = errors.New("control no longer leases the shard to this task")

// loadShard produces every position of the SKUs of one shard of load epoch and returns how
// many records it delivered. The error is nil only if every record was delivered. Failures are
// counted in the shard's own counter, which the heartbeat does not touch; what the heartbeat
// reports (the load's error count) is added only when the shard ends, and only if it is still
// the current load, so a replaced load's late failures do not land in the next one's.
//
// Two contexts, on purpose. The pacer stops on pacerCtx, cancelled when the last record has
// been queued. The records carry recCtx, which stays live until the flush has returned:
// cancelling it earlier would fail the records still buffered in the client, and those would
// not be delivered. recCtx is cancelled early only to abandon the shard (lease lost, the task
// stopping).
func (p *Producer) loadShard(ctx context.Context, shard, epoch int) (int64, error) {
	sku, j := shard, 0
	var shardOK, shardErrs, hbErrs atomic.Int64
	var lost atomic.Bool
	recCtx, abandon := context.WithCancel(ctx)
	defer abandon()
	pacerCtx, stopPacer := context.WithCancel(recCtx)
	defer stopPacer()
	defer func() {
		if n := hbErrs.Load(); n > 0 {
			p.mu.Lock()
			if p.seedEpoch == epoch {
				p.seedErrs.Add(n)
			}
			p.mu.Unlock()
		}
	}()
	// Check the lease once a second, for as long as the shard is being worked on: the flush
	// can take 30 s, during which the shard may be given to another task.
	leaseDone := make(chan struct{})
	defer close(leaseDone)
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-leaseDone:
				return
			case <-t.C:
				if !p.holdsShard(shard, epoch) {
					lost.Store(true)
					abandon()
					return
				}
			}
		}
	}()
	Pace(pacerCtx, func() float64 {
		// Nothing is produced for a shard that is no longer ours, not even until the next
		// lease check: two tasks would be loading it at twice the fixed rate.
		if lost.Load() || !p.holdsShard(shard, epoch) {
			return 0
		}
		return min(p.Ctl.Plan().SeedRate, SeedRecordsPerSec)
	}, func() {
		if sku >= catalog.SKUs {
			stopPacer() // not abandon: the buffered records must still be delivered
			return
		}
		loc := catalog.Location(p.Catalog.LocationAt(sku, j))
		// Seed quantities are the same every time, so loading twice changes nothing.
		p.send(recCtx, catalog.SKU(sku), loc, (sku+j)%(catalog.MaxQuantity+1), nil, &hbErrs, &shardOK, &shardErrs)
		if j++; j >= p.Catalog.Count(sku) {
			sku, j = sku+catalog.Shards, 0
		}
	})
	// The pacer ends only when the last record is queued, or the shard is abandoned.
	switch {
	case ctx.Err() != nil:
		return 0, ctx.Err()
	case lost.Load():
		return 0, errLeaseLost
	}
	fctx, fcancel := context.WithTimeout(ctx, 30*time.Second)
	defer fcancel()
	if err := p.Sender.Flush(fctx); err != nil {
		hbErrs.Add(1) // shown in the UI's error count, not only logged
		return 0, fmt.Errorf("could not flush the shard: %w", err)
	}
	if lost.Load() { // given away while the flush ran
		return 0, errLeaseLost
	}
	if n := shardErrs.Load(); n > 0 {
		return 0, fmt.Errorf("%d records of the shard were not delivered", n)
	}
	return shardOK.Load(), nil
}
