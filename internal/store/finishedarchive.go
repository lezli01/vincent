package store

import (
	"context"
	"fmt"
	"slices"
	"time"
)

// FinishedArchiveDue returns the root tasks the age-based auto-archive (task
// 155) may archive now: every `done` or `aborted` root whose finished_at is
// at or before cutoff, in archive order.
//
// finished_at is restamped on every entry into `done` or `aborted`
// (TransitionTask), so a follow-up that returns a task to the state it came
// from restarts its clock (decision 4). Lanes are never returned: archiving a
// root archives its subtree (§7.6), and a lane is never archived on its own.
//
// A task of an issue is in scope whether the issue is open or closed
// (decision 3). The one exception is an issue's main task — the role that
// owns the issue's branch and worktree (task 134) — which is held while
// anything else on its issue still needs that line:
//
//  1. a root of the issue is not settled (a later main task, a side task
//     whose merge-back has not run, the merge-back itself);
//  2. a chat on the issue is live (task 140);
//  3. a handoff item claims the issue — a `return` follows up the issue's
//     latest main task, and an archived one refuses it (task 143 decision 10);
//  4. a side task or merge-back of the issue is not yet archived — the
//     closed-issue archive's side-work-first order (task 143 decision 5,
//     review F3), kept by holding rather than by ordering, since side work
//     ages on its own clock.
//
// Those are clauses 2 and 3 of ArchivableIssueTasks and the claim
// IssueHandoffClaimed reads, plus 4. A held main task is simply not
// returned; it is asked again on the next sweep. Linked-chat locks, dirty
// worktrees and unfinished lanes are not filtered here: Runner.Archive
// refuses those and the worker reports them.
//
// Within the result, side work, merge-backs and tasks with no issue come
// first, main tasks last, each by finished_at then id — the oldest first, so
// a sweep cut short by shutdown has done the most overdue work.
func (s *Store) FinishedArchiveDue(ctx context.Context, cutoff time.Time) ([]int64, error) {
	f := issueArchiveSQL()
	isMain := `(COALESCE(t.issue_worktree, '') = ? AND t.workflow_name <> ? AND t.issue_id IS NOT NULL)`
	mainArgs := []any{IssueWorktreeMain, mergeBackWorkflow}
	//nolint:gosec // G202: the fragments render bind markers only, never values.
	q := `
SELECT t.id
FROM tasks t
LEFT JOIN issues i ON i.id = t.issue_id
WHERE t.parent_task_id IS NULL
  AND t.state IN (?, ?)
  AND t.finished_at IS NOT NULL
  AND t.finished_at <= ?
  AND NOT (` + isMain + ` AND (
        ` + f.unsettledRoots.sql + `
     OR ` + f.liveChats.sql + `
     OR EXISTS (SELECT 1 FROM handoff_items hi WHERE hi.issue_id = i.id AND ` + claimCond + `)
     OR EXISTS (
          SELECT 1 FROM tasks w
          WHERE w.issue_id = i.id AND w.parent_task_id IS NULL AND w.id <> t.id
            AND w.state <> ?
            AND (COALESCE(w.issue_worktree, '') = ? OR w.workflow_name = ?))))
ORDER BY CASE WHEN ` + isMain + ` THEN 1 ELSE 0 END, t.finished_at, t.id`
	args := []any{string(TaskDone), string(TaskAborted), formatTime(cutoff)}
	args = append(args, mainArgs...)
	args = append(args, f.unsettledRoots.args...)
	args = append(args, f.liveChats.args...)
	args = append(args, string(TaskArchived), IssueWorktreeSide, mergeBackWorkflow)
	args = append(args, slices.Clone(mainArgs)...)

	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("finished tasks due for archive: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("finished tasks due for archive: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("finished tasks due for archive: %w", err)
	}
	return ids, nil
}
