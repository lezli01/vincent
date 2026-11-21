package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/lezli01/vincent/internal/chatstate"
)

// A chat's draft and stash, and the project's prompt history (task 164,
// #1012, spec §5.5).

// EventChatDraft is a chat's draft or stash changing (task 164). It carries
// has_draft, has_stash and draft_updated_at — never the text, which can be as
// long as a prompt and has no business in the durable events table — so the
// board repaints its `✎ draft` mark without a fetch and an open chat re-reads
// the draft with GET /v1/chats/{id}/draft.
const EventChatDraft = "chat.draft_changed"

// MaxChatDraftBytes bounds a draft and a stash. It is what the send route can
// carry — POST /v1/chats/{id}/send reads an ordinary 64 KiB body (§13.1) — so
// a draft the daemon keeps is always one it would accept as a message.
const MaxChatDraftBytes = 64 << 10

// ErrDraftTooLong is a draft or stash over MaxChatDraftBytes.
var ErrDraftTooLong = fmt.Errorf("a draft must be at most %d bytes", MaxChatDraftBytes)

// SetChatDraft replaces a chat's draft; see UpdateChatDraft.
func (s *Store) SetChatDraft(ctx context.Context, chatID int64, draft string) (*Chat, error) {
	return s.UpdateChatDraft(ctx, chatID, &draft, nil)
}

// SetChatStash replaces a chat's stash slot; see UpdateChatDraft.
func (s *Store) SetChatStash(ctx context.Context, chatID int64, stash string) (*Chat, error) {
	return s.UpdateChatDraft(ctx, chatID, nil, &stash)
}

// UpdateChatDraft writes whichever of draft and stash is non-nil, last writer
// wins, and publishes chat.draft_changed. A terminal chat is
// ErrInvalidChatAction: its draft was cleared when it ended and nothing could
// ever send a new one. A write that changes nothing is not a write: the row,
// its draft_updated_at and the events table are left as they are and no
// event is published (review F5) — every chat.draft_changed makes each
// follower re-read, and a debounced composer would otherwise publish one per
// pause in typing whether or not the text moved.
func (s *Store) UpdateChatDraft(ctx context.Context, chatID int64, draft, stash *string) (*Chat, error) {
	for _, v := range []*string{draft, stash} {
		if v != nil && len(*v) > MaxChatDraftBytes {
			return nil, ErrDraftTooLong
		}
	}
	var out *Chat
	var ev *Event
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		c, err := scanChat(tx.QueryRowContext(ctx, `SELECT `+chatColumns+` FROM chats WHERE id = ?`, chatID))
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("chat %d: %w", chatID, ErrNotFound)
		}
		if err != nil {
			return fmt.Errorf("get chat %d: %w", chatID, err)
		}
		if chatstate.Terminal(c.State) {
			return fmt.Errorf("chat %d: %w", chatID, ErrInvalidChatAction)
		}
		if (draft == nil || *draft == c.Draft) && (stash == nil || *stash == c.Stash) {
			out = c
			return nil
		}
		if draft != nil {
			c.Draft = *draft
		}
		if stash != nil {
			c.Stash = *stash
		}
		if ev, err = writeChatDraftTx(ctx, tx, c); err != nil {
			return err
		}
		out = c
		return nil
	})
	if err != nil {
		return nil, err
	}
	if ev != nil {
		s.notify(ev)
	}
	return out, nil
}

// writeChatDraftTx stores c's draft and stash as they now read, stamps
// draft_updated_at and appends chat.draft_changed, which it returns for the
// caller to publish after the commit. It is the one draft write: a reject's
// note (RecordChatHunkReject, task 169) goes through it so the columns and
// the event cannot drift from UpdateChatDraft's.
func writeChatDraftTx(ctx context.Context, tx *sql.Tx, c *Chat) (*Event, error) {
	now := time.Now()
	c.DraftUpdatedAt = &now
	if _, err := tx.ExecContext(ctx,
		`UPDATE chats SET draft = ?, stash = ?, draft_updated_at = ? WHERE id = ?`,
		c.Draft, c.Stash, formatTime(now), c.ID); err != nil {
		return nil, fmt.Errorf("set chat %d draft: %w", c.ID, err)
	}
	ev, err := chatDraftEvent(c)
	if err != nil {
		return nil, err
	}
	if err := appendEventTx(ctx, tx, ev); err != nil {
		return nil, err
	}
	return ev, nil
}

// clearChatDraftTx empties c's draft and stash inside the transaction that
// ends it (task 164): a terminal chat can never send, so a draft kept on it
// would be text nobody can reach, exactly as a queued message would (task 156
// decision 6). No event: the terminal transition's own event already tells a
// board to drop the row's mark.
func clearChatDraftTx(ctx context.Context, tx *sql.Tx, c *Chat) error {
	if c.Draft == "" && c.Stash == "" {
		return nil
	}
	if _, err := tx.ExecContext(ctx, `UPDATE chats SET draft = '', stash = '' WHERE id = ?`, c.ID); err != nil {
		return fmt.Errorf("clear chat %d draft: %w", c.ID, err)
	}
	c.Draft, c.Stash = "", ""
	return nil
}

// chatDraftEvent is chat.draft_changed: chatEvent plus whether a draft and a
// stash are held and when they were written.
func chatDraftEvent(c *Chat) (*Event, error) {
	ev, err := chatEvent(EventChatDraft, c, nil)
	if err != nil {
		return nil, err
	}
	var body map[string]any
	if err := json.Unmarshal(ev.Payload, &body); err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", EventChatDraft, err)
	}
	body["has_draft"] = c.Draft != ""
	body["has_stash"] = c.Stash != ""
	if c.DraftUpdatedAt != nil {
		body["draft_updated_at"] = c.DraftUpdatedAt.UTC()
	}
	if ev.Payload, err = json.Marshal(body); err != nil {
		return nil, fmt.Errorf("marshal %s event: %w", EventChatDraft, err)
	}
	return ev, nil
}

// The default and ceiling for a prompt-history listing: a picker a human
// scrolls, not an export.
const (
	DefaultChatPromptLimit = 50
	MaxChatPromptLimit     = 500
	// chatPromptRowCap bounds the turn rows one listing reads before it
	// collapses duplicates, so a project whose history is one prompt sent a
	// thousand times still answers in one bounded read.
	chatPromptRowCap = 5000
)

// ChatPrompt is one prompt a project's chats have sent: the newest turn that
// sent this exact text, with the chat it is in.
type ChatPrompt struct {
	Prompt    string
	ChatID    int64
	ChatTitle string
	ChatState chatstate.State
	Seq       int
	SentAt    time.Time
}

// ListChatPrompts lists the prompts sent in every chat of a project — live,
// archived, closed and handed off alike (task 164 decision 5) — newest first,
// with identical texts collapsed to their newest occurrence (decision 6).
//
// Each term of the query must appear, by substring and ignoring case. Terms
// of MinSearchTermRunes or more runes are matched on 0061's trigram index
// over prompts (task 158); a shorter term cannot use that index, so it is
// checked on the rows the index returned (review F4) — `fix a` narrows as
// much as `fix` does, and more. A query with no term that long — an empty
// one included — is answered by recency alone rather than refused, so the
// picker always has something to show and narrows from a term's third character
// on. Terms are quoted, so FTS5 syntax is only ever text to find.
func (s *Store) ListChatPrompts(ctx context.Context, projectID int64, query string, limit int) ([]ChatPrompt, error) {
	if limit <= 0 {
		limit = DefaultChatPromptLimit
	}
	limit = min(limit, MaxChatPromptLimit)
	q := `SELECT t.prompt, t.chat_id, c.title, c.state, t.seq, t.started_at FROM chat_turns t
		JOIN chats c ON c.id = t.chat_id`
	args := []any{}
	long, short := splitPromptTerms(strings.Fields(query))
	if len(long) > 0 {
		quoted := make([]string, len(long))
		for i, t := range long {
			quoted[i] = `"` + strings.ReplaceAll(t, `"`, `""`) + `"`
		}
		// The column filter `prompt :` keeps an answer's text from matching:
		// this lists prompts, and an answer is not one.
		q += ` JOIN chat_turn_fts f ON f.rowid = t.id WHERE chat_turn_fts MATCH ? AND`
		args = append(args, `prompt : (`+strings.Join(quoted, " AND ")+`)`)
	} else {
		q += ` WHERE`
	}
	q += ` c.project_id = ? AND t.prompt != '' ORDER BY t.started_at DESC, t.id DESC LIMIT ?`
	args = append(args, projectID, chatPromptRowCap)
	rows, err := s.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list chat prompts: %w", err)
	}
	defer func() { _ = rows.Close() }()
	seen := map[string]bool{}
	var out []ChatPrompt
	for rows.Next() {
		var p ChatPrompt
		var sentAt string
		if err := rows.Scan(&p.Prompt, &p.ChatID, &p.ChatTitle, (*string)(&p.ChatState), &p.Seq, &sentAt); err != nil {
			return nil, fmt.Errorf("scan chat prompt: %w", err)
		}
		if seen[p.Prompt] || !containsFolded(p.Prompt, short) {
			continue
		}
		seen[p.Prompt] = true
		if p.SentAt, err = parseTime(sentAt); err != nil {
			return nil, err
		}
		out = append(out, p)
		if len(out) == limit {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list chat prompts: %w", err)
	}
	return out, nil
}

// splitPromptTerms parts a query's terms into those long enough for the
// trigram index (task 158 decision 2) and the shorter ones. The short ones
// are kept only beside a long one, folded to lower case for containsFolded:
// a query made only of short terms lists by recency (task 164 decision 6).
func splitPromptTerms(terms []string) (long, short []string) {
	for _, t := range terms {
		if utf8.RuneCountInString(t) >= MinSearchTermRunes {
			long = append(long, t)
		} else {
			short = append(short, strings.ToLower(t))
		}
	}
	if len(long) == 0 {
		return nil, nil
	}
	return long, short
}

// containsFolded reports whether s contains every one of the lower-cased
// terms, ignoring case as the trigram index does.
func containsFolded(s string, terms []string) bool {
	if len(terms) == 0 {
		return true
	}
	s = strings.ToLower(s)
	for _, t := range terms {
		if !strings.Contains(s, t) {
			return false
		}
	}
	return true
}
