package tui

import (
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

func i64(n int64) *int64 { return &n }

// handoffBoardFixture is the board with one pending hand-off card, one
// claimed by H7 with its task paused, and one idle in progress.
func handoffBoardFixture() *issuesView {
	v := newIssuesView()
	v.client = offlineClient()
	v.project = projectSel{id: 1, name: "api"}
	v.issues = []apiclient.Issue{
		{
			ID: 10, ProjectID: 1, Title: "Ready one", State: "open", Lane: "hand_off", TaskCount: 1,
			Delivery: &apiclient.Delivery{Kind: "change"},
			Handoff:  &apiclient.IssueHandoff{State: markerPending},
		},
		{
			ID: 11, ProjectID: 1, Title: "Claimed one", State: "open", Lane: "hand_off", TaskCount: 1,
			Delivery: &apiclient.Delivery{Kind: "reply"},
			Handoff:  &apiclient.IssueHandoff{State: markerClaimed, HandoffID: i64(7), TaskID: i64(70), Paused: true},
		},
		{
			ID: 12, ProjectID: 1, Title: "Idle one", State: "open", Lane: "in_progress", TaskCount: 1,
			Handoff: &apiclient.IssueHandoff{State: markerIdle},
		},
	}
	v.loaded = true
	v.clampCursor()
	return v
}

// cursorOn moves the board's cursor to issue id.
func cursorOn(t *testing.T, v *issuesView, id int64) {
	t.Helper()
	for i, row := range v.lines() {
		if !row.header && !row.none && row.issue.ID == id {
			v.cursor = i
			v.remember(v.lines())
			return
		}
	}
	t.Fatalf("issue #%d is not on the board", id)
}

func TestIssueHandoffCell(t *testing.T) {
	v := handoffBoardFixture()
	for _, tc := range []struct {
		iss  apiclient.Issue
		want string
	}{
		{v.issues[0], "change"},
		{v.issues[1], "reply · claimed (H7) · paused"},
		{v.issues[2], "idle"},
		{apiclient.Issue{Lane: "hand_off", Handoff: &apiclient.IssueHandoff{State: markerWaiting, HandoffID: i64(3)}}, "waiting"},
		{apiclient.Issue{Lane: "hand_off", Delivery: &apiclient.Delivery{Kind: "none"}, Handoff: &apiclient.IssueHandoff{State: markerDelivered}}, "none · delivered"},
		{apiclient.Issue{Lane: "open"}, ""},
	} {
		if got := issueHandoffCell(tc.iss); got != tc.want {
			t.Errorf("cell = %q, want %q", got, tc.want)
		}
	}
	// The marker survives a narrow row; the tags give way first.
	iss := v.issues[1]
	iss.Labels = []string{"backend", "urgent"}
	line := ansi.Strip(issueLine(iss, 70, false, true))
	if !strings.Contains(line, "claimed (H7)") || strings.Contains(line, "[backend]") {
		t.Errorf("narrow row %q: want the marker kept and the tags shed", line)
	}
}

func TestIssueHandoffSelection(t *testing.T) {
	v := handoffBoardFixture()
	cursorOn(t, v, 11)
	v.update(registryKey(t, issueSelectKey))
	if len(v.picked) != 0 || !strings.Contains(v.note, "already in handoff H7") {
		t.Fatalf("a claimed card was selected (picked %v, note %q)", v.picked, v.note)
	}
	cursorOn(t, v, 10)
	v.update(registryKey(t, issueSelectKey))
	if !v.picked[10] {
		t.Fatal("space did not select the pending card")
	}
	if h := ansi.Strip(v.headerLine(120)); !strings.Contains(h, "1 selected") {
		t.Errorf("header %q does not count the selection", h)
	}
	v.update(registryKey(t, issueSelectAllKey))
	if len(v.picked) != 0 {
		t.Fatal("V with a selection did not clear it")
	}
	v.update(registryKey(t, issueSelectAllKey))
	if !v.picked[10] || len(v.picked) != 1 {
		t.Fatalf("V selected %v, want only the pending card", v.picked)
	}
}

func TestIssueHandoffTargets(t *testing.T) {
	v := handoffBoardFixture()
	cursorOn(t, v, 10)
	ids, note := v.handoffTargets()
	if note != "" || !slices.Equal(ids, []int64{10}) {
		t.Fatalf("cursor fallback = %v %q, want [10]", ids, note)
	}
	cursorOn(t, v, 12)
	if _, note := v.handoffTargets(); !strings.Contains(note, "not in the hand-off lane") {
		t.Fatalf("an in-progress cursor gave note %q", note)
	}
	v.picked = map[int64]bool{10: true}
	if ids, _ := v.handoffTargets(); !slices.Equal(ids, []int64{10}) {
		t.Fatalf("selection = %v, want [10]", ids)
	}
}

func TestHandoffFormRequest(t *testing.T) {
	v := handoffBoardFixture()
	v.openHandoff(handoffWorkflowsMsg{projectID: 1, issueIDs: []int64{10}, workflows: []apiclient.WorkflowEntry{
		{Name: "default"}, {Name: "integrate", Role: "handoff"}, {Name: "ship", Role: "handoff"},
	}})
	f := v.handoff
	if f == nil || !slices.Equal(f.workflows, []string{"integrate", "ship"}) {
		t.Fatalf("form = %+v, want the two handoff workflows", f)
	}
	f.updateKey(registryKey(t, "right")) // workflow → ship
	f.updateKey(registryKey(t, "down"))  // issue.comment
	f.updateKey(registryKey(t, "down"))  // issue.close
	f.updateKey(registryKey(t, "space"))
	for range hfTitle - 2 {
		f.updateKey(registryKey(t, "down"))
	}
	f.title.SetValue("Integrate the batch")
	f.updateKey(registryKey(t, "down")) // paused
	f.updateKey(registryKey(t, "space"))
	req := f.request()
	if req.Workflow != "ship" || !slices.Equal(req.IssueIDs, []int64{10}) || !slices.Equal(req.Grant, []string{"issue.close"}) ||
		req.Title != "Integrate the batch" || !req.Paused || req.ProjectID != 1 {
		t.Fatalf("request = %+v", req)
	}
	if f.idemKey == "" {
		t.Fatal("the form carries no idempotency key")
	}
}

func TestHandoffFormRefusesWithoutWorkflow(t *testing.T) {
	v := handoffBoardFixture()
	v.openHandoff(handoffWorkflowsMsg{projectID: 1, issueIDs: []int64{10}, workflows: []apiclient.WorkflowEntry{{Name: "default"}}})
	if v.handoff != nil || !strings.Contains(v.note, "no `role: handoff` workflow") {
		t.Fatalf("form opened with no handoff workflow (note %q)", v.note)
	}
}

func TestIssueHandoffLiveBindings(t *testing.T) {
	v := handoffBoardFixture()
	has := func(rows []binding, key string) bool {
		return slices.ContainsFunc(rows, func(b binding) bool { return b.key == key })
	}
	rows := bindingsFor(ctxIssues)
	cursorOn(t, v, 12)
	if live := v.liveBindings(rows); !has(live, ".") || has(live, ",") || has(live, "I") {
		t.Error("on an idle card: want ready, not return or handoff")
	}
	cursorOn(t, v, 11)
	if live := v.liveBindings(rows); has(live, ".") || !has(live, ",") || has(live, "I") {
		t.Error("on a claimed card: want return only")
	}
	cursorOn(t, v, 10)
	if live := v.liveBindings(rows); !has(live, ".") || has(live, ",") || !has(live, "I") {
		t.Error("on a pending card: want ready (unready) and handoff")
	}
}

func TestNewTaskRefusedOnClaimedIssue(t *testing.T) {
	v := handoffBoardFixture()
	cursorOn(t, v, 11)
	_, cmd := v.update(registryKey(t, "a"))
	if cmd != nil || v.note != "issue #11 is in handoff H7 — return it first" {
		t.Fatalf("a on a claimed issue: cmd %v, note %q", cmd != nil, v.note)
	}
}

func TestIssueDetailHandoffSection(t *testing.T) {
	v := issueFixture()
	v.loaded = true
	v.issue.Handoffs = []apiclient.IssueHandoffEntry{
		{HandoffID: 9, TaskID: 90, State: "settled", Outcome: "returned", OutcomeBy: "human"},
	}
	page := ansi.Strip(strings.Join(v.pageLines(100), "\n"))
	if !strings.Contains(page, "Handoff") || !strings.Contains(page, "H9") || !strings.Contains(page, "#90") ||
		!strings.Contains(page, "returned by human") {
		t.Fatalf("page has no Handoff section row:\n%s", page)
	}
}

func TestHandoffReasonWords(t *testing.T) {
	err := &apiclient.Error{Status: 409, Code: "conflict", Details: map[string]string{
		"reason": "issue_not_eligible", "issue_id": "4", "handoff_id": "2",
	}}
	if got := handoffReasonWords(err); got != "issue #4 is already in handoff H2" {
		t.Errorf("words = %q", got)
	}
}

func init() {
	panelKeyProbes[ctxHandoffForm] = map[string]func(*testing.T){}
	panelKeyProbes[ctxIssues][issueSelectKey] = func(t *testing.T) {
		v := handoffBoardFixture()
		cursorOn(t, v, 10)
		v.update(registryKey(t, issueSelectKey))
		if !v.picked[10] {
			t.Fatal("space did not select the pending card")
		}
	}
	panelKeyProbes[ctxIssues][issueSelectAllKey] = func(t *testing.T) {
		v := handoffBoardFixture()
		v.update(registryKey(t, issueSelectAllKey))
		if !v.picked[10] {
			t.Fatal("V did not select the pending cards")
		}
	}
	panelKeyProbes[ctxIssues]["I"] = func(t *testing.T) {
		v := handoffBoardFixture()
		cursorOn(t, v, 10)
		_, cmd := v.update(registryKey(t, "I"))
		if cmd == nil {
			t.Fatal("I did not read the project's workflows for the form")
		}
	}
	panelKeyProbes[ctxIssues]["."] = func(t *testing.T) {
		v := handoffBoardFixture()
		cursorOn(t, v, 12)
		v.update(registryKey(t, "."))
		if v.w.act == nil || v.w.act.action != issueActReady {
			t.Fatal(". on an idle issue did not mark it ready")
		}
	}
	panelKeyProbes[ctxIssues][","] = func(t *testing.T) {
		v := handoffBoardFixture()
		cursorOn(t, v, 11)
		v.update(registryKey(t, ","))
		if v.w.act == nil || v.w.act.action != issueActReturn || v.w.act.stage != iaConfirm {
			t.Fatal(", on a claimed issue did not ask before returning it")
		}
	}
	panelKeyProbes[ctxIssue]["."] = func(t *testing.T) {
		v := issueFixture()
		v.loaded = true
		v.issue.Lane, v.issue.Handoff = "hand_off", &apiclient.IssueHandoff{State: markerPending}
		v.update(registryKey(t, "."))
		if v.w.act == nil || v.w.act.action != issueActUnready {
			t.Fatal(". on a pending issue did not mark it not ready")
		}
	}
	panelKeyProbes[ctxIssue][","] = func(t *testing.T) {
		v := issueFixture()
		v.loaded = true
		v.issue.Handoff = &apiclient.IssueHandoff{State: markerWaiting, HandoffID: i64(3)}
		v.update(registryKey(t, ","))
		if v.w.act == nil || v.w.act.action != issueActReturn {
			t.Fatal(", on a waiting issue did not ask to return it")
		}
	}
	form := func() *handoffForm {
		return newHandoffForm(offlineClient(), 1, []int64{10}, []string{"integrate"})
	}
	panelKeyProbes[ctxHandoffForm]["down"] = func(t *testing.T) {
		f := form()
		f.updateKey(registryKey(t, "down"))
		if f.row != 1 {
			t.Fatal("down did not move to the first grant")
		}
	}
	panelKeyProbes[ctxHandoffForm]["space"] = func(t *testing.T) {
		f := form()
		f.row = 1
		f.updateKey(registryKey(t, "space"))
		if !f.grant[0] {
			t.Fatal("space did not tick the grant")
		}
	}
	panelKeyProbes[ctxHandoffForm]["ctrl+s"] = func(t *testing.T) {
		f := form()
		if cmd, _ := f.updateKey(registryKey(t, "ctrl+s")); cmd == nil || !f.busy {
			t.Fatal("ctrl+s did not submit")
		}
	}
	panelKeyProbes[ctxHandoffForm]["esc"] = func(t *testing.T) {
		f := form()
		if _, closed := f.updateKey(registryKey(t, "esc")); !closed {
			t.Fatal("esc did not close an untouched form")
		}
	}
}

// taskEventFor is a task event of type typ for task id in project 1.
func taskEventFor(typ string, id int64) apiclient.EventNote {
	return apiclient.EventNote{Event: apiclient.Event{
		ID: 1, Type: typ, ProjectID: i64(1), TaskID: i64(id), Payload: []byte(`{"to":"paused"}`),
	}}
}

// TestIssuesBoardRelistsOnHandoffTaskPause is review F4 on #872: pausing or
// resuming a handoff's task is a task.state_changed naming only the task, so
// a card's `· paused` went stale until an unrelated event re-listed.
func TestIssuesBoardRelistsOnHandoffTaskPause(t *testing.T) {
	for _, tc := range []struct {
		name string
		note apiclient.EventNote
		want bool
	}{
		{"the claiming handoff's task changes state", taskEventFor("task.state_changed", 70), true},
		{"another task changes state", taskEventFor("task.state_changed", 71), false},
		{"the handoff's task only advances a step", taskEventFor("task.step_advanced", 70), false},
	} {
		v := handoffBoardFixture()
		if got := v.updateNote(tc.note) != nil; got != tc.want {
			t.Errorf("%s: refresh %v, want %v", tc.name, got, tc.want)
		}
	}
}

// TestIssueScreenRefreshesOnHandoffTaskPause is the issue screen's half of
// review F4: its marker names the handoff's task, which no Handoffs entry
// carries while the handoff is live.
func TestIssueScreenRefreshesOnHandoffTaskPause(t *testing.T) {
	v := issueFixture()
	v.loaded = true
	v.issue.Handoff = &apiclient.IssueHandoff{State: markerClaimed, HandoffID: i64(7), TaskID: i64(70)}
	note := taskEventFor("task.state_changed", 70)
	note.Event.ProjectID = i64(2)
	if v.updateNote(note) == nil {
		t.Error("a pause of the marker's handoff task did not refresh the issue screen")
	}
	v = issueFixture()
	v.loaded = true
	note = taskEventFor("task.state_changed", 99)
	if v.updateNote(note) != nil {
		t.Error("an unrelated task's state change refreshed the issue screen")
	}
}
