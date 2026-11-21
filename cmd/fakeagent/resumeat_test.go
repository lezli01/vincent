package main_test

import (
	"errors"
	"os/exec"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// TestResumeAtProbeAnswers pins the two sentences the claude adapter's
// capability probe tells apart (task 167): refused without --resume by a
// build that knows the flag, an unknown option by one that does not. Both
// exit 1 with nothing on stdout, as 2.1.330 does.
func TestResumeAtProbeAnswers(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)
	for _, tt := range []struct {
		env, want string
	}{
		{"", "Error: --resume-session-at requires --resume"},
		{"FAKEAGENT_CLAUDE_NO_RESUME_AT=1", "error: unknown option '--resume-session-at'"},
	} {
		cmd := exec.Command(bin, "-p", "--resume-session-at", "00000000-0000-0000-0000-000000000000")
		cmd.Env = cmd.Environ()
		if tt.env != "" {
			cmd.Env = append(cmd.Env, tt.env)
		}
		var stderr strings.Builder
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.ExitCode() != 1 {
			t.Errorf("env %q: err = %v, want exit 1", tt.env, err)
		}
		if len(out) != 0 {
			t.Errorf("env %q: stdout = %q, want none", tt.env, out)
		}
		if got := strings.TrimSpace(stderr.String()); got != tt.want {
			t.Errorf("env %q: stderr = %q, want %q", tt.env, got, tt.want)
		}
	}
}

// TestForkWithoutAStoreStillReportsANewSession: with no session store there
// is nothing to cut, but a forked run's lines still carry an id that is not
// the resumed one, and its assistant lines carry a uuid.
func TestForkWithoutAStoreStillReportsANewSession(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)
	out := runAgent(t, bin, []string{
		"-p", "--output-format", "stream-json", "--resume", "s1", "--resume-session-at", "fake-msg-1-1", "--fork-session",
	})
	if strings.Contains(out, `"session_id":"s1"`) || !strings.Contains(out, `"session_id":"s1-fork"`) {
		t.Errorf("forked run's session ids:\n%s", out)
	}
	if !strings.Contains(out, `"uuid":"fake-msg-1-1"`) {
		t.Errorf("assistant lines carry no uuid:\n%s", out)
	}
}
