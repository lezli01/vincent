package tui

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// The new-task form's sources, against the real handlers (§15 view 3, task
// 130.13). The form has no GitHub issue row: a task is started from a vincent
// issue with `a` on the issue screens, and the form shows that issue on a
// read-only source row. A plain draft is asserted, at the process level, to
// make no GitHub call at all.

// issueWorkflows are two workflows whose declared fields differ, so a
// workflow switch changes what an issue's prefill fills.
var issueWorkflows = map[string]string{
	"fix-issue.yaml": `name: fix-issue
fields:
  - {name: issue, type: string}
  - {name: kind, type: string}
steps:
  - {id: gate, type: manual, instructions: review}
`,
	"triage.yaml": `name: triage
fields:
  - {name: labels, type: string}
  - {name: kind, type: string}
steps:
  - {id: gate, type: manual, instructions: review}
`,
}

// seedIssue files a local issue straight into the store.
func (h *newTaskLiveHarness) seedIssue(t *testing.T, title string) *store.Issue {
	t.Helper()
	iss, err := h.st.CreateIssue(context.Background(), store.NewIssue{
		ProjectID: h.projectID, Title: title, Body: "It crashes on start.",
		Kind: "bug", Author: "human", Labels: []string{"p1"},
	}, issuestate.Human)
	if err != nil {
		t.Fatalf("CreateIssue: %v", err)
	}
	return iss
}

// seedForm opens the form seeded with an issue, as `a` would, and waits for
// the daemon's preview of it.
func (h *newTaskLiveHarness) seedForm(t *testing.T, issueID int64) *newTask {
	t.Helper()
	h.send(newTaskFromIssueMsg{projectID: h.projectID, issueID: issueID})
	n := h.form(t)
	h.p.until(10*time.Second, "the seeded issue to be read", func() bool {
		return n.loaded && n.issue != nil && n.issue.ID == issueID && n.issuePrefill != nil
	})
	return n
}

// pick chooses a value on a picker row the way applyPick does for a human,
// and runs whatever it asked for.
func (h *newTaskLiveHarness) pick(n *newTask, row ntRow, value string) {
	h.p.push(n.applyPick(row, value, false))
}

func (n *newTask) fieldValue(name string) (string, bool) {
	for _, f := range n.fields {
		if f.key == name {
			return f.value, true
		}
	}
	return "", false
}

func (h *newTaskLiveHarness) createdTasks(t *testing.T, issueID int64) []store.Task {
	t.Helper()
	tasks, err := h.st.ListTasks(t.Context(), store.TaskFilter{IssueID: issueID})
	if err != nil {
		t.Fatalf("list tasks: %v", err)
	}
	return tasks
}

// TestPlainDraftMakesNoGitHubCall is decision 7's half that deletes: on a
// project whose GitHub integration is usable, `n` draws no issue row and the
// daemon invokes no `gh` — asserted from the fake's argv log, not inferred.
func TestPlainDraftMakesNoGitHubCall(t *testing.T) {
	h, argv := newGitHubLiveHarness(t, liveOptions{remote: ghLiveOrigin})
	h.sendKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	n := h.form(t)
	h.p.until(10*time.Second, "the form to settle on a workflow", func() bool {
		_, ok := n.resolved()
		return n.loaded && n.workflow != "" && ok
	})
	if n.rowVisible(ntSource) {
		t.Error("a plain draft draws a source row")
	}
	for _, row := range n.visibleRows() {
		if row == ntSource {
			t.Fatal("ntSource is in a plain draft's visible rows")
		}
	}
	if view := n.render(160, 60); strings.Contains(view, "issue") {
		t.Errorf("a plain draft mentions an issue:\n%s", view)
	}
	req := n.request()
	if req.IssueID != nil {
		t.Errorf("a plain draft sends issue_id=%v", req.IssueID)
	}
	if calls := ghLiveCalls(t, argv); calls != "" {
		t.Errorf("a plain draft invoked gh:\n%s", calls)
	}
}

// TestAddOnTheIssueListSeedsTheForm: `a` on the selected issue opens the form
// with its source row, the prefill in the editable rows, and a create that
// carries `issue_id` and every row.
func TestAddOnTheIssueListSeedsTheForm(t *testing.T) {
	h := newNewTaskLiveHarness(t)
	iss := h.seedIssue(t, "Crash on start")
	list := issuesListView(t, h)
	h.send(selectViewMsg{id: viewIssues})
	h.p.until(10*time.Second, "the issues list", func() bool { return list.loaded && len(list.rows()) == 1 })

	h.sendKey(keyPress("a"))
	h.p.until(5*time.Second, "a to open the new-task form", func() bool { return h.m.active == viewNewTask })
	n := h.form(t)
	h.p.until(10*time.Second, "the seeded issue to be read", func() bool {
		return n.issue != nil && n.issuePrefill != nil
	})
	if !n.rowVisible(ntSource) {
		t.Fatal("an issue-seeded draft has no source row")
	}
	src := n.rowValue(ntSource)
	want := "#" + strconv.FormatInt(iss.ID, 10) + " Crash on start"
	if !strings.Contains(src, want) || !strings.Contains(src, "open") {
		t.Errorf("source row = %q, want %q and its state", src, want)
	}
	if strings.Contains(src, "already started") {
		t.Errorf("a fresh issue claims tasks already started: %q", src)
	}
	// Read-only: enter on it opens nothing.
	n.cursor = ntSource
	h.sendKey(keyPress("enter"))
	if n.mode != ntNavigating || n.pick != nil {
		t.Errorf("enter on the source row opened %v (mode %v)", n.pick, n.mode)
	}
	if got := n.titleText(); got != "Crash on start" {
		t.Errorf("title = %q, want the issue's", got)
	}
	if got := n.desc.Value(); !strings.Contains(got, "It crashes on start.") {
		t.Errorf("description = %q, want the issue body", got)
	}

	h.sendKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	h.p.until(20*time.Second, "the task to be created", func() bool {
		return len(h.createdTasks(t, iss.ID)) == 1
	})
	created := h.createdTasks(t, iss.ID)[0]
	if created.IssueID == nil || *created.IssueID != iss.ID {
		t.Errorf("stored issue_id = %v, want %d", created.IssueID, iss.ID)
	}
	if created.GitHubIssue != nil {
		t.Errorf("an issue-seeded task stored a GitHub issue snapshot: %+v", created.GitHubIssue)
	}
}

// TestIssuePrefillFollowsTheWorkflowUntilTyped is decision 21.2: each
// workflow the draft settles on re-applies the prefill to rows the human has
// not typed in, a typed title or field is never overwritten, and an answer
// for a workflow the draft has left is dropped.
func TestIssuePrefillFollowsTheWorkflowUntilTyped(t *testing.T) {
	h := newNewTaskLiveHarnessWith(t, liveOptions{workflows: issueWorkflows})
	iss := h.seedIssue(t, "Crash on start")
	n := h.seedForm(t, iss.ID)
	id := strconv.FormatInt(iss.ID, 10)

	h.pick(n, ntWorkflow, "fix-issue")
	h.p.until(10*time.Second, "fix-issue's prefill", func() bool {
		v, _ := n.fieldValue("issue")
		return n.workflow == "fix-issue" && v == id
	})
	if v, _ := n.fieldValue("kind"); v != "bug" {
		t.Errorf("kind = %q, want the issue's kind", v)
	}

	// The human types a title and a field.
	n.titleIn.SetValue("My own framing")
	for i := range n.fields {
		if n.fields[i].key == "kind" {
			n.fields[i].value = "feature"
		}
	}

	h.pick(n, ntWorkflow, "triage")
	h.p.until(10*time.Second, "triage's prefill", func() bool {
		v, _ := n.fieldValue("labels")
		return n.workflow == "triage" && v == "p1"
	})
	if got := n.titleText(); got != "My own framing" {
		t.Errorf("title = %q, the switch overwrote a typed value", got)
	}
	if v, _ := n.fieldValue("kind"); v != "feature" {
		t.Errorf("kind = %q, the switch overwrote a typed field", v)
	}
	if v, ok := n.fieldValue("issue"); ok {
		t.Errorf("fix-issue's untouched issue=%q rode along into triage", v)
	}
	if got := n.desc.Value(); !strings.Contains(got, "It crashes on start.") {
		t.Errorf("description = %q, want the untouched prefill kept", got)
	}

	// A late answer for fix-issue is not an answer about this draft.
	n.applyIssue(ntIssueMsg{
		projectID: h.projectID, workflow: "fix-issue", issueID: iss.ID,
		issue: apiclient.Issue{ID: iss.ID, Prefill: &apiclient.GitHubPrefill{Description: "STALE"}},
	})
	if n.desc.Value() == "STALE" {
		t.Error("a stale workflow's preview overwrote the description")
	}

	req := n.request()
	if req.IssueID == nil || *req.IssueID != iss.ID {
		t.Fatalf("request issue_id = %v, want %d", req.IssueID, iss.ID)
	}
	if req.Fields["kind"] != "feature" || req.Fields["labels"] != "p1" {
		t.Errorf("request fields = %v, want every row as shown", req.Fields)
	}
}

// TestIssueSeedNotesTasksAlreadyStarted is decision 21.3: a second task from
// the same issue is allowed, and the source row says how many came first.
// The issue detail then lists every root task, newest first (decision 21.1).
func TestIssueSeedNotesTasksAlreadyStarted(t *testing.T) {
	h := newNewTaskLiveHarnessWith(t, liveOptions{workflows: issueWorkflows})
	iss := h.seedIssue(t, "Crash on start")

	for i := range 2 {
		n := h.seedForm(t, iss.ID)
		if i == 1 {
			h.p.until(10*time.Second, "the count note", func() bool {
				return strings.Contains(n.rowValue(ntSource), "1 task already started from this issue")
			})
		}
		h.pick(n, ntWorkflow, "fix-issue")
		h.p.until(10*time.Second, "fix-issue's prefill", func() bool {
			_, ok := n.fieldValue("issue")
			return ok
		})
		h.sendKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
		// The create lands on the new task's workspace; the next seed must
		// come after that switch, or the switch lands on top of it.
		h.p.until(20*time.Second, "the task to be created and opened", func() bool {
			return len(h.createdTasks(t, iss.ID)) == i+1 && h.m.active == viewTask
		})
	}

	detail := issueDetailView(t, h)
	h.send(openIssueMsg{id: iss.ID})
	h.p.until(10*time.Second, "the issue's tasks", func() bool { return detail.loaded && len(detail.tasks) == 2 })
	if detail.tasks[0].ID < detail.tasks[1].ID {
		t.Errorf("linked tasks #%d, #%d; want newest first", detail.tasks[0].ID, detail.tasks[1].ID)
	}
}

// TestClosedIssueSeedWarns: a closed issue may be started from. The source
// row says it is closed, and the create's warning reaches the workspace.
func TestClosedIssueSeedWarns(t *testing.T) {
	h := newNewTaskLiveHarness(t)
	iss := h.seedIssue(t, "Already fixed?")
	if _, err := h.st.TransitionIssue(context.Background(), iss.ID, issuestate.Close,
		issuestate.Completed, nil, issuestate.Human); err != nil {
		t.Fatalf("close: %v", err)
	}
	n := h.seedForm(t, iss.ID)
	if src := n.rowValue(ntSource); !strings.Contains(src, "closed") {
		t.Errorf("source row = %q, want the issue's closed state", src)
	}
	h.sendKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	tv := h.m.views[viewTask].(*taskView)
	h.p.until(20*time.Second, "the create's warning", func() bool {
		return strings.Contains(tv.detail.actions.status, "created with warnings") &&
			strings.Contains(tv.detail.actions.status, "closed")
	})
}

// TestWorkspaceShowsTheIssue: a task started from an issue says so on the
// Overview and in Task Details, and the palette's key-less row opens the
// issue, from which esc comes back to the workspace.
func TestWorkspaceShowsTheIssue(t *testing.T) {
	h := newNewTaskLiveHarness(t)
	iss := h.seedIssue(t, "Crash on start")
	h.seedForm(t, iss.ID)
	h.sendKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	tv := h.m.views[viewTask].(*taskView)
	h.p.until(20*time.Second, "the workspace on the new task", func() bool {
		return h.m.active == viewTask && tv.detail.loaded && tv.detail.task.Issue != nil
	})

	want := "#" + strconv.FormatInt(iss.ID, 10) + " Crash on start"
	if overview := strings.Join(tv.overviewLines(160, 60), "\n"); !strings.Contains(overview, want) {
		t.Errorf("Overview does not name the issue %q:\n%s", want, overview)
	}
	details := strings.Join(tv.detailLines(160), "\n")
	if !strings.Contains(details, "Issue") || !strings.Contains(details, "Crash on start") {
		t.Errorf("Task Details has no Issue section:\n%s", details)
	}

	extras := tv.paletteExtras()
	if len(extras) != 1 || extras[0].action == nil || extras[0].key != "" {
		t.Fatalf("palette extras = %+v, want one key-less row", extras)
	}
	h.p.push(extras[0].action)
	detail := issueDetailView(t, h)
	h.p.until(10*time.Second, "the issue screen", func() bool {
		return h.m.active == viewIssue && detail.loaded && detail.issue.ID == iss.ID
	})
	h.sendKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	h.p.until(5*time.Second, "esc back to the workspace", func() bool { return h.m.active == viewTask })
}
