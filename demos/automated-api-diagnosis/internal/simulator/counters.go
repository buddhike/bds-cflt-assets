package simulator

import (
	"strconv"
	"sync"
)

// apiCounters accumulates one API's activity between stats broadcasts.
// snapshotAndReset both reads and clears it, so each broadcast reports
// only what happened in that window (not a running total).
type apiCounters struct {
	mu           sync.Mutex
	total        int64
	statusCounts map[string]int64
	lastDuration int64
}

func (c *apiCounters) record(status, durationMs int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.total++
	if c.statusCounts == nil {
		c.statusCounts = make(map[string]int64)
	}
	c.statusCounts[strconv.Itoa(status)]++
	c.lastDuration = int64(durationMs)
}

type statsSnapshot struct {
	total        int64
	statusCounts map[string]int64
	lastDuration int64
}

func (c *apiCounters) snapshotAndReset() statsSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	snap := statsSnapshot{total: c.total, statusCounts: c.statusCounts, lastDuration: c.lastDuration}
	c.total = 0
	c.statusCounts = nil
	return snap
}
