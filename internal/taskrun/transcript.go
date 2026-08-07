package taskrun

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// transcript is one attempt's transcript file (spec §12.2:
// {data_dir}/transcripts/{task_id}/{step_index}-{attempt}.jsonl). Agent
// stream lines are written verbatim so the file stays replayable; vincent's
// own annotations are namespaced `vincent.*` (phase 1 decision).
type transcript struct {
	path string
	mu   sync.Mutex
	f    *os.File
}

// openTranscript creates the transcript file for one attempt.
func openTranscript(dataDir string, taskID int64, stepIndex, attempt int) (*transcript, error) {
	dir := filepath.Join(dataDir, "transcripts", strconv.FormatInt(taskID, 10))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create transcript dir: %w", err)
	}
	path := filepath.Join(dir, fmt.Sprintf("%d-%d.jsonl", stepIndex, attempt))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create transcript: %w", err)
	}
	return &transcript{path: path, f: f}, nil
}

// Path is the transcript's location, recorded on the StepRun.
func (t *transcript) Path() string { return t.path }

// Raw appends a verbatim stream line.
func (t *transcript) Raw(line []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	_, _ = t.f.Write(append(line, '\n'))
}

// Note appends a namespaced vincent annotation.
func (t *transcript) Note(kind string, fields map[string]any) {
	entry := make(map[string]any, len(fields)+2)
	for k, v := range fields {
		entry[k] = v
	}
	entry["type"] = "vincent." + kind
	entry["ts"] = time.Now().UTC().Format(time.RFC3339)
	b, err := json.Marshal(entry)
	if err != nil {
		return
	}
	t.Raw(b)
}

// Output appends one line of process output, tagged with its stream and the
// phase that produced it (the step's own command, or its check).
func (t *transcript) Output(phase, stream, text string) {
	t.Note("output", map[string]any{"phase": phase, "stream": stream, "text": text})
}

// Close closes the file.
func (t *transcript) Close() {
	t.mu.Lock()
	defer t.mu.Unlock()
	_ = t.f.Close()
}

// outputTail keeps the last n lines of a process's output for the step's
// result summary (§8.4 `.Steps`) and for the retry failure block (§8.4).
type outputTail struct {
	limit int
	lines []string
}

func newOutputTail(limit int) *outputTail { return &outputTail{limit: limit} }

func (o *outputTail) add(line string) {
	o.lines = append(o.lines, line)
	if len(o.lines) > o.limit {
		o.lines = o.lines[len(o.lines)-o.limit:]
	}
}

func (o *outputTail) String() string { return strings.Join(o.lines, "\n") }
