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

// EventChatShell is a chat's shell run changing (task 170, §13.3): started,
// journaled, ended, marked for the next prompt or taken by a turn. It is
// chatEvent's payload plus the run's id and state, so a client refetches the
// one run and `GET /v1/chats/{id}/events` matches it by the chat's id.
const EventChatShell = "chat.shell_changed"

// The states of a chat shell run. `running` is the only one that is not an
// ending.
const (
	ChatShellRunning     = "running"
	ChatShellExited      = "exited"
	ChatShellTimedOut    = "timed_out"
	ChatShellInterrupted = "interrupted"
	ChatShellCanceled    = "canceled"
	ChatShellFailed      = "failed"
)

// ErrChatShellBusy is a shell command refused because the chat already has
// one running: one command per chat at a time (task 170).
var ErrChatShellBusy = errors.New("this chat already has a shell command running")

// ErrChatShellNotAttachable is an attach on a run that is still running or
// that a turn already took.
var ErrChatShellNotAttachable = errors.New("this shell run cannot be attached")

// ChatShellRun is one command a human ran in a chat's workspace (task 170,
// §5.5).
type ChatShellRun struct {
	ID        int64
	ChatID    int64
	Command   string
	State     string
	ExitCode  *int
	StartedAt time.Time
	EndedAt   *time.Time
	// OutputPath is the file holding the run's capped combined output. It is
	// exported for the chat export, which reads the file itself.
	OutputPath  string
	OutputBytes int64
	Truncated   bool
	// PID and ProcIdentity are §12.4's journal, as a turn's are.
	PID          *int
	ProcIdentity *string
	// Attach marks a finished run for the next prompt; AttachedTurn is the
	// seq of the turn that took it.
	Attach       bool
	AttachedTurn *int
	// AfterTurn is the seq of the last turn that existed when the run
	// started, 0 for none.
	AfterTurn int
}

const chatShellColumns = `id, chat_id, command, state, exit_code, started_at, ended_at,
	output_path, output_bytes, truncated, pid, proc_identity, attach, attached_turn, after_turn`

func scanChatShellRun(r rowScanner) (*ChatShellRun, error) {
	var (
		run       ChatShellRun
		exit, pid sql.NullInt64
		taken     sql.NullInt64
		started   string
		ended     sql.NullString
		ident     sql.NullString
	)
	if err := r.Scan(&run.ID, &run.ChatID, &run.Command, &run.State, &exit, &started, &ended,
		&run.OutputPath, &run.OutputBytes, &run.Truncated, &pid, &ident, &run.Attach, &taken,
		&run.AfterTurn); err != nil {
		return nil, err
	}
	var err error
	if run.StartedAt, err = parseTime(started); err != nil {
		return nil, err
	}
	if run.EndedAt, err = parseTimePtr(ended); err != nil {
		return nil, err
	}
	if exit.Valid {
		n := int(exit.Int64)
		run.ExitCode = &n
	}
	if pid.Valid {
		n := int(pid.Int64)
		run.PID = &n
	}
	if taken.Valid {
		n := int(taken.Int64)
		run.AttachedTurn = &n
	}
	if ident.Valid {
		run.ProcIdentity = &ident.String
	}
	return &run, nil
}

// chatShellEvent is chat.shell_changed for run on chat c.
func chatShellEvent(c *Chat, run *ChatShellRun) (*Event, error) {
	ev, err := chatEvent(EventChatShell, c, nil)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(ev.Payload, &body); err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", EventChatShell, err)
	}
	body["shell_run_id"] = run.ID
	body["shell_state"] = run.State
	if ev.Payload, err = json.Marshal(body); err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", EventChatShell, err)
	}
	return ev, nil
}

// chatShellTx runs fn over the chat's row in one transaction and appends a
// chat.shell_changed event for each run fn returns, publishing them after
// the commit.
func (s *Store) chatShellTx(
	ctx context.Context, chatID int64, fn func(*sql.Tx, *Chat) ([]*ChatShellRun, error),
) error {
	var evs []*Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		evs = nil
		c, err := scanChat(tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE id = ?`, chatID))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("chat %d: %w", chatID, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("get chat %d: %w", chatID, err)
		}
		runs, err := fn(tx, c)
		if err != nil {
			return err
		}
		for _, run := range runs {
			ev, err := chatShellEvent(c, run)
			if err != nil {
				return err
			}
			if err := appendEventTx(ctx, tx, ev); err != nil {
				return err
			}
			evs = append(evs, ev)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.notify(evs...)
	return nil
}

// CreateChatShellRun records a shell command as `running` on a live chat,
// before anything is spawned (crash-first). It refuses a terminal chat with
// ErrInvalidChatAction and a chat that already has a run `running` with
// ErrChatShellBusy.
func (s *Store) CreateChatShellRun(ctx context.Context, chatID int64, command string, now time.Time) (*ChatShellRun, error) {
	var out *ChatShellRun
	err := s.chatShellTx(ctx, chatID, func(tx *sql.Tx, c *Chat) ([]*ChatShellRun, error) {
		if chatstate.Terminal(c.State) {
			return nil, fmt.Errorf("chat %d: %w", chatID, ErrInvalidChatAction)
		}
		var busy int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM chat_shell_runs WHERE chat_id = ? AND state = ?`,
			chatID, ChatShellRunning).Scan(&busy); err != nil {
			return nil, fmt.Errorf("chat %d shell runs: %w", chatID, err)
		}
		if busy > 0 {
			return nil, fmt.Errorf("chat %d: %w", chatID, ErrChatShellBusy)
		}
		var after int
		if err := tx.QueryRowContext(ctx,
			`SELECT COALESCE(MAX(seq), 0) FROM chat_turns WHERE chat_id = ?`, chatID).Scan(&after); err != nil {
			return nil, fmt.Errorf("chat %d turns: %w", chatID, err)
		}
		res, err := tx.ExecContext(ctx,
			`INSERT INTO chat_shell_runs (chat_id, command, state, started_at, after_turn) VALUES (?, ?, ?, ?, ?)`,
			chatID, command, ChatShellRunning, formatTime(now), after)
		if err != nil {
			return nil, fmt.Errorf("create chat %d shell run: %w", chatID, err)
		}
		id, err := res.LastInsertId()
		if err != nil {
			return nil, fmt.Errorf("create chat %d shell run: %w", chatID, err)
		}
		out = &ChatShellRun{
			ID: id, ChatID: chatID, Command: command, State: ChatShellRunning,
			StartedAt: now.UTC(), AfterTurn: after,
		}
		return []*ChatShellRun{out}, nil
	})
	return out, err
}

// UpdateChatShellRun writes the fields the runner owns — state, exit code,
// ending, output and the process journal — and publishes the change. The
// attach mark and attached_turn are not written here: they have their own
// writers, and a run ending must not undo a mark set meanwhile.
func (s *Store) UpdateChatShellRun(ctx context.Context, run *ChatShellRun) error {
	return s.chatShellTx(ctx, run.ChatID, func(tx *sql.Tx, _ *Chat) ([]*ChatShellRun, error) {
		res, err := tx.ExecContext(ctx, `UPDATE chat_shell_runs SET
			state = ?, exit_code = ?, ended_at = ?, output_path = ?, output_bytes = ?,
			truncated = ?, pid = ?, proc_identity = ? WHERE id = ? AND chat_id = ?`,
			run.State, run.ExitCode, formatTimePtr(run.EndedAt), run.OutputPath, run.OutputBytes,
			run.Truncated, run.PID, run.ProcIdentity, run.ID, run.ChatID)
		if err != nil {
			return nil, fmt.Errorf("update chat shell run %d: %w", run.ID, err)
		}
		if err := oneRowAffected(res, fmt.Sprintf("chat shell run %d", run.ID)); err != nil {
			return nil, err
		}
		return []*ChatShellRun{run}, nil
	})
}

// GetChatShellRun returns one run of a chat, or ErrNotFound.
func (s *Store) GetChatShellRun(ctx context.Context, chatID, id int64) (*ChatShellRun, error) {
	run, err := scanChatShellRun(s.db.QueryRowContext(ctx,
		`SELECT `+chatShellColumns+` FROM chat_shell_runs WHERE id = ? AND chat_id = ?`, id, chatID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("chat %d shell run %d: %w", chatID, id, ErrNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("get chat shell run %d: %w", id, err)
	}
	return run, nil
}

// ListChatShellRuns returns a chat's shell runs, oldest first.
func (s *Store) ListChatShellRuns(ctx context.Context, chatID int64) ([]*ChatShellRun, error) {
	return s.queryChatShellRuns(ctx,
		`SELECT `+chatShellColumns+` FROM chat_shell_runs WHERE chat_id = ? ORDER BY id`, chatID)
}

// ListRunningChatShellRuns returns every run still `running`, in every chat:
// what §12.4 recovery finalizes at startup.
func (s *Store) ListRunningChatShellRuns(ctx context.Context) ([]*ChatShellRun, error) {
	return s.queryChatShellRuns(ctx,
		`SELECT `+chatShellColumns+` FROM chat_shell_runs WHERE state = ? ORDER BY id`, ChatShellRunning)
}

func (s *Store) queryChatShellRuns(ctx context.Context, q string, args ...any) ([]*ChatShellRun, error) {
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list chat shell runs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []*ChatShellRun{}
	for rows.Next() {
		run, err := scanChatShellRun(rows)
		if err != nil {
			return nil, fmt.Errorf("scan chat shell run: %w", err)
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

// SetChatShellAttach marks, or unmarks, a finished run for the chat's next
// prompt. A run that is still running, or that a turn already took, answers
// ErrChatShellNotAttachable; a terminal chat answers ErrInvalidChatAction,
// since it has no next prompt.
func (s *Store) SetChatShellAttach(ctx context.Context, chatID, id int64, attach bool) (*ChatShellRun, error) {
	var out *ChatShellRun
	err := s.chatShellTx(ctx, chatID, func(tx *sql.Tx, c *Chat) ([]*ChatShellRun, error) {
		run, err := scanChatShellRun(tx.QueryRowContext(ctx,
			`SELECT `+chatShellColumns+` FROM chat_shell_runs WHERE id = ? AND chat_id = ?`, id, chatID))
		if errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("chat %d shell run %d: %w", chatID, id, ErrNotFound)
		}
		if err != nil {
			return nil, fmt.Errorf("get chat shell run %d: %w", id, err)
		}
		if chatstate.Terminal(c.State) {
			return nil, fmt.Errorf("chat %d: %w", chatID, ErrInvalidChatAction)
		}
		if run.State == ChatShellRunning || run.AttachedTurn != nil {
			return nil, fmt.Errorf("chat %d shell run %d: %w", chatID, id, ErrChatShellNotAttachable)
		}
		out = run
		if run.Attach == attach {
			return nil, nil
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE chat_shell_runs SET attach = ? WHERE id = ?`, attach, id); err != nil {
			return nil, fmt.Errorf("attach chat shell run %d: %w", id, err)
		}
		run.Attach = attach
		return []*ChatShellRun{run}, nil
	})
	return out, err
}

// TakeChatShellRuns hands the chat's marked runs to the turn with seq
// turnSeq: each is recorded as taken by it and its mark cleared, in one
// transaction, so no second turn can be given the same output. It returns
// the runs taken, oldest first.
func (s *Store) TakeChatShellRuns(ctx context.Context, chatID int64, turnSeq int) ([]*ChatShellRun, error) {
	var out []*ChatShellRun
	err := s.chatShellTx(ctx, chatID, func(tx *sql.Tx, _ *Chat) ([]*ChatShellRun, error) {
		out = nil
		rows, err := tx.QueryContext(ctx, `SELECT `+chatShellColumns+` FROM chat_shell_runs
			WHERE chat_id = ? AND attach = 1 AND attached_turn IS NULL AND state <> ? ORDER BY id`,
			chatID, ChatShellRunning)
		if err != nil {
			return nil, fmt.Errorf("chat %d marked shell runs: %w", chatID, err)
		}
		for rows.Next() {
			run, err := scanChatShellRun(rows)
			if err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan chat shell run: %w", err)
			}
			out = append(out, run)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
		for _, run := range out {
			if _, err := tx.ExecContext(ctx,
				`UPDATE chat_shell_runs SET attach = 0, attached_turn = ? WHERE id = ?`, turnSeq, run.ID); err != nil {
				return nil, fmt.Errorf("take chat shell run %d: %w", run.ID, err)
			}
			seq := turnSeq
			run.Attach, run.AttachedTurn = false, &seq
		}
		return out, nil
	})
	return out, err
}
