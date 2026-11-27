// Package worker is the producer and query roles: tasks that poll the control service for
// what to do and how fast, and do it.
package worker

import (
	"context"
	"time"
)

// tick is how often the pacer wakes. Rates above 100 per second fire several times per
// tick.
const tick = 10 * time.Millisecond

// Pace calls fire at rate() per second until ctx ends. It is open loop: if fire is slow
// the pacer does not wait for it (a slow service must not slow the load down), and credit
// is capped at one second of rate plus one call, so an idle spell does not become a burst.
// The extra call matters below 1 per second: a cap of r would be under 1 and nothing would
// ever fire. A rate below 1 per second (a total split over more tasks than it has calls)
// fires once every 1/r seconds.
func Pace(ctx context.Context, rate func() float64, fire func()) {
	t := time.NewTicker(tick)
	defer t.Stop()
	last := time.Now()
	var credit float64
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			r := rate()
			dt := now.Sub(last).Seconds()
			last = now
			if r <= 0 {
				credit = 0
				continue
			}
			credit = min(credit+r*dt, r+1)
			for credit >= 1 {
				credit--
				fire()
			}
		}
	}
}
