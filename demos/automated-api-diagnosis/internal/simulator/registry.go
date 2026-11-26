package simulator

import "sync"

// OutcomeBoard is the shared state each API's last outcome is published to,
// so dependent APIs can read it when deciding whether to cascade a failure.
type OutcomeBoard struct {
	mu   sync.RWMutex
	data map[string]Outcome
}

func newOutcomeBoard() *OutcomeBoard {
	return &OutcomeBoard{data: make(map[string]Outcome)}
}

func (b *OutcomeBoard) Set(name string, o Outcome) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data[name] = o
}

// Get returns the last known outcome for each of the given names, skipping
// any that have never produced yet.
func (b *OutcomeBoard) Get(names []string) []Outcome {
	b.mu.RLock()
	defer b.mu.RUnlock()
	out := make([]Outcome, 0, len(names))
	for _, n := range names {
		if o, ok := b.data[n]; ok {
			out = append(out, o)
		}
	}
	return out
}

// DefaultAPIs is the fixed set of 10 simulated APIs, loosely modeled on the
// example log's naming convention. Not user-configurable in this MVP.
func DefaultAPIs() []APIConfig {
	return []APIConfig{
		{Name: "API_GATEWAY_FEATURE_EVALUATION_API_V1", Path: "/api/v4/features/simpleresponse", Rate: 8, LatencyMs: 15},
		{Name: "PAYMENTS_API_V1", Path: "/api/v1/payments", Rate: 6, LatencyMs: 120},
		{Name: "BOOKING_API_V1", Path: "/api/v1/bookings", Rate: 7, LatencyMs: 200},
		{Name: "CHECKIN_API_V1", Path: "/api/v1/checkin", Rate: 5, LatencyMs: 90},
		{Name: "LOYALTY_API_V1", Path: "/api/v1/loyalty/points", Rate: 9, LatencyMs: 60},
		{Name: "FLIGHT_STATUS_API_V1", Path: "/api/v1/flights/status", Rate: 35, LatencyMs: 40},
		{Name: "BAGGAGE_API_V1", Path: "/api/v1/baggage/track", Rate: 6, LatencyMs: 100},
		{Name: "NOTIFICATIONS_API_V1", Path: "/api/v1/notifications", Rate: 30, LatencyMs: 30},
		{Name: "SEAT_MAP_API_V1", Path: "/api/v1/seatmap", Rate: 8, LatencyMs: 50},
		{Name: "CUSTOMER_PROFILE_API_V1", Path: "/api/v1/customers/profile", Rate: 7, LatencyMs: 70},
	}
}
