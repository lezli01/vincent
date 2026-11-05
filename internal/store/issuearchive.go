package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/taskstate"
)

// mergeBackWorkflow is the workflow name a merge-back task runs under. It is
// workflow.MergeBackName, repeated because the store does not import
// internal/workflow; TestMergeBackWorkflowMatchesWorkflow pins the two.
const mergeBackWorkflow = "__merge_back"

// issueArchiveFrom is the FROM both auto-archive reads share: a task t, its
// issue i, and a merge-back's source side task src.
const issueArchiveFrom = `FROM tasks t
JOIN issues i ON i.id = t.issue_id
LEFT JOIN tasks src ON src.id = t.merge_source_task_id`

// issueArchiveFrags are the predicates ArchivableIssueTasks and
// ClosedIssueArchiveCandidates share, so the doctor's row (#929) and the
// worker's query (#927) cannot come to disagree about which tasks a closed
// issue's auto-archive covers.
type issueArchiveFrags struct {
	// candidate is the task-level rule over t, i and src: the issue is
	// closed, t is a done or aborted root, and its cutoff is at or before
	// closed_at.
	candidate sqlFrag
	// unsettledRoots is true when a root of i — one created after the close
	// included — is not settled.
	unsettledRoots sqlFrag
	// liveChats is true when a chat on i is in a non-terminal state.
	liveChats sqlFrag
	// order is the archive order: side work, merge-backs and role-less
	// roots first, main tasks last, each group by id.
	order sqlFrag
}

func issueArchiveSQL() issueArchiveFrags {
	settled := make([]any, 0, len(taskstate.All))
	for _, st := range taskstate.All {
		if taskstate.Settled(st) {
			settled = append(settled, string(st))
		}
	}
	terminal := make([]any, 0, len(chatstate.All))
	for _, st := range chatstate.All {
		if chatstate.Terminal(st) {
			terminal = append(terminal, string(st))
		}
	}
	return issueArchiveFrags{
		candidate: sqlFrag{
			sql: `i.state = ?
  AND i.closed_at IS NOT NULL
  AND t.parent_task_id IS NULL
  AND t.state IN (?, ?)
  AND CASE
        WHEN t.workflow_name = ? AND src.id IS NOT NULL THEN src.created_at
        ELSE t.created_at
      END <= i.closed_at`,
			args: []any{string(issuestate.Closed), string(TaskDone), string(TaskAborted), mergeBackWorkflow},
		},
		unsettledRoots: sqlFrag{
			sql: `EXISTS (
    SELECT 1 FROM tasks u
    WHERE u.issue_id = i.id AND u.parent_task_id IS NULL
      AND u.state NOT IN ` + placeholders(len(settled)) + `)`,
			args: settled,
		},
		liveChats: sqlFrag{
			sql: `EXISTS (
    SELECT 1 FROM chats c
    WHERE c.issue_id = i.id
      AND c.state NOT IN ` + placeholders(len(terminal)) + `)`,
			args: terminal,
		},
		order: sqlFrag{
			sql:  `CASE WHEN COALESCE(t.issue_worktree, '') = ? AND t.workflow_name <> ? THEN 1 ELSE 0 END, t.id`,
			args: []any{IssueWorktreeMain, mergeBackWorkflow},
		},
	}
}

// ArchivableIssueTasks returns the root tasks of issueID that the daemon
// may archive now, in archive order. It returns nil when the issue may not
// be archived yet (#925's auto-archive; the worker is #928).
//
// The answer is derived from current rows only, in one statement, so asking
// twice is safe and a replayed close asks the same question again:
//
//  1. The issue must exist and be closed (spec §5.6). Its closed_at is the
//     newest close, since a reopen nulls it.
//  2. Every root task of the issue (task 130 decision 5: lanes are never the
//     issue's work) must be settled in taskstate.Settled's sense, including
//     roots created after the close — a live post-close main task may hold
//     the issue's main branch, which archiving its predecessor would strand
//     (task 134).
//  3. No chat on the issue (task 140) may be in a state chatstate.Terminal
//     does not cover.
//
// It then returns the done and aborted roots — archived ones are already
// gone — whose cutoff is at or before closed_at. The cutoff is a task's own
// created_at, except for a merge-back whose source side task survives, which
// takes the source's: a merge-back is inserted at its side task's `→ done`,
// so it is younger than the work it lands. An orphaned merge-back (source
// deleted, merge_source_task_id NULL) falls back to its own created_at, the
// closest surviving proxy.
//
// Side tasks, merge-backs and role-less roots (rows older than migration
// 0043, which never held the issue's main branch) come first, and the
// issue's main tasks last, each group by id: a main task archived before the
// side work merged into it would release the branch that work lands on.
// Linked-chat locks are not filtered here; Runner.Archive refuses those and
// the worker reports them.
func (s *Store) ArchivableIssueTasks(ctx context.Context, issueID int64) ([]int64, error) {
	f := issueArchiveSQL()
	//nolint:gosec // G202: the fragments render bind markers only, never values.
	q := `
SELECT t.id
` + issueArchiveFrom + `
WHERE i.id = ?
  AND ` + f.candidate.sql + `
  AND NOT ` + f.unsettledRoots.sql + `
  AND NOT ` + f.liveChats.sql + `
ORDER BY ` + f.order.sql
	args := []any{issueID}
	args = append(args, f.candidate.args...)
	args = append(args, f.unsettledRoots.args...)
	args = append(args, f.liveChats.args...)
	args = append(args, f.order.args...)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("archivable tasks of issue %d: %w", issueID, err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("archivable tasks of issue %d: %w", issueID, err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("archivable tasks of issue %d: %w", issueID, err)
	}
	return ids, nil
}

// IssueArchiveCandidate is one root task the auto-archive of a closed issue
// covers by its task-level rule (task 143 decision 12): what doctor's
// issue_archive row reports on, with the issue-level facts that may hold it.
type IssueArchiveCandidate struct {
	TaskID, IssueID, ProjectID int64
	Title                      string
	State                      TaskState
	// Role is the task's issue_worktree role: "main", "side" or "".
	Role         string
	WorkflowName string
	WorktreePath string
	// Quiescent is false when another root of the issue is unsettled or an
	// issue chat is live — ArchivableIssueTasks' clauses 2 and 3.
	Quiescent bool
	// HandoffClaimed is Store.IssueHandoffClaimed for the issue.
	HandoffClaimed bool
}

// ClosedIssueArchiveCandidates returns, across every closed issue, the root
// tasks ArchivableIssueTasks' task-level rule covers — closed issue, done or
// aborted root, cutoff at or before closed_at — whether or not the issue is
// quiescent or claimed, which each candidate reports instead. Ordered by
// issue, then in archive order. For a quiescent, unclaimed issue the set is
// exactly ArchivableIssueTasks'.
func (s *Store) ClosedIssueArchiveCandidates(ctx context.Context) ([]IssueArchiveCandidate, error) {
	f := issueArchiveSQL()
	//nolint:gosec // G202: the fragments render bind markers only, never values.
	q := `
SELECT t.id, i.id, t.project_id, t.title, t.state, COALESCE(t.issue_worktree, ''), t.workflow_name,
  COALESCE(t.worktree_path, ''),
  ` + f.unsettledRoots.sql + `,
  ` + f.liveChats.sql + `
` + issueArchiveFrom + `
WHERE ` + f.candidate.sql + `
ORDER BY i.id, ` + f.order.sql
	args := slices.Clone(f.unsettledRoots.args)
	args = append(args, f.liveChats.args...)
	args = append(args, f.candidate.args...)
	args = append(args, f.order.args...)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("issue archive candidates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []IssueArchiveCandidate
	for rows.Next() {
		var c IssueArchiveCandidate
		var state string
		var unsettled, live bool
		if err := rows.Scan(&c.TaskID, &c.IssueID, &c.ProjectID, &c.Title, &state, &c.Role, &c.WorkflowName,
			&c.WorktreePath, &unsettled, &live); err != nil {
			return nil, fmt.Errorf("issue archive candidates: %w", err)
		}
		c.State = TaskState(state)
		c.Quiescent = !unsettled && !live
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("issue archive candidates: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("issue archive candidates: %w", err)
	}
	claimed := map[int64]bool{}
	for i := range out {
		id := out[i].IssueID
		v, ok := claimed[id]
		if !ok {
			if v, err = s.IssueHandoffClaimed(ctx, id); err != nil {
				return nil, err
			}
			claimed[id] = v
		}
		out[i].HandoffClaimed = v
	}
	return out, nil
}

// UnarchivedLaneWorktrees returns the worktree paths of every descendant of
// taskID that is not archived and still names one — the directories an
// archive of the root would cascade into and check for local changes.
func (s *Store) UnarchivedLaneWorktrees(ctx context.Context, taskID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		WITH RECURSIVE subtree(id) AS (
			SELECT id FROM tasks WHERE parent_task_id = ?
			UNION ALL
			SELECT t.id FROM tasks t JOIN subtree ON t.parent_task_id = subtree.id
		)
		SELECT t.worktree_path FROM tasks t JOIN subtree ON subtree.id = t.id
		WHERE t.state <> ? AND t.worktree_path IS NOT NULL AND t.worktree_path <> ''
		ORDER BY t.id`, taskID, string(TaskArchived))
	if err != nil {
		return nil, fmt.Errorf("lane worktrees of task %d: %w", taskID, err)
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, fmt.Errorf("lane worktrees of task %d: %w", taskID, err)
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("lane worktrees of task %d: %w", taskID, err)
	}
	return out, nil
}

// SubtreeOpenLinkedChatID returns the id of an open chat locking taskID or
// any unarchived descendant of it, or 0 when none is locked — the subtree an
// archive of the root cascades through, each lane by the ordinary Archive
// and so each refused by its own lock (review F1).
func (s *Store) SubtreeOpenLinkedChatID(ctx context.Context, taskID int64) (int64, error) {
	args := append([]any{taskID, taskID, string(TaskArchived)}, terminalChatStateArgs()...)
	var id int64
	err := s.db.QueryRowContext(ctx, `
		WITH RECURSIVE subtree(id) AS (
			SELECT id FROM tasks WHERE parent_task_id = ?
			UNION ALL
			SELECT t.id FROM tasks t JOIN subtree ON t.parent_task_id = subtree.id
		)
		SELECT c.id FROM chats c
		WHERE (c.linked_task_id = ? OR c.linked_task_id IN (
			SELECT t.id FROM tasks t JOIN subtree ON subtree.id = t.id WHERE t.state <> ?))
		AND c.state NOT IN (?, ?, ?)
		ORDER BY c.id LIMIT 1`, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("open chats of task %d's subtree: %w", taskID, err)
	}
	return id, nil
}

// ClosedIssueIDs returns the ids of every closed issue, in id order.
func (s *Store) ClosedIssueIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM issues WHERE state = ? ORDER BY id`, string(issuestate.Closed))
	if err != nil {
		return nil, fmt.Errorf("closed issues: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("closed issues: %w", err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("closed issues: %w", err)
	}
	return out, nil
}
