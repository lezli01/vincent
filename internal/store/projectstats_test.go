package store

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/taskstate"
)

func mustTask(t *testing.T, s *Store, task *Task) *Task {
	t.Helper()
	if err := s.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("CreateTask(%s): %v", task.Title, err)
	}
	return task
}

func laneOf(parent *Task, title string, state TaskState) *Task {
	lane := newTask(parent.ProjectID, title, state)
	lane.BranchName = "vincent/0-" + title
	lane.ParentTaskID = &parent.ID
	idx := 0
	lane.ParentStepIndex = &idx
	lane.LaneID = title
	return lane
}

func mustChat(t *testing.T, s *Store, projectID int64, title string, state chatstate.State) {
	t.Helper()
	c := &Chat{
		ProjectID: projectID, Title: title, Agent: "claude", State: state,
		Branch: "vincent/c-" + title, BaseBranch: "main", PermissionMode: "full_auto",
	}
	if err := s.CreateChat(t.Context(), c); err != nil {
		t.Fatalf("CreateChat(%s): %v", title, err)
	}
}

// TestProjectStatsCountsEveryTaskRow is task 132 decision 21 in numbers: the
// task figures cover lanes as well as roots, so they line up with
// slots_used, and a blocked lane under an awaiting_children parent is one
// attention, not two.
func TestProjectStatsCountsEveryTaskRow(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	empty := testProject(t, s, "empty")

	parent := mustTask(t, s, newTask(p.ID, "parent", TaskAwaitingChildren))
	mustTask(t, s, laneOf(parent, "lane-blocked", TaskBlocked))
	mustTask(t, s, laneOf(parent, "lane-running", TaskRunning))
	mustTask(t, s, newTask(p.ID, "queued", TaskQueued))
	mustTask(t, s, newTask(p.ID, "gate", TaskAwaitingGate))
	mustTask(t, s, newTask(p.ID, "done", TaskDone))
	mustTask(t, s, newTask(p.ID, "archived", TaskArchived))

	stats, err := s.ProjectStats(ctx)
	if err != nil {
		t.Fatalf("ProjectStats: %v", err)
	}
	got := stats[p.ID]
	want := map[taskstate.State]int{
		taskstate.AwaitingChildren: 1, taskstate.Blocked: 1, taskstate.Running: 1,
		taskstate.Queued: 1, taskstate.AwaitingGate: 1, taskstate.Done: 1,
	}
	if !reflect.DeepEqual(got.TasksByState, want) {
		t.Errorf("TasksByState = %v, want %v (lanes in, zeros and archived out)", got.TasksByState, want)
	}
	// parent, both lanes, queued, gate: five unsettled rows.
	if got.TasksActive != 5 {
		t.Errorf("TasksActive = %d, want 5", got.TasksActive)
	}
	// The blocked lane and the gate; the parent is not counted again.
	if got.TasksAttention != 2 {
		t.Errorf("TasksAttention = %d, want 2", got.TasksAttention)
	}
	if got.LastActivityAt == nil {
		t.Error("LastActivityAt is nil for a project with tasks")
	}

	// The slot figure is untouched by any of this: the running lane alone.
	slots, err := s.SlotCountsByProject(ctx)
	if err != nil {
		t.Fatalf("SlotCountsByProject: %v", err)
	}
	if slots[p.ID] != 1 {
		t.Errorf("slots = %d, want 1", slots[p.ID])
	}

	if e, ok := stats[empty.ID]; ok {
		t.Errorf("an empty project has stats %+v; want it absent (read as zero)", e)
	}
}

// TestProjectStatsIssuesChatsSyncAndActivity covers the non-task figures,
// per project, and that they stay in their own project.
func TestProjectStatsIssuesChatsSyncAndActivity(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p1 := testProject(t, s, "p1")
	p2 := testProject(t, s, "p2")

	local := mustCreateIssue(t, s, NewIssue{ProjectID: p1.ID, Title: "local"})
	mustUpsertRemote(t, s, remoteIn(p1.ID, "k1", 1))
	closed := mustCreateIssue(t, s, NewIssue{ProjectID: p1.ID, Title: "closed"})
	if _, err := s.TransitionIssue(ctx, closed.ID, issuestate.Close, issuestate.Completed, nil, issuestate.Human); err != nil {
		t.Fatalf("close issue: %v", err)
	}
	// local is active through an unsettled root task; a lane on another
	// issue would not make that one active (issueSelect's definition).
	worked := newTask(p1.ID, "worked", TaskRunning)
	worked.IssueID = &local.ID
	mustTask(t, s, worked)

	mustChat(t, s, p1.ID, "idle", chatstate.Idle)
	mustChat(t, s, p1.ID, "asking", chatstate.AwaitingInput)
	mustChat(t, s, p1.ID, "gone", chatstate.Archived)
	mustChat(t, s, p2.ID, "p2-running", chatstate.Running)

	okAt := time.Now().UTC().Truncate(time.Second)
	if err := s.PutIssueSyncState(ctx, IssueSyncState{
		ProjectID: p1.ID, Provider: "github", Repo: "o/r", OK: true, LastAttemptAt: &okAt, LastOKAt: &okAt,
	}); err != nil {
		t.Fatalf("PutIssueSyncState: %v", err)
	}

	stats, err := s.ProjectStats(ctx)
	if err != nil {
		t.Fatalf("ProjectStats: %v", err)
	}
	a := stats[p1.ID]
	if a.IssuesOpen != 2 || a.IssuesOpenImported != 1 || a.IssuesActive != 1 {
		t.Errorf("p1 issues = open %d imported %d active %d, want 2 1 1",
			a.IssuesOpen, a.IssuesOpenImported, a.IssuesActive)
	}
	if a.ChatsLive != 2 || a.ChatsAwaitingInput != 1 {
		t.Errorf("p1 chats = live %d awaiting %d, want 2 1", a.ChatsLive, a.ChatsAwaitingInput)
	}
	if a.IssueSync == nil || !a.IssueSync.OK || a.IssueSync.LastOKAt == nil {
		t.Errorf("p1 sync = %+v, want the stored ok row", a.IssueSync)
	}
	if a.LastActivityAt == nil {
		t.Fatal("p1 LastActivityAt is nil")
	}

	b := stats[p2.ID]
	if b.IssuesOpen != 0 || b.ChatsLive != 1 || b.ChatsAwaitingInput != 0 || b.IssueSync != nil {
		t.Errorf("p2 = %+v, want one live chat and nothing else", b)
	}
	if b.TasksByState != nil || b.TasksActive != 0 {
		t.Errorf("p2 tasks = %v/%d, want none", b.TasksByState, b.TasksActive)
	}
	if b.LastActivityAt == nil {
		t.Error("p2 LastActivityAt is nil; its chat is activity")
	}
}

// TestProjectStatsStatementCountIsFixed shows by construction that a stats
// request costs the same number of statements for ten projects as for a
// hundred: ProjectStats is exactly the passes, and the figures come out
// right at both sizes.
func TestProjectStatsStatementCountIsFixed(t *testing.T) {
	if n := len(projectStatsPasses); n != 5 {
		t.Fatalf("ProjectStats runs %d passes; update this test and the task 132 doc together", n)
	}
	for _, projects := range []int{10, 100} {
		s := openTest(t)
		for i := range projects {
			p := testProject(t, s, fmt.Sprintf("p%d", i))
			mustTask(t, s, newTask(p.ID, fmt.Sprintf("t%d", i), TaskBlocked))
		}
		stats, err := s.ProjectStats(t.Context())
		if err != nil {
			t.Fatalf("ProjectStats(%d projects): %v", projects, err)
		}
		if len(stats) != projects {
			t.Fatalf("stats for %d projects, want %d", len(stats), projects)
		}
		for id, st := range stats {
			if st.TasksAttention != 1 {
				t.Errorf("project %d attention = %d, want 1", id, st.TasksAttention)
			}
		}
	}
}

// BenchmarkProjectStats is the seeded figure behind "no migration": twenty
// projects, four thousand tasks and two thousand issues, read with the
// indexes that already exist.
func BenchmarkProjectStats(b *testing.B) {
	s, err := Open(b.TempDir() + "/bench.db")
	if err != nil {
		b.Fatalf("Open: %v", err)
	}
	b.Cleanup(func() { _ = s.Close() })
	ctx := b.Context()
	states := []TaskState{TaskQueued, TaskRunning, TaskBlocked, TaskDone, TaskArchived, TaskAwaitingGate}
	for i := range 20 {
		p := &Project{Name: fmt.Sprintf("p%d", i), Path: fmt.Sprintf("/p%d", i), DefaultBranch: "main"}
		if err := s.CreateProject(ctx, p); err != nil {
			b.Fatalf("CreateProject: %v", err)
		}
		for j := range 200 {
			task := newTask(p.ID, fmt.Sprintf("t%d-%d", i, j), states[j%len(states)])
			if err := s.CreateTask(ctx, task, nil); err != nil {
				b.Fatalf("CreateTask: %v", err)
			}
		}
		for j := range 100 {
			if _, err := s.CreateIssue(ctx, NewIssue{ProjectID: p.ID, Title: fmt.Sprintf("i%d", j)}, issuestate.Human); err != nil {
				b.Fatalf("CreateIssue: %v", err)
			}
		}
	}
	for b.Loop() {
		if _, err := s.ProjectStats(ctx); err != nil {
			b.Fatal(err)
		}
	}
}
