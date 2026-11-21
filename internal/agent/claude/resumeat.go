package claude

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/agent"
)

// Resuming a session at a point (task 167 decision A, §9.2).
//
// `--resume <id> --resume-session-at <uuid> --fork-session` reloads a session
// only up to the assistant message <uuid> and continues it under a new
// session id, which is what lets a rewound chat's next turn see exactly the
// turns the rewind kept. testdata/stream_resume_at_2.1.330.jsonl is that run:
// a session that had answered ALPHA then BETA, resumed at ALPHA's message,
// lists ALPHA alone under a session id it had never had.
//
// `--resume-session-at` is undocumented — 2.1.x's `--help` does not list it —
// so the capability is detected, never declared, and never read from a
// version table: a build that drops the flag must turn it off by itself. The
// probe asks the CLI to parse the flag without --resume. A claude that knows
// it refuses the combination by name before reading stdin or touching the
// network; one that does not fails commander's unknown-option check first.
// Both exit 1; the sentence is the verdict. Fixtures:
// probe_resume_at_present_2.1.330.txt is 2.1.330's answer verbatim, and
// probe_resume_at_absent_2.1.330.txt is commander's unknown-option sentence
// as 2.1.330 prints it for a flag it lacks, with the flag's name in it.

// resumeAtProbeTimeout bounds the probe like `--help`'s: it starts the same
// node process and returns before any work.
const resumeAtProbeTimeout = helpTimeout

// resumeAtProbeArgs never names --resume, so no build can take it for a run:
// a CLI that knows the flag rejects the combination, and one that does not
// rejects the flag.
var resumeAtProbeArgs = []string{"-p", "--resume-session-at", "00000000-0000-0000-0000-000000000000"}

// parseResumeAtProbe reads the probe's combined output. Present is a refusal
// that names the flag and is not commander's unknown-option error. Anything
// else — a crash, a sentence vincent has not seen — is absent: the cost of a
// wrong false is a seeded turn, of a wrong true a failed one.
func parseResumeAtProbe(out string) bool {
	return strings.Contains(out, "--resume-session-at") && !strings.Contains(out, "unknown option")
}

// resumeAtCache holds the last probe's verdict for one binary. It is keyed
// by path and by the file's size and modification time, so an upgraded
// claude is probed again without anything having to notice the upgrade. It
// lives on the Adapter rather than in the package: the daemon keeps one
// adapter for its life, and a test that builds a fresh one gets a fresh
// probe rather than another test's answer.
type resumeAtCache struct {
	mu      sync.Mutex
	key     string
	present bool
}

// binaryKey identifies the file at path, or "" when it cannot be read —
// which leaves the verdict uncached.
func binaryKey(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	return path + "\x00" + fi.ModTime().UTC().Format(time.RFC3339Nano) + "\x00" + strconv.FormatInt(fi.Size(), 10)
}

// resumeAtPoint reports whether the claude a run through l would start
// honours --resume-session-at. A host run's verdict is cached per binary; a
// launcher's (a containerized step, which no chat is) is probed every time,
// since the binary is not a file here to key on.
func (a *Adapter) resumeAtPoint(ctx context.Context, l agent.Launcher, path string) bool {
	key := ""
	if l == nil {
		key = binaryKey(path)
	}
	if key != "" {
		a.resumeAt.mu.Lock()
		if a.resumeAt.key == key {
			present := a.resumeAt.present
			a.resumeAt.mu.Unlock()
			return present
		}
		a.resumeAt.mu.Unlock()
	}
	stdout, stderr, err := agent.ProbeWith(ctx, l, resumeAtProbeTimeout, path, resumeAtProbeArgs...)
	// A probe that never finished is no verdict about the binary, and is not
	// remembered as one. Every other error is the expected exit 1.
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return false
	}
	present := parseResumeAtProbe(string(stdout) + string(stderr))
	if key != "" {
		a.resumeAt.mu.Lock()
		a.resumeAt.key, a.resumeAt.present = key, present
		a.resumeAt.mu.Unlock()
	}
	return present
}

// SupportsResumeAtPoint implements agent.ResumeAtPointer: whether the
// configured claude on this host honours --resume-session-at, from the cached
// probe above. A binary that cannot be resolved cannot.
func (a *Adapter) SupportsResumeAtPoint() bool {
	path, err := a.resolvePath()
	if err != nil {
		return false
	}
	return a.resumeAtPoint(context.Background(), nil, path)
}
