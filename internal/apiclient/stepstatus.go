package apiclient

import (
	"context"
	"fmt"
	"net/url"
)

// EventTaskStatusChanged is the durable SSE type a running step's status
// change arrives as (§13.3, task 036). Payload
// `{task_id, step_id, message}`; a client re-fetches rather than rendering
// the payload, the way it does for every other state event.
//
// It is declared here for the reason the action names are: the client owns
// its wire vocabulary and keys onto the daemon's strings rather than
// importing them. A test pins the two together.
const EventTaskStatusChanged = "task.status_changed"

// SetStepStatus records what a running step is doing, in its own words
// (§13.2, task 036). taskID and stepID are §8.5's VINCENT_TASK_ID and
// VINCENT_STEP_ID: the step's process is the caller, so those are the only
// two things it knows about itself.
//
// It returns the message as stored — flattened to one line and truncated to
// the daemon's cap, so a caller can see what a reader will see. A step that
// is no longer running is an *Error carrying 409: the write is refused
// rather than silently dropped.
func (c *Client) SetStepStatus(ctx context.Context, taskID int64, stepID, message string) (string, error) {
	var out struct {
		Message string `json:"message"`
	}
	path := fmt.Sprintf("/v1/tasks/%d/steps/%s/status", taskID, url.PathEscape(stepID))
	if err := c.post(ctx, path, map[string]string{"message": message}, &out); err != nil {
		return "", err
	}
	return out.Message, nil
}
