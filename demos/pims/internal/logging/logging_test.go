package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"
)

type redacted struct{}

func (redacted) LogValue() slog.Value { return slog.StringValue("[REDACTED]") }

func TestTeeHandlerForwardsToSinkAndBase(t *testing.T) {
	var buf bytes.Buffer
	var got []Entry
	h := NewTeeHandler(slog.NewTextHandler(&buf, nil), func(e Entry) { got = append(got, e) })

	log := slog.New(h).With("component", "terraform").WithGroup("tf")
	log.Info("Apply finished", "resources", 3, "secret", redacted{})

	if len(got) != 1 {
		t.Fatalf("sink received %d entries, want 1", len(got))
	}
	e := got[0]
	if e.Message != "Apply finished" || e.Level != "INFO" {
		t.Fatalf("unexpected entry: %+v", e)
	}
	want := "component=terraform tf.resources=3 tf.secret=[REDACTED]"
	if e.Attrs != want {
		t.Fatalf("Attrs = %q, want %q", e.Attrs, want)
	}
	if !strings.Contains(buf.String(), "Apply finished") {
		t.Fatalf("base handler did not receive record: %q", buf.String())
	}
}

func TestTeeHandlerGroupThenAttrsAndNestedGroups(t *testing.T) {
	var got []Entry
	h := NewTeeHandler(slog.NewTextHandler(&bytes.Buffer{}, nil), func(e Entry) { got = append(got, e) })

	slog.New(h).WithGroup("sim").With("rate", 5).Info("Tick", slog.Group("event", "kind", "visit"))

	want := "sim.rate=5 sim.event.kind=visit"
	if len(got) != 1 || got[0].Attrs != want {
		t.Fatalf("got %+v, want Attrs %q", got, want)
	}
}

func TestTeeHandlerNilSink(t *testing.T) {
	var buf bytes.Buffer
	slog.New(NewTeeHandler(slog.NewTextHandler(&buf, nil), nil)).Info("Still logged")
	if !strings.Contains(buf.String(), "Still logged") {
		t.Fatalf("base handler did not receive record: %q", buf.String())
	}
}

func TestTeeHandlerRespectsLevel(t *testing.T) {
	var got []Entry
	base := slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelWarn})
	log := slog.New(NewTeeHandler(base, func(e Entry) { got = append(got, e) }))

	log.Info("Ignored")
	log.Warn("Kept")

	if len(got) != 1 || got[0].Message != "Kept" {
		t.Fatalf("got %+v, want only the warning", got)
	}
}
