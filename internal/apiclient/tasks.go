package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// Task is one row of GET /v1/tasks (§13.2). The client owns this struct
// rather than importing the server's: they live in one binary and are
// integration-tested against the real handlers, so drift cannot ship
// unnoticed, and the API package keeps its DTOs unexported.
type Task struct {
	ID          int64             `json:"id"`
	ProjectID   int64             `json:"project_id"`
	ProjectName string            `json:"project_name"`
	Title       string            `json:"title"`
	Fields      map[string]string `json:"fields,omitempty"`
	Workflow    string            `json:"workflow"`
	State       string            `json:"state"`
	Priority    int               `json:"priority"`

	// BranchName is the task's branch (§5.3). It belongs on the list row and
	// not on TaskDetail alone: with configurable names (task 001) a
	// `vincent/*` glob no longer finds every branch vincent made, so the
	// cleanup guidance sends a reader to `vincent task ls --archived` for
	// them. GET /v1/tasks has always served it; only this struct dropped it.
	BranchName string `json:"branch_name"`

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

	// StatusMessage is the newest step run's status message (§5.4, task
	// 033), served on the list row so a board never fetches step rows for
	// it. Nil when the newest attempt said nothing.
	StatusMessage *string `json:"status_message"`

	// ParentTaskID, LaneID and LaneOrder identify a fan-out lane (§7.6, task
	// 014); all nil for a root task.
	ParentTaskID *int64  `json:"parent_task_id"`
	LaneID       *string `json:"lane_id"`
	LaneOrder    *int    `json:"lane_order"`
	// Children is the subtree rollup, served on the detail endpoint whenever
	// the task has lanes. Lanes are hidden from the task list by design, and
	// this is what pays for that: it is where a blocked lane becomes visible.
	Children *ChildrenRollup `json:"children,omitempty"`
	// Loop is the §7.8 rollup, present only while a `loop` step is the
	// current one. It is what a board renders `loop 4/10` from.
	Loop *LoopRollup `json:"loop,omitempty"`

	BlockReason      *string  `json:"block_reason"`
	PauseRequested   bool     `json:"pause_requested"`
	AvailableActions []string `json:"available_actions"`

	// QueuedReason and AdmitNotBefore describe a queued task waiting on
	// something other than a free slot (§11) — `usage_limit` today, with the
	// instant the daemon will try again. Both nil is the ordinary queue.
	QueuedReason   *string    `json:"queued_reason"`
	AdmitNotBefore *time.Time `json:"admit_not_before"`

	CreatedAt  time.Time  `json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	ArchivedAt *time.Time `json:"archived_at"`
}

// Hold reports a queued task that is waiting on something other than a free
// slot (§11): the reason, and when the daemon will try again. ok is false for
// the ordinary queue, which is every task that carries no queued_reason.
func (t Task) Hold() (reason string, until *time.Time, ok bool) {
	if t.QueuedReason == nil || *t.QueuedReason == "" {
		return "", nil, false
	}
	return *t.QueuedReason, t.AdmitNotBefore, true
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
	// ParentID lists one fan-out parent's lanes, in merge order (§7.6, task
	// 014). Lanes are excluded from every other listing by default, so this
	// is how a client drills into a subtree.
	ParentID int64
	// IncludeChildren asks for the flat everything, lanes included.
	IncludeChildren bool
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
	if o.ParentID != 0 {
		q.Set("parent_id", strconv.FormatInt(o.ParentID, 10))
	}
	if o.IncludeChildren {
		q.Set("include_children", "true")
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

// StepRun is one attempt of one step (§5.4) as the detail view reads it.
type StepRun struct {
	ID        int64  `json:"id"`
	StepIndex int    `json:"step_index"`
	StepID    string `json:"step_id"`
	StepName  string `json:"step_name"`
	StepType  string `json:"step_type"`
	Attempt   int    `json:"attempt"`
	// Iteration is which pass of an enclosing `loop` produced this attempt —
	// 1-based, 0 outside a loop (§7.8). Body steps share the loop's
	// StepIndex, so this and StepID together identify one of them.
	Iteration int `json:"iteration"`
	// LoopItem is the `for_each` item that iteration ran on; nil for a
	// `count:` loop and outside a loop.
	LoopItem *string `json:"loop_item"`
	State    string  `json:"state"`

	Agent  *string `json:"agent"`
	Model  *string `json:"model"`
	Effort *string `json:"effort"`

	ExitCode      *int    `json:"exit_code"`
	CheckExitCode *int    `json:"check_exit_code"`
	FailureReason *string `json:"failure_reason"`
	// SkipReason is "condition" for an attempt skipped by a false `if:`
	// guard (§7.7) and nil for the human `skip` action (§6) — both of which
	// carry State "skipped".
	SkipReason    *string `json:"skip_reason"`
	ResultSummary string  `json:"result_summary"`
	// StatusMessage is what the step said about itself (§5.4, task 033):
	// free text its own process set while it was running, and nil when it
	// said nothing. It is never a failure cause — `failure_reason` is the
	// daemon's verdict, and a killed step can be carrying a message it set
	// long before — so a client renders it as the step's last status, not
	// beside the reason.
	StatusMessage *string `json:"status_message"`

	// TranscriptPath is nil for a step that produced no transcript — a manual
	// gate or a skipped step. The output pane renders such a row's metadata
	// instead of fetching.
	TranscriptPath *string  `json:"transcript_path"`
	InputTokens    *int64   `json:"input_tokens"`
	OutputTokens   *int64   `json:"output_tokens"`
	CostUSD        *float64 `json:"cost_usd"`
	// InputWaitMS is time this attempt spent waiting on a human (§7.4).
	InputWaitMS int64 `json:"input_wait_ms"`
	// PromptOverride/RunOverride report a human-edited retry (§6).
	PromptOverride bool `json:"prompt_override"`
	RunOverride    bool `json:"run_override"`

	StartedAt  time.Time  `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
}

// Duration is the attempt's §17 active time: wall clock minus the time it
// spent waiting on a human, which is not work the step did. A running
// attempt is measured to now. ok is false before the attempt started.
func (s StepRun) Duration(now time.Time) (time.Duration, bool) {
	if s.StartedAt.IsZero() {
		return 0, false
	}
	end := now
	if s.FinishedAt != nil {
		end = *s.FinishedAt
	}
	d := end.Sub(s.StartedAt) - time.Duration(s.InputWaitMS)*time.Millisecond
	if d < 0 {
		return 0, true
	}
	return d, true
}

// Live reports whether this attempt is still producing output, which is what
// makes follow mode meaningful (a finished attempt will never gain a line).
func (s StepRun) Live() bool { return s.FinishedAt == nil && s.State == "running" }

// ChildrenRollup summarizes a fan-out subtree (§13.2, task 014). Blocked and
// AwaitingGate are ids: the client re-fetches whichever it decides to show,
// the way §13.3 hands out everything else.
type ChildrenRollup struct {
	Total        int            `json:"total"`
	Settled      int            `json:"settled"`
	ByState      map[string]int `json:"by_state"`
	Blocked      []int64        `json:"blocked"`
	AwaitingGate []int64        `json:"awaiting_gate"`
}

// LoopRollup is the §7.8 loop rollup: where a task is inside the `loop` step
// it is currently on.
type LoopRollup struct {
	Driver        string `json:"driver"`
	Iteration     int    `json:"iteration"`
	MaxIterations int    `json:"max_iterations"`
	Item          string `json:"item,omitempty"`
}

// Display is the short form a board row shows beside the k/n step column:
// "loop 4/10", plus the item when a `for_each` is running one. Empty before
// the first iteration has a row, when there is nothing yet to report.
func (r *LoopRollup) Display() string {
	if r == nil || r.Iteration == 0 {
		return ""
	}
	out := fmt.Sprintf("loop %d/%d", r.Iteration, r.MaxIterations)
	if r.Item != "" {
		out += " " + r.Item
	}
	return out
}

// Summary is the short form a board row shows beside `awaiting_children` —
// "2 blocked", "3/5 done". It names what a human has to act on first, since
// that is the whole reason the rollup exists.
func (r *ChildrenRollup) Summary() string {
	if r == nil || r.Total == 0 {
		return ""
	}
	switch {
	case len(r.Blocked) > 0:
		return fmt.Sprintf("%d blocked", len(r.Blocked))
	case len(r.AwaitingGate) > 0:
		return fmt.Sprintf("%d at a gate", len(r.AwaitingGate))
	default:
		return fmt.Sprintf("%d/%d done", r.Settled, r.Total)
	}
}

// TaskDetail is GET /v1/tasks/{id}: the task plus every attempt of every
// step it has run.
type TaskDetail struct {
	Task
	Description  string          `json:"description"`
	WorktreePath *string         `json:"worktree_path"`
	PendingInput json.RawMessage `json:"pending_input,omitempty"`
	// GitHubIssue is the issue this task was created from (task 035); nil for
	// a task created without one. It is the snapshot as captured, never
	// refreshed — a client renders it as the task's history, not as the
	// issue's current state.
	GitHubIssue *GitHubIssue `json:"github_issue,omitempty"`
	Steps       []StepRun    `json:"steps"`
	// Warnings is set on the POST /v1/tasks 201 only: advisory findings that
	// did not block creation, such as a model the catalog does not know.
	// POST /v1/tasks/{id}/repair reports the same findings about its own
	// selection, in its own body (task 025).
	Warnings []string `json:"warnings,omitempty"`
	// WorkflowSteps is the task's snapshot: the text edit+retry opens in an
	// editor, and a gate's instructions.
	WorkflowSteps []WorkflowStep `json:"workflow_steps,omitempty"`
}

// WorkflowStep is one step of the task's snapshot (§13.2).
type WorkflowStep struct {
	Index        int    `json:"index"`
	ID           string `json:"id"`
	Type         string `json:"type"`
	Prompt       string `json:"prompt,omitempty"`
	Run          string `json:"run,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	// ResolvedFrom is the chain of workflows this step was spliced through
	// (§7.9, task 019), outermost first. Empty for a step the task's own
	// workflow wrote, and for a daemon that predates the field — which are
	// indistinguishable and treated the same.
	ResolvedFrom []string `json:"resolved_from,omitempty"`
}

// EditableText reports the text edit+retry opens for this step and the
// override field it belongs in: a prompt for an agent step, a command for
// command and check steps. ok is false for a manual gate, which has neither —
// the field name is what keeps the client from sending the mismatched pair
// the daemon would reject.
func (s WorkflowStep) EditableText() (text, field string, ok bool) {
	switch s.Type {
	case "agent":
		return s.Prompt, "prompt", true
	case "command", "check":
		return s.Run, "run", true
	default:
		return "", "", false
	}
}

// Step returns the snapshot step at index i.
func (t TaskDetail) Step(i int) (WorkflowStep, bool) {
	for _, s := range t.WorkflowSteps {
		if s.Index == i {
			return s, true
		}
	}
	return WorkflowStep{}, false
}

// GetTask fetches one task with its step history — one call, because the
// detail endpoint already embeds steps[].
func (c *Client) GetTask(ctx context.Context, id int64) (TaskDetail, error) {
	var out TaskDetail
	if err := c.get(ctx, "/v1/tasks/"+strconv.FormatInt(id, 10), &out); err != nil {
		return TaskDetail{}, err
	}
	return out, nil
}

// ListTasks fetches the task list the board renders.
func (c *Client) ListTasks(ctx context.Context, opts ListTasksOptions) ([]Task, error) {
	var out []Task
	if err := c.get(ctx, "/v1/tasks"+opts.query(), &out); err != nil {
		return nil, err
	}
	return out, nil
}

// CreateTaskRequest is the POST /v1/tasks body (§13.2). Every optional field
// is a pointer so an omitted one keeps the server's own fallback: Workflow
// falls through to the project default then adhoc, BaseBranch to the
// project's default branch, and the Agent/Model/Effort triple to the
// workflow's defaults (§8.6 level 3). Sending "" instead of omitting would
// ask the daemon to override those defaults with nothing.
//
// It is deliberately not the retry-scoped Override: that carries
// prompt_override/run_override and shares no field with this one.
type CreateTaskRequest struct {
	ProjectID   int64             `json:"project_id"`
	Workflow    *string           `json:"workflow,omitempty"`
	Title       string            `json:"title"`
	Description *string           `json:"description,omitempty"`
	Fields      map[string]string `json:"fields,omitempty"`
	BaseBranch  *string           `json:"base_branch,omitempty"`
	// BranchName names this task's branch outright, overriding the project and
	// config templates (task 001). Used verbatim, never rendered.
	BranchName *string `json:"branch_name,omitempty"`
	Priority   *int    `json:"priority,omitempty"`
	Agent      *string `json:"agent,omitempty"`
	Model      *string `json:"model,omitempty"`
	Effort     *string `json:"effort,omitempty"`
	// GitHubIssue creates the task from a GitHub issue (task 035). The daemon
	// fetches it, prefills whatever this request left unset, and persists the
	// snapshot; anything set here wins over the issue-derived value.
	GitHubIssue *int `json:"github_issue,omitempty"`
}

// CreateTask creates a task and returns it as the daemon recorded it. The
// 201 body carries Warnings for anything advisory — a catalog-unknown model
// or effort — which is not an error: the task exists and will run.
func (c *Client) CreateTask(ctx context.Context, req CreateTaskRequest) (TaskDetail, error) {
	var out TaskDetail
	if err := c.post(ctx, "/v1/tasks", req, &out); err != nil {
		return TaskDetail{}, err
	}
	return out, nil
}
