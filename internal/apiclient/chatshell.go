package apiclient

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// A chat's shell passthrough (task 170, §5.5, §13.2): a command a human runs
// in the chat's workspace through the daemon. It is not a turn, and its
// output reaches the agent only when the finished run is attached to the
// next prompt.

// EventChatShell is the event a shell run's every change publishes: started,
// ended, marked for the next prompt, taken by a turn. Its payload carries
// the chat's `id`, `shell_run_id` and `shell_state`.
const EventChatShell = "chat.shell_changed"

// The states of a ChatShellRun. Running is the only one that is not an
// ending.
const (
	ChatShellRunning     = "running"
	ChatShellExited      = "exited"
	ChatShellTimedOut    = "timed_out"
	ChatShellInterrupted = "interrupted"
	ChatShellCanceled    = "canceled"
	ChatShellFailed      = "failed"
)

// ChatShellRun is one shell command run in a chat.
type ChatShellRun struct {
	ID      int64  `json:"id"`
	ChatID  int64  `json:"chat_id"`
	Command string `json:"command"`
	State   string `json:"state"`
	// ExitCode is set for an `exited` run only.
	ExitCode  *int       `json:"exit_code"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at"`
	// OutputBytes is the size of the stored output; Truncated says the
	// daemon's cap was reached and the stored output ends in a marker.
	OutputBytes int64 `json:"output_bytes"`
	Truncated   bool  `json:"truncated"`
	// Attach marks the run for the chat's next prompt; AttachedTurn is the
	// seq of the turn that took it, after which Attach is false again.
	Attach       bool `json:"attach"`
	AttachedTurn *int `json:"attached_turn"`
	// AfterTurn is the seq of the last turn that existed when the run
	// started, 0 for none: the run is drawn after that turn.
	AfterTurn int `json:"after_turn"`
}

// RunChatShell is POST /v1/chats/{id}/shell: it starts command and returns
// the run, still running. A terminal chat, and a chat with a command already
// running, are a 409 invalid_state.
func (c *Client) RunChatShell(ctx context.Context, chatID int64, command string) (*ChatShellRun, error) {
	var out ChatShellRun
	body := map[string]string{"command": command}
	if err := c.post(ctx, fmt.Sprintf("/v1/chats/%d/shell", chatID), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ChatShellRuns is GET /v1/chats/{id}/shell: the chat's runs, oldest first.
func (c *Client) ChatShellRuns(ctx context.Context, chatID int64) ([]ChatShellRun, error) {
	var out struct {
		Runs []ChatShellRun `json:"runs"`
	}
	if err := c.get(ctx, fmt.Sprintf("/v1/chats/%d/shell", chatID), &out); err != nil {
		return nil, err
	}
	return out.Runs, nil
}

// ChatShellRun is GET /v1/chats/{id}/shell/{run_id}.
func (c *Client) ChatShellRun(ctx context.Context, chatID, runID int64) (*ChatShellRun, error) {
	var out ChatShellRun
	if err := c.get(ctx, fmt.Sprintf("/v1/chats/%d/shell/%d", chatID, runID), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ChatShellOutput is GET /v1/chats/{id}/shell/{run_id}/output: the stored,
// capped output as text. For a run still running it is the output so far.
func (c *Client) ChatShellOutput(ctx context.Context, chatID, runID int64) (string, error) {
	path := fmt.Sprintf("/v1/chats/%d/shell/%d/output", chatID, runID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, nil)
	if err != nil {
		return "", fmt.Errorf("build request %s: %w", path, err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.rest.Do(req)
	if err != nil {
		return "", fmt.Errorf("GET %s: %w", path, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", decodeError(resp)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(data), nil
}

// AttachChatShell is POST /v1/chats/{id}/shell/{run_id}/attach: mark, or
// unmark, a finished run for the chat's next prompt. A run still running or
// already taken by a turn is a 409 invalid_state.
func (c *Client) AttachChatShell(ctx context.Context, chatID, runID int64, attach bool) (*ChatShellRun, error) {
	var out ChatShellRun
	body := map[string]bool{"attach": attach}
	if err := c.post(ctx, fmt.Sprintf("/v1/chats/%d/shell/%d/attach", chatID, runID), body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}
