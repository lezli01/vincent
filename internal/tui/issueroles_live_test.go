package tui

import (
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
)

// Task 134.16 against the real handlers: the board card and the issue
// detail follow the main worktree's occupant from events, and the new-task
// form's `separate`/`agent` choice creates a side task merged back by an
// agent.
func TestIssueWorktreeRolesAgainstTheRealAPI(t *testing.T) {
	h := newNewTaskLiveHarnessWith(t, liveOptions{workflows: issueWorkflows})
	iss := h.seedIssue(t, "Crash on start")

	// The board is up before any task exists; everything after this is
	// learned from events.
	list := issuesListView(t, h)
	h.send(selectViewMsg{id: viewIssues})
	h.p.until(10*time.Second, "the issues list", func() bool { return list.loaded && len(list.rows()) == 1 })

	// A main task, created over the API: the scheduler admits it, and its
	// manual gate keeps it holding the issue's main worktree.
	workflow := "fix-issue"
	main, err := h.m.client.CreateTask(t.Context(), apiclient.CreateTaskRequest{
		ProjectID: h.projectID, Title: "the main work", Workflow: &workflow, IssueID: &iss.ID,
	})
	if err != nil {
		t.Fatalf("create the main task: %v", err)
	}
	occupied := "● #" + strconv.FormatInt(main.ID, 10) + " awaiting_gate"
	h.p.until(20*time.Second, "the card naming the occupant", func() bool {
		return strings.Contains(ansi.Strip(list.render(160, 40)), occupied)
	})

	// The detail names the occupant too.
	detail := issueDetailView(t, h)
	h.send(openIssueMsg{id: iss.ID, projectID: iss.ProjectID})
	h.p.until(10*time.Second, "the detail's main worktree", func() bool {
		out := ansi.Strip(detail.render(160, 80))
		return strings.Contains(out, "Main worktree") && strings.Contains(out, "#"+strconv.FormatInt(main.ID, 10)+"  the main work  awaiting_gate")
	})

	// `a` from the detail: the main worktree is busy, so the form offers
	// separate first and names the occupant.
	h.sendKey(keyPress("a"))
	n := h.form(t)
	h.p.until(10*time.Second, "the worktree row", func() bool { return n.rowVisible(ntWorktree) })
	if !n.separate || !strings.Contains(ansi.Strip(n.rowValue(ntWorktree)), "busy with #"+strconv.FormatInt(main.ID, 10)) {
		t.Fatalf("worktree row = %q (separate %v), want separate preselected and the occupant named",
			ansi.Strip(n.rowValue(ntWorktree)), n.separate)
	}
	if n.rowVisible(ntBranch) || n.rowVisible(ntBranchName) || !n.rowVisible(ntMergeBack) {
		t.Fatal("separate does not swap the branch rows for the merge-back row")
	}
	h.p.push(n.applyPick(ntWorkflow, workflow, false))
	h.p.until(10*time.Second, "fix-issue's prefill", func() bool {
		_, ok := n.fieldValue("issue")
		return ok
	})
	n.cursor = ntMergeBack
	h.p.push(n.activate())
	h.sendKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	var side store.Task
	h.p.until(20*time.Second, "the side task", func() bool {
		for _, task := range h.createdTasks(t, iss.ID) {
			if task.IssueWorktree == store.IssueWorktreeSide {
				side = task
				return true
			}
		}
		return false
	})
	if side.MergeOnConflict != store.MergeOnConflictAgent {
		t.Errorf("side task merge_back.on_conflict = %q, want agent", side.MergeOnConflict)
	}

	// Back on the board, the side task shows on the card from its events.
	h.send(selectViewMsg{id: viewIssues})
	h.p.until(20*time.Second, "the card counting the side task", func() bool {
		return strings.Contains(ansi.Strip(list.render(160, 40)), "+1 side")
	})

	// The detail, opened, annotates it; cancelling the main task frees the
	// worktree, and the detail re-reads from the event.
	h.send(openIssueMsg{id: iss.ID, projectID: iss.ProjectID})
	h.p.until(10*time.Second, "the side task's annotation", func() bool {
		return strings.Contains(ansi.Strip(detail.render(160, 80)), "side · merge agent")
	})
	if _, err := h.m.client.Cancel(t.Context(), main.ID); err != nil {
		t.Fatalf("cancel the main task: %v", err)
	}
	h.p.until(20*time.Second, "the main worktree freed", func() bool {
		return strings.Contains(ansi.Strip(detail.render(160, 80)), "occupant  free")
	})
}
