package taskrun

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// TestStepRunRecordsItsCommitRange is task 164.5's engine leg: each attempt
// that runs in the worktree journals the HEAD it started at and the HEAD it
// ended at, so a step that commits has a range of its own and a step that
// does not has an empty one. The two steps chain: the second starts where
// the first ended.
func TestStepRunRecordsItsCommitRange(t *testing.T) {
	h := newEngineHarness(t)
	base := strings.TrimSpace(testrepo.Run(t, h.repo, "rev-parse", "refs/heads/main"))
	// `git commit --allow-empty` is spelled the same under /bin/sh and pwsh.
	task := h.createTask(t, "name: range\nsteps:\n"+
		commandStep("commit", `git commit --allow-empty -m "step commit"`, "max_retries: 0")+
		commandStep("look", "git status", "max_retries: 0"))
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%s), want done", done.State, done.BlockReason)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 2 {
		t.Fatalf("step runs = %d, want 2: %+v", len(runs), runs)
	}
	tip := h.tip(t, done.BranchName)
	if tip == base {
		t.Fatalf("branch tip %s is the base: the step committed nothing", tip)
	}

	commit, look := runs[0], runs[1]
	if commit.HeadStart == nil || *commit.HeadStart != base {
		t.Errorf("commit step head_start = %v, want the base %s", commit.HeadStart, base)
	}
	if commit.HeadEnd == nil || *commit.HeadEnd != tip {
		t.Errorf("commit step head_end = %v, want the branch tip %s", commit.HeadEnd, tip)
	}
	if look.HeadStart == nil || look.HeadEnd == nil || *look.HeadStart != tip || *look.HeadEnd != tip {
		t.Errorf("non-committing step range = %v..%v, want %s..%s", look.HeadStart, look.HeadEnd, tip, tip)
	}
}
