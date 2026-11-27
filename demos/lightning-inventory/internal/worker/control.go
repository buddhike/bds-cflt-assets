package worker

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"lightninginv/internal/protocol"
)

// PollEvery is how often a worker reports to control.
const PollEvery = time.Second

// StaleAfter is how long a worker keeps running on the last plan when control does not
// answer. After that it stops: a load nobody can stop is not allowed. It is shorter than the
// 5 s control waits before it stops counting a silent worker (server.aliveFor), so a worker
// cut off from control has stopped before control hands its share to the others and the total
// never exceeds the operator's rate. (The other way round, a worker that has just joined is
// counted from its first heartbeat while the others still run on the share they were last
// given: for up to about a second after a task starts or stops the total can be above the
// rate. The bounds in this package still hold per task.)
const StaleAfter = 3 * time.Second

// holdFor is how long the counts of heartbeats control did not receive are kept, to be sent
// with the next one. Control's own window is 30 s, so older counts would only land in the
// wrong second.
const holdFor = 30 * time.Second

// PollTimeout is how long one heartbeat may take. A heartbeat that reaches control is
// answered, as far as this worker knows, within it, so the worker's last good poll is at
// most PollTimeout after the moment control last heard from it. The worker therefore stops by
// StaleAfter + PollTimeout after that moment, which must be before control's aliveFor (the
// server's test checks it).
const PollTimeout = time.Second

// Control is a worker's link to the control service.
type Control struct {
	URL   string
	Token string
	HTTP  *http.Client
	ID    string
	// Every is how often Loop polls; PollEvery when zero.
	Every time.Duration
	// Hold is how long the counts of undelivered heartbeats are kept; holdFor when zero.
	Hold time.Duration

	// latest is the last plan and when it arrived, together (one load sees both). The time
	// keeps its monotonic reading, so a step of the wall clock neither makes a fresh plan stale
	// nor keeps a stale one.
	latest atomic.Pointer[stamped]
}

type stamped struct {
	plan protocol.Plan
	at   time.Time
}

// setPlan stores a plan received at at.
func (c *Control) setPlan(p protocol.Plan, at time.Time) { c.latest.Store(&stamped{plan: p, at: at}) }

// NewControl returns a link with a fresh worker id.
func NewControl(url, token string) *Control {
	return &Control{URL: strings.TrimRight(url, "/"), Token: token, HTTP: &http.Client{Timeout: PollTimeout}, ID: newID()}
}

// newID is 6 random bytes in hex. If the system has no randomness (it always has) the clock
// stands in: an id only has to differ between a few tasks.
func newID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		slog.Warn("Could not read random bytes for the worker id; using the clock", "error", err)
		n := uint64(time.Now().UnixNano())
		for i := range b {
			b[i] = byte(n >> (8 * i))
		}
	}
	return hex.EncodeToString(b)
}

// Plan is the latest plan; the zero plan (do nothing) when control has not been heard
// from lately.
func (c *Control) Plan() protocol.Plan {
	l := c.latest.Load()
	if l == nil || time.Since(l.at) > StaleAfter {
		return protocol.Plan{}
	}
	return l.plan
}

// Poll sends one heartbeat and stores the plan it gets back.
func (c *Control) Poll(ctx context.Context, hb protocol.Heartbeat) error {
	hb.ID = c.ID
	body, err := json.Marshal(hb)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL+protocol.HeartbeatPath, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("User-Agent", protocol.UserAgent)
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return fmt.Errorf("control answered %d", resp.StatusCode)
	}
	var p protocol.Plan
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&p); err != nil {
		return err
	}
	c.setPlan(p, time.Now())
	return nil
}

// Loop polls every Every (PollEvery) with whatever collect returns, until ctx ends. The
// counts of a heartbeat that did not get through are kept and sent with the next one (for up
// to holdFor), so a failed poll does not lose them. A failure is logged when it starts and
// again when its cause changes, and the recovery is logged once.
func (c *Control) Loop(ctx context.Context, collect func() protocol.Heartbeat) {
	every := c.Every
	if every <= 0 {
		every = PollEvery
	}
	t := time.NewTicker(every)
	defer t.Stop()
	hold := c.Hold
	if hold <= 0 {
		hold = holdFor
	}
	var pending *protocol.Heartbeat
	var failingSince time.Time
	var cause string
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if ctx.Err() != nil {
			return // a tick and the end can be ready together: do not collect counts nobody will receive
		}
		hb := collect()
		if pending != nil {
			if time.Since(failingSince) > hold {
				pending = nil // too old to be worth sending
			} else {
				hb.Merge(*pending)
			}
		}
		if err := c.Poll(ctx, hb); err != nil {
			if ctx.Err() != nil {
				return
			}
			if pending == nil {
				failingSince = time.Now()
			}
			pending = &hb
			if msg := err.Error(); msg != cause {
				slog.Warn("Could not reach the control service", "error", err)
				cause = msg
			}
			continue
		}
		if pending != nil {
			slog.Info("Reached the control service again")
		}
		pending, cause = nil, ""
	}
}
