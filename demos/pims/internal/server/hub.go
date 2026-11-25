package server

import (
	"encoding/json"
	"sync"
)

// event is one server-sent event.
type event struct {
	name string
	data []byte
}

// hub fans events out to the open browser tabs. A tab that cannot keep up loses
// events rather than slowing the others: the UI reloads its state on reconnect.
type hub struct {
	mu     sync.Mutex
	subs   map[chan event]struct{}
	closed bool
}

func newHub() *hub { return &hub{subs: map[chan event]struct{}{}} }

// subscribe returns a channel of events and a function that ends the subscription.
func (h *hub) subscribe() (<-chan event, func()) {
	ch := make(chan event, 128)
	h.mu.Lock()
	if h.closed {
		close(ch)
		h.mu.Unlock()
		return ch, func() {}
	}
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
	}
}

func (h *hub) publish(name string, v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- event{name, data}:
		default: // a slow tab
		}
	}
}

// close ends every subscription.
func (h *hub) close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
}
