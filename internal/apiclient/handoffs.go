package apiclient

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// Handoff states (§5.7): derived by the daemon from the task and the items.
const (
	HandoffLive    = "live"
	HandoffWaiting = "waiting"
	HandoffSettled = "settled"
)

// Handoff is one handoff (§5.7, task 137): an integration task and the
// issues it claimed. Items is empty on a list row; Task is set only on the
// create's answer, so a client can follow the task it started.
type Handoff struct {
	ID        int64         `json:"id"`
	ProjectID int64         `json:"project_id"`
	TaskID    int64         `json:"task_id"`
	State     string        `json:"state"`
	Grant     []string      `json:"grant"`
	CreatedBy string        `json:"created_by"`
	TriggerID string        `json:"trigger_id,omitempty"`
	CreatedAt time.Time     `json:"created_at"`
	Items     []HandoffItem `json:"items,omitempty"`
	Task      *TaskDetail   `json:"task,omitempty"`
}

// HandoffItem is one claimed issue: its delivery as it stood at the claim,
// and the outcome once one is recorded ("" while unresolved).
type HandoffItem struct {
	IssueID        int64      `json:"issue_id"`
	DeliveryTaskID *int64     `json:"delivery_task_id"`
	Delivery       *Delivery  `json:"delivery"`
	Outcome        string     `json:"outcome"`
	OutcomeAt      *time.Time `json:"outcome_at"`
	OutcomeBy      string     `json:"outcome_by"`
	Note           string     `json:"note"`
}

// HandoffSelect is a create's `select:` (task 137 decision 8). Every list
// may be empty; Max 0 takes every eligible issue. Lane and Order may only be
// hand_off and priority, and may be left out.
type HandoffSelect struct {
	Lane     string   `json:"lane,omitempty"`
	Delivery []string `json:"delivery,omitempty"`
	Kind     []string `json:"kind,omitempty"`
	Labels   []string `json:"labels,omitempty"`
	Max      int      `json:"max,omitempty"`
	Order    string   `json:"order,omitempty"`
}

// CreateHandoffRequest is POST /v1/handoffs' body: exactly one of IssueIDs
// and Select, then the task options POST /v1/tasks takes.
type CreateHandoffRequest struct {
	ProjectID int64          `json:"project_id"`
	Workflow  string         `json:"workflow"`
	IssueIDs  []int64        `json:"issue_ids,omitempty"`
	Select    *HandoffSelect `json:"select,omitempty"`
	Grant     []string       `json:"grant,omitempty"`
	Title     string         `json:"title,omitempty"`
	Paused    bool           `json:"paused,omitempty"`

	Description    string            `json:"description,omitempty"`
	Fields         map[string]string `json:"fields,omitempty"`
	Priority       *int              `json:"priority,omitempty"`
	Agent          string            `json:"agent,omitempty"`
	Model          string            `json:"model,omitempty"`
	Effort         string            `json:"effort,omitempty"`
	Restricted     bool              `json:"restricted,omitempty"`
	MaxTaskCostUSD *float64          `json:"max_task_cost_usd,omitempty"`
}

// HandoffListOptions filters ListHandoffs; zero values match everything.
type HandoffListOptions struct {
	ProjectID int64
	States    []string
	IssueID   int64
}

func (o HandoffListOptions) query() string {
	v := url.Values{}
	if o.ProjectID != 0 {
		v.Set("project_id", strconv.FormatInt(o.ProjectID, 10))
	}
	for _, s := range o.States {
		v.Add("state", s)
	}
	if o.IssueID != 0 {
		v.Set("issue_id", strconv.FormatInt(o.IssueID, 10))
	}
	if len(v) == 0 {
		return ""
	}
	return "?" + v.Encode()
}

// CreateHandoff starts a handoff. A non-empty idempotencyKey makes the
// create replayable: re-sending the same request with the same key returns
// the handoff the first send started (§13.1).
func (c *Client) CreateHandoff(ctx context.Context, req CreateHandoffRequest, idempotencyKey string) (Handoff, error) {
	var hdr http.Header
	if idempotencyKey != "" {
		hdr = http.Header{"Idempotency-Key": {idempotencyKey}}
	}
	var out Handoff
	if err := c.sendWith(ctx, http.MethodPost, "/v1/handoffs", hdr, req, &out); err != nil {
		return Handoff{}, err
	}
	return out, nil
}

// ListHandoffs lists handoffs, newest first, without their items.
func (c *Client) ListHandoffs(ctx context.Context, opts HandoffListOptions) ([]Handoff, error) {
	var out []Handoff
	if err := c.get(ctx, "/v1/handoffs"+opts.query(), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// GetHandoff fetches one handoff with its items.
func (c *Client) GetHandoff(ctx context.Context, id int64) (Handoff, error) {
	var out Handoff
	if err := c.get(ctx, fmt.Sprintf("/v1/handoffs/%d", id), &out); err != nil {
		return Handoff{}, err
	}
	return out, nil
}
