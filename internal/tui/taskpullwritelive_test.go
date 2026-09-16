package tui

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/store"
)

// The Pull Request tab's writes against the **real** API handlers, with the
// daemon's GitHub client pointed at cmd/fakegh (task 068.4). This is what
// keeps the popup's request and the server's DTO from drifting.

// liveWriteKey sends one key through the root and pumps the reply the way the
// runtime would, until the tab has nothing in flight.
func liveWriteKey(t *testing.T, h *newTaskLiveHarness, tv *taskView, key string) {
	t.Helper()
	h.sendKey(synthKey(key))
	h.p.until(15*time.Second, "the write after "+key+" to answer", func() bool {
		for _, busy := range tv.pullTab.inflight {
			if busy {
				return false
			}
		}
		return true
	})
}

func TestPullWriteRoundTripsAgainstTheRealHandlers(t *testing.T) {
	t.Setenv("FAKEGH_STATE_FILE", filepath.Join(t.TempDir(), "state.json"))
	h, argv := newGitHubLiveHarness(t, liveOptions{remote: ghLiveOrigin})
	h.p.until(10*time.Second, "the GitHub probes to answer", func() bool {
		return h.m.githubAvailable()
	})

	task := &store.Task{
		ProjectID: h.projectID, Title: "Add a thing", WorkflowName: "implement",
		BaseBranch: "main", BranchName: "vincent/1-add-a-thing", State: store.TaskDone,
	}
	if err := h.st.CreateTask(context.Background(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if _, err := h.st.SetTaskGitHubPull(context.Background(), task.ID, &github.PullLink{
		Repo: "octo/repo", Number: 412, Source: "human", LinkedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SetTaskGitHubPull: %v", err)
	}

	_, cmd := h.m.Update(selectTaskMsg{id: task.ID, state: string(store.TaskDone)})
	h.p.push(cmd)
	tv, ok := h.m.views[viewTask].(*taskView)
	if !ok {
		t.Fatalf("view %d is %T", viewTask, h.m.views[viewTask])
	}
	h.p.until(10*time.Second, "the workspace to fetch its pull request", func() bool {
		return h.m.active == viewTask && tv.pull.Pull != nil
	})
	h.sendKey(tea.KeyPressMsg{Code: '7', Text: "7"})
	h.p.until(10*time.Second, "the check rollup", func() bool {
		return tv.tab == taskTabPull && tv.pullTab.loaded && len(tv.pullTab.checks.Runs) > 0
	})

	// Re-run: fakegh's failed Actions check on run 5150, reached with the
	// tab's own cursor.
	for i, run := range tv.pullTab.checks.Runs {
		if run.RunID == 5150 && run.Failed() {
			for range i {
				h.sendKey(tea.KeyPressMsg{Code: tea.KeyDown})
			}
			break
		}
	}
	if run := tv.selectedCheck(); run == nil || run.RunID != 5150 || !run.Failed() {
		t.Fatalf("fixture: the cursor is on %+v, want the failed Actions row", run)
	}
	liveWriteKey(t, h, tv, "ctrl+r")
	liveWriteKey(t, h, tv, "y")
	if tv.pullTab.noteBad || tv.pullTab.note != "re-run requested for Actions run 5150" {
		t.Fatalf("re-run note = %q (bad=%v)", tv.pullTab.note, tv.pullTab.noteBad)
	}
	if calls := ghLiveCalls(t, argv); !strings.Contains(calls, "run rerun 5150") {
		t.Fatalf("gh was not asked to re-run 5150:\n%s", calls)
	}

	// Merge: squash, pinned to the head the rollup reported.
	head := tv.pullTab.checks.Ref
	for _, key := range []string{"m", "right", "right", "y"} {
		liveWriteKey(t, h, tv, key)
	}
	if tv.pullTab.noteBad || tv.pullTab.note != "merged octo/repo#412 (squash)" {
		t.Fatalf("merge note = %q (bad=%v)", tv.pullTab.note, tv.pullTab.noteBad)
	}
	if calls := ghLiveCalls(t, argv); !strings.Contains(calls, "pr merge 412 -R octo/repo --squash --match-head-commit "+head) {
		t.Fatalf("gh was not asked for the pinned merge:\n%s", calls)
	}
	h.p.until(10*time.Second, "the refetch to show the merge", func() bool {
		return tv.pull.Pull != nil && tv.pull.Pull.Merged
	})
	if tv.canMerge() {
		t.Fatal("m is still offered on a merged pull request")
	}

	// A second merge is the daemon's not_mergeable, and the note line carries
	// the daemon's message for it rather than anything gh said.
	h.p.push(tv.mergePullCmd("merge", head))
	h.p.until(15*time.Second, "the refused merge to answer", func() bool {
		return tv.pullTab.noteBad
	})
	want := "could not merge octo/repo#412: " + github.Message(github.ReasonNotMergeable)
	if tv.pullTab.note != want {
		t.Fatalf("refusal note = %q, want %q", tv.pullTab.note, want)
	}
}
