package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

// Chat is one conversation (spec §5.5, §13.2). The wire types live here, in
// the one client both the TUI and the CLI consume, so client and server
// cannot drift without a *live_test.go noticing.
type Chat struct {
	ID           int64           `json:"id"`
	ProjectID    int64           `json:"project_id"`
	Title        string          `json:"title"`
	State        string          `json:"state"`
	Agent        string          `json:"agent"`
	Model        string          `json:"model,omitempty"`
	Effort       string          `json:"effort,omitempty"`
	Branch       string          `json:"branch"`
	BaseBranch   string          `json:"base_branch"`
	BaseSHA      string          `json:"base_sha,omitempty"`
	WorktreePath string          `json:"worktree_path,omitempty"`
	SessionID    string          `json:"session_id,omitempty"`
	PendingInput json.RawMessage `json:"pending_input,omitempty"`
	CreatedAt    time.Time       `json:"created_at"`
	UpdatedAt    time.Time       `json:"updated_at"`
}

// ChatTurn is one exchange in a chat.
type ChatTurn struct {
	ID           int64      `json:"id"`
	ChatID       int64      `json:"chat_id"`
	Seq          int        `json:"seq"`
	Prompt       string     `json:"prompt"`
	State        string     `json:"state"`
	FailReason   string     `json:"fail_reason,omitempty"`
	ErrorMessage string     `json:"error_message,omitempty"`
	ResultText   string     `json:"result_text,omitempty"`
	SessionID    string     `json:"session_id,omitempty"`
	InputTokens  int64      `json:"input_tokens"`
	OutputTokens int64      `json:"output_tokens"`
	CostUSD      *float64   `json:"cost_usd"`
	ExitCode     *int       `json:"exit_code,omitempty"`
	StartedAt    time.Time  `json:"started_at"`
	EndedAt      *time.Time `json:"ended_at,omitempty"`
	DurationMS   *int64     `json:"duration_ms,omitempty"`
}

// CreateChatRequest is the POST /v1/chats body. An omitted agent resolves
// server-side to one that can resume a session (§9.1); an omitted base_branch
// to the project's default.
type CreateChatRequest struct {
	ProjectID  int64  `json:"project_id"`
	Title      string `json:"title"`
	Agent      string `json:"agent,omitempty"`
	Model      string `json:"model,omitempty"`
	Effort     string `json:"effort,omitempty"`
	BaseBranch string `json:"base_branch,omitempty"`
}

// CreateChat starts a chat: a title, a project, an agent, and a worktree and
// branch of its own.
func (c *Client) CreateChat(ctx context.Context, req CreateChatRequest) (*Chat, error) {
	var out Chat
	if err := c.post(ctx, "/v1/chats", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ListChats fetches chats, optionally narrowed to one project. Tasks never
// appear here, and chats never appear in ListTasks.
func (c *Client) ListChats(ctx context.Context, projectID int64) ([]Chat, error) {
	path := "/v1/chats"
	if projectID > 0 {
		path += fmt.Sprintf("?project_id=%d", projectID)
	}
	var out struct {
		Chats []Chat `json:"chats"`
	}
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out.Chats, nil
}

// GetChat fetches one chat with its whole conversation, in order.
func (c *Client) GetChat(ctx context.Context, id int64) (*Chat, []ChatTurn, error) {
	var out struct {
		Chat  Chat       `json:"chat"`
		Turns []ChatTurn `json:"turns"`
	}
	if err := c.get(ctx, fmt.Sprintf("/v1/chats/%d", id), &out); err != nil {
		return nil, nil, err
	}
	return &out.Chat, out.Turns, nil
}

// SendChat starts a turn. A 409 with code `chat_cap_reached` means
// `max_parallel_chats` chats are already running — the send is refused, not
// queued (§11).
func (c *Client) SendChat(ctx context.Context, id int64, message string) (*ChatTurn, error) {
	var out ChatTurn
	body := map[string]string{"message": message}
	if err := c.post(ctx, fmt.Sprintf("/v1/chats/%d/send", id), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// AnswerChat answers a chat's pending §7.4 input request.
func (c *Client) AnswerChat(ctx context.Context, id int64, resp InputResponse) error {
	return c.post(ctx, fmt.Sprintf("/v1/chats/%d/answer", id), resp, nil)
}

// CancelChat stops a chat's live turn.
func (c *Client) CancelChat(ctx context.Context, id int64) error {
	return c.post(ctx, fmt.Sprintf("/v1/chats/%d/cancel", id), nil, nil)
}

// ArchiveChat archives a chat, removing its worktree and taking an empty
// branch with it. force is the way past a dirty worktree, as on a task.
func (c *Client) ArchiveChat(ctx context.Context, id int64, force bool) (*Chat, error) {
	path := fmt.Sprintf("/v1/chats/%d/archive", id)
	if force {
		path += "?force=true"
	}
	var out Chat
	if err := c.post(ctx, path, nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// StreamChat subscribes to GET /v1/chats/{id}/events: that chat's durable
// events interleaved with its live output (§13.3). It is the per-task stream's
// twin — Last-Event-ID resumes the durable events, live output is ephemeral
// and never replayed — so a reconnect catches up by re-fetching the running
// turn's transcript and discarding chunks at or before its NextOffset.
func (c *Client) StreamChat(ctx context.Context, chatID int64, opts StreamOptions) <-chan Note {
	ch := make(chan Note)
	go c.streamLoop(ctx, "/v1/chats/"+strconv.FormatInt(chatID, 10)+"/events", opts, ch)
	return ch
}

// ChatTurnTranscript fetches one turn's transcript in normalized form,
// returning the records and the offset to resume from. The turn is named by
// its 1-based seq, not by a run id: a chat turn is its own run.
func (c *Client) ChatTurnTranscript(
	ctx context.Context, chatID int64, seq int, opts TranscriptOptions,
) (records []TranscriptRecord, nextOffset int64, err error) {
	path := fmt.Sprintf("/v1/chats/%d/turns/%d/transcript%s", chatID, seq, opts.query("normalized"))
	resp, nextOffset, err := c.transcriptAt(ctx, path)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	records, err = decodeTranscript(resp.Body)
	return records, nextOffset, err
}
