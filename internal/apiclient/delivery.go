package apiclient

import (
	"context"
	"fmt"
	"time"
)

// EventTaskDeliveryChanged is the durable SSE type a recorded delivery
// arrives as (§13.3, task 137.2). Payload `{kind, implicit}`; a client
// re-fetches the task for the text. Declared here, as EventTaskStatusChanged
// is, because the client owns its wire vocabulary; a test pins it to the
// daemon's string.
const EventTaskDeliveryChanged = "task.delivery_changed"

// Delivery kinds (§5.3).
const (
	DeliveryChange = "change"
	DeliveryReply  = "reply"
	DeliveryNone   = "none"
)

// Delivery is what a task's run delivered (§5.3, task 137.2): the workshop's
// result, which a handoff reads. Branch, Base and HeadSHA are the daemon's;
// Implicit marks one the daemon wrote because the run recorded none.
type Delivery struct {
	Kind        string    `json:"kind"`
	Title       string    `json:"title,omitempty"`
	Body        string    `json:"body,omitempty"`
	Summary     string    `json:"summary,omitempty"`
	HeadSHA     string    `json:"head_sha,omitempty"`
	Branch      string    `json:"branch,omitempty"`
	Base        string    `json:"base,omitempty"`
	CloseReason string    `json:"close_reason,omitempty"`
	Proof       string    `json:"proof,omitempty"`
	Implicit    bool      `json:"implicit"`
	RecordedAt  time.Time `json:"recorded_at"`
	// Ready is nil when the delivery carries no readiness, which reads ready
	// (task 137 decision 13); ReadyBy says who set it.
	Ready   *bool  `json:"ready,omitempty"`
	ReadyBy string `json:"ready_by,omitempty"`
}

// DeliveryInput is what a step, or a human correcting a finished task,
// records. An empty Kind is `change`; an empty HeadSHA is the branch tip,
// which the daemon resolves, because clients never touch git.
type DeliveryInput struct {
	Kind        string `json:"kind,omitempty"`
	Title       string `json:"title,omitempty"`
	Body        string `json:"body,omitempty"`
	Summary     string `json:"summary,omitempty"`
	HeadSHA     string `json:"head_sha,omitempty"`
	CloseReason string `json:"close_reason,omitempty"`
	Proof       string `json:"proof,omitempty"`
	// Ready marks the delivery ready to hand off: on a running task until a
	// later call without it, on a done task as the caller's mark.
	Ready bool `json:"ready,omitempty"`
}

type taskDelivery struct {
	TaskID   int64     `json:"task_id"`
	Delivery *Delivery `json:"delivery"`
}

// GetTaskDelivery reads a task's delivery in full; nil before it has one.
func (c *Client) GetTaskDelivery(ctx context.Context, taskID int64) (*Delivery, error) {
	var out taskDelivery
	if err := c.get(ctx, fmt.Sprintf("/v1/tasks/%d/delivery", taskID), &out); err != nil {
		return nil, err
	}
	return out.Delivery, nil
}

// SetTaskDelivery records what the task's run delivered, replacing any
// earlier delivery (§13.2, task 137.2), and returns it as stored. A task that
// is neither running nor done is an *Error carrying 409.
func (c *Client) SetTaskDelivery(ctx context.Context, taskID int64, in DeliveryInput) (*Delivery, error) {
	var out taskDelivery
	if err := c.post(ctx, fmt.Sprintf("/v1/tasks/%d/delivery", taskID), in, &out); err != nil {
		return nil, err
	}
	return out.Delivery, nil
}
