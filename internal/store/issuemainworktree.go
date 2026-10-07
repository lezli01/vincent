package store

// Handing an issue's main worktree from one main-role task to the next
// (task 134.12, decisions 3, 7, 14, 16, 17).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrIssueWorktreeNotHeld is a transfer refused because the predecessor no
// longer names the directory, or the successor already names one: something
// moved the worktree between the caller's read and the transfer, and writing
// anyway would leave two rows naming it or none.
var ErrIssueWorktreeNotHeld = errors.New("issue main worktree is not held by the predecessor")

// IssueHasLiveMainTaskError is an issue delete refused because one of its
// main-role tasks has not settled (task 134.12). Deleting the issue would
// strand a task that is still working in, or queued for, the issue's main
// worktree; the API answers 409 naming the task.
type IssueHasLiveMainTaskError struct {
	IssueID int64
	TaskID  int64
}

func (e *IssueHasLiveMainTaskError) Error() string {
	return fmt.Sprintf("issue %d has main task %d still in progress; finish, cancel or archive it first",
		e.IssueID, e.TaskID)
}

// AsIssueHasLiveMainTask extracts a *IssueHasLiveMainTaskError from err, if
// that is what it is.
func AsIssueHasLiveMainTask(err error) (*IssueHasLiveMainTaskError, bool) {
	var e *IssueHasLiveMainTaskError
	ok := errors.As(err, &e)
	return e, ok
}

// liveIssueMainTaskTx returns the lowest-id unsettled main-role task of the
// issue, or 0 when every one is done, aborted or archived.
func liveIssueMainTaskTx(ctx context.Context, tx *sql.Tx, issueID int64) (int64, error) {
	args := append([]any{issueID}, settledTaskStates()...)
	var id int64
	err := tx.QueryRowContext(ctx, `SELECT id FROM tasks
		WHERE issue_id = ? AND issue_worktree = 'main' AND state NOT IN `+placeholders(len(settledTaskStates()))+`
		ORDER BY id LIMIT 1`, args...).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("read issue %d live main task: %w", issueID, err)
	default:
		return id, nil
	}
}

// IssueMainWorktreeHolder returns the unarchived main-role task of issueID,
// other than excludeTaskID, whose row names a worktree — the directory the
// next main task receives — or (nil, nil) when none does. Transfer keeps it
// to at most one row; should several ever name one, the lowest id answers,
// so the reading is stable.
func (s *Store) IssueMainWorktreeHolder(ctx context.Context, issueID, excludeTaskID int64) (*Task, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks
		WHERE issue_id = ? AND issue_worktree = 'main' AND archived_at IS NULL AND id <> ?
		  AND worktree_path IS NOT NULL AND worktree_path <> ''
		ORDER BY id LIMIT 1`, issueID, excludeTaskID)
	t, err := scanTask(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("issue %d main worktree holder: %w", issueID, err)
	}
	return t, nil
}

// TransferIssueWorktree hands the issue's main worktree at path from task
// fromID, which has settled, to task toID, which is being admitted (task
// 134.12), in one transaction — HandoffChat's shape, for the same reason: two
// rows naming one directory, or none, is the ambiguity the §10 reclaimer must
// never see.
//
// Both rows are re-read inside the transaction. The predecessor must still
// name path and the successor must name nothing; otherwise the transfer
// fails closed with ErrIssueWorktreeNotHeld and writes nothing. A chat
// still open on the predecessor fails it closed too, with a wrapped
// *TaskLockedError naming the chat.
//
// The predecessor keeps tipSHA as its end_sha, the commit its work ended on,
// and lets go of the directory. The successor starts from that same commit:
// base_sha is tipSHA, and base_refresh stays NULL because no base refresh
// happened — the directory was received, not created (task 125 decision 7).
// Any end_sha the successor carries from an earlier hand-over of its own is
// cleared: it is working again, so its range runs to the branch (review F4
// of #770).
//
// No event is appended, as SetTaskProgress appends none for a worktree-path
// write: it is bookkeeping no client renders, and the successor's own
// → running transition is what announces it.
func (s *Store) TransferIssueWorktree(ctx context.Context, fromID, toID int64, path, tipSHA string) error {
	if path == "" {
		return fmt.Errorf("transfer issue worktree %d → %d: %w", fromID, toID, ErrIssueWorktreeNotHeld)
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var fromPath, toPath sql.NullString
		if err := tx.QueryRowContext(ctx,
			`SELECT worktree_path FROM tasks WHERE id = ?`, fromID).Scan(&fromPath); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("task %d: %w", fromID, ErrNotFound)
			}
			return fmt.Errorf("read task %d worktree: %w", fromID, err)
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT worktree_path FROM tasks WHERE id = ?`, toID).Scan(&toPath); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("task %d: %w", toID, ErrNotFound)
			}
			return fmt.Errorf("read task %d worktree: %w", toID, err)
		}
		if fromPath.String != path || toPath.String != "" {
			return fmt.Errorf("transfer issue worktree %d → %d: task %d names %q, task %d names %q: %w",
				fromID, toID, fromID, fromPath.String, toID, toPath.String, ErrIssueWorktreeNotHeld)
		}
		// A chat linked to the predecessor works in this directory too. The
		// scheduler's occupancy predicate kept the successor queued while one
		// was open, but a chat opened between that walk and this transaction
		// would be left running beside the successor's agent in one
		// worktree (review F5 of #770). OpenLinkedChat re-reads the
		// worktree_path in its own transaction, so once this commits no
		// chat can open on the predecessor; this closes the other side.
		if chatID, err := openLinkedChatTx(ctx, tx, fromID); err != nil {
			return err
		} else if chatID != 0 {
			return fmt.Errorf("transfer issue worktree %d → %d: %w",
				fromID, toID, &TaskLockedError{TaskID: fromID, ChatID: chatID})
		}
		now := formatTime(time.Now())
		if _, err := tx.ExecContext(ctx, `
			UPDATE tasks SET end_sha = ?, worktree_path = NULL, updated_at = ? WHERE id = ?`,
			nullString(tipSHA), now, fromID); err != nil {
			return fmt.Errorf("release task %d worktree: %w", fromID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE tasks SET worktree_path = ?, base_sha = ?, base_refresh = NULL, end_sha = NULL, updated_at = ?
			WHERE id = ?`,
			path, nullString(tipSHA), now, toID); err != nil {
			return fmt.Errorf("hand task %d worktree: %w", toID, err)
		}
		return nil
	})
}

// IssueMainBranchLine answers who started the issue's line of main tasks on
// branch (task 134.12): the lowest-id main-role task of issueID carrying it,
// archived included, and whether that task adopted the branch rather than
// cutting it. firstID is 0 when no main task of the issue has ever carried
// it.
//
// It is the line, not a row, that says whose branch it is. A joiner is never
// adopted, and a legacy joiner bound before 134.12 is adopted whoever cut
// the branch, so neither flag tells a successor of a branch the human
// pointed the first main task at (task 125 decision 6) from one vincent cut.
// The first task's flag does: it is the one that met the branch.
func (s *Store) IssueMainBranchLine(
	ctx context.Context, issueID int64, branch string,
) (firstID int64, adopted bool, err error) {
	err = s.db.QueryRowContext(ctx, `SELECT id, adopted_branch FROM tasks
		WHERE issue_id = ? AND issue_worktree = 'main' AND branch_name = ?
		ORDER BY id LIMIT 1`, issueID, branch).Scan(&firstID, &adopted)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, false, nil
	case err != nil:
		return 0, false, fmt.Errorf("issue %d main branch %q line: %w", issueID, branch, err)
	default:
		return firstID, adopted, nil
	}
}

// IssueMainBranchHeld reports whether a main-role task of issueID carrying
// branch, archived or not, has ever held it in a worktree: one names a
// worktree now, or recorded an end_sha, which only letting go of one writes
// — the transfer, or the archive of a task that held it. It is what makes an
// existing branch the issue's own rather than a stranger's (review F3 of
// #770): a branch that appeared before the issue's first main task was
// admitted — a human's `git branch`, a fetch — was held by none of them,
// and must still block `branch_exists` (task 001) rather than be taken over.
func (s *Store) IssueMainBranchHeld(ctx context.Context, issueID int64, branch string) (bool, error) {
	n, err := s.countTasks(ctx, `SELECT COUNT(*) FROM tasks
		WHERE issue_id = ? AND issue_worktree = 'main' AND branch_name = ?
		  AND ((worktree_path IS NOT NULL AND worktree_path <> '') OR (end_sha IS NOT NULL AND end_sha <> ''))`,
		issueID, branch)
	if err != nil {
		return false, fmt.Errorf("issue %d main branch %q held: %w", issueID, branch, err)
	}
	return n > 0, nil
}
