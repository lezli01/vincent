package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/lezli01/vincent/internal/chatstate"
)

// Chat event types (spec §13.3, task 063). They ride the one durable event
// table and the one broker: a client that already follows `GET /v1/events`
// sees chats without a second stream.
const (
	EventChatCreated  = "chat.created"
	EventChatState    = "chat.state_changed"
	EventChatTurn     = "chat.turn_changed"
	EventChatArchived = "chat.archived"
	// EventChatHandedOff is a chat whose worktree and branch now belong to a
	// task (task 074). It carries the task's id, so a client that follows the
	// stream can link the two without a fetch.
	EventChatHandedOff = "chat.handed_off"
	// EventChatQueue is a chat's queue of follow-up messages changing (task
	// 156): a message queued, edited, deleted or started, the queue paused,
	// resumed or cleared. It carries queued_count and queue_paused, so a
	// client re-renders the badge without a fetch.
	EventChatQueue = "chat.queue_changed"
	// EventChatUpdated is a chat whose display or run settings changed
	// without a state change: a rename (task 161), or its model, effort or
	// permission mode changed by PATCH /v1/chats/{id} (task 162) — which
	// applies from the next turn when made during a live one. It carries the
	// chat, so a list repaints without a fetch.
	EventChatUpdated = "chat.updated"
	// EventChatViewed is a person having looked at the chat (task 162): its
	// last_viewed_at advanced, so its unread mark may have cleared in every
	// client, not just the one that viewed it.
	EventChatViewed = "chat.viewed"
)

// ChatEventTypes is the chat.* family, in the order it is declared. It exists
// so a per-chat SSE replay can narrow the events table to chat rows before
// filtering on the payload's id: a chat event carries no task_id column, so
// without this a replay would scan every task event behind the cursor.
func ChatEventTypes() []string {
	return []string{EventChatCreated, EventChatState, EventChatTurn, EventChatArchived, EventChatHandedOff, EventChatClosed, EventChatQueue, EventChatUpdated, EventChatDraft, EventChatViewed}
}

// IsChatEvent reports whether an event type belongs to the chat family.
func IsChatEvent(t string) bool {
	return slices.Contains(ChatEventTypes(), t)
}

const chatColumns = `id, project_id, title, state, agent, model, effort, permission_mode,
	branch, adopted_branch, base_branch, base_sha, base_refresh, worktree_path, session_id, pending_input, handoff_task_id,
	linked_task_id, opening_context, issue_id, end_sha, created_at, updated_at,
	queue_paused, queue_pause_reason, draft, stash, draft_updated_at,
	(SELECT COUNT(*) FROM chat_queued_messages q WHERE q.chat_id = chats.id),
	last_viewed_at, ` + chatUnreadExpr

// chatUnreadExpr derives Chat.Unread in SQL (task 162) so a list stays one
// query: the chat's latest terminal turn finished after it was last viewed,
// or it has one at all when it was never viewed. A running turn never counts
// — there is nothing to read until it ends. ended_at and last_viewed_at are
// both store.TimeFormat, so the TEXT comparison is chronological; an
// interrupted turn recovery left without ended_at falls back to started_at.
// It names the table unaliased, which every `SELECT chatColumns FROM chats`
// is.
const chatUnreadExpr = `COALESCE((
		SELECT MAX(COALESCE(ct.ended_at, ct.started_at)) FROM chat_turns ct
		WHERE ct.chat_id = chats.id AND ct.state IN ('done', 'failed', 'interrupted')
	) > COALESCE(chats.last_viewed_at, ''), 0) AS unread`

const chatTurnColumns = `id, chat_id, seq, prompt, state, fail_reason, error_message, result_text,
	session_id, input_tokens, output_tokens, cost_usd, exit_code, pid, proc_identity,
	started_at, ended_at, duration_ms, session_input_tokens, session_output_tokens,
	cache_read_tokens, cache_creation_tokens, context_tokens, context_window,
	session_cache_read_tokens, session_cache_creation_tokens, model, effort, permission_mode,
	mention_context`

// CreateChat inserts c, assigning its ID and timestamps, and writes the
// durable chat.created event in the same transaction (§13.3).
//
// The chat is created before its worktree exists, exactly as a task is: the id
// is what names the branch and the directory, so it has to be allocated first.
// The worktree claim lands in the same CreateAndClaim window tasks use (§10).
func (s *Store) CreateChat(ctx context.Context, c *Chat) error {
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
	var ev *Event
	err = s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO chats (project_id, title, state, agent, model, effort, permission_mode,
				branch, adopted_branch, base_branch, base_sha, base_refresh, worktree_path, session_id, pending_input,
				created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ProjectID, c.Title, string(c.State), c.Agent, nullString(c.Model), nullString(c.Effort),
			c.PermissionMode, c.Branch, c.AdoptedBranch, c.BaseBranch, nullString(c.BaseSHA), refreshJSON, nullString(c.WorktreePath),
			nullString(c.SessionID), nullString(string(c.PendingInput)),
			formatTime(c.CreatedAt), formatTime(c.UpdatedAt))
		if err != nil {
			return fmt.Errorf("insert chat: %w", err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return fmt.Errorf("insert chat: %w", err)
		}
		c.ID = id
		ev, err = chatEvent(EventChatCreated, c, nil)
		if err != nil {
			return err
		}
		return appendEventTx(ctx, tx, ev)
	})
	if err != nil {
		return err
	}
	s.notify(ev)
	return nil
}

// chatEvent builds a chat.* event. It carries the chat's id, title and state
// so a list can be re-rendered without a fetch, and nothing more — the same
// shape, and the same reasoning, as the task events beside it (§13.3).
//
// TaskID stays nil: a chat is not a task, and a per-task stream must never
// deliver one. Clients filter chat events by type, and `GET /v1/chats/{id}/events`
// filters by the payload's id.
func chatEvent(evType string, c *Chat, turn *ChatTurn) (*Event, error) {
	body := map[string]any{"id": c.ID, "title": c.Title, "state": string(c.State)}
	// A chat opened on an issue names it (task 140.9): its state moves the
	// issue's row — the occupant, `attention` while it waits on a person —
	// and a client re-reads the issue without fetching the chat first.
	if c.IssueID != nil {
		body["issue_id"] = *c.IssueID
	}
	if turn != nil {
		body["turn_id"] = turn.ID
		body["turn_seq"] = turn.Seq
		body["turn_state"] = string(turn.State)
		if turn.FailReason != "" {
			body["fail_reason"] = turn.FailReason
		}
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", evType, err)
	}
	pid := c.ProjectID
	ev := &Event{Type: evType, ProjectID: &pid, Payload: payload}
	// A linked chat's events name its task (task 119), so a client can
	// re-fetch the task whose lock the chat just placed or lifted.
	if err := linkedChatEventPayload(ev, c); err != nil {
		return nil, err
	}
	return ev, nil
}

// GetChat returns the chat with the given id, or ErrNotFound.
func (s *Store) GetChat(ctx context.Context, id int64) (*Chat, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE id = ?`, id)
	c, err := scanChat(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("chat %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get chat %d: %w", id, err)
	}
	return c, nil
}

// ChatFilter narrows ListChats. Zero values mean "no filter", except
// Archived, whose zero value excludes both terminal states.
//
// That default reverses task 074's: the original comment here reasoned that a
// listing which hid archived rows would make `vincent chat list --all` a lie,
// but the lie it produced instead was the chats board, where every chat that
// ever existed piles up forever and a `handed_off` row sits beside a live
// conversation (issue #298). The caller still decides — it just says so with
// ArchivedOnly or ArchivedAll rather than by being the only party that could
// filter, which no caller ever was.
type ChatFilter struct {
	ProjectID *int64
	// TaskID narrows to the chats linked to one task (task 119), which is how
	// a task's workspace lists its conversations.
	TaskID *int64
	// IssueID narrows to the conversations that concern one issue (#931): a
	// chat opened on the issue (`issue_id`, task 140), a chat linked to any
	// task of the issue (`linked_task_id`, task 119), or a free chat handed
	// off into one (`handoff_task_id`, task 074). Zero means no filter, the
	// way TaskFilter.IssueID's zero does.
	//
	// "Any task" includes fan-out lanes, which TaskFilter.IssueID leaves out
	// on purpose: task 130 decision 5 keeps lanes off "the issue's tasks"
	// because a lane is part of its root's work, not a separate item of work,
	// and that stands for the tasks list. This filter answers a different
	// question — which conversations concern the issue — and a chat on a lane
	// is still one of those. Lanes are not Work-tab rows, so without this the
	// chat would appear nowhere on its issue.
	IssueID int64
	States  []chatstate.State
	// Archived selects how terminal chats are treated, the way TaskFilter's
	// field of the same name and type does (§13.2). It covers *every*
	// terminal state — `archived`, `handed_off` and `closed` alike (§5.5,
	// task 074 decision 5, task 119) — because all are equally done with,
	// whatever the parameter's name says. An explicit States always wins.
	Archived ArchivedFilter
	// ArchivedBefore and ArchivedSince bound when the chat ended, measured
	// over `updated_at` and with no new column (task 092). Task 074 decision
	// 6 and task 079 decision 2 both recorded that a terminal transition is
	// the last write a chat row takes, so `updated_at` already *is* when it
	// ended, and TerminalChatIDsBefore has measured retention off it since.
	// Zero means no bound.
	//
	// Unlike the task side these do not narrow to terminal rows on their own —
	// every chat has an `updated_at` — so a caller that wants the archived
	// board's meaning sets Archived as well, which is what the API does.
	ArchivedBefore time.Time
	ArchivedSince  time.Time
	// Limit and Offset page the listing, the way TaskFilter's fields of the
	// same name do. 0 = unlimited.
	Limit  int
	Offset int
}

// ListChats returns chats newest first, which is the order a conversation
// list is read in.
func (s *Store) ListChats(ctx context.Context, f ChatFilter) ([]Chat, error) {
	q := `SELECT ` + chatColumns + ` FROM chats WHERE 1=1`
	var args []any
	archivedOnly := f.Archived == ArchivedOnly && len(f.States) == 0
	if f.ProjectID != nil {
		q += ` AND project_id = ?`
		args = append(args, *f.ProjectID)
	}
	if f.TaskID != nil {
		q += ` AND linked_task_id = ?`
		args = append(args, *f.TaskID)
	}
	if f.IssueID != 0 {
		// Each arm is indexed: idx_chats_issue, chats_linked_task_idx,
		// chats_handoff_task_idx, and tasks_issue_idx for the subqueries.
		q += ` AND (issue_id = ?` +
			` OR linked_task_id IN (SELECT id FROM tasks WHERE issue_id = ?)` +
			` OR handoff_task_id IN (SELECT id FROM tasks WHERE issue_id = ?))`
		args = append(args, f.IssueID, f.IssueID, f.IssueID)
	}
	if len(f.States) > 0 {
		//nolint:gosec // G202: placeholders renders bind markers only, never values.
		q += ` AND state IN ` + placeholders(len(f.States))
		for _, st := range f.States {
			args = append(args, string(st))
		}
	} else {
		// An explicit States always wins, for the reason an explicit State
		// wins over TaskFilter.Archived: asking for state=archived and
		// getting nothing back because the default excludes archives would
		// be absurd.
		switch f.Archived {
		case ArchivedExclude:
			q += ` AND state NOT IN (?, ?, ?)`
			args = append(args, terminalChatStateArgs()...)
		case ArchivedOnly:
			q += ` AND state IN (?, ?, ?)`
			args = append(args, terminalChatStateArgs()...)
		case ArchivedAll:
		}
	}
	if !f.ArchivedBefore.IsZero() {
		q += ` AND updated_at < ?`
		args = append(args, formatTime(f.ArchivedBefore))
	}
	if !f.ArchivedSince.IsZero() {
		q += ` AND updated_at >= ?`
		args = append(args, formatTime(f.ArchivedSince))
	}
	// A terminal-only listing is newest-*ended* first, the chat mirror of the
	// task side's `archived_at DESC` (task 092): `id DESC` would sort by when
	// the conversation started, and an archive is read by when it ended.
	if archivedOnly {
		q += ` ORDER BY updated_at DESC, id DESC`
	} else {
		q += ` ORDER BY id DESC`
	}
	if f.Limit > 0 || f.Offset > 0 {
		limit := f.Limit
		if limit == 0 {
			limit = -1 // SQLite: negative LIMIT means unlimited
		}
		q += ` LIMIT ? OFFSET ?`
		args = append(args, limit, f.Offset)
	}
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list chats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []Chat
	for rows.Next() {
		c, err := scanChat(rows)
		if err != nil {
			return nil, fmt.Errorf("scan chat: %w", err)
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chats: %w", err)
	}
	return out, nil
}

// SetChatState moves a chat to state and publishes chat.state_changed. It is
// the one write that changes §5.5 state; internal/chatrun is its only caller
// besides the API's archive and cancel handlers, which mirrors the task path's
// single-writer arrangement.
func (s *Store) SetChatState(ctx context.Context, id int64, st chatstate.State) (*Chat, error) {
	return s.updateChat(ctx, id, EventChatState, func(c *Chat) {
		if chatstate.Terminal(c.State) {
			// A terminal chat stays terminal. The one writer that can
			// reach here late is a turn's finish racing a close (task
			// 119): the close already waited for the turn, but a write
			// that lands after it must not reopen the chat.
			return
		}
		c.State = st
		if st != chatstate.AwaitingInput {
			// Leaving awaiting_input for any reason retires the request: a
			// pending question outliving the process that asked it is an
			// answer route with nothing to answer (§7.4).
			c.PendingInput = nil
		}
	})
}

// SetChatTitle renames a chat and publishes chat.updated (task 161). It is
// legal in every state, terminal ones included: the title is display only.
// The branch is never touched — it was named from the title once, at
// creation, and keeps that slug.
func (s *Store) SetChatTitle(ctx context.Context, id int64, title string) (*Chat, error) {
	return s.updateChat(ctx, id, EventChatUpdated, func(c *Chat) { c.Title = title })
}

// SetChatSession records the agent CLI's own session id for the chat (§7.3,
// amended). It is written after every turn, not just the first: claude may
// hand a resumed conversation a new id, and the next turn must resume the one
// that actually ran.
func (s *Store) SetChatSession(ctx context.Context, id int64, sessionID string) (*Chat, error) {
	return s.updateChat(ctx, id, "", func(c *Chat) { c.SessionID = sessionID })
}

// ChatSettingsPatch is a PATCH /v1/chats/{id} (task 162 decision 5): each
// non-nil field replaces the chat's own. An empty Model or Effort clears it
// back to the adapter's default.
type ChatSettingsPatch struct {
	Title          *string
	Model          *string
	Effort         *string
	PermissionMode *string
	// KeepModelOnceStarted refuses a Model or Effort that differs from the
	// row's once the chat has a session (task 162 decision 8), for an
	// adapter with no capture of a resumed session honouring the change.
	// The API decides it from the adapter; the store judges "started" from
	// the row it is about to write, so a first turn that begins between the
	// API's read and this write cannot slip a model change past the check
	// (review F2).
	KeepModelOnceStarted bool
}

// ChatStarted reports whether c has begun a session: a turn has run or is
// running, so a later one resumes rather than starts (task 162 decision 8).
func ChatStarted(c *Chat) bool {
	return c.SessionID != "" || chatstate.HoldsProcess(c.State)
}

// ErrChatModelFixed is a model or effort change refused because the chat has
// started a session its adapter cannot resume on a different one (task 162
// decision 8). The API turns it into agent_cannot_change_on_resume.
var ErrChatModelFixed = errors.New("the chat's session keeps the model and effort it started with")

// UpdateChatSettings applies p to the chat and publishes chat.updated. A
// terminal chat is ErrInvalidChatAction: its conversation is over (task 162
// decision 5). A live turn keeps what it was started with — its settings are
// on its turn row — so the change applies from the next turn. With
// p.KeepModelOnceStarted, a changed model or effort on a started chat is
// ErrChatModelFixed, judged inside the write's transaction.
func (s *Store) UpdateChatSettings(ctx context.Context, id int64, p ChatSettingsPatch) (*Chat, error) {
	return s.updateChatOrRefuse(ctx, id, EventChatUpdated, func(c *Chat) error {
		if chatstate.Terminal(c.State) {
			return fmt.Errorf("chat %d is %s: %w", id, c.State, ErrInvalidChatAction)
		}
		if p.KeepModelOnceStarted && ChatStarted(c) &&
			((p.Model != nil && *p.Model != c.Model) || (p.Effort != nil && *p.Effort != c.Effort)) {
			return fmt.Errorf("chat %d: %w", id, ErrChatModelFixed)
		}
		if p.Title != nil {
			c.Title = *p.Title
		}
		if p.Model != nil {
			c.Model = *p.Model
		}
		if p.Effort != nil {
			c.Effort = *p.Effort
		}
		if p.PermissionMode != nil {
			c.PermissionMode = *p.PermissionMode
		}
		return nil
	})
}

// SetChatPendingInput stores or clears the §7.4 request a chat is waiting on.
func (s *Store) SetChatPendingInput(ctx context.Context, id int64, req json.RawMessage) (*Chat, error) {
	return s.updateChat(ctx, id, "", func(c *Chat) { c.PendingInput = req })
}

// SetChatBranch records the branch name, which is only knowable once the row
// has an id (§10). It is a separate write for the same reason a task's is.
func (s *Store) SetChatBranch(ctx context.Context, id int64, branch string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE chats SET branch = ?, updated_at = ? WHERE id = ?`,
		branch, formatTime(time.Now()), id); err != nil {
		return fmt.Errorf("set chat %d branch: %w", id, err)
	}
	return nil
}

// SetChatWorktree records or clears the §10 claim. Clearing it is archive's
// half of the claim window: the directory is gone, so the row must stop
// naming it before the lock is released (task 005).
func (s *Store) SetChatWorktree(ctx context.Context, id int64, path, baseSHA string) (*Chat, error) {
	return s.updateChat(ctx, id, "", func(c *Chat) {
		c.WorktreePath = path
		if baseSHA != "" {
			c.BaseSHA = baseSHA
		}
	})
}

// updateChat applies mutate to the row and writes it back, publishing evType
// when it is non-empty. Read-modify-write under the store's single connection
// is safe for the same reason the task path's is: one writer, serialized.
func (s *Store) updateChat(ctx context.Context, id int64, evType string, mutate func(*Chat)) (*Chat, error) {
	return s.updateChatOrRefuse(ctx, id, evType, func(c *Chat) error {
		mutate(c)
		return nil
	})
}

// updateChatOrRefuse is updateChat whose mutate may refuse the write: a
// non-nil error rolls the transaction back and is returned as it is.
func (s *Store) updateChatOrRefuse(ctx context.Context, id int64, evType string, mutate func(*Chat) error) (*Chat, error) {
	var out *Chat
	var ev *Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE id = ?`, id)
		c, err := scanChat(row)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("chat %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("get chat %d: %w", id, err)
		}
		if err := mutate(c); err != nil {
			return err
		}
		c.UpdatedAt = time.Now()
		if chatstate.Terminal(c.State) {
			// Archive deletes the queue in the transaction that ends the
			// chat (task 156 decision 6): nothing can ever drain it.
			if err := deleteChatQueueTx(ctx, tx, c.ID); err != nil {
				return err
			}
			if err := clearChatDraftTx(ctx, tx, c); err != nil {
				return err
			}
			if err := setQueuePauseTx(ctx, tx, c, false, ""); err != nil {
				return err
			}
		}
		// base_refresh is written back as read unless mutate replaced it, so
		// SetChatWorktree clearing the claim keeps the record (task 099).
		refreshJSON, err := marshalBaseRefresh(c.BaseRefresh)
		if err != nil {
			return fmt.Errorf("update chat %d: %w", id, err)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE chats SET title = ?, state = ?, model = ?, effort = ?, permission_mode = ?, base_sha = ?,
				base_refresh = ?, worktree_path = ?, session_id = ?, pending_input = ?, updated_at = ?
			WHERE id = ?`,
			c.Title, string(c.State), nullString(c.Model), nullString(c.Effort), c.PermissionMode, nullString(c.BaseSHA),
			refreshJSON, nullString(c.WorktreePath), nullString(c.SessionID), nullString(string(c.PendingInput)),
			formatTime(c.UpdatedAt), c.ID); err != nil {
			return fmt.Errorf("update chat %d: %w", id, err)
		}
		out = c
		if evType == "" {
			return nil
		}
		ev, err = chatEvent(evType, c, nil)
		if err != nil {
			return err
		}
		return appendEventTx(ctx, tx, ev)
	})
	if err != nil {
		return nil, err
	}
	s.notify(ev)
	return out, nil
}

// MarkChatViewed records that a person looked at chat id at `at` (task 162)
// and returns the chat as it now reads, Unread re-derived. It is monotonic:
// last_viewed_at becomes max(existing, at), so a client with a slow clock or
// a late request never resurrects an unread mark another client cleared.
// The chat.viewed event is written, in the same transaction, only when the
// value advanced — a repeat view announces nothing. updated_at is left
// alone on purpose: it orders the archive and drives auto-archive, and
// looking at a chat is not a change to it.
func (s *Store) MarkChatViewed(ctx context.Context, id int64, at time.Time) (*Chat, error) {
	var out *Chat
	var ev *Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			UPDATE chats SET last_viewed_at = ?
			WHERE id = ? AND (last_viewed_at IS NULL OR last_viewed_at < ?)`,
			formatTime(at), id, formatTime(at))
		if err != nil {
			return fmt.Errorf("mark chat %d viewed: %w", id, err)
		}
		n, err := res.RowsAffected()
		if err != nil {
			return fmt.Errorf("mark chat %d viewed: %w", id, err)
		}
		c, err := scanChat(tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE id = ?`, id))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("chat %d: %w", id, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("get chat %d: %w", id, err)
		}
		out = c
		if n == 0 {
			return nil
		}
		ev, err = chatEvent(EventChatViewed, c, nil)
		if err != nil {
			return err
		}
		return appendEventTx(ctx, tx, ev)
	})
	if err != nil {
		return nil, err
	}
	s.notify(ev)
	return out, nil
}

// CreateChatTurn inserts a running turn, assigning its seq, and moves the chat
// to `running` in the same transaction. Both halves are one write because a
// chat that is running with no turn row — or the reverse — is the state
// recovery cannot tell from a crash (§12.4).
func (s *Store) CreateChatTurn(ctx context.Context, chatID int64, prompt string) (*ChatTurn, error) {
	return s.CreateChatTurnWith(ctx, chatID, prompt, TurnSettings{})
}

// CreateChatTurnWith is CreateChatTurn with per-turn overrides (task 162
// decision 6): each non-empty field of ov replaces the chat's own setting for
// this turn only, and the resolved values are recorded on the turn row.
func (s *Store) CreateChatTurnWith(ctx context.Context, chatID int64, prompt string, ov TurnSettings) (*ChatTurn, error) {
	var turn *ChatTurn
	var ev *Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE id = ?`, chatID)
		c, err := scanChat(row)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("chat %d: %w", chatID, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("get chat %d: %w", chatID, err)
		}
		// Send is legal from a live chat too, but there it queues (task
		// 156): only an idle chat's send starts a turn.
		next, ok := chatstate.Next(c.State, chatstate.Send)
		if !ok || c.State != chatstate.Idle {
			return fmt.Errorf("chat %d: %w", chatID, ErrInvalidChatAction)
		}
		t, err := insertChatTurnTx(ctx, tx, c, prompt, ov)
		if err != nil {
			return err
		}
		c.State = next
		c.UpdatedAt = time.Now()
		if _, err := tx.ExecContext(ctx,
			`UPDATE chats SET state = ?, updated_at = ? WHERE id = ?`,
			string(c.State), formatTime(c.UpdatedAt), c.ID); err != nil {
			return fmt.Errorf("update chat %d: %w", chatID, err)
		}
		turn = t
		ev, err = chatEvent(EventChatTurn, c, t)
		if err != nil {
			return err
		}
		return appendEventTx(ctx, tx, ev)
	})
	if err != nil {
		return nil, err
	}
	s.notify(ev)
	return turn, nil
}

// insertChatTurnTx inserts a running turn for chat c with the next seq. The
// caller moves the chat's state; this writes the turn row only.
//
// The turn's model, effort and permission mode are resolved here, in the
// transaction that writes the row (task 162 decision 7): ov's field, else the
// chat's current setting. The runner runs the turn with exactly what is
// recorded, so a PATCH that lands while the turn runs cannot change it.
func insertChatTurnTx(ctx context.Context, tx *sql.Tx, c *Chat, prompt string, ov TurnSettings) (*ChatTurn, error) {
	chatID := c.ID
	var seq int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM chat_turns WHERE chat_id = ?`, chatID).Scan(&seq); err != nil {
		return nil, fmt.Errorf("next chat turn seq: %w", err)
	}
	t := &ChatTurn{
		ChatID: chatID, Seq: seq, Prompt: prompt,
		State: chatstate.TurnRunning, SessionID: c.SessionID, StartedAt: time.Now(),
	}
	resolved := ov.Resolve(c)
	t.Model, t.Effort, t.PermissionMode = resolved.Model, resolved.Effort, resolved.PermissionMode
	res, err := tx.ExecContext(ctx, `
		INSERT INTO chat_turns (chat_id, seq, prompt, state, session_id, started_at, model, effort, permission_mode)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.ChatID, t.Seq, t.Prompt, string(t.State), nullString(t.SessionID), formatTime(t.StartedAt),
		nullString(t.Model), nullString(t.Effort), nullString(t.PermissionMode))
	if err != nil {
		return nil, fmt.Errorf("insert chat turn: %w", err)
	}
	if t.ID, err = res.LastInsertId(); err != nil {
		return nil, fmt.Errorf("insert chat turn: %w", err)
	}
	return t, nil
}

// ErrInvalidChatAction is a §5.5 transition the FSM does not allow. The API
// turns it into a 409, the way an invalid §6 action becomes one.
var ErrInvalidChatAction = errors.New("action not allowed in this chat state")

// UpdateChatTurn writes the whole turn row back and publishes
// chat.turn_changed. The chat's own state is not touched here: a turn ending
// and the chat returning to idle are separate transitions, and chatrun does
// them in that order so a reader never sees an idle chat with a running turn.
func (s *Store) UpdateChatTurn(ctx context.Context, t *ChatTurn) error {
	var ev *Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := updateChatTurnTx(ctx, tx, t); err != nil {
			return err
		}
		row := tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE id = ?`, t.ChatID)
		c, err := scanChat(row)
		if err != nil {
			return fmt.Errorf("get chat %d: %w", t.ChatID, err)
		}
		ev, err = chatEvent(EventChatTurn, c, t)
		if err != nil {
			return err
		}
		return appendEventTx(ctx, tx, ev)
	})
	if err != nil {
		return err
	}
	s.notify(ev)
	return nil
}

// updateChatTurnTx writes the whole turn row back inside tx.
func updateChatTurnTx(ctx context.Context, tx *sql.Tx, t *ChatTurn) error {
	if _, err := tx.ExecContext(ctx, `
		UPDATE chat_turns SET state = ?, fail_reason = ?, error_message = ?, result_text = ?,
			session_id = ?, input_tokens = ?, output_tokens = ?, cost_usd = ?, exit_code = ?,
			pid = ?, proc_identity = ?, ended_at = ?, duration_ms = ?,
			session_input_tokens = ?, session_output_tokens = ?,
			cache_read_tokens = ?, cache_creation_tokens = ?, context_tokens = ?, context_window = ?,
			session_cache_read_tokens = ?, session_cache_creation_tokens = ?
		WHERE id = ?`,
		string(t.State), nullString(t.FailReason), nullString(t.ErrorMessage), nullString(t.ResultText),
		nullString(t.SessionID), t.InputTokens, t.OutputTokens, t.CostUSD, t.ExitCode,
		t.PID, t.ProcIdentity, formatTimePtr(t.EndedAt), t.DurationMS,
		t.SessionInputTokens, t.SessionOutputTokens,
		t.CacheReadTokens, t.CacheCreationTokens, t.ContextTokens, t.ContextWindow,
		t.SessionCacheReadTokens, t.SessionCacheCreationTokens, t.ID); err != nil {
		return fmt.Errorf("update chat turn %d: %w", t.ID, err)
	}
	return nil
}

// SetChatTurnMentionContext records the block turn id's mentions expanded
// into (task 164 decision 2). It is written once, when the turn starts and
// before the agent is spawned, and never again: it is the snapshot of what the
// agent was given, which a later edit to the mentioned object must not change.
// It publishes nothing of its own — the turn's next chat.turn_changed carries
// it — because no client acts on the moment it is written.
func (s *Store) SetChatTurnMentionContext(ctx context.Context, id int64, mentionContext string) error {
	if _, err := s.db.ExecContext(ctx,
		`UPDATE chat_turns SET mention_context = ? WHERE id = ?`, nullString(mentionContext), id); err != nil {
		return fmt.Errorf("set chat turn %d mention context: %w", id, err)
	}
	return nil
}

// ListChatTurns returns a chat's turns in conversation order.
func (s *Store) ListChatTurns(ctx context.Context, chatID int64) ([]ChatTurn, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+chatTurnColumns+` FROM chat_turns WHERE chat_id = ? ORDER BY seq`, chatID)
	if err != nil {
		return nil, fmt.Errorf("list chat turns: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ChatTurn
	for rows.Next() {
		t, err := scanChatTurn(rows)
		if err != nil {
			return nil, fmt.Errorf("scan chat turn: %w", err)
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chat turns: %w", err)
	}
	return out, nil
}

// chatTurnCountBatch keeps one IN list well under SQLite's bound-variable limit.
const chatTurnCountBatch = 500

// ChatTurnCounts returns how many turns each of the chats has. A chat with no
// turns has no entry.
func (s *Store) ChatTurnCounts(ctx context.Context, chatIDs []int64) (map[int64]int, error) {
	out := make(map[int64]int, len(chatIDs))
	for ids := range slices.Chunk(chatIDs, chatTurnCountBatch) {
		if err := s.addChatTurnCounts(ctx, ids, out); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// addChatTurnCounts adds one batch's counts to out.
func (s *Store) addChatTurnCounts(ctx context.Context, ids []int64, out map[int64]int) error {
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	//nolint:gosec // G202: placeholders renders bind markers only, never values.
	rows, err := s.db.QueryContext(ctx,
		`SELECT chat_id, COUNT(*) FROM chat_turns WHERE chat_id IN `+placeholders(len(ids))+` GROUP BY chat_id`, args...)
	if err != nil {
		return fmt.Errorf("count chat turns: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return fmt.Errorf("scan chat turn count: %w", err)
		}
		out[id] = n
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate chat turn counts: %w", err)
	}
	return nil
}

// GetChatTurn returns one turn by id, or ErrNotFound.
func (s *Store) GetChatTurn(ctx context.Context, id int64) (*ChatTurn, error) {
	row := s.db.QueryRowContext(ctx, `SELECT `+chatTurnColumns+` FROM chat_turns WHERE id = ?`, id)
	t, err := scanChatTurn(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("chat turn %d: %w", id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get chat turn %d: %w", id, err)
	}
	return t, nil
}

// SessionUsage is the raw session totals one chat turn recorded (task 138.11
// decision 1, task 157 decision 3).
type SessionUsage struct {
	InputTokens, OutputTokens            int64
	CacheReadTokens, CacheCreationTokens int64
}

// PreviousSessionUsage returns the raw session totals recorded by the
// chat's latest turn before seq that ran on sessionID, and whether one
// exists (task 138.11 decision 1, §9.9). A turn that recorded no totals —
// an adapter reporting per-run figures, or one that never reached a usage
// line — is skipped, so the delta is taken from the last figure the session
// actually reported. A turn written before the cache totals were recorded
// (migration 0060) answers 0 for them, which makes the next turn's cache
// share the whole session's — the first-turn rule.
func (s *Store) PreviousSessionUsage(ctx context.Context, chatID int64, seq int, sessionID string) (SessionUsage, bool, error) {
	var u SessionUsage
	if sessionID == "" {
		return u, false, nil
	}
	err := s.db.QueryRowContext(ctx, `
		SELECT session_input_tokens, session_output_tokens,
			COALESCE(session_cache_read_tokens, 0), COALESCE(session_cache_creation_tokens, 0)
		FROM chat_turns
		WHERE chat_id = ? AND seq < ? AND session_id = ?
			AND session_input_tokens IS NOT NULL AND session_output_tokens IS NOT NULL
		ORDER BY seq DESC LIMIT 1`, chatID, seq, sessionID).Scan(
		&u.InputTokens, &u.OutputTokens, &u.CacheReadTokens, &u.CacheCreationTokens)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionUsage{}, false, nil
	}
	if err != nil {
		return SessionUsage{}, false, fmt.Errorf("previous session usage of chat %d: %w", chatID, err)
	}
	return u, true, nil
}

// ListRunningChatTurns returns every turn still marked running — what §12.4
// recovery finalizes as `interrupted` at startup (task 063 decision 5).
func (s *Store) ListRunningChatTurns(ctx context.Context) ([]ChatTurn, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+chatTurnColumns+` FROM chat_turns WHERE state = ? ORDER BY id`,
		string(chatstate.TurnRunning))
	if err != nil {
		return nil, fmt.Errorf("list running chat turns: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ChatTurn
	for rows.Next() {
		t, err := scanChatTurn(rows)
		if err != nil {
			return nil, fmt.Errorf("scan chat turn: %w", err)
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chat turns: %w", err)
	}
	return out, nil
}

// CountChatsHoldingProcess returns how many chats currently own a live agent
// process — the tally `max_parallel_chats` is checked against (§11, task 063
// decision 1).
func (s *Store) CountChatsHoldingProcess(ctx context.Context) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM chats WHERE state IN (?, ?)`,
		string(chatstate.Running), string(chatstate.AwaitingInput)).Scan(&n); err != nil {
		return 0, fmt.Errorf("count running chats: %w", err)
	}
	return n, nil
}

// ListChatWorktreeClaims is the chat half of gc's claim set (§10, task 063
// decision 9). A chat's worktree lives under the same root a task's does, and
// the reclaimer's rule is that the claim decides — so a chat that claims a
// directory keeps it, and a chat row that has been archived (worktree_path
// cleared) claims nothing, exactly like an archived task.
func (s *Store) ListChatWorktreeClaims(ctx context.Context) ([]ChatWorktreeClaim, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, COALESCE(worktree_path, ''), state FROM chats ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list chat worktree claims: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var out []ChatWorktreeClaim
	for rows.Next() {
		var c ChatWorktreeClaim
		if err := rows.Scan(&c.ChatID, &c.Path, (*string)(&c.State)); err != nil {
			return nil, fmt.Errorf("scan chat worktree claim: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chat worktree claims: %w", err)
	}
	return out, nil
}

// ListChatIDs returns every chat id, archived rows included — the transcript
// root's claim set, on the same terms ListTaskIDs sets.
func (s *Store) ListChatIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM chats ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list chat ids: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan chat id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate chat ids: %w", err)
	}
	return ids, nil
}

func scanChat(r rowScanner) (*Chat, error) {
	var c Chat
	var model, effort, baseSHA, baseRefresh, worktreePath, sessionID, pending sql.NullString
	var handoffTaskID, linkedTaskID sql.NullInt64
	var openingContext, endSHA sql.NullString
	var issueID sql.NullInt64
	var createdAt, updatedAt string
	var pauseReason, draftUpdatedAt, lastViewedAt sql.NullString
	if err := r.Scan(&c.ID, &c.ProjectID, &c.Title, (*string)(&c.State), &c.Agent, &model, &effort,
		&c.PermissionMode, &c.Branch, &c.AdoptedBranch, &c.BaseBranch, &baseSHA, &baseRefresh, &worktreePath, &sessionID, &pending,
		&handoffTaskID, &linkedTaskID, &openingContext, &issueID, &endSHA, &createdAt, &updatedAt,
		&c.QueuePaused, &pauseReason, &c.Draft, &c.Stash, &draftUpdatedAt, &c.QueuedCount,
		&lastViewedAt, &c.Unread); err != nil {
		return nil, err
	}
	var err error
	if c.DraftUpdatedAt, err = parseTimePtr(draftUpdatedAt); err != nil {
		return nil, err
	}
	c.QueuePauseReason = pauseReason.String
	if issueID.Valid {
		id := issueID.Int64
		c.IssueID = &id
	}
	c.EndSHA = endSHA.String
	if linkedTaskID.Valid {
		id := linkedTaskID.Int64
		c.LinkedTaskID = &id
	}
	c.OpeningContext = openingContext.String
	c.BaseRefresh = unmarshalBaseRefresh(baseRefresh)
	if handoffTaskID.Valid {
		id := handoffTaskID.Int64
		c.HandoffTaskID = &id
	}
	c.Model, c.Effort = model.String, effort.String
	c.BaseSHA, c.WorktreePath, c.SessionID = baseSHA.String, worktreePath.String, sessionID.String
	if pending.Valid && pending.String != "" {
		c.PendingInput = json.RawMessage(pending.String)
	}
	if c.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if c.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, err
	}
	if c.LastViewedAt, err = parseTimePtr(lastViewedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

func scanChatTurn(r rowScanner) (*ChatTurn, error) {
	var t ChatTurn
	var failReason, errMsg, resultText, sessionID, procIdentity sql.NullString
	var model, effort, mode, mentionContext sql.NullString
	var startedAt string
	var endedAt sql.NullString
	if err := r.Scan(&t.ID, &t.ChatID, &t.Seq, &t.Prompt, (*string)(&t.State), &failReason, &errMsg,
		&resultText, &sessionID, &t.InputTokens, &t.OutputTokens, &t.CostUSD, &t.ExitCode,
		&t.PID, &procIdentity, &startedAt, &endedAt, &t.DurationMS,
		&t.SessionInputTokens, &t.SessionOutputTokens,
		&t.CacheReadTokens, &t.CacheCreationTokens, &t.ContextTokens, &t.ContextWindow,
		&t.SessionCacheReadTokens, &t.SessionCacheCreationTokens, &model, &effort, &mode,
		&mentionContext); err != nil {
		return nil, err
	}
	t.MentionContext = mentionContext.String
	t.Model, t.Effort, t.PermissionMode = model.String, effort.String, mode.String
	t.FailReason, t.ErrorMessage = failReason.String, errMsg.String
	t.ResultText, t.SessionID = resultText.String, sessionID.String
	if procIdentity.Valid {
		id := procIdentity.String
		t.ProcIdentity = &id
	}
	var err error
	if t.StartedAt, err = parseTime(startedAt); err != nil {
		return nil, err
	}
	if t.EndedAt, err = parseTimePtr(endedAt); err != nil {
		return nil, err
	}
	return &t, nil
}

// TerminalChatIDsBefore returns the ids of chats that ended before cutoff —
// the chat half of transcript pruning (§12.3 retention), the mirror of
// ArchivedTaskIDsBefore.
//
// Every terminal state counts (task 074; `closed` since task 119). A handed-off chat's transcripts age
// out on the same clock an archived one's do, and they can: they live under
// `{transcripts}/chat-{id}`, which the task that took the worktree never
// claims, so pruning them cannot reach task-owned state. The turn rows and the
// link survive — that is what "immutable linked history" means here.
//
// It measures from `updated_at` rather than from an `archived_at` column
// because chats have neither: both endings are terminal §5.5 states, so the
// transition into one is the last write a chat row ever takes and its
// `updated_at` *is* the moment it ended. Adding a column to restate that would
// be a migration for a value already on the row.
func (s *Store) TerminalChatIDsBefore(ctx context.Context, cutoff time.Time) ([]int64, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM chats WHERE state IN (?, ?, ?) AND updated_at < ? ORDER BY id`,
		string(chatstate.Archived), string(chatstate.HandedOff), string(chatstate.Closed), formatTime(cutoff))
	if err != nil {
		return nil, fmt.Errorf("list terminal chats: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan terminal chat id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list terminal chats: %w", err)
	}
	return ids, nil
}
