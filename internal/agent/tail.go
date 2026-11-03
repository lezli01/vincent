package agent

import (
	"strings"
	"sync"
)

// TailWriter keeps the last max bytes written to it. Every adapter points a
// CLI's stderr at one: a failed run's error message is built from that tail,
// and bounding it is what stops a chatty CLI from growing the daemon's memory
// for as long as the step runs. It never fails a write, so the process's
// stderr pipe is always drained.
type TailWriter struct {
	mu  sync.Mutex
	buf []byte
	max int
}

// NewTailWriter returns a TailWriter that retains the last limit bytes.
func NewTailWriter(limit int) *TailWriter { return &TailWriter{max: limit} }

// Write appends p and trims the buffer to its last max bytes. It always
// reports len(p), nil.
func (w *TailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.max {
		w.buf = w.buf[len(w.buf)-w.max:]
	}
	return len(p), nil
}

// String returns the retained tail with surrounding whitespace trimmed.
func (w *TailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return strings.TrimSpace(string(w.buf))
}
