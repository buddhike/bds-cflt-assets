// Package logging wires slog so that every record is written to the
// process log and also forwarded to the PIMS UI activity log.
package logging

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// Entry is a log record flattened for display in the UI.
type Entry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Message string    `json:"message"`
	Attrs   string    `json:"attrs"`
}

// Sink receives every record at or above the handler's level. It must not
// block, since it is called on the logging goroutine.
type Sink func(Entry)

// TeeHandler forwards records to a base handler and to a Sink.
type TeeHandler struct {
	base   slog.Handler
	sink   Sink
	prefix string      // dotted group prefix applied to record attrs
	attrs  []slog.Attr // attrs added via WithAttrs, already prefixed
}

// NewTeeHandler returns a handler that writes to base and to sink.
func NewTeeHandler(base slog.Handler, sink Sink) *TeeHandler {
	return &TeeHandler{base: base, sink: sink}
}

// Enabled reports whether the base handler accepts the level.
func (h *TeeHandler) Enabled(ctx context.Context, l slog.Level) bool {
	return h.base.Enabled(ctx, l)
}

// Handle writes r to the base handler and forwards it to the sink.
func (h *TeeHandler) Handle(ctx context.Context, r slog.Record) error {
	if h.sink != nil {
		var parts []string
		for _, a := range h.attrs {
			parts = appendAttr(parts, "", a)
		}
		r.Attrs(func(a slog.Attr) bool {
			parts = appendAttr(parts, h.prefix, a)
			return true
		})
		h.sink(Entry{
			Time:    r.Time,
			Level:   r.Level.String(),
			Message: r.Message,
			Attrs:   strings.Join(parts, " "),
		})
	}
	return h.base.Handle(ctx, r)
}

// WithAttrs returns a handler that includes attrs on every record.
func (h *TeeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.base = h.base.WithAttrs(attrs)
	next.attrs = append([]slog.Attr(nil), h.attrs...)
	for _, a := range attrs {
		if h.prefix != "" {
			a.Key = h.prefix + a.Key
		}
		next.attrs = append(next.attrs, a)
	}
	return &next
}

// WithGroup returns a handler that nests subsequent attrs under name.
func (h *TeeHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}
	next := *h
	next.base = h.base.WithGroup(name)
	next.prefix = h.prefix + name + "."
	return &next
}

func appendAttr(parts []string, prefix string, a slog.Attr) []string {
	a.Value = a.Value.Resolve()
	if a.Equal(slog.Attr{}) {
		return parts
	}
	if a.Value.Kind() == slog.KindGroup {
		p := prefix
		if a.Key != "" {
			p = prefix + a.Key + "."
		}
		for _, ga := range a.Value.Group() {
			parts = appendAttr(parts, p, ga)
		}
		return parts
	}
	return append(parts, fmt.Sprintf("%s%s=%v", prefix, a.Key, a.Value.Any()))
}
