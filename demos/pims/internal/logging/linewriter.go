package logging

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
)

// maxLine caps buffered output so a tool that never prints a newline cannot
// grow memory without bound.
const maxLine = 64 << 10

// LineWriter is an io.Writer that logs each complete line written to it.
// It is used to stream subprocess output (terraform, docker compose) into
// slog, and from there to the UI.
type LineWriter struct {
	log *slog.Logger
	msg string

	mu  sync.Mutex
	buf bytes.Buffer
}

// NewLineWriter returns a writer that logs every line as msg with a
// "line" attribute.
func NewLineWriter(log *slog.Logger, msg string) *LineWriter {
	return &LineWriter{log: log, msg: msg}
}

// Write buffers p and logs any complete lines.
func (w *LineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for {
		line, err := w.buf.ReadString('\n')
		if err != nil {
			// Incomplete line: keep it until more output arrives, unless it
			// has grown past maxLine, in which case emit it as is.
			w.buf.Reset()
			if len(line) >= maxLine {
				w.emit(line)
			} else {
				w.buf.WriteString(line)
			}
			return len(p), nil
		}
		w.emit(line)
	}
}

// Flush logs any trailing partial line.
func (w *LineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.buf.Len() > 0 {
		w.emit(w.buf.String())
		w.buf.Reset()
	}
}

func (w *LineWriter) emit(line string) {
	line = strings.TrimRight(line, "\r\n")
	if strings.TrimSpace(line) == "" {
		return
	}
	w.log.Info(w.msg, "line", line)
}
