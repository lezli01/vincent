package taskrun

// A remote configured as https://user:token@host/… puts the token into every
// git message that names it. block_detail is served by the API, so a
// git-source failure's userinfo must never reach the column (issue #594).

import (
	"errors"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
	"github.com/lezli01/vincent/internal/worktree"
)

func TestBlockDetailNeverCarriesURLUserinfo(t *testing.T) {
	h := newEngineHarness(t)
	task := h.createTask(t, "name: t\nsteps:\n"+commandStep("s", "exit 0"))
	tr, _ := taskstate.Next(task.State, taskstate.Admit)
	running, _, err := h.store.TransitionTask(t.Context(), task.ID, task.State, tr.To, store.TaskChange{})
	if err != nil {
		t.Fatalf("admit: %v", err)
	}

	const remote = "https://someone:s3cr3t-token@git.example.com/o/r.git"
	// Shaped as worktree.CreatePullAndClaim reports a failed fetch: the
	// remote in the message, and git's stderr naming it again.
	cause := &worktree.Error{
		Reason:  worktree.ReasonPullFetchFailed,
		Message: "could not fetch refs/pull/7/head from " + remote + " for pull request #7: fatal: unable to access '" + remote + "/'",
		Err:     errors.New("exit status 128: fatal: repository '" + remote + "' not found"),
	}
	h.runner.fail(running, cause.Reason, worktreeDetail(cause), h.runner.deps.Logger, "create worktree", cause)

	got, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.State != store.TaskBlocked {
		t.Fatalf("state = %s, want blocked", got.State)
	}
	for _, secret := range []string{"s3cr3t-token", "someone"} {
		if strings.Contains(got.BlockDetail, secret) {
			t.Errorf("block_detail carries URL userinfo %q: %q", secret, got.BlockDetail)
		}
	}
	if !strings.Contains(got.BlockDetail, "https://git.example.com/o/r.git") {
		t.Errorf("block_detail = %q, want it to still name the remote without its userinfo", got.BlockDetail)
	}
}
