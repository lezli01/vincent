package store

import (
	"context"
	"fmt"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/taskstate"
)

// mergeBackWorkflow is the workflow name a merge-back task runs under. It is
// workflow.MergeBackName, repeated because the store does not import
// internal/workflow; TestMergeBackWorkflowMatchesWorkflow pins the two.
const mergeBackWorkflow = "__merge_back"

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
	//nolint:gosec // G202: placeholders renders bind markers only, never values.
	q := `
SELECT t.id
FROM tasks t
JOIN issues i ON i.id = t.issue_id
LEFT JOIN tasks src ON src.id = t.merge_source_task_id
WHERE i.id = ?
  AND i.state = ?
  AND i.closed_at IS NOT NULL
  AND t.parent_task_id IS NULL
  AND t.state IN (?, ?)
  AND NOT EXISTS (
    SELECT 1 FROM tasks u
    WHERE u.issue_id = i.id AND u.parent_task_id IS NULL
      AND u.state NOT IN ` + placeholders(len(settled)) + `)
  AND NOT EXISTS (
    SELECT 1 FROM chats c
    WHERE c.issue_id = i.id
      AND c.state NOT IN ` + placeholders(len(terminal)) + `)
  AND CASE
        WHEN t.workflow_name = ? AND src.id IS NOT NULL THEN src.created_at
        ELSE t.created_at
      END <= i.closed_at
ORDER BY
  CASE WHEN COALESCE(t.issue_worktree, '') = ? AND t.workflow_name <> ? THEN 1 ELSE 0 END,
  t.id`
	args := []any{issueID, string(issuestate.Closed), string(TaskDone), string(TaskAborted)}
	args = append(args, settled...)
	args = append(args, terminal...)
	args = append(args, mergeBackWorkflow, IssueWorktreeMain, mergeBackWorkflow)

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
