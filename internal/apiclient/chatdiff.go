package apiclient

import (
	"context"
	"fmt"
	"net/url"
)

// A chat's diff and its per-hunk review (task 169, #1018, §5.5, §13.2).

// The scopes of a chat diff: what the chat's directory holds against the
// chat's base, or against the checkpoint its latest turn started from.
const (
	ChatDiffScopeBase = "base"
	ChatDiffScopeTurn = "turn"
)

// The chat diff routes' 409 codes (Error.Code).
const (
	// CodeChatHunkStale: the hash is not in the scope's current diff — read
	// the diff again.
	CodeChatHunkStale = "chat_hunk_stale"
	// CodeChatHunkConflict: git would not reverse-apply the hunk.
	CodeChatHunkConflict = "chat_hunk_conflict"
	// CodeChatHunkNotRejectable: the file is binary, a rename or mode-only.
	CodeChatHunkNotRejectable = "chat_hunk_not_rejectable"
	// CodeChatTurnLive: a reject or an accept while the chat is running,
	// awaiting input or draining its queue.
	CodeChatTurnLive = "chat_turn_live"
	// CodeChatDiffUnavailable: no directory, no base, or the turn scope with
	// no checkpoint; the message carries the reason.
	CodeChatDiffUnavailable = "chat_diff_unavailable"
)

// ChatDiff is GET /v1/chats/{id}/diff: the chat's working tree — untracked
// files included, ignored ones not — against the scope's start.
type ChatDiff struct {
	Scope string `json:"scope"`
	// From is the commit diffed from: the merge-base with the chat's base, or
	// the latest turn's checkpoint.
	From    string         `json:"from"`
	Added   int            `json:"added"`
	Removed int            `json:"removed"`
	Files   []ChatDiffFile `json:"files"`
}

// ChatDiffFile is one file of a ChatDiff. Path and OldPath are /-separated on
// every OS; OldPath is set only for a rename.
type ChatDiffFile struct {
	Path    string `json:"path"`
	OldPath string `json:"old_path"`
	// Status is added, deleted, modified or renamed.
	Status string `json:"status"`
	Binary bool   `json:"binary"`
	// Rejectable is false for a binary file, a rename and a mode-only change:
	// their hunks cannot be reverted one at a time.
	Rejectable bool           `json:"rejectable"`
	Added      int            `json:"added"`
	Removed    int            `json:"removed"`
	Hunks      []ChatDiffHunk `json:"hunks"`
}

// ChatDiffHunk is one hunk. Hash identifies it by content, independent of its
// line numbers, and is unique within its file.
type ChatDiffHunk struct {
	Hash   string `json:"hash"`
	Header string `json:"header"`
	// Body is the lines after the header, each with its ' ', '+', '-' or '\'
	// prefix, joined by "\n" with no trailing newline; a "\r" is kept.
	Body     string `json:"body"`
	Added    int    `json:"added"`
	Removed  int    `json:"removed"`
	Accepted bool   `json:"accepted"`
}

// ChatDiffStat is GET /v1/chats/{id}/diff/stat: the base scope's totals.
type ChatDiffStat struct {
	Files   int `json:"files"`
	Added   int `json:"added"`
	Removed int `json:"removed"`
}

// ChatHunkReject is a reject's answer: the chat's draft after the call, which
// now opens with the note telling the agent what was reverted. NoteAdded is
// false, with Warning saying so, when the note did not fit the draft — the
// hunk is reverted all the same.
type ChatHunkReject struct {
	Draft     string `json:"draft"`
	NoteAdded bool   `json:"note_added"`
	Warning   string `json:"warning"`
}

type chatHunkRequest struct {
	Scope string `json:"scope,omitempty"`
	Path  string `json:"path"`
	Hunk  string `json:"hunk"`
}

// ChatDiff reads a chat's diff. scope is ChatDiffScopeBase, ChatDiffScopeTurn,
// or empty for base. A chat with nothing to diff from is a 409
// CodeChatDiffUnavailable.
func (c *Client) ChatDiff(ctx context.Context, chatID int64, scope string) (*ChatDiff, error) {
	path := fmt.Sprintf("/v1/chats/%d/diff", chatID)
	if scope != "" {
		path += "?scope=" + url.QueryEscape(scope)
	}
	var out ChatDiff
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ChatDiffStat reads the base scope's totals in one cheap call.
func (c *Client) ChatDiffStat(ctx context.Context, chatID int64) (*ChatDiffStat, error) {
	var out ChatDiffStat
	if err := c.get(ctx, fmt.Sprintf("/v1/chats/%d/diff/stat", chatID), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// RejectChatHunk reverse-applies one hunk of the scope's current diff in the
// chat's working tree — never the index or HEAD — and leaves a note for the
// agent at the top of the chat's draft. hunk is the full hash.
func (c *Client) RejectChatHunk(ctx context.Context, chatID int64, scope, path, hunk string) (*ChatHunkReject, error) {
	var out ChatHunkReject
	body := chatHunkRequest{Scope: scope, Path: path, Hunk: hunk}
	if err := c.post(ctx, fmt.Sprintf("/v1/chats/%d/diff/reject", chatID), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AcceptChatHunk marks one hunk of the scope's current diff accepted. It is
// only a mark: nothing in git changes.
func (c *Client) AcceptChatHunk(ctx context.Context, chatID int64, scope, path, hunk string) error {
	body := chatHunkRequest{Scope: scope, Path: path, Hunk: hunk}
	return c.post(ctx, fmt.Sprintf("/v1/chats/%d/diff/accept", chatID), body, nil)
}

// UnacceptChatHunk removes a hunk's accepted mark. Removing one that is not
// there is not an error.
func (c *Client) UnacceptChatHunk(ctx context.Context, chatID int64, path, hunk string) error {
	body := chatHunkRequest{Path: path, Hunk: hunk}
	return c.post(ctx, fmt.Sprintf("/v1/chats/%d/diff/unaccept", chatID), body, nil)
}
