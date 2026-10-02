package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// The issue screens (§15 views 12 and 13, task 130.9) against the **real**
// API handlers over httptest, which is what keeps the apiclient's Issue and
// the server's DTO from drifting.

func issuesListView(t *testing.T, h *newTaskLiveHarness) *issuesView {
	t.Helper()
	v, ok := h.m.views[viewIssues].(*issuesView)
	if !ok {
		t.Fatalf("view %d is %T, want *issuesView", viewIssues, h.m.views[viewIssues])
	}
	return v
}

func issueDetailView(t *testing.T, h *newTaskLiveHarness) *issueView {
	t.Helper()
	v, ok := h.m.views[viewIssue].(*issueView)
	if !ok {
		t.Fatalf("view %d is %T, want *issueView", viewIssue, h.m.views[viewIssue])
	}
	return v
}

func (h *newTaskLiveHarness) send(msg tea.Msg) {
	_, cmd := h.m.Update(msg)
	h.p.push(cmd)
}

// One walk through both screens, because every step after the first depends
// on what the one before it left on screen: the list across two projects,
// `/` and `s`, `enter` into the detail, a linked task's workspace and back,
// live refreshes on issue.* and task events, and an imported issue's `o`.
func TestIssueScreensAgainstTheRealAPI(t *testing.T) {
	h := newNewTaskLiveHarness(t)
	ctx := context.Background()

	// A second project, with no GitHub integration either: issues are
	// vincent's own, and the list is not gated on a probe.
	other := &store.Project{Name: "second", Path: testrepo.Init(t, "main"), DefaultBranch: "main"}
	if err := h.st.CreateProject(ctx, other); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	local, err := h.st.CreateIssue(ctx, store.NewIssue{
		ProjectID: h.projectID, Title: "Crash on start", Body: "It **crashes**.", Kind: "bug",
		Labels: []string{"p1"}, Priority: 1, Author: "human",
	}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	imported, _, err := h.st.UpsertRemoteIssue(ctx, store.RemoteIssue{
		ProjectID: other.ID, Provider: "github", RemoteKey: "I_41", Repo: "octo/web", Number: 41,
		URL: "https://github.com/octo/web/issues/41", RemoteJSON: `{"state":"open"}`,
		Title: "Dark mode", Author: "octocat", State: issuestate.Open,
	}, issuestate.Human)
	if err != nil {
		t.Fatalf("UpsertRemoteIssue: %v", err)
	}
	issueID := local.ID
	task := &store.Task{
		ProjectID: h.projectID, Title: "fix the crash", WorkflowName: "implement",
		BaseBranch: "main", BranchName: "vincent/1-fix-the-crash", State: store.TaskBlocked, IssueID: &issueID,
	}
	if err := h.st.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}

	list := issuesListView(t, h)
	h.send(selectViewMsg{id: viewIssues})
	h.p.until(10*time.Second, "the issues list", func() bool {
		return list.loaded && len(list.rows()) == 2
	})
	out := ansi.Strip(list.render(160, 40))
	for _, want := range []string{"live", "second", "Crash on start", "Dark mode", "octo/web#41", "● 1 task", "[p1]"} {
		if !strings.Contains(out, want) {
			t.Errorf("the list is missing %q:\n%s", want, out)
		}
	}

	// `/` narrows client-side, by project name here.
	h.sendKey(keyPress("/"))
	h.typeText("second")
	h.sendKey(keyPress("enter"))
	if rows := list.rows(); len(rows) != 1 || rows[0].issue.ID != imported.ID {
		t.Fatalf("filter 'second' left %d rows, want the imported issue alone", len(rows))
	}

	// `o` on the imported issue hands its page to a browser.
	opened := withLiveFakeOpener(t)
	h.sendKey(keyPress("o"))
	h.p.until(5*time.Second, "the browser hand-off", func() bool { return len(opened()) == 1 })
	if got := opened(); got[0] != "https://github.com/octo/web/issues/41" {
		t.Fatalf("o opened %v", got)
	}
	// A hand-off that worked clears its own note, so one esc is the filter.
	h.p.until(5*time.Second, "the hand-off's note to clear", func() bool { return list.note == "" })
	h.sendKey(keyPress("esc"))
	if len(list.rows()) != 2 {
		t.Fatalf("esc did not clear the filter: %d rows", len(list.rows()))
	}

	// `s` re-lists with state=closed: nothing is closed.
	h.sendKey(keyPress("s"))
	h.p.until(10*time.Second, "the closed listing", func() bool {
		return list.state == "closed" && list.loaded && !list.loading && len(list.issues) == 0
	})
	h.sendKey(keyPress("s"))
	h.sendKey(keyPress("s"))
	h.p.until(10*time.Second, "the open listing again", func() bool {
		return list.state == "open" && !list.loading && len(list.issues) == 2
	})

	// An issue.updated re-lists with no keypress.
	renamed := "Crash on start (macOS)"
	cur, err := h.st.UpdateIssue(ctx, local.ID, local.Version, store.IssuePatch{Title: &renamed}, issuestate.Human)
	if err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	h.p.until(10*time.Second, "the renamed row", func() bool {
		return strings.Contains(ansi.Strip(list.render(160, 40)), renamed)
	})

	// enter opens the detail on the selected issue.
	for i, row := range list.rows() {
		if row.issue.ID == local.ID {
			list.cursor = i
		}
	}
	detail := issueDetailView(t, h)
	h.sendKey(keyPress("enter"))
	h.p.until(10*time.Second, "the issue detail", func() bool {
		return h.m.active == viewIssue && detail.loaded && detail.id == local.ID && len(detail.tasks) == 1
	})
	out = ansi.Strip(detail.render(120, 60))
	for _, want := range []string{renamed, "urgent", "bug", "fix the crash", "1 task, 1 active"} {
		if !strings.Contains(out, want) {
			t.Errorf("the detail is missing %q:\n%s", want, out)
		}
	}

	// enter on the linked task opens its workspace; esc comes back here.
	h.sendKey(keyPress("enter"))
	h.p.until(10*time.Second, "the linked task's workspace", func() bool { return h.m.active == viewTask })
	h.sendKey(keyPress("esc"))
	h.p.until(10*time.Second, "esc back to the issue", func() bool { return h.m.active == viewIssue })

	// An issue.updated on this issue re-reads the detail.
	again := "Crash on start (all platforms)"
	if _, err := h.st.UpdateIssue(ctx, local.ID, cur.Version, store.IssuePatch{Title: &again}, issuestate.Human); err != nil {
		t.Fatalf("UpdateIssue: %v", err)
	}
	h.p.until(10*time.Second, "the detail to re-read", func() bool { return detail.issue.Title == again })

	// A task.state_changed on the linked task re-reads the detail, and the
	// list's active marker goes out.
	if _, _, err := h.st.TransitionTask(ctx, task.ID, store.TaskBlocked, store.TaskAborted, store.TaskChange{}); err != nil {
		t.Fatalf("TransitionTask: %v", err)
	}
	h.p.until(10*time.Second, "the detail to drop the settled task", func() bool {
		return len(detail.tasks) == 0 && len(detail.issue.Tasks.ActiveIDs) == 0
	})
	h.p.until(10*time.Second, "the list's active marker to go out", func() bool {
		for _, iss := range list.issues {
			if iss.ID == local.ID {
				return !iss.Active && iss.TaskCount == 1
			}
		}
		return false
	})

	// esc returns to the list with the selection kept.
	h.sendKey(keyPress("esc"))
	h.p.until(5*time.Second, "esc back to the list", func() bool { return h.m.active == viewIssues })
	if row, ok := list.current(); !ok || row.issue.ID != local.ID {
		t.Fatalf("the list lost its selection: %+v", row)
	}
}
