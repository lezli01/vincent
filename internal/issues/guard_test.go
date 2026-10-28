package issues

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// guardFixture is one project with two imported issues in the hand_off
// lane, each delivered by its own main task, and a live handoff over the
// first with grant.
type guardFixture struct {
	svc            *Service
	st             *store.Store
	pid            int64
	claimed, other *store.Issue
	// delivering is claimed's delivering task, otherDelivering other's.
	delivering, otherDelivering *store.Task
	handoff                     *store.Handoff
}

func newGuardFixture(t *testing.T, grant ...string) guardFixture {
	t.Helper()
	svc, st, pid := newService(t)
	f := guardFixture{svc: svc, st: st, pid: pid}
	f.claimed, f.delivering = handedOffImport(t, st, pid, 1)
	f.other, f.otherDelivering = handedOffImport(t, st, pid, 2)
	gj, err := store.GrantJSON(grant)
	if err != nil {
		t.Fatal(err)
	}
	f.handoff = &store.Handoff{CreatedBy: store.HandoffByHuman, GrantJSON: gj}
	task := guardTask(pid, "handoff", store.TaskRunning)
	if err := st.CreateHandoff(t.Context(), f.handoff, []int64{f.claimed.ID}, task, nil); err != nil {
		t.Fatalf("CreateHandoff: %v", err)
	}
	return f
}

func guardTask(pid int64, title string, state store.TaskState) *store.Task {
	return &store.Task{
		ProjectID: pid, Title: title, WorkflowName: "adhoc", WorkflowSnapshot: "steps: []",
		BaseBranch: "main", BranchName: "vincent/0-" + title, State: state,
	}
}

// handedOffImport is a live GitHub import #n whose main task finished done
// with a change delivery: an issue in the hand_off lane.
func handedOffImport(t *testing.T, st *store.Store, pid int64, n int) (*store.Issue, *store.Task) {
	t.Helper()
	ctx := t.Context()
	key := fmt.Sprintf("I_%d", n)
	iss, _, err := st.UpsertRemoteIssue(ctx, store.RemoteIssue{
		ProjectID: pid, Provider: "github", RemoteKey: key, Repo: "o/r", Number: n,
		Title: key, State: issuestate.Open,
	}, issuestate.Sync)
	if err != nil {
		t.Fatal(err)
	}
	task := guardTask(pid, key, store.TaskRunning)
	task.IssueID, task.IssueWorktree = &iss.ID, store.IssueWorktreeMain
	if err := st.CreateTask(ctx, task, nil); err != nil {
		t.Fatal(err)
	}
	d := &store.Delivery{
		Kind: store.DeliveryChange, Title: key, Body: "b", HeadSHA: "abc",
		Branch: task.BranchName, Base: "main", RecordedAt: time.Now().UTC().Truncate(time.Second),
	}
	done, _, err := st.TransitionTask(ctx, task.ID, store.TaskRunning, store.TaskDone, store.TaskChange{Delivery: d})
	if err != nil {
		t.Fatal(err)
	}
	return iss, done
}

func ptr(v int64) *int64 { return &v }

// TestForgeWriteGuard is task 137.7's one guard: a human is allowed as
// itself; an agent only as a live handoff's task, for a capability its
// grant names, on a target in its scope — and then as `handoff`.
func TestForgeWriteGuard(t *testing.T) {
	f := newGuardFixture(t, "issue.close", "issue.comment", "pull.merge")
	ctx := t.Context()
	handoffTask := ptr(f.handoff.TaskID)

	if v, err := f.svc.ForgeWrite(ctx, Caller{By: issuestate.Human}, store.CapPullMerge, Target{TaskID: 999}); err != nil || v.By != issuestate.Human {
		t.Errorf("human = %+v, %v; want allowed as human", v, err)
	}
	allowed := []struct {
		name string
		cap  store.Capability
		t    Target
	}{
		{"close an item's issue", store.CapIssueClose, Target{IssueID: f.claimed.ID}},
		{"comment on an item's issue", store.CapIssueComment, Target{IssueID: f.claimed.ID}},
		{"merge the handoff task's pull", store.CapPullMerge, Target{TaskID: f.handoff.TaskID}},
		{"merge an item's delivering task's pull", store.CapPullMerge, Target{TaskID: f.delivering.ID}},
	}
	for _, tc := range allowed {
		v, err := f.svc.ForgeWrite(ctx, Caller{By: issuestate.Agent, TaskID: handoffTask}, tc.cap, tc.t)
		if err != nil || v.By != issuestate.Handoff || v.HandoffID != f.handoff.ID {
			t.Errorf("%s = %+v, %v; want allowed as handoff %d", tc.name, v, err, f.handoff.ID)
		}
	}
	refused := []struct {
		name string
		c    Caller
		cap  store.Capability
		t    Target
	}{
		{"no acting task (a chat marker)", Caller{By: issuestate.Agent}, store.CapIssueClose, Target{IssueID: f.claimed.ID}},
		{"a task that is no handoff's", Caller{By: issuestate.Agent, TaskID: ptr(f.delivering.ID)}, store.CapIssueClose, Target{IssueID: f.claimed.ID}},
		{"an issue that is not an item", Caller{By: issuestate.Agent, TaskID: handoffTask}, store.CapIssueClose, Target{IssueID: f.other.ID}},
		{"a pull outside the scope", Caller{By: issuestate.Agent, TaskID: handoffTask}, store.CapPullMerge, Target{TaskID: f.otherDelivering.ID}},
		{"a capability not granted", Caller{By: issuestate.Agent, TaskID: handoffTask}, store.CapPullCreate, Target{TaskID: f.handoff.TaskID}},
	}
	for _, tc := range refused {
		if _, err := f.svc.ForgeWrite(ctx, tc.c, tc.cap, tc.t); !errors.Is(err, ErrForgeWriteNeedsHuman) {
			t.Errorf("%s = %v, want ErrForgeWriteNeedsHuman", tc.name, err)
		}
	}

	if _, _, err := f.st.TransitionTask(ctx, f.handoff.TaskID, store.TaskRunning, store.TaskDone, store.TaskChange{}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.ForgeWrite(ctx, Caller{By: issuestate.Agent, TaskID: handoffTask}, store.CapIssueClose,
		Target{IssueID: f.claimed.ID}); !errors.Is(err, ErrForgeWriteNeedsHuman) {
		t.Errorf("a settled handoff = %v, want ErrForgeWriteNeedsHuman", err)
	}
}

// TestGrantedIssueWrites: a granted close of an imported item is made as
// `handoff`, carries handoff_id and enqueues set_state; a granted comment
// is stored and enqueues add_comment; an ungranted agent is refused, and a
// local issue needs no grant and stays `agent`.
func TestGrantedIssueWrites(t *testing.T) {
	f := newGuardFixture(t, "issue.close", "issue.comment")
	ctx := t.Context()
	agent := Caller{By: issuestate.Agent, TaskID: ptr(f.handoff.TaskID)}

	by, wctx, err := f.svc.IssueWriteAs(ctx, agent, store.CapIssueClose, f.claimed.ID)
	if err != nil || by != issuestate.Handoff {
		t.Fatalf("IssueWriteAs close = %s, %v; want handoff", by, err)
	}
	if _, err := f.svc.Close(wctx, by, f.claimed.ID, "", nil); err != nil {
		t.Fatalf("granted close: %v", err)
	}
	ev := lastEvent(t, f.st, store.EventIssueStateChanged)
	if ev.payload["by"] != string(issuestate.Handoff) || ev.payload["handoff_id"] != float64(f.handoff.ID) {
		t.Errorf("state event payload = %v, want by handoff and handoff_id %d", ev.payload, f.handoff.ID)
	}
	writes, err := f.st.IssueWrites(ctx, f.claimed.ID)
	if err != nil || len(writes) != 1 || writes[0].Op != store.OutboxOpSetState || writes[0].Origin != issuestate.Handoff {
		t.Fatalf("writes after the close = %+v, %v; want one set_state by handoff", writes, err)
	}

	by, wctx, err = f.svc.IssueWriteAs(ctx, agent, store.CapIssueComment, f.claimed.ID)
	if err != nil || by != issuestate.Handoff {
		t.Fatalf("IssueWriteAs comment = %s, %v; want handoff", by, err)
	}
	c, err := f.svc.Comment(wctx, by, f.claimed.ID, "task 1", "landed")
	if err != nil {
		t.Fatalf("granted comment: %v", err)
	}
	writes, _ = f.st.IssueWrites(ctx, f.claimed.ID)
	if len(writes) != 2 || writes[1].Op != store.OutboxOpAddComment || writes[1].CommentID == nil || *writes[1].CommentID != c.ID {
		t.Fatalf("writes after the comment = %+v; want an add_comment for comment %d", writes, c.ID)
	}
	if got, _ := f.svc.Get(ctx, f.claimed.ID); got.Sync == nil || got.Sync.Op != store.OutboxOpSetState {
		t.Errorf("issue sync = %+v, want the state write, not the comment's", got.Sync)
	}

	// A human's comment on the same thread is still refused (decision 24.3).
	if _, err := f.svc.Comment(ctx, issuestate.Human, f.claimed.ID, "me", "hi"); !errors.Is(err, ErrMirrored) {
		t.Errorf("human comment = %v, want ErrMirrored", err)
	}
	// An ungranted agent is refused before anything is written.
	if _, _, err := f.svc.IssueWriteAs(ctx, Caller{By: issuestate.Agent}, store.CapIssueComment, f.claimed.ID); !errors.Is(err, ErrForgeWriteNeedsHuman) {
		t.Errorf("ungranted comment = %v, want ErrForgeWriteNeedsHuman", err)
	}
	// A local issue needs no grant: the actor stays agent.
	local := mustCreate(t, f.svc, CreateInput{ProjectID: f.pid, Title: "local"})
	by, _, err = f.svc.IssueWriteAs(ctx, Caller{By: issuestate.Agent}, store.CapIssueClose, local.ID)
	if err != nil || by != issuestate.Agent {
		t.Errorf("local close as = %s, %v; want agent", by, err)
	}
}

type decodedEvent struct{ payload map[string]any }

func lastEvent(t *testing.T, st *store.Store, typ string) decodedEvent {
	t.Helper()
	evs, err := st.ListEvents(context.Background(), store.EventFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for i := len(evs) - 1; i >= 0; i-- {
		if evs[i].Type == typ {
			var payload map[string]any
			if err := json.Unmarshal(evs[i].Payload, &payload); err != nil {
				t.Fatal(err)
			}
			return decodedEvent{payload: payload}
		}
	}
	t.Fatalf("no %s event", typ)
	return decodedEvent{}
}
