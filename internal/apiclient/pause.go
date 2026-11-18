package apiclient

import (
	"context"
	"time"
)

// The three states of the global pause (task 149), as GET /v1/daemon/pause
// reports them.
const (
	PauseActive  = "active"
	PausePausing = "pausing"
	PausePaused  = "paused"
)

// GlobalPause is the daemon-wide pause (task 149, §13.2).
type GlobalPause struct {
	// State is PauseActive, PausePausing or PausePaused.
	State string `json:"state"`
	// PausedAt is when the pause was taken; nil while active.
	PausedAt *time.Time `json:"paused_at"`
	Reason   string     `json:"reason"`
	// Running is the tasks still finishing a step; a pause with any is
	// still pausing.
	Running int `json:"running"`
	// AwaitingInput is the tasks holding an agent idle on a question, which
	// park at the step boundary after the answer.
	AwaitingInput int `json:"awaiting_input"`
	// Queued is the tasks admission is holding back.
	Queued int `json:"queued"`
	// Held is the tasks the pause parked; Continue resumes them.
	Held int `json:"held"`
	// Resumed is how many tasks a Continue resumed; nil elsewhere.
	Resumed *int `json:"resumed,omitempty"`
}

// Active reports whether no global pause is in effect.
func (p GlobalPause) Active() bool { return p.State == "" || p.State == PauseActive }

// GlobalPause reads the daemon-wide pause.
func (c *Client) GlobalPause(ctx context.Context) (GlobalPause, error) {
	var out GlobalPause
	if err := c.get(ctx, "/v1/daemon/pause", &out); err != nil {
		return GlobalPause{}, err
	}
	return out, nil
}

// PauseAll takes the daemon-wide pause: nothing new is admitted and every
// running task parks at its next step boundary. reason may be empty.
func (c *Client) PauseAll(ctx context.Context, reason string) (GlobalPause, error) {
	var body any
	if reason != "" {
		body = map[string]string{"reason": reason}
	}
	var out GlobalPause
	if err := c.post(ctx, "/v1/daemon/pause", body, &out); err != nil {
		return GlobalPause{}, err
	}
	return out, nil
}

// ContinueAll lifts the daemon-wide pause and resumes the tasks it parked.
func (c *Client) ContinueAll(ctx context.Context) (GlobalPause, error) {
	var out GlobalPause
	if err := c.post(ctx, "/v1/daemon/continue", nil, &out); err != nil {
		return GlobalPause{}, err
	}
	return out, nil
}
