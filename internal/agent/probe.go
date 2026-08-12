package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/procx"
)

// stderrLimit caps how much of a failed probe's stderr reaches the error
// message. A probe that answers with a stack trace still has to fit in one log
// record; the transcript is not the place a probe's diagnosis belongs.
const stderrLimit = 200

// Probe runs one short-lived probe subprocess — a `--version`, a `--help`, a
// `status`, a `models` call — and returns its stdout and stderr separately.
// Every adapter's §9.5/§9.6 probes go through here, for two reasons that are
// about correctness rather than about sharing code (T4.21, T4.22).
//
// It passes procx.NoWindow, so a probe cannot put a window on the user's
// desktop. The daemon normally has no console of its own — a detached `daemon
// start`, or the Windows Scheduled Task once the daemon has released the
// console it was handed — and on Windows a console-subsystem child of a
// console-less parent is given a console, i.e. a window, unless its creator
// says otherwise. T3.8 established this for step processes and for git; the
// probes were left spawning bare exec.Cmds.
//
// And it says how the probe failed. On Windows a context deadline is enforced
// by TerminateProcess(pid, 1), so a probe killed by its own timeout reports
// what a genuine failure reports — "exit status 1" — and the two become
// indistinguishable in the log. That cost a real diagnosis: at the logon after
// a reboot, a cold `codex --version` took longer than its bound and the daemon
// recorded `codex --version failed: exit status 1` against a CLI that was
// installed and healthy.
//
// The returned error wraps context.DeadlineExceeded on a timeout — so a caller
// that reads meaning into an exit status can tell a refusal from a probe that
// never finished — and carries the child's stderr, which .Output() keeps inside
// an *exec.ExitError nobody unwraps. The *exec.ExitError stays in the chain
// either way.
func Probe(ctx context.Context, timeout time.Duration, path string, args ...string) (stdout, stderr []byte, err error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var out, errOut bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...) //nolint:gosec // the resolved agent binary, arguments built by the adapter
	cmd.Stdout, cmd.Stderr = &out, &errOut
	procx.NoWindow(cmd)
	runErr := cmd.Run()
	stdout, stderr = out.Bytes(), errOut.Bytes()
	switch {
	case runErr == nil:
		return stdout, stderr, nil
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return stdout, stderr, fmt.Errorf("timed out after %s: %w", timeout, ctx.Err())
	case ctx.Err() != nil:
		// The daemon is shutting down, or the request went away. Never a
		// verdict about the binary.
		return stdout, stderr, fmt.Errorf("probe canceled: %w", ctx.Err())
	default:
		if line := firstLine(stderr); line != "" {
			return stdout, stderr, fmt.Errorf("%w: %s", runErr, line)
		}
		return stdout, stderr, runErr
	}
}

// firstLine returns the first non-empty line of b, truncated to stderrLimit.
// CLIs put the sentence that matters first and the usage banner after it.
func firstLine(b []byte) string {
	for line := range strings.SplitSeq(string(b), "\n") {
		s := strings.TrimSpace(line)
		if s == "" {
			continue
		}
		// Runes, not bytes: a CLI's error message carries whatever the
		// user's paths and locale carry, and half a rune in a log record is
		// a second defect to read past.
		if r := []rune(s); len(r) > stderrLimit {
			return string(r[:stderrLimit]) + "…"
		}
		return s
	}
	return ""
}
