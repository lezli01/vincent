package store

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/taskstate"
	"github.com/lezli01/vincent/internal/workflow"
)

// TestMergeBackWorkflowMatchesWorkflow pins the store's copy of the
// merge-back workflow name to the one internal/workflow gives it.
func TestMergeBackWorkflowMatchesWorkflow(t *testing.T) {
	if mergeBackWorkflow != workflow.MergeBackName {
		t.Fatalf("mergeBackWorkflow = %q, want workflow.MergeBackName %q", mergeBackWorkflow, workflow.MergeBackName)
	}
}

// closeSeed is one root (or lane) seeded for TestArchivableIssueTasks.
type closeSeed struct {
	role      string // "main", "side" or "" (no role)
	state     TaskState
	at        time.Duration // created_at relative to the close
	mergeBack bool          // workflow_name is the merge-back workflow
	source    int           // 1-based index of the merge-back's source; 0 = none (orphaned)
	parent    int           // 1-based index of the lane's parent; 0 = root
}

const (
	preClose  = -time.Hour
	postClose = time.Hour
)

// archiveCase is one row of archiveCases.
type archiveCase struct {
	name  string
	open  bool
	tasks []closeSeed
	chats []chatstate.State
	want  []int // 1-based indexes into tasks
}

// archiveCases is the table TestArchivableIssueTasks and the doctor
// candidates' parity test both walk.
func archiveCases() []archiveCase {
	done, aborted := TaskDone, TaskAborted
	return []archiveCase{
		{
			name:  "open issue",
			open:  true,
			tasks: []closeSeed{{role: "main", state: done, at: preClose}},
		},
		{
			name: "unsettled side task",
			tasks: []closeSeed{
				{role: "main", state: done, at: preClose},
				{role: "side", state: TaskRunning, at: preClose},
			},
		},
		{
			name: "unsettled merge-back",
			tasks: []closeSeed{
				{role: "main", state: done, at: preClose},
				{role: "side", state: done, at: preClose},
				{role: "main", state: TaskQueued, at: postClose, mergeBack: true, source: 2},
			},
		},
		{
			name: "unsettled main task",
			tasks: []closeSeed{
				{role: "side", state: done, at: preClose},
				{role: "main", state: TaskBlocked, at: preClose},
			},
		},
		{
			name: "unsettled root created after the close",
			tasks: []closeSeed{
				{role: "main", state: done, at: preClose},
				{role: "main", state: TaskRunning, at: postClose},
			},
		},
		{
			name:  "idle issue chat",
			tasks: []closeSeed{{role: "main", state: done, at: preClose}},
			chats: []chatstate.State{chatstate.Closed, chatstate.Idle},
		},
		{
			name:  "running issue chat",
			tasks: []closeSeed{{role: "main", state: done, at: preClose}},
			chats: []chatstate.State{chatstate.Running},
		},
		{
			name:  "awaiting_input issue chat",
			tasks: []closeSeed{{role: "main", state: done, at: preClose}},
			chats: []chatstate.State{chatstate.AwaitingInput},
		},
		{
			name:  "terminal issue chats do not hold",
			tasks: []closeSeed{{role: "main", state: done, at: preClose}},
			chats: []chatstate.State{chatstate.Closed, chatstate.Archived, chatstate.HandedOff},
			want:  []int{1},
		},
		{
			name: "settled root created after the close is excluded",
			tasks: []closeSeed{
				{role: "main", state: done, at: preClose},
				{role: "main", state: aborted, at: postClose},
				{role: "", state: done, at: postClose},
			},
			want: []int{1},
		},
		{
			name: "post-close merge-back of a pre-close side task is included",
			tasks: []closeSeed{
				{role: "main", state: done, at: preClose},
				{role: "side", state: done, at: preClose},
				{role: "main", state: done, at: postClose, mergeBack: true, source: 2},
			},
			want: []int{2, 3, 1},
		},
		{
			name: "post-close merge-back of a post-close side task is excluded",
			tasks: []closeSeed{
				{role: "main", state: done, at: preClose},
				{role: "side", state: done, at: postClose},
				{role: "main", state: done, at: postClose, mergeBack: true, source: 2},
			},
			want: []int{1},
		},
		{
			name: "orphaned merge-back goes by its own created_at",
			tasks: []closeSeed{
				{role: "main", state: done, at: preClose},
				{role: "main", state: done, at: preClose, mergeBack: true},
				{role: "main", state: done, at: postClose, mergeBack: true},
			},
			want: []int{2, 1},
		},
		{
			name: "fan-out lanes are never returned",
			tasks: []closeSeed{
				{role: "", state: done, at: preClose},
				{role: "", state: done, at: preClose, parent: 1},
				{role: "", state: TaskRunning, at: preClose, parent: 1},
			},
			want: []int{1},
		},
		{
			name: "side work and merge-backs before main tasks, by id",
			tasks: []closeSeed{
				{role: "main", state: done, at: preClose},
				{role: "side", state: aborted, at: preClose},
				{role: "main", state: aborted, at: preClose},
				{role: "", state: done, at: preClose},
				{role: "side", state: done, at: preClose},
				{role: "main", state: done, at: preClose, mergeBack: true, source: 5},
			},
			want: []int{2, 4, 5, 6, 1, 3},
		},
		{
			name: "archived root is not returned and does not hold",
			tasks: []closeSeed{
				{role: "main", state: TaskArchived, at: preClose},
				{role: "main", state: done, at: preClose},
			},
			want: []int{2},
		},
		{
			name: "every root archived",
			tasks: []closeSeed{
				{role: "side", state: TaskArchived, at: preClose},
				{role: "main", state: TaskArchived, at: preClose},
			},
		},
	}
}

// seedArchiveCase stages one archiveCase and returns its issue and task ids.
func seedArchiveCase(t *testing.T, s *Store, tc archiveCase) (int64, []int64) {
	t.Helper()
	p := testProject(t, s, "p1")
	is := testIssue(t, s, p.ID, "the issue")
	closedAt := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	if !tc.open {
		execT(t, s, `UPDATE issues SET state = 'closed', closed_at = ? WHERE id = ?`,
			closedAt.Format(TimeFormat), is.ID)
	}
	ids := seedArchiveTasks(t, s, p.ID, is.ID, closedAt, tc.tasks)
	for _, st := range tc.chats {
		c := testChat(t, s, p.ID)
		execT(t, s, `UPDATE chats SET issue_id = ?, state = ? WHERE id = ?`, is.ID, string(st), c.ID)
	}
	return is.ID, ids
}

// TestArchivableIssueTasks (#927): the daemon may archive an issue's settled
// roots only once the issue is closed, every root is settled and no issue
// chat is live; then the roots that existed by the close come back, side
// work and merge-backs before main tasks.
func TestArchivableIssueTasks(t *testing.T) {
	for _, tc := range archiveCases() {
		t.Run(tc.name, func(t *testing.T) {
			s := openTest(t)
			issueID, ids := seedArchiveCase(t, s, tc)
			is := struct{ ID int64 }{issueID}

			got, err := s.ArchivableIssueTasks(t.Context(), is.ID)
			if err != nil {
				t.Fatalf("ArchivableIssueTasks: %v", err)
			}
			var want []int64
			for _, i := range tc.want {
				want = append(want, ids[i-1])
			}
			if !slices.Equal(got, want) {
				t.Errorf("ArchivableIssueTasks = %v, want %v (seeded ids %v)", got, want, ids)
			}
		})
	}
}

// TestArchivableIssueTasksMissingIssue: an issue that does not exist is not
// an error, only nothing to archive.
func TestArchivableIssueTasksMissingIssue(t *testing.T) {
	s := openTest(t)
	got, err := s.ArchivableIssueTasks(t.Context(), 9999)
	if err != nil || len(got) != 0 {
		t.Fatalf("ArchivableIssueTasks(missing) = %v, %v; want none, nil", got, err)
	}
}

// TestArchivableIssueTasksSettledSet pins the query's settled and terminal
// lists to taskstate.Settled and chatstate.Terminal across every state: a
// root in any unsettled state holds the issue, and a chat in any non-terminal
// state does.
func TestArchivableIssueTasksSettledSet(t *testing.T) {
	closedAt := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	for _, st := range taskstate.All {
		t.Run("task "+string(st), func(t *testing.T) {
			s := openTest(t)
			p := testProject(t, s, "p1")
			is := testIssue(t, s, p.ID, "the issue")
			execT(t, s, `UPDATE issues SET state = 'closed', closed_at = ? WHERE id = ?`,
				closedAt.Format(TimeFormat), is.ID)
			seedArchiveTasks(t, s, p.ID, is.ID, closedAt, []closeSeed{
				{role: "main", state: TaskDone, at: preClose},
				{role: "side", state: st, at: preClose},
			})
			got, err := s.ArchivableIssueTasks(t.Context(), is.ID)
			if err != nil {
				t.Fatalf("ArchivableIssueTasks: %v", err)
			}
			if held := len(got) == 0; held == taskstate.Settled(st) {
				t.Errorf("root in %s: got %v, settled = %v", st, got, taskstate.Settled(st))
			}
		})
	}
	for _, st := range chatstate.All {
		t.Run("chat "+string(st), func(t *testing.T) {
			s := openTest(t)
			p := testProject(t, s, "p1")
			is := testIssue(t, s, p.ID, "the issue")
			execT(t, s, `UPDATE issues SET state = 'closed', closed_at = ? WHERE id = ?`,
				closedAt.Format(TimeFormat), is.ID)
			seedArchiveTasks(t, s, p.ID, is.ID, closedAt, []closeSeed{{role: "main", state: TaskDone, at: preClose}})
			c := testChat(t, s, p.ID)
			execT(t, s, `UPDATE chats SET issue_id = ?, state = ? WHERE id = ?`, is.ID, string(st), c.ID)
			got, err := s.ArchivableIssueTasks(t.Context(), is.ID)
			if err != nil {
				t.Fatalf("ArchivableIssueTasks: %v", err)
			}
			if held := len(got) == 0; held == chatstate.Terminal(st) {
				t.Errorf("chat in %s: got %v, terminal = %v", st, got, chatstate.Terminal(st))
			}
		})
	}
}

// seedArchiveTasks creates tasks as plain issue roots, then writes the
// columns under test directly: the role, merge-back and lane invariants
// CreateTask enforces are beside the point of a read-only query.
func seedArchiveTasks(t *testing.T, s *Store, projectID, issueID int64, closedAt time.Time, tasks []closeSeed) []int64 {
	t.Helper()
	ids := make([]int64, len(tasks))
	for i, tk := range tasks {
		task := newTask(projectID, fmt.Sprintf("i%d-t%c", issueID, rune('a'+i)), TaskQueued)
		task.IssueID = &issueID
		mustCreate(t, s, task)
		ids[i] = task.ID

		var role, source, parent, archivedAt any
		if tk.role != "" {
			role = tk.role
		}
		if tk.source > 0 {
			source = ids[tk.source-1]
		}
		if tk.parent > 0 {
			parent = ids[tk.parent-1]
		}
		if tk.state == TaskArchived {
			archivedAt = closedAt.Add(2 * postClose).Format(TimeFormat)
		}
		wf := "adhoc"
		if tk.mergeBack {
			wf = mergeBackWorkflow
		}
		execT(t, s, `UPDATE tasks SET issue_worktree = ?, state = ?, workflow_name = ?,
			merge_source_task_id = ?, parent_task_id = ?, created_at = ?, archived_at = ?
			WHERE id = ?`,
			role, string(tk.state), wf, source, parent,
			closedAt.Add(tk.at).Format(TimeFormat), archivedAt, task.ID)
	}
	return ids
}

func execT(t *testing.T, s *Store, q string, args ...any) {
	t.Helper()
	if _, err := s.db.ExecContext(t.Context(), q, args...); err != nil {
		t.Fatalf("exec %q: %v", q, err)
	}
}
