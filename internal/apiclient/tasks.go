package apiclient

import (
	"context"
	"net/url"
	"strconv"
	"time"
)

// Task is one row of GET /v1/tasks (§13.2). The client owns this struct
// rather than importing the server's: they live in one binary and are
// integration-tested against the real handlers, so drift cannot ship
// unnoticed, and the API package keeps its DTOs unexported.
type Task struct {
	ID          int64  `json:"id"`
	ProjectID   int64  `json:"project_id"`
	ProjectName string `json:"project_name"`
	Title       string `json:"title"`
	Workflow    string `json:"workflow"`
	State       string `json:"state"`
	Priority    int    `json:"priority"`

	// CurrentStep is zero-based; StepTotal is the snapshot's step count.
	// A board renders them as k/n with k = CurrentStep+1, clamped, because
	// a finished task's cursor sits one past the last step.
	CurrentStep int    `json:"current_step"`
	StepTotal   int    `json:"step_total"`
	StepName    string `json:"step_name"`

	// CostUSD is nil when no attempt reported a cost — which is not the
	// same as free, and must not render as $0.00.
	CostUSD      *float64 `json:"cost_usd"`
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`

	BlockReason      *string  `json:"block_reason"`
	PauseRequested   bool     `json:"pause_requested"`
	AvailableActions []string `json:"available_actions"`

	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	ArchivedAt *time.Time `json:"archived_at"`
}

// StepDisplay renders the k/n pair. It reports ok=false when the snapshot
// had no steps, so a caller renders a dash instead of "1/0".
func (t Task) StepDisplay() (k, n int, ok bool) {
	if t.StepTotal <= 0 {
		return 0, 0, false
	}
	k = t.CurrentStep + 1
	if k > t.StepTotal {
		k = t.StepTotal // a finished task's cursor sits past the last step
	}
	if k < 1 {
		k = 1
	}
	return k, t.StepTotal, true
}

// Elapsed reports how long the task has been alive: wall clock from
// StartedAt to FinishedAt, or to now while it is still running (§15 — the
// board's elapsed is deliberately not §17's active time, which would hide
// the wait on a task that is blocked on a human). It reports ok=false for a
// task that has never started.
func (t Task) Elapsed(now time.Time) (time.Duration, bool) {
	if t.StartedAt == nil {
		return 0, false
	}
	end := now
	if t.FinishedAt != nil {
		end = *t.FinishedAt
	}
	d := end.Sub(*t.StartedAt)
	if d < 0 {
		return 0, true
	}
	return d, true
}

// ArchivedScope selects how ListTasks treats archived tasks (§13.2).
type ArchivedScope string

const (
	// ArchivedExclude omits archived tasks. The zero value, and the default
	// the server applies when the parameter is absent.
	ArchivedExclude ArchivedScope = ""
	// ArchivedOnly returns archived tasks and nothing else.
	ArchivedOnly ArchivedScope = "true"
	// ArchivedAll returns both.
	ArchivedAll ArchivedScope = "all"
)

// ListTasksOptions filters GET /v1/tasks. Zero values mean "no filter".
type ListTasksOptions struct {
	ProjectID int64
	State     string
	Archived  ArchivedScope
	Limit     int
	Offset    int
}

func (o ListTasksOptions) query() string {
	q := url.Values{}
	if o.ProjectID != 0 {
		q.Set("project_id", strconv.FormatInt(o.ProjectID, 10))
	}
	if o.State != "" {
		q.Set("state", o.State)
	}
	if o.Archived != ArchivedExclude {
		q.Set("archived", string(o.Archived))
	}
	if o.Limit > 0 {
		q.Set("limit", strconv.Itoa(o.Limit))
	}
	if o.Offset > 0 {
		q.Set("offset", strconv.Itoa(o.Offset))
	}
	if len(q) == 0 {
		return ""
	}
	return "?" + q.Encode()
}

// ListTasks fetches the task list the board renders.
func (c *Client) ListTasks(ctx context.Context, opts ListTasksOptions) ([]Task, error) {
	var out []Task
	if err := c.get(ctx, "/v1/tasks"+opts.query(), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// AgentStatus is one adapter's availability from GET /v1/info.
type AgentStatus struct {
	Name          string `json:"name"`
	Available     bool   `json:"available"`
	Version       string `json:"version"`
	SupportsInput bool   `json:"supports_input"`
	Error         string `json:"error,omitempty"`
}

// Info is the GET /v1/info body: what the board header reports about the
// daemon itself.
type Info struct {
	Version          string        `json:"version"`
	PID              int           `json:"pid"`
	UptimeSeconds    int64         `json:"uptime_seconds"`
	MaxParallelTasks int           `json:"max_parallel_tasks"`
	Agents           []AgentStatus `json:"agents"`
}

// Info fetches daemon identity, caps and adapter availability. The board
// calls it once per connect: availability rides a daemon-side cache that
// only moves when a CLI is installed mid-session.
func (c *Client) Info(ctx context.Context) (Info, error) {
	var out Info
	if err := c.get(ctx, "/v1/info", &out); err != nil {
		return Info{}, err
	}
	return out, nil
}
