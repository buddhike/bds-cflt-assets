package logging

import (
	"bytes"
	"log/slog"
	"testing"
)

func TestLineWriterSplitsAndFlushes(t *testing.T) {
	var got []string
	h := NewTeeHandler(slog.NewTextHandler(&bytes.Buffer{}, nil), func(e Entry) { got = append(got, e.Attrs) })
	w := NewLineWriter(slog.New(h), "Terraform output")

	_, _ = w.Write([]byte("first\nsec"))
	_, _ = w.Write([]byte("ond\r\n\n"))
	_, _ = w.Write([]byte("partial"))
	w.Flush()

	want := []string{"line=first", "line=second", "line=partial"}
	if len(got) != len(want) {
		t.Fatalf("got %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}

func TestLineWriterEmitsOversizedPartialLine(t *testing.T) {
	var got []Entry
	h := NewTeeHandler(slog.NewTextHandler(&bytes.Buffer{}, nil), func(e Entry) { got = append(got, e) })
	w := NewLineWriter(slog.New(h), "Output")

	_, _ = w.Write(bytes.Repeat([]byte("x"), maxLine))
	if len(got) != 1 {
		t.Fatalf("oversized partial line should be emitted, got %d entries", len(got))
	}
	if w.buf.Len() != 0 {
		t.Fatalf("buffer not cleared, len = %d", w.buf.Len())
	}
}
