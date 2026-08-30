// Package transcript is the one transcript writer (spec §12.2): a JSONL file
// holding an agent run's verbatim stream lines, plus vincent's own
// `vincent.*` annotations.
//
// It was extracted from internal/taskrun when chats arrived (task 063): a
// chat turn produces exactly the same artifact as a step attempt, and a
// second copy of the size cap, the latching error and the append-and-report-
// position contract would have been two files that had to agree forever.
// internal/taskrun keeps its own filename convention and calls Open with the
// name it builds; internal/chatrun does the same with its own.
package transcript

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Writer is one attempt's transcript file (spec §12.2:
// {data_dir}/transcripts/{task_id}/{step_index}-{attempt}.jsonl, with a
// sub-step of a `parallel` group taking {step_index}-{step_id}-{attempt}).
// Agent
// stream lines are written verbatim so the file stays replayable; vincent's
// own annotations are namespaced `vincent.*` (phase 1 decision).
type Writer struct {
	path string
	mu   sync.Mutex
	f    *os.File
	// size is the byte length written so far. Every append returns the
	// position after it, which live-output chunks carry so a client can tell
	// exactly which lines its transcript fetch already covered (§13.3).
	size int64
	// max is the §12.3 per-attempt cap; 0 disables it. Past the cap writes
	// are dropped and exceeded latches, which the step executors poll to fail
	// the attempt (§18 transcript_limit).
	max      int64
	exceeded bool
	// err is the first write, encode or close failure. A transcript that
	// could not record what happened is not the lossless record §12.2
	// promises, and the step executors turn it into transcript_io_error
	// rather than let an attempt claim success over evidence that is not
	// there (§7.1, §18).
	err    error
	closed bool
}

// Open creates (truncating) the transcript file called name inside dir,
// making dir if it is missing. Callers own the naming convention: a step
// attempt's name encodes its index, iteration and attempt (§12.2), a chat
// turn's its sequence number (§5.5), and nothing parses either — the path is
// stored on the row.
func Open(dir, name string) (*Writer, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create transcript dir: %w", err)
	}
	path := filepath.Join(dir, name)
	//nolint:gosec // G304: path is built by the daemon from its own data dir and a caller-owned name
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create transcript: %w", err)
	}
	return &Writer{path: path, f: f}, nil
}

// Size is the byte length written so far. It is what a caller compares
// across a write to tell an accepted append from one the cap dropped.
func (t *Writer) Size() int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.size
}

// Path is the transcript's location, recorded on the StepRun.
func (t *Writer) Path() string { return t.path }

// SetMax installs the per-attempt cap (§12.3). Zero or negative disables it.
func (t *Writer) SetMax(limit int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.max = limit
}

// Exceeded reports that the cap was passed. It latches: once a transcript is
// over the limit the attempt is doomed, and un-latching on a later smaller
// write would let a run creep past the cap indefinitely.
func (t *Writer) Exceeded() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.exceeded
}

// Err reports the first write, encode or close failure, or nil.
//
// Like Exceeded it latches, and for a stronger reason: a line that failed to
// land is gone, and a later successful write does not put it back. Disk-full,
// permission and short-write faults all arrive here, and ENOSPC on a buffered
// filesystem usually arrives at Close and nowhere else — which is why Close
// feeds this latch too.
func (t *Writer) Err() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.err
}

// fail latches the first error. The caller holds t.mu.
func (t *Writer) fail(err error) {
	if t.err == nil {
		t.err = err
	}
}

// Raw appends a verbatim stream line and reports the file offset just past
// it. A short or failed write still advances by what landed, so the offset
// never claims more of the file than exists.
//
// The line that trips the cap is written whole rather than truncated: a
// transcript is replayed by a JSONL parser (§13.2), and half a line would
// turn a size failure into a parse failure for every reader afterwards. The
// overshoot is bounded by one line.
func (t *Writer) Raw(line []byte) int64 { return t.write(line, false) }

// write appends a line. force bypasses the cap, which exactly one caller
// needs: the annotation recording *why* the transcript stopped. Suppressing
// that line would leave a reader with a file that simply ends.
func (t *Writer) write(line []byte, force bool) int64 {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.exceeded && !force {
		return t.size
	}
	want := len(line) + 1
	n, err := t.f.Write(append(line, '\n'))
	t.size += int64(n)
	switch {
	case err != nil:
		t.fail(err)
	case n < want:
		// io.Writer's contract says a short write reports an error, and
		// os.File honours it; checking anyway costs a comparison and is the
		// difference between trusting the contract and having checked.
		t.fail(io.ErrShortWrite)
	}
	if t.max > 0 && t.size > t.max {
		t.exceeded = true
	}
	return t.size
}

// Note appends a namespaced vincent annotation and reports the offset past
// it.
func (t *Writer) Note(kind string, fields map[string]any) int64 {
	return t.note(kind, fields, false)
}

// NoteOverLimit is Note for the one annotation the cap must not suppress:
// the record of why the transcript stops.
func (t *Writer) NoteOverLimit(kind string, fields map[string]any) int64 {
	return t.note(kind, fields, true)
}

func (t *Writer) note(kind string, fields map[string]any, force bool) int64 {
	entry := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		entry[k] = v
	}
	entry["type"] = "vincent." + kind
	entry["ts"] = time.Now().UTC().Format(time.RFC3339)
	b, err := json.Marshal(entry)
	if err != nil {
		t.mu.Lock()
		defer t.mu.Unlock()
		t.fail(err)
		return t.size
	}
	return t.write(b, force)
}

// OutputFields is one record of process output, tagged with its stream and
// the phase that produced it (the step's own command, or its check).
//
// partial marks a piece of a line too long to record whole: the next output
// record on the same phase and stream continues it (#139, §12.2). It is
// absent rather than false on an ordinary line, so the common record keeps
// the shape every reader already knows.
//
// One map serves the transcript record and the §13.3 live chunk, which is
// what keeps the durable and live shapes from drifting.
// OutputFields renders the fields of a `vincent.output` note.
func OutputFields(phase, stream, text string, partial bool) map[string]any {
	f := map[string]any{"phase": phase, "stream": stream, "text": text}
	if partial {
		f["partial"] = true
	}
	return f
}

// Close closes the file, latching a close failure into Err.
//
// It is idempotent, because runAttempt closes explicitly — before it judges
// the attempt, so a close-time ENOSPC still reaches the outcome — while the
// deferred close stays as the guard for every early return.
func (t *Writer) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.closed {
		return
	}
	t.closed = true
	if err := t.f.Close(); err != nil {
		t.fail(err)
	}
}
