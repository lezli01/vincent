package trigger

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/procx"
)

// The `type: command` source, per task 096 appendix A.

// CursorEnv is the variable the previous watermark is handed back in. It is
// empty on an unseeded trigger's poll.
const CursorEnv = "VINCENT_TRIGGER_CURSOR"

// DefaultCommandTimeout bounds one poll command, process tree and all.
//
// Fixed, not configured, in decision 13's style — and longer than notify's
// ten seconds because the job is different: a poll is typically a search
// against a vendor's REST API over the user's network, where a slow page is
// ordinary, while a notifier is a local toast. A minute still kills a hung
// command long before anyone wonders why a trigger went quiet, and the poll
// status reads failing the moment it does.
const DefaultCommandTimeout = time.Minute

// maxStdout bounds what one poll may print. A source's page of events is
// kilobytes; the bound exists so a runaway command cannot grow the daemon's
// heap without limit. Over it, the poll fails rather than parsing a
// truncated line as if it were the last one.
const maxStdout = 8 << 20

// stderrTail is how much of a failed command's stderr its error carries.
const stderrTail = 2048

// Event is one NDJSON line: the whole object, as it reaches `.Event`.
type Event map[string]any

// ID is the event's reserved `id`, "" when it has none.
func (e Event) ID() string {
	s, _ := e["id"].(string)
	return s
}

// PollResult is what one successful run of a source produced.
type PollResult struct {
	Events []Event
	// Cursor is the watermark line's value, nil when the command printed
	// none — which leaves the stored cursor where it was.
	Cursor *string
	// Refused counts lines that were not events: not a JSON object, or an
	// object without a string `id`. Each was logged.
	Refused int
}

// PollError is a poll that failed: the command could not start, exited
// non-zero, timed out or printed too much. The cursor does not advance.
type PollError struct {
	Reason string
	Stderr string
}

func (e *PollError) Error() string {
	if e.Stderr == "" {
		return e.Reason
	}
	return e.Reason + ": " + e.Stderr
}

// runCommand executes argv directly — never through a shell — with the
// cursor in CursorEnv, and parses its stdout.
//
// procx.Start is what gives this NoWindow on Windows and a killable process
// tree everywhere, exactly as notify's children get (task 046): the daemon is
// normally console-less, and a poll script that shells out to curl and jq
// leaves grandchildren a plain Process.Kill would orphan.
func runCommand(ctx context.Context, argv []string, cursor string, env []string,
	timeout time.Duration, log *slog.Logger,
) (*PollResult, error) {
	// G204: the argv is the trigger file's, which belongs to the invoking user
	// and is owner-only (decision 20); running it is the feature (§16).
	cmd := exec.Command(argv[0], argv[1:]...) //nolint:gosec // G204: see above
	cmd.Env = withCursor(env, cursor)
	var stdout capBuffer
	stdout.limit = maxStdout
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &lockedWriter{w: &stderr}
	// A grandchild that inherited stdout and outlives the kill would hold
	// the pipe open and park Wait; the tree kill should prevent that, and
	// this is the bound if it does not.
	cmd.WaitDelay = 5 * time.Second

	proc, err := procx.Start(cmd)
	if err != nil {
		return nil, &PollError{Reason: fmt.Sprintf("start %s: %v", argv[0], err)}
	}
	defer proc.Release()

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	var waitErr error
	select {
	case waitErr = <-done:
	case <-timer.C:
		_ = proc.Kill()
		<-done
		logStderr(log, stderr.String())
		return nil, &PollError{Reason: "killed after " + timeout.String() + ": poll command did not exit"}
	case <-ctx.Done():
		_ = proc.Kill()
		<-done
		return nil, ctx.Err()
	}
	logStderr(log, stderr.String())
	if waitErr != nil {
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			return nil, &PollError{Reason: "exit status " + strconv.Itoa(exitErr.ExitCode()), Stderr: tail(stderr.String())}
		}
		return nil, &PollError{Reason: waitErr.Error()}
	}
	if stdout.over {
		return nil, &PollError{Reason: fmt.Sprintf("poll command printed more than %d bytes", maxStdout)}
	}
	return parseOutput(stdout.buf.Bytes(), log), nil
}

// parseOutput reads NDJSON: one event per line, blank lines skipped, and an
// optional last line `{"cursor": "..."}` that is the new watermark.
func parseOutput(out []byte, log *slog.Logger) *PollResult {
	var lines [][]byte
	sc := bufio.NewScanner(bytes.NewReader(out))
	sc.Buffer(make([]byte, 0, 64<<10), maxStdout)
	for sc.Scan() {
		if l := bytes.TrimSpace(sc.Bytes()); len(l) > 0 {
			lines = append(lines, append([]byte(nil), l...))
		}
	}
	res := &PollResult{}
	for i, l := range lines {
		var obj map[string]any
		if err := json.Unmarshal(l, &obj); err != nil || obj == nil {
			res.Refused++
			log.Warn("trigger event refused: not a JSON object", "line", i+1)
			continue
		}
		if c, ok := obj["cursor"]; ok && len(obj) == 1 && i == len(lines)-1 {
			if s, ok := c.(string); ok {
				res.Cursor = &s
				continue
			}
			res.Refused++
			log.Warn("trigger cursor refused: not a string", "line", i+1)
			continue
		}
		if Event(obj).ID() == "" {
			res.Refused++
			log.Warn("trigger event refused: no string id", "line", i+1)
			continue
		}
		res.Events = append(res.Events, Event(obj))
	}
	return res
}

// withCursor is env with CursorEnv set to cursor, replacing any inherited
// value — the daemon's own environment must not leak a stale watermark.
func withCursor(env []string, cursor string) []string {
	if env == nil {
		env = os.Environ()
	}
	out := make([]string, 0, len(env)+1)
	for _, kv := range env {
		if k, _, _ := strings.Cut(kv, "="); strings.EqualFold(k, CursorEnv) {
			continue
		}
		out = append(out, kv)
	}
	return append(out, CursorEnv+"="+cursor)
}

// logStderr sends what the command wrote to stderr to the daemon log, one
// record per line (appendix A: "stderr is captured into the daemon log").
func logStderr(log *slog.Logger, s string) {
	for _, line := range strings.Split(strings.TrimSpace(s), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			log.Info("trigger command stderr", "line", line)
		}
	}
}

func tail(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > stderrTail {
		s = s[len(s)-stderrTail:]
	}
	return s
}

// capBuffer keeps at most limit bytes and records that it dropped more.
type capBuffer struct {
	mu    sync.Mutex
	buf   bytes.Buffer
	limit int
	over  bool
}

func (b *capBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if room := b.limit - b.buf.Len(); room < len(p) {
		if room > 0 {
			b.buf.Write(p[:room])
		}
		b.over = true
		return len(p), nil
	}
	return b.buf.Write(p) //nolint:wrapcheck // bytes.Buffer.Write never errors
}

// lockedWriter bounds stderr and serializes writes: exec copies stderr on
// its own goroutine.
type lockedWriter struct {
	mu sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.w.Len() < maxStdout {
		l.w.Write(p)
	}
	return len(p), nil
}
