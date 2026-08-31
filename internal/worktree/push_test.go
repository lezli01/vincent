package worktree

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/testrepo"
)

// The branch push (task 069 decision 5). Real repositories throughout, for
// the reason pull_test.go uses them: whether a push sets an upstream and
// whether a diverged remote rejects it are git-side facts the API takes on
// faith, and a fake would only prove that this package believes them.

// The argv assertion, and the point of the whole feature's honesty: no force
// flag can appear in a push vincent makes. It is checked here rather than
// left to review because "we do not force-push" is the kind of promise that
// is one convenience commit away from being false.
func TestPushArgsNeverForce(t *testing.T) {
	args := pushArgs("origin", "vincent/1-a-thing")
	for _, a := range args {
		lower := strings.ToLower(a)
		if strings.Contains(lower, "force") || strings.HasPrefix(a, "+") {
			t.Fatalf("push argv carries a force flag: %v", args)
		}
	}
	// The whole argv, spelled out: anything added to it has to be added here
	// too, which is the review this test is standing in for.
	want := []string{"push", "--set-upstream", "origin", "vincent/1-a-thing"}
	if !slices.Equal(args, want) {
		t.Fatalf("push argv is %v, want %v", args, want)
	}
	// The prompt is disabled, so a credential helper that wants a terminal
	// fails instead of hanging a request handler.
	if !slices.Contains(pushEnv(), "GIT_TERMINAL_PROMPT=0") {
		t.Fatalf("push env does not disable the terminal prompt: %v", pushEnv())
	}
}

// A branch with no upstream is pushed and gets one.
func TestPushBranchSetsUpstream(t *testing.T) {
	m, remote, local := pullRepos(t)
	testrepo.Run(t, local, "checkout", "-q", "-b", "vincent/1-a-thing")
	testrepo.Run(t, local, "commit", "-q", "--allow-empty", "-m", "work")

	out, err := m.PushBranch(context.Background(), local, "vincent/1-a-thing")
	if err != nil {
		t.Fatalf("PushBranch: %v", err)
	}
	if !out.SetUpstream {
		t.Error("the branch had no upstream and PushBranch did not report setting one")
	}
	if out.Remote != "origin" || out.Branch != "vincent/1-a-thing" {
		t.Errorf("PushBranch reported %+v", out)
	}
	// The remote actually has it — the whole point, since a compare URL for a
	// branch nobody pushed is the dead page this exists to remove.
	if got := testrepo.Run(t, remote, "rev-parse", "refs/heads/vincent/1-a-thing"); got == "" {
		t.Fatal("the branch is not on the remote")
	}
	if got := testrepo.Run(t, local, "config", "--get", "branch.vincent/1-a-thing.remote"); got != "origin" {
		t.Errorf("branch.remote is %q, want origin", got)
	}
	// Pushing again is fine: -u is idempotent and there is nothing new to
	// send, which is what makes the un-conditional -u correct.
	if _, err := m.PushBranch(context.Background(), local, "vincent/1-a-thing"); err != nil {
		t.Fatalf("second PushBranch: %v", err)
	}
}

// A remote whose branch has moved on rejects a non-fast-forward push, and the
// refusal is named. Nothing is forced past it: the local branch may hold
// commits nobody pushed, and discarding them silently is dishonest (task 064
// decision 4's reason, held here).
func TestPushBranchDivergedIsRejectedAndNamed(t *testing.T) {
	m, remote, local := pullRepos(t)
	pushHead(t, remote, local, "vincent/2-diverged", "somebody else's commit")

	// A local branch of the same name from master: it shares no history with
	// what is on the remote, so the push is a non-fast-forward.
	testrepo.Run(t, local, "checkout", "-q", "-b", "vincent/2-diverged", "master")
	testrepo.Run(t, local, "commit", "-q", "--allow-empty", "-m", "our commit")
	before := testrepo.Run(t, remote, "rev-parse", "refs/heads/vincent/2-diverged")

	_, err := m.PushBranch(context.Background(), local, "vincent/2-diverged")
	if err == nil {
		t.Fatal("a diverged push succeeded; it must be rejected, never forced")
	}
	if reason := ReasonOf(err); reason != ReasonPushRejected {
		t.Fatalf("diverged push reported %q, want %q", reason, ReasonPushRejected)
	}
	// And it changed nothing on the remote.
	if after := testrepo.Run(t, remote, "rev-parse", "refs/heads/vincent/2-diverged"); after != before {
		t.Fatalf("the remote branch moved from %s to %s on a rejected push", before, after)
	}
}

// An empty branch name is refused before git is run at all.
func TestPushBranchRefusesEmptyBranch(t *testing.T) {
	m, _, local := pullRepos(t)
	if _, err := m.PushBranch(context.Background(), local, "  "); err == nil {
		t.Fatal("PushBranch accepted an empty branch name")
	}
}

// The reason mapping, against git's own sentences. Each string is what git
// prints on the failure it names; none of them reaches a client, only the
// constant they map to does.
func TestPushReason(t *testing.T) {
	for _, tc := range []struct{ stderr, want string }{
		{"! [rejected] main -> main (non-fast-forward)", ReasonPushRejected},
		{"remote: error: GH006: Protected branch update failed", ReasonPushRejected},
		{"Updates were rejected because the remote contains work; fetch first", ReasonPushRejected},
		{"remote: error: pre-receive hook declined", ReasonPushRejected},
		{"fatal: could not read Username for 'https://github.com': terminal prompts disabled", ReasonPushNoCredential},
		{"remote: Invalid username or token. Password authentication is not supported", ReasonPushNoCredential},
		{"git@github.com: Permission denied (publickey).", ReasonPushNoCredential},
		{"fatal: unable to access 'https://github.com/o/r/': Could not resolve host", ReasonPushFailed},
	} {
		if got := pushReason(stderrError(tc.stderr)); got != tc.want {
			t.Errorf("pushReason(%q) = %q, want %q", tc.stderr, got, tc.want)
		}
	}
}

// stderrError is a git failure carrying one line of stderr.
type stderrErr struct{ msg string }

func (e stderrErr) Error() string { return e.msg }

func stderrError(msg string) error { return stderrErr{msg} }
