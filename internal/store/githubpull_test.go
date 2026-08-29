package store

import (
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/github"
)

// The `github_pull_json` column (migration 0018, task 052). It is a
// **pointer**, not a snapshot: what survives a restart is repo, number, who
// linked it and whether a human refused it — nothing renderable.

func TestTaskGitHubPullRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "linked")

	in := newTask(p.ID, "has-a-pull", TaskQueued)
	if err := s.CreateTask(ctx, in, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	// A task with no link reads back with none, and the column is SQL NULL —
	// the same "absence is absence" 0014 set.
	out, err := s.GetTask(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if out.GitHubPull != nil {
		t.Fatalf("an unlinked task read back with a link: %+v", out.GitHubPull)
	}

	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	updated, err := s.SetTaskGitHubPull(ctx, in.ID, LinkPull("octo/repo", 412, github.SourceAuto, now))
	if err != nil {
		t.Fatalf("SetTaskGitHubPull: %v", err)
	}
	if !updated.GitHubPull.Linked() || updated.GitHubPull.Number != 412 {
		t.Fatalf("link not returned by the write: %+v", updated.GitHubPull)
	}
	// The link survives a reload, which is the durability claim: a task still
	// names its pull request after a daemon restart.
	out, err = s.GetTask(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if out.GitHubPull == nil || out.GitHubPull.Repo != "octo/repo" ||
		out.GitHubPull.Number != 412 || out.GitHubPull.Source != github.SourceAuto {
		t.Fatalf("link did not round-trip: %+v", out.GitHubPull)
	}
	if !out.GitHubPull.LinkedAt.Equal(now) {
		t.Errorf("linked_at = %v, want %v", out.GitHubPull.LinkedAt, now)
	}
}

// TestSuppressedPullIsStored is the three-state claim. A human's unlink must
// be *stored*, not spelled as an absent column: the reconciler has to be able
// to read the refusal, and an empty column only says "never matched".
func TestSuppressedPullIsStored(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "refused")
	in := newTask(p.ID, "refused-a-pull", TaskQueued)
	if err := s.CreateTask(ctx, in, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	if _, err := s.SetTaskGitHubPull(ctx, in.ID,
		LinkPull("octo/repo", 412, github.SourceAuto, now)); err != nil {
		t.Fatalf("link: %v", err)
	}
	before, err := s.GetTask(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if _, err := s.SetTaskGitHubPull(ctx, in.ID, SuppressPull(before.GitHubPull, now)); err != nil {
		t.Fatalf("suppress: %v", err)
	}
	out, err := s.GetTask(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if out.GitHubPull == nil {
		t.Fatal("a human unlink cleared the column: the refusal is not recoverable")
	}
	if out.GitHubPull.Linked() {
		t.Error("a suppressed link still reads as linked")
	}
	if out.GitHubPull.Number != 412 || out.GitHubPull.Source != github.SourceHuman {
		t.Errorf("the suppression forgot what was refused: %+v", out.GitHubPull)
	}
}

// TestLinkCandidatesScopesToProject: the reconciler matches branch names
// within one project, and never reaches an archived task.
func TestLinkCandidatesScopesToProject(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	mine := testProject(t, s, "mine")
	theirs := testProject(t, s, "theirs")

	a := newTask(mine.ID, "mine-one", TaskQueued)
	a.BranchName = "vincent/1-mine"
	b := newTask(theirs.ID, "theirs-one", TaskQueued)
	b.BranchName = "vincent/1-mine" // the same branch name, a different project
	for _, task := range []*Task{a, b} {
		if err := s.CreateTask(ctx, task, nil); err != nil {
			t.Fatalf("CreateTask: %v", err)
		}
	}
	got, err := s.LinkCandidates(ctx, mine.ID)
	if err != nil {
		t.Fatalf("LinkCandidates: %v", err)
	}
	if len(got) != 1 || got[0].TaskID != a.ID {
		t.Fatalf("candidates = %+v, want only task %d", got, a.ID)
	}
	if got[0].BranchName != "vincent/1-mine" {
		t.Errorf("branch = %q, want vincent/1-mine", got[0].BranchName)
	}
	if got[0].Pull != nil {
		t.Errorf("an unlinked candidate carries a link: %+v", got[0].Pull)
	}
}

// TestSetTaskGitHubPullRecordsEvent: a link is news, and a running TUI has to
// re-render without polling. The event is durable and carries the task id.
func TestSetTaskGitHubPullRecordsEvent(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "eventful")
	in := newTask(p.ID, "linked", TaskQueued)
	if err := s.CreateTask(ctx, in, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	var seen *Event
	s.SetEventHook(func(e *Event) {
		if e.Type == EventTaskGitHubPullChanged {
			seen = e
		}
	})
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	if _, err := s.SetTaskGitHubPull(ctx, in.ID,
		LinkPull("octo/repo", 412, github.SourceAuto, now)); err != nil {
		t.Fatalf("link: %v", err)
	}
	if seen == nil {
		t.Fatal("no task.github_pull_changed event was published")
	}
	if seen.TaskID == nil || *seen.TaskID != in.ID {
		t.Errorf("event task id = %v, want %d", seen.TaskID, in.ID)
	}
	if seen.ID == 0 {
		t.Error("the event was published without a durable id")
	}
	// The link is a fact about GitHub, not about the task's own progress, so
	// updated_at is deliberately untouched.
	before, err := s.GetTask(ctx, in.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if !before.UpdatedAt.Equal(in.UpdatedAt) {
		t.Errorf("updated_at moved to %v from %v: a link is not task progress",
			before.UpdatedAt, in.UpdatedAt)
	}
}
