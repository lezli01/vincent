package store

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
)

// ageSeed is one task seeded for TestFinishedArchiveDue.
type ageSeed struct {
	state     TaskState
	ago       time.Duration // finished_at relative to now; 0 = NULL
	issue     bool          // on the case's issue
	role      string        // "main", "side" or ""
	mergeBack bool
	parent    int // 1-based index of the lane's parent; 0 = root
}

// TestFinishedArchiveDue pins task 155's due set: done or aborted roots
// finished at or before the cutoff, an issue's main task held while its
// issue still needs the line, side work and issue-less tasks first.
func TestFinishedArchiveDue(t *testing.T) {
	const after = 12 * time.Hour
	old, young := 13*time.Hour, time.Hour
	done, aborted := TaskDone, TaskAborted
	cases := []struct {
		name    string
		tasks   []ageSeed
		chats   []chatstate.State
		claimed bool
		closed  bool
		want    []int
	}{
		{
			name: "age, state and roots",
			tasks: []ageSeed{
				{state: done, ago: old},
				{state: aborted, ago: old},
				{state: done, ago: young},
				{state: TaskRunning},
				{state: TaskBlocked},
				{state: TaskArchived, ago: old},
				{state: done, ago: old, parent: 1},
				{state: done},
			},
			want: []int{1, 2},
		},
		{
			name: "oldest first",
			tasks: []ageSeed{
				{state: done, ago: old},
				{state: done, ago: old + time.Hour},
			},
			want: []int{2, 1},
		},
		{
			name: "an open issue's tasks are in scope",
			tasks: []ageSeed{
				{state: done, ago: old, issue: true, role: "main"},
				{state: aborted, ago: old, issue: true, role: "side"},
				{state: done, ago: old, issue: true},
			},
			// The main task follows on the next sweep, once the side task
			// is archived (see "main released once side work is archived").
			want: []int{2, 3},
		},
		{
			name:   "a closed issue's tasks are in scope",
			closed: true,
			tasks:  []ageSeed{{state: done, ago: old, issue: true, role: "main"}},
			want:   []int{1},
		},
		{
			name: "main held by an unsettled root",
			tasks: []ageSeed{
				{state: done, ago: old, issue: true, role: "main"},
				{state: TaskRunning, issue: true, role: "main"},
			},
		},
		{
			name: "main held by side work not yet due",
			tasks: []ageSeed{
				{state: done, ago: old, issue: true, role: "main"},
				{state: done, ago: young, issue: true, role: "side"},
			},
		},
		{
			name: "main held by a due side task, which goes first",
			tasks: []ageSeed{
				{state: done, ago: old, issue: true, role: "main"},
				{state: done, ago: old, issue: true, role: "side"},
			},
			want: []int{2},
		},
		{
			name: "main held by a merge-back not yet archived",
			tasks: []ageSeed{
				{state: done, ago: old, issue: true, role: "main"},
				{state: done, ago: young, issue: true, role: "main", mergeBack: true},
			},
		},
		{
			name: "a merge-back is side work, not main",
			tasks: []ageSeed{
				{state: done, ago: old, issue: true, role: "main", mergeBack: true},
			},
			want: []int{1},
		},
		{
			name: "main released once side work is archived",
			tasks: []ageSeed{
				{state: done, ago: old, issue: true, role: "main"},
				{state: TaskArchived, ago: old, issue: true, role: "side"},
				{state: TaskArchived, ago: old, issue: true, role: "main", mergeBack: true},
			},
			want: []int{1},
		},
		{
			name: "main not held by an older main task",
			tasks: []ageSeed{
				{state: done, ago: old, issue: true, role: "main"},
				{state: done, ago: young, issue: true, role: "main"},
			},
			want: []int{1},
		},
		{
			name:  "main held by a live issue chat",
			tasks: []ageSeed{{state: done, ago: old, issue: true, role: "main"}},
			chats: []chatstate.State{chatstate.Idle},
		},
		{
			name:  "terminal issue chats do not hold",
			tasks: []ageSeed{{state: done, ago: old, issue: true, role: "main"}},
			chats: []chatstate.State{chatstate.Closed, chatstate.Archived},
			want:  []int{1},
		},
		{
			name: "side work is not held by a live issue chat",
			tasks: []ageSeed{
				{state: done, ago: old, issue: true, role: "side"},
			},
			chats: []chatstate.State{chatstate.Running},
			want:  []int{1},
		},
		{
			name:    "main held by a handoff claim",
			tasks:   []ageSeed{{state: done, ago: old, issue: true, role: "main"}},
			claimed: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := openTest(t)
			now := time.Now().UTC()
			p := testProject(t, s, "p1")
			is := testIssue(t, s, p.ID, "the issue")
			if tc.closed {
				execT(t, s, `UPDATE issues SET state = 'closed', closed_at = ? WHERE id = ?`,
					now.Add(-old).Format(TimeFormat), is.ID)
			}
			ids := make([]int64, len(tc.tasks))
			for i, tk := range tc.tasks {
				task := newTask(p.ID, fmt.Sprintf("t%c", rune('a'+i)), TaskQueued)
				if tk.issue {
					task.IssueID = &is.ID
				}
				mustCreate(t, s, task)
				ids[i] = task.ID
				var role, parent, finished any
				if tk.role != "" {
					role = tk.role
				}
				if tk.parent > 0 {
					parent = ids[tk.parent-1]
				}
				if tk.ago > 0 {
					finished = now.Add(-tk.ago).Format(TimeFormat)
				}
				wf := "adhoc"
				if tk.mergeBack {
					wf = mergeBackWorkflow
				}
				execT(t, s, `UPDATE tasks SET issue_worktree = ?, state = ?, workflow_name = ?,
					parent_task_id = ?, finished_at = ? WHERE id = ?`,
					role, string(tk.state), wf, parent, finished, task.ID)
			}
			for _, st := range tc.chats {
				c := testChat(t, s, p.ID)
				execT(t, s, `UPDATE chats SET issue_id = ?, state = ? WHERE id = ?`, is.ID, string(st), c.ID)
			}
			if tc.claimed {
				mustHandoff(t, s, p.ID, "h1", is.ID)
			}

			got, err := s.FinishedArchiveDue(t.Context(), now.Add(-after))
			if err != nil {
				t.Fatalf("FinishedArchiveDue: %v", err)
			}
			var want []int64
			for _, i := range tc.want {
				want = append(want, ids[i-1])
			}
			if !slices.Equal(got, want) {
				t.Errorf("FinishedArchiveDue = %v, want %v (seeded ids %v)", got, want, ids)
			}
		})
	}
}

// TestFinishedArchiveDueFollowUpRestartsClock (decision 4): finished_at is
// restamped by every entry into done, so a task followed up after its first
// finish is due only on its new clock.
func TestFinishedArchiveDueFollowUpRestartsClock(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	task := newTask(p.ID, "followed up", TaskQueued)
	mustCreate(t, s, task)
	for _, to := range []TaskState{TaskRunning, TaskDone} {
		from := task.State
		var err error
		if task, _, err = s.TransitionTask(ctx, task.ID, from, to, TaskChange{}); err != nil {
			t.Fatalf("%s → %s: %v", from, to, err)
		}
	}
	first := *task.FinishedAt
	due := func(cutoff time.Time) bool {
		t.Helper()
		ids, err := s.FinishedArchiveDue(ctx, cutoff)
		if err != nil {
			t.Fatal(err)
		}
		return slices.Contains(ids, task.ID)
	}
	if !due(first) {
		t.Fatal("a task finished exactly at the cutoff is not due")
	}
	for _, to := range []TaskState{TaskQueued, TaskRunning, TaskDone} {
		from := task.State
		var err error
		if task, _, err = s.TransitionTask(ctx, task.ID, from, to, TaskChange{}); err != nil {
			t.Fatalf("%s → %s: %v", from, to, err)
		}
	}
	if !task.FinishedAt.After(first) {
		t.Fatalf("finished_at %v not restamped past %v", task.FinishedAt, first)
	}
	if due(first) {
		t.Error("a followed-up task is still due on its first finish's clock")
	}
}
