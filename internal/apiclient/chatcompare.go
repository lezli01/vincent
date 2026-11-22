package apiclient

import (
	"context"
	"fmt"
	"net/http"
	"time"
)

// CompareRequest is the POST /v1/chats/{id}/compare body (task 171): fork
// the chat from the state before Turn once per agent and send both forks
// Prompt — turn Turn's own prompt when empty. PermissionMode is what each
// side is asked to run under, "" for restricted.
type CompareRequest struct {
	Turn           int      `json:"turn"`
	Prompt         string   `json:"prompt,omitempty"`
	Agents         []string `json:"agents"`
	PermissionMode string   `json:"permission_mode,omitempty"`
}

// CompareSide is one side of a compare: its chat (nil once that chat was
// permanently deleted), its agent, and the permission mode it actually runs
// under — full-auto where its agent cannot keep a chat restricted, which
// ModeNote then explains.
type CompareSide struct {
	ChatID         *int64 `json:"chat_id"`
	Agent          string `json:"agent"`
	PermissionMode string `json:"permission_mode"`
	ModeNote       string `json:"mode_note,omitempty"`
}

// ChatCompare pairs a compare's two forks.
type ChatCompare struct {
	ID           int64       `json:"id"`
	SourceChatID int64       `json:"source_chat_id"`
	Seq          int         `json:"seq"`
	Prompt       string      `json:"prompt"`
	Left         CompareSide `json:"left"`
	Right        CompareSide `json:"right"`
	KeptChatID   *int64      `json:"kept_chat_id"`
	CreatedAt    time.Time   `json:"created_at"`
}

// CompareStarted is one side as a compare started it: the fork, its first
// turn (already running), and how the fork was made.
type CompareStarted struct {
	Chat    Chat     `json:"chat"`
	Turn    ChatTurn `json:"turn"`
	Seeded  bool     `json:"seeded"`
	Skipped []string `json:"skipped"`
}

// CompareResult is a compare's answer.
type CompareResult struct {
	Compare ChatCompare    `json:"compare"`
	Left    CompareStarted `json:"left"`
	Right   CompareStarted `json:"right"`
}

// CompareChat forks the chat twice and starts both sides on one prompt, or
// refuses without creating anything: 409 `chat_cap_reached` when
// `max_parallel_chats` has no room for both, the fork's refusals otherwise.
// It makes two worktrees before it answers, so it is held to
// worktreeTimeout.
func (c *Client) CompareChat(ctx context.Context, id int64, req CompareRequest) (*CompareResult, error) {
	var out CompareResult
	path := fmt.Sprintf("/v1/chats/%d/compare", id)
	if err := c.sendVia(ctx, c.worktrees, http.MethodPost, path, nil, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetChatCompare is the compare chat id is a side of; a 404 when it is none.
func (c *Client) GetChatCompare(ctx context.Context, id int64) (*ChatCompare, error) {
	var out struct {
		Compare ChatCompare `json:"compare"`
	}
	if err := c.get(ctx, fmt.Sprintf("/v1/chats/%d/compare", id), &out); err != nil {
		return nil, err
	}
	return &out.Compare, nil
}

// KeepResult is keeping one side of a compare: the compare with KeptChatID
// set, the kept chat, the other side as archived (nil when it was already
// gone), and whether its branch was deleted.
type KeepResult struct {
	Compare       ChatCompare `json:"compare"`
	Kept          Chat        `json:"kept"`
	Archived      *Chat       `json:"archived"`
	BranchDeleted bool        `json:"branch_deleted"`
}

// KeepCompareSide keeps chat id, one side of a compare, and archives the
// other with its branch and checkpoint refs (task 171 decision 8). Refused
// 409 `chat_turn_live` while either side has a live turn, 409
// `compare_kept` once a side was kept, 404 for a chat that is no side.
// Removing a worktree is held to worktreeTimeout.
func (c *Client) KeepCompareSide(ctx context.Context, id int64) (*KeepResult, error) {
	var out KeepResult
	path := fmt.Sprintf("/v1/chats/%d/compare/keep", id)
	if err := c.sendVia(ctx, c.worktrees, http.MethodPost, path, nil, struct{}{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
