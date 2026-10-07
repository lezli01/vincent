package trigger

import (
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// laneDoc is an armed `type: issues` trigger whose created task's title
// spells out the lane keys, so a replay and a dry run show them.
func laneDoc(id, match string) string {
	return `id: ` + id + `
enabled: true
source:
  type: issues
  project: 1
match:
` + match + `
action:
  type: create_task
  title: '{{ .Event.action }} {{ .Event.from_lane }}>{{ .Event.lane }} by {{ .Event.by }}{{ if eq .Event.by "task" }} task {{ .Event.task_id }}{{ end }}'
`
}

// laneTasks numbers rootTask's branches, which must be unique.
var laneTasks atomic.Int64

// rootTask creates a queued root task on the issue, which moves its lane.
func (h *harness) rootTask(issueID int64) *store.Task {
	h.t.Helper()
	task := &store.Task{
		ProjectID: 1, Title: "work", WorkflowName: "adhoc", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", BranchName: "vincent/lane-" + itoa(laneTasks.Add(1)),
		State: store.TaskQueued, IssueID: &issueID,
	}
	if err := h.st.CreateTask(h.t.Context(), task, nil); err != nil {
		h.t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func (h *harness) moveTask(id int64, from, to store.TaskState) {
	h.t.Helper()
	if _, _, err := h.st.TransitionTask(h.t.Context(), id, from, to, store.TaskChange{}); err != nil {
		h.t.Fatalf("task %d %s → %s: %v", id, from, to, err)
	}
}

func (h *harness) moveIssue(id int64, action issuestate.Action, reason issuestate.Reason) {
	h.t.Helper()
	if _, err := h.st.TransitionIssue(h.t.Context(), id, action, reason, nil, issuestate.Human); err != nil {
		h.t.Fatalf("TransitionIssue %s: %v", action, err)
	}
}

// replayTitles is the titles of every task the triggers created.
func (h *harness) replayTitles() []string {
	var out []string
	for _, r := range h.api.requests() {
		out = append(out, r.body.Title)
	}
	return out
}

// TestIssuesLaneChangedPayload: each issue.lane_changed yields one
// lane_changed carrying the payload's lanes; a task-caused move reads
// `by: task` with its task_id, a close or reopen keeps its actor.
func TestIssuesLaneChangedPayload(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	iss := h.localIssue("lanes")
	task := h.rootTask(iss.ID)
	h.moveTask(task.ID, store.TaskQueued, store.TaskRunning)
	h.moveTask(task.ID, store.TaskRunning, store.TaskDone)
	h.moveIssue(iss.ID, issuestate.Close, issuestate.Completed)
	if _, err := h.st.TransitionIssue(ctx, iss.ID, issuestate.Reopen, "", nil, issuestate.Agent); err != nil {
		t.Fatal(err)
	}

	evs, err := h.st.ListEvents(ctx, store.EventFilter{Types: []string{store.EventIssueLaneChanged}})
	if err != nil {
		t.Fatal(err)
	}
	var got []Event
	for i := range evs {
		mapped, err := issueEvents(ctx, h.st, &evs[i])
		if err != nil {
			t.Fatal(err)
		}
		if len(mapped) != 1 {
			t.Fatalf("event %d mapped to %+v, want one", evs[i].ID, mapped)
		}
		if want := "issue:" + itoa(iss.ID) + ":lane_changed:" + itoa(evs[i].ID); mapped[0]["id"] != want {
			t.Errorf("id = %v, want %s", mapped[0]["id"], want)
		}
		got = append(got, mapped...)
	}
	want := []struct {
		from, to, by string
		task         bool
	}{
		{"open", "in_progress", "task", true},
		{"in_progress", "hand_off", "task", true},
		{"hand_off", "done", "human", false},
		{"done", "hand_off", "agent", false},
	}
	if len(got) != len(want) {
		t.Fatalf("mapped = %+v, want %d lane moves", got, len(want))
	}
	for i, w := range want {
		ev := got[i]
		if ev["action"] != ActionLaneChanged || ev["from_lane"] != w.from || ev["lane"] != w.to || ev["by"] != w.by ||
			ev["issue_id"] != iss.ID || ev["project_id"] != int64(1) || ev["author"] != "me" {
			t.Errorf("move %d = %+v, want %s → %s by %s", i, ev, w.from, w.to, w.by)
		}
		if _, ok := ev["from"]; ok {
			t.Errorf("move %d carries the state keys: %+v", i, ev)
		}
		if tid, ok := ev["task_id"]; ok != w.task || (ok && tid != task.ID) {
			t.Errorf("move %d task_id = %v (%v), want present %v as %d", i, tid, ok, w.task, task.ID)
		}
	}

	// An issue deleted before judging yields nothing.
	h.moveTask(task.ID, store.TaskDone, store.TaskArchived)
	if err := h.st.DeleteTaskCascade(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	if err := h.st.DeleteIssue(ctx, iss.ID, issuestate.Human); err != nil {
		t.Fatal(err)
	}
	if mapped, err := issueEvents(ctx, h.st, &evs[0]); err != nil || len(mapped) != 0 {
		t.Errorf("deleted issue mapped = %+v, %v", mapped, err)
	}
}

// TestIssuesLaneHandOffFires: `lane: hand_off` fires when a root task of
// the issue finishes done, and not on open → in_progress or hand_off →
// in_progress; `lane: done` fires on the close and `from_lane: done` on the
// reopen.
func TestIssuesLaneHandOffFires(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.write("handoff", laneDoc("handoff", "  action: lane_changed\n  lane: hand_off\n  by: task\n"))
	h.write("done", laneDoc("done", "  action: [closed, lane_changed]\n  lane: done\n  by: [human, task]\n"))
	h.write("back", laneDoc("back", "  action: lane_changed\n  from_lane: done\n  by: [human, agent, task]\n"))
	h.m.pollIssues(ctx)

	iss := h.localIssue("hand me off")
	first := h.rootTask(iss.ID)
	h.moveTask(first.ID, store.TaskQueued, store.TaskRunning)
	h.m.pollIssues(ctx)
	if titles := h.replayTitles(); len(titles) != 0 {
		t.Fatalf("open → in_progress fired %v", titles)
	}
	h.moveTask(first.ID, store.TaskRunning, store.TaskDone)
	h.m.pollIssues(ctx)
	second := h.rootTask(iss.ID)
	h.m.pollIssues(ctx)
	h.moveTask(second.ID, store.TaskQueued, store.TaskAborted)
	h.moveIssue(iss.ID, issuestate.Close, issuestate.Completed)
	h.moveIssue(iss.ID, issuestate.Reopen, "")
	h.m.pollIssues(ctx)

	want := []string{
		"lane_changed in_progress>hand_off by task task " + itoa(first.ID),
		"lane_changed in_progress>hand_off by task task " + itoa(second.ID),
		"lane_changed hand_off>done by human",
		"lane_changed done>hand_off by human",
	}
	// Triggers are polled in registry order, so compare the set.
	got := h.replayTitles()
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("replays =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	// The ledger records each delivery under its lane_changed id.
	for _, id := range []string{"handoff", "done", "back"} {
		for _, r := range h.ledger(id) {
			if r.Outcome == store.DeliveryFired && !strings.HasPrefix(r.EventID, "issue:"+itoa(iss.ID)+":lane_changed:") {
				t.Errorf("%s fired %q", id, r.EventID)
			}
		}
	}
	if n := count(h.ledger("handoff"), store.DeliveryFired); n != 2 {
		t.Errorf("handoff fired %d, want 2: %+v", n, h.ledger("handoff"))
	}
}

// TestIssuesLaneChangedIsOptIn: a trigger with no match.action never sees a
// lane move — not even as a filtered ledger row — and still fires closed
// exactly once on a close (task 134.7 decision 3).
func TestIssuesLaneChangedIsOptIn(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.write("any", issuesDoc("any", "allowed_actors: [me]\n"))
	h.m.pollIssues(ctx)
	iss := h.localIssue("quiet")
	task := h.rootTask(iss.ID)
	h.moveTask(task.ID, store.TaskQueued, store.TaskAborted)
	h.moveIssue(iss.ID, issuestate.Close, issuestate.NotPlanned)
	h.m.pollIssues(ctx)

	var actions []string
	for _, r := range h.ledger("any") {
		parts := strings.Split(r.EventID, ":")
		actions = append(actions, parts[2])
	}
	slices.Sort(actions)
	if want := []string{"closed", "opened"}; !slices.Equal(actions, want) {
		t.Errorf("ledger actions = %v, want %v", actions, want)
	}
	if n := count(h.ledger("any"), store.DeliveryFired); n != 2 {
		t.Errorf("fired = %d, want opened and closed", n)
	}
}

// TestIssuesLanePollDry: the live-poll dry run shows the lane_changed action
// and its keys in the rendered request.
func TestIssuesLanePollDry(t *testing.T) {
	h := newHarness(t)
	ctx := t.Context()
	h.write("handoff", laneDoc("handoff", "  action: lane_changed\n  by: task\n"))
	h.m.pollIssues(ctx)
	iss := h.localIssue("dry")
	task := h.rootTask(iss.ID)
	dry, err := h.m.PollDry(ctx, "handoff")
	if err != nil || dry.Seed || len(dry.Events) != 2 {
		t.Fatalf("PollDry = %+v, %v", dry, err)
	}
	// The opened is judged and filtered; the lane move is the one to fire.
	j := dry.Events[1]
	if !strings.Contains(j.EventID, ":lane_changed:") || j.Outcome != store.DeliveryFired || j.Action == nil {
		t.Fatalf("judgement = %+v", j)
	}
	if body, _ := j.Action.Body.(*CreateBody); body == nil || body.Title != "lane_changed open>in_progress by task task "+itoa(task.ID) {
		t.Errorf("rendered = %+v", j.Action.Body)
	}
}
