package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
)

const chatColumns = `id, project_id, title, state, agent, model, effort, permission_mode,
	branch, base_branch, base_sha, worktree_path, session_id, pending_input, created_at, updated_at`

const chatTurnColumns = `id, chat_id, seq, prompt, state, fail_reason, error_message, result_text,
	session_id, input_tokens, output_tokens, cost_usd, exit_code, pid, proc_identity,
	started_at, ended_at, duration_ms`

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
	var ev *Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO chats (project_id, title, state, agent, model, effort, permission_mode,
				branch, base_branch, base_sha, worktree_path, session_id, pending_input, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			c.ProjectID, c.Title, string(c.State), c.Agent, nullString(c.Model), nullString(c.Effort),
			c.PermissionMode, c.Branch, c.BaseBranch, nullString(c.BaseSHA), nullString(c.WorktreePath),
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
	return &Event{Type: evType, ProjectID: &pid, Payload: payload}, nil
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

// ChatFilter narrows ListChats. The zero value lists every chat, archived
// ones included — the caller decides, since a chat list that silently hid
// archived rows would make `vincent chat list --all` a lie.
type ChatFilter struct {
	ProjectID *int64
	States    []chatstate.State
}

// ListChats returns chats newest first, which is the order a conversation
// list is read in.
func (s *Store) ListChats(ctx context.Context, f ChatFilter) ([]Chat, error) {
	q := `SELECT ` + chatColumns + ` FROM chats WHERE 1=1`
	var args []any
	if f.ProjectID != nil {
		q += ` AND project_id = ?`
		args = append(args, *f.ProjectID)
	}
	if len(f.States) > 0 {
		//nolint:gosec // G202: placeholders renders bind markers only, never values.
		q += ` AND state IN ` + placeholders(len(f.States))
		for _, st := range f.States {
			args = append(args, string(st))
		}
	}
	q += ` ORDER BY id DESC`
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
		c.State = st
		if st != chatstate.AwaitingInput {
			// Leaving awaiting_input for any reason retires the request: a
			// pending question outliving the process that asked it is an
			// answer route with nothing to answer (§7.4).
			c.PendingInput = nil
		}
	})
}

// SetChatSession records the agent CLI's own session id for the chat (§7.3,
// amended). It is written after every turn, not just the first: claude may
// hand a resumed conversation a new id, and the next turn must resume the one
// that actually ran.
func (s *Store) SetChatSession(ctx context.Context, id int64, sessionID string) (*Chat, error) {
	return s.updateChat(ctx, id, "", func(c *Chat) { c.SessionID = sessionID })
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
		mutate(c)
		c.UpdatedAt = time.Now()
		if _, err := tx.ExecContext(ctx, `
			UPDATE chats SET title = ?, state = ?, model = ?, effort = ?, base_sha = ?,
				worktree_path = ?, session_id = ?, pending_input = ?, updated_at = ?
			WHERE id = ?`,
			c.Title, string(c.State), nullString(c.Model), nullString(c.Effort), nullString(c.BaseSHA),
			nullString(c.WorktreePath), nullString(c.SessionID), nullString(string(c.PendingInput)),
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

// CreateChatTurn inserts a running turn, assigning its seq, and moves the chat
// to `running` in the same transaction. Both halves are one write because a
// chat that is running with no turn row — or the reverse — is the state
// recovery cannot tell from a crash (§12.4).
func (s *Store) CreateChatTurn(ctx context.Context, chatID int64, prompt string) (*ChatTurn, error) {
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
		next, ok := chatstate.Next(c.State, chatstate.Send)
		if !ok {
			return fmt.Errorf("chat %d: %w", chatID, ErrInvalidChatAction)
		}
		var seq int
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq), 0) + 1 FROM chat_turns WHERE chat_id = ?`, chatID).Scan(&seq); err != nil {
			return fmt.Errorf("next chat turn seq: %w", err)
		}
		t := &ChatTurn{
			ChatID: chatID, Seq: seq, Prompt: prompt,
			State: chatstate.TurnRunning, SessionID: c.SessionID, StartedAt: time.Now(),
		}
		res, err := tx.ExecContext(ctx, `
			INSERT INTO chat_turns (chat_id, seq, prompt, state, session_id, started_at)
			VALUES (?, ?, ?, ?, ?, ?)`,
			t.ChatID, t.Seq, t.Prompt, string(t.State), nullString(t.SessionID), formatTime(t.StartedAt))
		if err != nil {
			return fmt.Errorf("insert chat turn: %w", err)
		}
		if t.ID, err = res.LastInsertId(); err != nil {
			return fmt.Errorf("insert chat turn: %w", err)
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
		if _, err := tx.ExecContext(ctx, `
			UPDATE chat_turns SET state = ?, fail_reason = ?, error_message = ?, result_text = ?,
				session_id = ?, input_tokens = ?, output_tokens = ?, cost_usd = ?, exit_code = ?,
				pid = ?, proc_identity = ?, ended_at = ?, duration_ms = ?
			WHERE id = ?`,
			string(t.State), nullString(t.FailReason), nullString(t.ErrorMessage), nullString(t.ResultText),
			nullString(t.SessionID), t.InputTokens, t.OutputTokens, t.CostUSD, t.ExitCode,
			t.PID, t.ProcIdentity, formatTimePtr(t.EndedAt), t.DurationMS, t.ID); err != nil {
			return fmt.Errorf("update chat turn %d: %w", t.ID, err)
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
	var model, effort, baseSHA, worktreePath, sessionID, pending sql.NullString
	var createdAt, updatedAt string
	if err := r.Scan(&c.ID, &c.ProjectID, &c.Title, (*string)(&c.State), &c.Agent, &model, &effort,
		&c.PermissionMode, &c.Branch, &c.BaseBranch, &baseSHA, &worktreePath, &sessionID, &pending,
		&createdAt, &updatedAt); err != nil {
		return nil, err
	}
	c.Model, c.Effort = model.String, effort.String
	c.BaseSHA, c.WorktreePath, c.SessionID = baseSHA.String, worktreePath.String, sessionID.String
	if pending.Valid && pending.String != "" {
		c.PendingInput = json.RawMessage(pending.String)
	}
	var err error
	if c.CreatedAt, err = parseTime(createdAt); err != nil {
		return nil, err
	}
	if c.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, err
	}
	return &c, nil
}

func scanChatTurn(r rowScanner) (*ChatTurn, error) {
	var t ChatTurn
	var failReason, errMsg, resultText, sessionID, procIdentity sql.NullString
	var startedAt string
	var endedAt sql.NullString
	if err := r.Scan(&t.ID, &t.ChatID, &t.Seq, &t.Prompt, (*string)(&t.State), &failReason, &errMsg,
		&resultText, &sessionID, &t.InputTokens, &t.OutputTokens, &t.CostUSD, &t.ExitCode,
		&t.PID, &procIdentity, &startedAt, &endedAt, &t.DurationMS); err != nil {
		return nil, err
	}
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
