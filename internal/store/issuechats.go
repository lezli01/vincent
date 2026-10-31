package store

// A chat opened on an issue (task 140, spec §5.5, §5.6): a third chat kind,
// beside the free chat and the chat linked to a task, which holds the
// issue's main worktree the way a main-role task does.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/issuestate"
)

// IssueWorktreeOccupiedError is an issue chat refused because something
// already holds the issue's main worktree (task 140 decision 2): a main task
// — admitted and unsettled, or settled with a linked chat open — or another
// live issue chat. Exactly one of TaskID and ChatID is set.
type IssueWorktreeOccupiedError struct {
	IssueID int64
	TaskID  int64
	ChatID  int64
}

func (e *IssueWorktreeOccupiedError) Error() string {
	if e.ChatID != 0 {
		return fmt.Sprintf("issue %d's main worktree is held by chat %d; close or archive it first", e.IssueID, e.ChatID)
	}
	return fmt.Sprintf("issue %d's main worktree is held by task %d; wait for it to finish, or close its chat",
		e.IssueID, e.TaskID)
}

// AsIssueWorktreeOccupied extracts a *IssueWorktreeOccupiedError from err.
func AsIssueWorktreeOccupied(err error) (*IssueWorktreeOccupiedError, bool) {
	var e *IssueWorktreeOccupiedError
	ok := errors.As(err, &e)
	return e, ok
}

// IssueHasLiveChatError is a write refused because a non-terminal chat is
// open on the issue (task 140 decisions 2 and 10): a second chat on it, or
// deleting the issue under it.
type IssueHasLiveChatError struct {
	IssueID int64
	ChatID  int64
}

func (e *IssueHasLiveChatError) Error() string {
	return fmt.Sprintf("issue %d has chat %d open; close or archive it first", e.IssueID, e.ChatID)
}

// AsIssueHasLiveChat extracts a *IssueHasLiveChatError from err.
func AsIssueHasLiveChat(err error) (*IssueHasLiveChatError, bool) {
	var e *IssueHasLiveChatError
	ok := errors.As(err, &e)
	return e, ok
}

// IssueChatBranchedError is a branch delete refused because the chat's
// branch is its issue's main branch (task 140 decision 4).
type IssueChatBranchedError struct {
	ChatID, IssueID int64
	Branch          string
}

func (e *IssueChatBranchedError) Error() string {
	return fmt.Sprintf("branch %q is issue %d's main branch; chat %d does not own it to delete",
		e.Branch, e.IssueID, e.ChatID)
}

// issueChatOccupantSQL names the live issue chat holding an issue's main
// worktree (task 140 decision 2): one that is not terminal. It is the chat
// half of the occupancy definition issueMainOccupantSQL is the task half of,
// and every reader of that one reads this one beside it — the issue row, the
// creation hint, the scheduler. It binds terminalChatStateArgs.
func issueChatOccupantSQL(issueRef string) string {
	return `SELECT oc.id FROM chats oc
		WHERE oc.issue_id = ` + issueRef + ` AND oc.state NOT IN (?, ?, ?)
		ORDER BY oc.id LIMIT 1`
}

// issueChatBranchSQL is the issue chats' share of the main-branch view (task
// 140 decision 4): the branch of any issue chat that was not handed off — a
// handed-off chat's task carries the branch itself.
func issueChatBranchSQL(issueRef string) string {
	return `SELECT branch FROM chats
		WHERE issue_id = ` + issueRef + ` AND state <> 'handed_off' AND branch <> ''
		ORDER BY id LIMIT 1`
}

// issueLatestChatCond is the lane's fourth fact (task 140 decision 5) over
// the issue alias `i`: an issue chat that was not handed off — any state,
// archived included — created after the latest finished main-line root task
// finished, or with no such task at all.
func issueLatestChatCond() sqlFrag {
	finished := latestDeliveryColumn("t.finished_at")
	return sqlFrag{
		sql: `EXISTS (SELECT 1 FROM chats lc WHERE lc.issue_id = i.id AND lc.state <> 'handed_off'
			AND (` + finished.sql + ` IS NULL OR lc.created_at > ` + finished.sql + `))`,
		args: append(append([]any{}, finished.args...), finished.args...),
	}
}

// issueLiveChatCond is a non-terminal issue chat over alias `i`: what makes
// an issue active, and its marker busy, the way an unsettled task does.
func issueLiveChatCond() sqlFrag {
	return sqlFrag{
		sql:  `EXISTS (SELECT 1 FROM chats ac WHERE ac.issue_id = i.id AND ac.state NOT IN (?, ?, ?))`,
		args: terminalChatStateArgs(),
	}
}

// issueAwaitingChatCond is an issue chat waiting on a person (task 140
// decision 5): it raises the issue's attention as a gated task does.
func issueAwaitingChatCond() sqlFrag {
	return sqlFrag{
		sql:  `EXISTS (SELECT 1 FROM chats wc WHERE wc.issue_id = i.id AND wc.state = ?)`,
		args: []any{string(chatstate.AwaitingInput)},
	}
}

// liveIssueChatTx returns the id of the live chat open on issueID, or 0.
func liveIssueChatTx(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, issueID int64,
) (int64, error) {
	var id int64
	err := q.QueryRowContext(ctx, issueChatOccupantSQL("?"),
		append([]any{issueID}, terminalChatStateArgs()...)...).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return 0, nil
	case err != nil:
		return 0, fmt.Errorf("read issue %d live chat: %w", issueID, err)
	default:
		return id, nil
	}
}

// issueMainBranchTx is the issue's main branch inside tx: a main-role task's,
// else an issue chat's (task 140 decision 4), or "" when it has none.
// excludeTaskID is never consulted, which is the task being bound.
func issueMainBranchTx(ctx context.Context, tx *sql.Tx, issueID, excludeTaskID int64) (string, error) {
	var main sql.NullString
	err := tx.QueryRowContext(ctx, `SELECT COALESCE(
		(SELECT branch_name FROM tasks
		  WHERE issue_id = ? AND issue_worktree = 'main' AND archived_at IS NULL AND id <> ?
		  ORDER BY id LIMIT 1),
		(`+issueChatBranchSQL("?")+`))`, issueID, excludeTaskID, issueID).Scan(&main)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return "", fmt.Errorf("read issue %d main branch: %w", issueID, err)
	}
	return main.String, nil
}

// CreateIssueChat inserts c, which names its issue in IssueID, as the
// issue's main-worktree holder (task 140 decisions 1, 2 and 9), and writes
// chat.created and any issue.lane_changed in the same transaction.
//
// Inside the transaction it refuses a second live chat on the issue
// (*IssueHasLiveChatError) and a main task occupying the worktree
// (*IssueWorktreeOccupiedError), so two concurrent opens cannot both win.
// The chat's branch is the issue's main branch when it has one — a queued
// main task's resolved name counts, which is what lets that task wait behind
// the chat — and otherwise nameBranch(id), which then becomes the main
// branch. c.Branch must be "" or that main branch: anything else is
// *MainBranchMismatchError.
func (s *Store) CreateIssueChat(ctx context.Context, c *Chat, nameBranch func(int64) string) error {
	if c.IssueID == nil {
		return fmt.Errorf("insert issue chat: no issue")
	}
	issueID := *c.IssueID
	now := time.Now()
	if c.CreatedAt.IsZero() {
		c.CreatedAt = now
	}
	c.UpdatedAt = now
	if c.State == "" {
		c.State = chatstate.Idle
	}
	refreshJSON, err := marshalBaseRefresh(c.BaseRefresh)
	if err != nil {
		return fmt.Errorf("insert chat: %w", err)
	}
	var ev, laneEv *Event
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		if live, err := liveIssueChatTx(ctx, tx, issueID); err != nil {
			return err
		} else if live != 0 {
			return &IssueHasLiveChatError{IssueID: issueID, ChatID: live}
		}
		var occupant sql.NullInt64
		args := append([]any{issueID}, issueMainOccupantArgs()...)
		if err := tx.QueryRowContext(ctx, issueMainOccupantSQL("?", ""), args...).Scan(&occupant); err != nil &&
			!errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("read issue %d main occupant: %w", issueID, err)
		}
		if occupant.Valid {
			return &IssueWorktreeOccupiedError{IssueID: issueID, TaskID: occupant.Int64}
		}
		main, err := issueMainBranchTx(ctx, tx, issueID, 0)
		if err != nil {
			return err
		}
		if main != "" && c.Branch != "" && c.Branch != main {
			return &MainBranchMismatchError{IssueID: issueID, Branch: c.Branch, MainBranch: main}
		}
		watch, err := watchIssueLaneTx(ctx, tx, issueID, c.ProjectID)
		if err != nil {
			return err
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO chats (project_id, title, state, agent, model, effort, permission_mode,
				branch, adopted_branch, base_branch, base_sha, base_refresh, worktree_path, session_id, pending_input,
				opening_context, issue_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, '', 0, ?, ?, ?, NULL, NULL, NULL, ?, ?, ?, ?)`,
			c.ProjectID, c.Title, string(c.State), c.Agent, nullString(c.Model), nullString(c.Effort),
			c.PermissionMode, c.BaseBranch, nullString(c.BaseSHA), refreshJSON, nullString(c.OpeningContext),
			issueID, formatTime(c.CreatedAt), formatTime(c.UpdatedAt))
		if err != nil {
			return fmt.Errorf("insert chat: %w", err)
		}
		if c.ID, err = res.LastInsertId(); err != nil {
			return fmt.Errorf("insert chat: %w", err)
		}
		c.Branch = main
		if main == "" {
			c.Branch = nameBranch(c.ID)
		}
		if _, err := tx.ExecContext(ctx, `UPDATE chats SET branch = ? WHERE id = ?`, c.Branch, c.ID); err != nil {
			return fmt.Errorf("set chat %d branch: %w", c.ID, err)
		}
		if ev, err = chatEvent(EventChatCreated, c, nil); err != nil {
			return err
		}
		if err := appendEventTx(ctx, tx, ev); err != nil {
			return err
		}
		if laneEv, err = watch.changedTx(ctx, tx, nil, issuestate.Human); err != nil || laneEv == nil {
			return err
		}
		return appendEventTx(ctx, tx, laneEv)
	})
	if err != nil {
		return err
	}
	s.notify(ev)
	s.notify(laneEv)
	return nil
}

// IssueMainWorktreeChatHolder returns the terminal issue chat of issueID
// still naming a worktree — the directory a closed or archived issue chat
// left in place for the next holder (task 140 decision 3) — or (nil, nil).
func (s *Store) IssueMainWorktreeChatHolder(ctx context.Context, issueID int64) (*Chat, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats
		WHERE issue_id = ? AND state IN (?, ?) AND worktree_path IS NOT NULL AND worktree_path <> ''
		ORDER BY id LIMIT 1`, issueID, string(chatstate.Closed), string(chatstate.Archived))
	c, err := scanChat(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("issue %d main worktree chat holder: %w", issueID, err)
	}
	return c, nil
}

// TransferIssueWorktreeFromChat is TransferIssueWorktree with a released
// issue chat as the predecessor (task 140 decision 3): in one transaction the
// chat keeps tipSHA as its end_sha and lets go of path, and task toID
// receives it. The chat must still name path and be closed or archived, and
// the task must name nothing; otherwise ErrIssueWorktreeNotHeld.
func (s *Store) TransferIssueWorktreeFromChat(ctx context.Context, chatID, toID int64, path, tipSHA string) error {
	if path == "" {
		return fmt.Errorf("transfer issue worktree chat %d → task %d: %w", chatID, toID, ErrIssueWorktreeNotHeld)
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		var fromPath, toPath sql.NullString
		var st string
		if err := tx.QueryRowContext(ctx,
			`SELECT worktree_path, state FROM chats WHERE id = ?`, chatID).Scan(&fromPath, &st); err != nil {
			return fmt.Errorf("read chat %d worktree: %w", chatID, err)
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT worktree_path FROM tasks WHERE id = ?`, toID).Scan(&toPath); err != nil {
			return fmt.Errorf("read task %d worktree: %w", toID, err)
		}
		released := st == string(chatstate.Closed) || st == string(chatstate.Archived)
		if fromPath.String != path || toPath.String != "" || !released {
			return fmt.Errorf("transfer issue worktree chat %d (%s) → task %d: %w",
				chatID, st, toID, ErrIssueWorktreeNotHeld)
		}
		now := formatTime(time.Now())
		if _, err := tx.ExecContext(ctx, `
			UPDATE chats SET end_sha = ?, worktree_path = NULL, updated_at = ? WHERE id = ?`,
			nullString(tipSHA), now, chatID); err != nil {
			return fmt.Errorf("release chat %d worktree: %w", chatID, err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE tasks SET worktree_path = ?, base_sha = ?, base_refresh = NULL, end_sha = NULL, updated_at = ?
			WHERE id = ?`, path, nullString(tipSHA), now, toID); err != nil {
			return fmt.Errorf("hand task %d worktree: %w", toID, err)
		}
		return nil
	})
}

// IssueWorktreeHolderRef names the predecessor an issue chat receives the
// main worktree from: a settled main task or a released issue chat.
type IssueWorktreeHolderRef struct {
	TaskID, ChatID int64
	Path           string
}

// IssueMainWorktreeAnyHolder is the directory a new issue chat receives
// (task 140 decision 1): a settled main task's, else a released issue
// chat's, or a zero ref when nothing names one.
func (s *Store) IssueMainWorktreeAnyHolder(ctx context.Context, issueID int64) (IssueWorktreeHolderRef, error) {
	t, err := s.IssueMainWorktreeHolder(ctx, issueID, 0)
	if err != nil {
		return IssueWorktreeHolderRef{}, err
	}
	if t != nil {
		return IssueWorktreeHolderRef{TaskID: t.ID, Path: t.WorktreePath}, nil
	}
	c, err := s.IssueMainWorktreeChatHolder(ctx, issueID)
	if err != nil || c == nil {
		return IssueWorktreeHolderRef{}, err
	}
	return IssueWorktreeHolderRef{ChatID: c.ID, Path: c.WorktreePath}, nil
}

// TransferIssueWorktreeToChat hands the issue's main worktree at path from
// its predecessor — task from.TaskID or chat from.ChatID — to the new issue
// chat chatID, in one transaction (task 140 decision 1, 134.12's shape). The
// predecessor keeps tipSHA as its end_sha; the chat starts from it as its
// base_sha. Either row having moved is ErrIssueWorktreeNotHeld.
func (s *Store) TransferIssueWorktreeToChat(
	ctx context.Context, from IssueWorktreeHolderRef, chatID int64, path, tipSHA string,
) error {
	if path == "" {
		return fmt.Errorf("transfer issue worktree to chat %d: %w", chatID, ErrIssueWorktreeNotHeld)
	}
	return s.withTx(ctx, func(tx *sql.Tx) error {
		table, id := "tasks", from.TaskID
		if from.ChatID != 0 {
			table, id = "chats", from.ChatID
		}
		var fromPath, toPath sql.NullString
		if err := tx.QueryRowContext(ctx,
			`SELECT worktree_path FROM `+table+` WHERE id = ?`, id).Scan(&fromPath); err != nil {
			return fmt.Errorf("read %s %d worktree: %w", table, id, err)
		}
		if err := tx.QueryRowContext(ctx,
			`SELECT worktree_path FROM chats WHERE id = ?`, chatID).Scan(&toPath); err != nil {
			return fmt.Errorf("read chat %d worktree: %w", chatID, err)
		}
		if fromPath.String != path || toPath.String != "" {
			return fmt.Errorf("transfer issue worktree %s %d → chat %d: %w", table, id, chatID, ErrIssueWorktreeNotHeld)
		}
		if from.TaskID != 0 {
			if chatOpen, err := openLinkedChatTx(ctx, tx, from.TaskID); err != nil {
				return err
			} else if chatOpen != 0 {
				return &TaskLockedError{TaskID: from.TaskID, ChatID: chatOpen}
			}
		}
		now := formatTime(time.Now())
		//nolint:gosec // G202: table is one of two constants above.
		if _, err := tx.ExecContext(ctx, `
			UPDATE `+table+` SET end_sha = ?, worktree_path = NULL, updated_at = ? WHERE id = ?`,
			nullString(tipSHA), now, id); err != nil {
			return fmt.Errorf("release %s %d worktree: %w", table, id, err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE chats SET worktree_path = ?, base_sha = ?, base_refresh = NULL, end_sha = NULL, updated_at = ?
			WHERE id = ?`, path, nullString(tipSHA), now, chatID); err != nil {
			return fmt.Errorf("hand chat %d worktree: %w", chatID, err)
		}
		return nil
	})
}

// SetChatEndSHA stamps the commit an issue chat's work ended on (task 140
// decision 3), at its release.
func (s *Store) SetChatEndSHA(ctx context.Context, id int64, sha string) error {
	if _, err := s.db.ExecContext(ctx, `UPDATE chats SET end_sha = ? WHERE id = ?`, nullString(sha), id); err != nil {
		return fmt.Errorf("set chat %d end_sha: %w", id, err)
	}
	return nil
}

// IssueChatBranchHeld reports whether an issue chat of issueID has carried
// branch, which makes an existing branch the issue's own when a main task
// revives it (task 140 decision 4, IssueMainBranchHeld's chat half).
func (s *Store) IssueChatBranchHeld(ctx context.Context, issueID int64, branch string) (bool, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chats WHERE issue_id = ? AND branch = ?`,
		issueID, branch).Scan(&n); err != nil {
		return false, fmt.Errorf("issue %d chat branch %q: %w", issueID, branch, err)
	}
	return n > 0, nil
}

// IssueHasWaitingMainTask reports whether an unarchived main-role task of
// issueID has not settled — a successor queued for the main worktree, which
// an archived issue chat leaves the directory in place for (task 140
// decision 4).
func (s *Store) IssueHasWaitingMainTask(ctx context.Context, issueID int64) (bool, error) {
	args := append([]any{issueID}, settledTaskStates()...)
	n, err := s.countTasks(ctx, `SELECT COUNT(*) FROM tasks
		WHERE issue_id = ? AND issue_worktree = 'main' AND archived_at IS NULL
		  AND state NOT IN `+placeholders(len(settledTaskStates())), args...)
	if err != nil {
		return false, fmt.Errorf("issue %d waiting main task: %w", issueID, err)
	}
	return n > 0, nil
}
