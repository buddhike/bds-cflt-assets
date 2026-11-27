// Package protocol is what the control service and the worker tasks say to each other.
// A worker polls once a second; control answers with whether to run and the worker's
// share of the rate.
package protocol

// Scenario names. They are also the CloudWatch dimension values.
const (
	ScenarioBySKU       = "by_sku"
	ScenarioByLocations = "by_locations"
)

// Scenarios in display order.
var Scenarios = []string{ScenarioBySKU, ScenarioByLocations}

// QueryStats are the counts since the last heartbeat for one scenario.
type QueryStats struct {
	OK      int64 `json:"ok"`
	Errors  int64 `json:"errors"`
	Dropped int64 `json:"dropped"`
	Rows    int64 `json:"rows"`
}

// UserAgent is sent on every request the demo makes to Confluent and to control.
const UserAgent = "lightning-inventory-demo/1"

// Bounds on what a heartbeat may say, so a faulty or hostile worker cannot skew the charts
// or the shard bookkeeping. A real worker's second is far below them.
const (
	// MaxCount bounds every per-heartbeat count.
	MaxCount = 10_000_000
	// MaxRows bounds the rows counted in one heartbeat (2000 rows x 1000 queries x seconds).
	MaxRows = 10_000_000_000
)

// Heartbeat is a worker's report. Counters are deltas since the previous heartbeat.
type Heartbeat struct {
	ID   string `json:"id"`
	Role string `json:"role"`

	Produced      int64 `json:"produced,omitempty"`
	ProduceErrors int64 `json:"produce_errors,omitempty"`
	SeedErrors    int64 `json:"seed_errors,omitempty"`
	// SeedEpoch is the load (Plan.SeedEpoch) that SeedErrors, SeedDone and SeedRecords belong to.
	// Control ignores them when it is not the current one, so a report from an earlier load
	// cannot mark a shard of the next one done.
	SeedEpoch int `json:"seed_epoch,omitempty"`
	// SeedDone lists shards the worker has finished loading in SeedEpoch, and SeedRecords
	// says how many records each held. Control adds a shard's records once, when it first
	// accepts the shard as done from its owner, so a resend or a shard handed to another
	// worker cannot count twice.
	SeedDone    []int         `json:"seed_done,omitempty"`
	SeedRecords map[int]int64 `json:"seed_records,omitempty"`

	Queries map[string]QueryStats `json:"queries,omitempty"`
}

// Merge adds o's counters to h, as when a heartbeat could not be delivered and its counts
// ride on the next one. SeedDone is a set, not a delta, so it is the union.
func (h *Heartbeat) Merge(o Heartbeat) {
	h.Produced += o.Produced
	h.ProduceErrors += o.ProduceErrors
	// The seed fields belong to one load. If the two differ, the newer load's replace the
	// older one's: they are not additive, and control would ignore the old ones anyway.
	switch {
	case o.SeedEpoch > h.SeedEpoch:
		h.SeedEpoch, h.SeedErrors, h.SeedDone, h.SeedRecords = o.SeedEpoch, 0, nil, nil
	case o.SeedEpoch < h.SeedEpoch:
		o.SeedErrors, o.SeedDone, o.SeedRecords = 0, nil, nil
	}
	h.SeedErrors += o.SeedErrors
	for sh, n := range o.SeedRecords {
		if h.SeedRecords == nil {
			h.SeedRecords = map[int]int64{}
		}
		h.SeedRecords[sh] = n
	}
	seen := map[int]bool{}
	for _, s := range h.SeedDone {
		seen[s] = true
	}
	for _, s := range o.SeedDone {
		if !seen[s] {
			seen[s] = true
			h.SeedDone = append(h.SeedDone, s)
		}
	}
	for sc, st := range o.Queries {
		if h.Queries == nil {
			h.Queries = map[string]QueryStats{}
		}
		cur := h.Queries[sc]
		cur.OK += st.OK
		cur.Errors += st.Errors
		cur.Dropped += st.Dropped
		cur.Rows += st.Rows
		h.Queries[sc] = cur
	}
}

// Valid reports whether every count is within bounds. Unknown scenario names are ignored by
// control, but there may not be many.
func (h Heartbeat) Valid() bool {
	for _, n := range []int64{h.Produced, h.ProduceErrors, h.SeedErrors} {
		if n < 0 || n > MaxCount {
			return false
		}
	}
	for _, n := range h.SeedRecords {
		if n < 0 || n > MaxCount {
			return false
		}
	}
	if h.SeedEpoch < 0 || len(h.SeedDone) > 64 || len(h.SeedRecords) > 64 || len(h.Queries) > 8 {
		return false
	}
	for _, st := range h.Queries {
		for _, n := range []int64{st.OK, st.Errors, st.Dropped} {
			if n < 0 || n > MaxCount {
				return false
			}
		}
		if st.Rows < 0 || st.Rows > MaxRows {
			return false
		}
	}
	return true
}

// Plan is control's answer.
type Plan struct {
	// Run says whether this worker's load should run.
	Run bool `json:"run"`
	// Rate is this worker's share, per second, of the rate the operator set.
	Rate float64 `json:"rate"`
	// Workers is how many workers of this role control counts.
	Workers int `json:"workers"`

	// SeedEpoch counts the catalogue loads the operator has started (1 for the first). A
	// producer that sees a new one forgets which shards it had finished.
	SeedEpoch int `json:"seed_epoch,omitempty"`

	// SeedShards are the shards this producer is to load, and SeedRate its share of
	// the loading rate, in records per second.
	SeedShards []int   `json:"seed_shards,omitempty"`
	SeedRate   float64 `json:"seed_rate,omitempty"`
}

// Header names for the worker token.
const (
	HeartbeatPath = "/internal/heartbeat"
)
