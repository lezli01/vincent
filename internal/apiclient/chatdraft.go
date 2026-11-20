package apiclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// ChatDraft is a chat's unsent draft and its one stash slot (task 164), as
// GET and PUT /v1/chats/{id}/draft answer them.
type ChatDraft struct {
	Draft     string     `json:"draft"`
	Stash     string     `json:"stash"`
	UpdatedAt *time.Time `json:"updated_at"`
}

// ChatDraftUpdate is a PUT's body: a nil field is left as it is.
type ChatDraftUpdate struct {
	Draft *string `json:"draft,omitempty"`
	Stash *string `json:"stash,omitempty"`
}

// ChatDraft fetches GET /v1/chats/{id}/draft.
func (c *Client) ChatDraft(ctx context.Context, id int64) (*ChatDraft, error) {
	var out ChatDraft
	if err := c.get(ctx, fmt.Sprintf("/v1/chats/%d/draft", id), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PutChatDraft is PUT /v1/chats/{id}/draft. The write is last-writer-wins.
func (c *Client) PutChatDraft(ctx context.Context, id int64, u ChatDraftUpdate) (*ChatDraft, error) {
	var out ChatDraft
	if err := c.send(ctx, http.MethodPut, fmt.Sprintf("/v1/chats/%d/draft", id), u, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ChatPrompt is one prompt GET /v1/chats/prompts lists: the newest turn that
// sent this text, and the chat it is in.
type ChatPrompt struct {
	Prompt    string    `json:"prompt"`
	ChatID    int64     `json:"chat_id"`
	ChatTitle string    `json:"chat_title"`
	ChatState string    `json:"chat_state"`
	Seq       int       `json:"seq"`
	SentAt    time.Time `json:"sent_at"`
}

// ChatPrompts is GET /v1/chats/prompts (task 164): every prompt the project's
// chats sent, archived ones included, newest first and de-duplicated. A query
// with no term of three characters lists by recency; otherwise every term
// narrows by substring.
// A limit of zero is the daemon's default.
func (c *Client) ChatPrompts(ctx context.Context, projectID int64, query string, limit int) ([]ChatPrompt, error) {
	v := url.Values{}
	v.Set("project_id", strconv.FormatInt(projectID, 10))
	if query != "" {
		v.Set("q", query)
	}
	if limit > 0 {
		v.Set("limit", strconv.Itoa(limit))
	}
	var out struct {
		Prompts []ChatPrompt `json:"prompts"`
	}
	if err := c.get(ctx, "/v1/chats/prompts?"+v.Encode(), &out); err != nil {
		return nil, err
	}
	return out.Prompts, nil
}
