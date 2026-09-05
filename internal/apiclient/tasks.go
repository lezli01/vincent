package apiclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
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

	// WorkflowOrigin is where the task's workflow definition came from (§5.3,
	// task 043). Nil for a task created before origin was recorded; the
	// renderers below say `unknown` for that rather than guessing, because a
	// guess is exactly the silent substitution this field exists to expose.
	WorkflowOrigin *WorkflowOrigin `json:"workflow_origin,omitempty"`

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
	// 036), served on the list row so a board never fetches step rows for
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
	// LoopTotal is how many iterations the admission that wrote this row
	// planned — the real extent, 0 outside a loop and 0 for a row written
	// before the column existed.
	LoopTotal int    `json:"loop_total"`
	State     string `json:"state"`

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
	// StatusMessage is what the step said about itself (§5.4, task 036):
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

	// The rendered input this attempt was handed (§5.4, issue #323): the §8.4
	// render as the adapter or the shell received it, and the full recorded
	// bytes rather than the flags above. nil means nothing was recorded — an
	// attempt from before the record existed, and every field the step type
	// has no input for — while a non-nil empty string is a render that
	// produced nothing, which a client must say differently.
	//
	// RenderedIf is display only: a guard is re-evaluated every time it is
	// reached and is never sticky (§7.7), so this is what it rendered to on
	// this attempt, not a decision anything reads back. RenderedForEach is a
	// JSON array of the resolved items.
	RenderedPrompt  *string `json:"rendered_prompt"`
	RenderedRun     *string `json:"rendered_run"`
	RenderedCheck   *string `json:"rendered_check"`
	RenderedIf      *string `json:"rendered_if"`
	RenderedForEach *string `json:"rendered_for_each"`
	// InputTruncated says a recorded field lost bytes to the store's size
	// ceiling, so what came back is a prefix of what the step got.
	InputTruncated bool `json:"input_truncated"`

	// The run-time resolution behind this attempt: which level supplied the
	// agent, model and effort (§8.6), and the limits and shell it ran under.
	// nil / 0 mean not recorded. These are the row's own values and not a
	// re-resolution — config hot-reloads and task overrides are patchable, so
	// they can disagree with what a client would compute now, and the row is
	// the one that is right about what ran.
	AgentSource    *string `json:"agent_source"`
	ModelSource    *string `json:"model_source"`
	EffortSource   *string `json:"effort_source"`
	PermissionMode *string `json:"permission_mode"`
	TimeoutMS      int64   `json:"timeout_ms"`
	CheckTimeoutMS int64   `json:"check_timeout_ms"`
	Shell          *string `json:"shell"`
	WorkDir        *string `json:"work_dir"`

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
	Driver    string `json:"driver"`
	Iteration int    `json:"iteration"`
	// MaxIterations is the bound the loop would block on. Total is what it is
	// actually running: for a `for_each` the derived list's length, which the
	// snapshot cannot know because the list is rendered at run time. They are
	// two numbers — a 3-item list bounded by a ceiling of 10 has Total 3 and
	// MaxIterations 10 — and the counter reports Total whenever it has one.
	MaxIterations int    `json:"max_iterations"`
	Item          string `json:"item,omitempty"`
	Total         int    `json:"total,omitempty"`
	// BodyStep names the body step the current iteration is on, with
	// BodyIndex its 1-based place among BodyTotal body steps. Absent together
	// when the server could not place the row in a body it recognizes.
	BodyStep  string `json:"body_step,omitempty"`
	BodyIndex int    `json:"body_index,omitempty"`
	BodyTotal int    `json:"body_total,omitempty"`
}

// Clauses is the rollup's parts in priority order — the counter, the
// for_each item, the body step — so a narrow column can drop from the
// tail. Nil before the first iteration has a row, when there is nothing yet
// to report.
func (r *LoopRollup) Clauses() []string {
	if r == nil || r.Iteration == 0 {
		return nil
	}
	// Total is the loop's own extent and MaxIterations only the bound it is
	// under; the fallback is for a server that has no extent to give — an
	// iteration whose row predates the column.
	total := r.Total
	if total == 0 {
		total = r.MaxIterations
	}
	out := make([]string, 0, 3)
	out = append(out, fmt.Sprintf("loop %d/%d", r.Iteration, total))
	if r.Item != "" {
		out = append(out, r.Item)
	}
	if r.BodyStep != "" {
		out = append(out, fmt.Sprintf("%s %d/%d", r.BodyStep, r.BodyIndex, r.BodyTotal))
	}
	return out
}

// Display is the full short form a header shows beside the k/n step column:
// "loop 4/10 · alpha · repair 2/3". Empty before the first iteration has a
// row. A column too narrow for all of it reads Clauses and drops from the
// tail rather than truncating this string.
func (r *LoopRollup) Display() string {
	return strings.Join(r.Clauses(), " · ")
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
	Description string `json:"description"`
	// BaseBranch and the three overrides have always been on the wire; only
	// this struct dropped them. `vincent workflow render --task` binds a real
	// task's §8.4 context and its §8.6 level-2 override from exactly these
	// four, and re-deriving either client-side is what task 048 declined to
	// do.
	BaseBranch     string          `json:"base_branch"`
	AgentOverride  *string         `json:"agent_override"`
	ModelOverride  *string         `json:"model_override"`
	EffortOverride *string         `json:"effort_override"`
	WorktreePath   *string         `json:"worktree_path"`
	PendingInput   json.RawMessage `json:"pending_input,omitempty"`
	// GitHubIssue is the issue this task was created from (task 035); nil for
	// a task created without one. It is the snapshot as captured, never
	// refreshed — a client renders it as the task's history, not as the
	// issue's current state.
	GitHubIssue *GitHubIssue `json:"github_issue,omitempty"`
	// GitHubPull is this task's pull-request link (task 052); nil for a task
	// no pull request has ever matched. It is a pointer, not a snapshot —
	// TaskGitHubPull fetches what the pull request currently says.
	GitHubPull *GitHubPullLink `json:"github_pull,omitempty"`
	Steps      []StepRun       `json:"steps"`
	// Warnings is set on the POST /v1/tasks 201 only: advisory findings that
	// did not block creation, such as a model the catalog does not know.
	// POST /v1/tasks/{id}/repair reports the same findings about its own
	// selection, in its own body (task 025).
	Warnings []string `json:"warnings,omitempty"`
	// WorkflowSteps is the task's snapshot: the text edit+retry opens in an
	// editor, and a gate's instructions.
	WorkflowSteps []WorkflowStep `json:"workflow_steps,omitempty"`
	// SourceChatID is the chat this task was handed off from (task 074), or
	// nil when it was created any other way.
	SourceChatID *int64 `json:"source_chat_id,omitempty"`
}

// WorkflowOrigin is a task's recorded workflow provenance (§5.3, task 043):
// which scope of the registry won the shadowing walk, the source file relative
// to that scope's root, and a digest of the bytes it was created from. A
// fan-out lane instead carries `derived` naming its parent, whose snapshot its
// steps came from (§7.6).
//
// It is frozen at creation. The digest names the file version the task was
// created from, not the bytes the engine ran: include expansion, fan-out
// resolution and `edit + retry` all rewrite the snapshot afterwards.
type WorkflowOrigin struct {
	Scope        string `json:"scope"`
	File         string `json:"file,omitempty"`
	Digest       string `json:"digest,omitempty"`
	ParentTaskID *int64 `json:"parent_task_id,omitempty"`
}

// Source names where the definition came from, without the digest: `built-in`,
// `project .vincent/workflows/adhoc.yaml`, `derived from task 41`, or `unknown`
// for a task that predates the record. A nil receiver is the unrecorded case,
// so a caller never has to guard.
func (o *WorkflowOrigin) Source() string {
	if o == nil || o.Scope == "" {
		return "unknown"
	}
	switch o.Scope {
	case "builtin":
		return "built-in"
	case "derived":
		if o.ParentTaskID != nil {
			return fmt.Sprintf("derived from task %d", *o.ParentTaskID)
		}
		return "derived"
	}
	if o.File == "" {
		return o.Scope
	}
	return o.Scope + " " + o.File
}

// Display is Source plus the digest — the full audit line. The digest is
// printed whole rather than abbreviated: it is the part a reader compares
// against a file, and half of a hash compares against nothing.
func (o *WorkflowOrigin) Display() string {
	src := o.Source()
	if o == nil || o.Digest == "" {
		return src
	}
	return src + " " + o.Digest
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
	// GitHubPull creates the task **from** a GitHub pull request and runs it
	// on that pull request's head branch (task 064). The daemon resolves it,
	// prefills title, description and a declared `pull` field, writes the
	// link, and names the branch; everything the caller supplies explicitly
	// still wins, except the branch, which the pull request decides.
	GitHubPull *int `json:"github_pull,omitempty"`
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

// DiffSection is one section of GET /v1/tasks/{id}/diff?by=lane (§7.6,
// §13.2): what a single fan-out lane contributed to the parent's branch, or —
// for the one section with Remainder set — the parent's own commits and
// uncommitted work.
//
// The daemon computes the attribution because only it can: it walks the parent
// branch's own merges, which is the one place the lane a commit came from is
// recorded. Asking each lane for its own diff instead would cost a request per
// lane on a live-refreshing surface, and would double-count a dependency a
// `needs:` lane merged into itself.
type DiffSection struct {
	// LaneID and ChildTaskID name the lane. Both are zero on the remainder.
	LaneID      string `json:"lane_id"`
	ChildTaskID int64  `json:"child_task_id"`
	// MergeCommit is the merge the section was cut from; empty on the
	// remainder.
	MergeCommit string `json:"merge_commit"`
	// Remainder marks the section that belongs to no lane. Exactly one
	// section carries it, and it is last.
	Remainder bool `json:"remainder"`
	// Diff is a unified diff, spelled as the ungrouped endpoint spells its
	// whole body.
	Diff string `json:"diff"`
}

// DiffByLane fetches the task's diff attributed to its fan-out lanes.
//
// A task that fanned out nothing is not an error and not a special case: it
// comes back as a single remainder section holding the whole diff, which is
// what lets a caller ask for the grouped form unconditionally and render the
// flat one when that is all there is.
//
// Diff (actions.go) remains the ungrouped text/plain call, unchanged; the two
// answer the same 409s for a task with no worktree and for one whose worktree
// is gone.
func (c *Client) DiffByLane(ctx context.Context, id int64) ([]DiffSection, error) {
	var out struct {
		Sections []DiffSection `json:"sections"`
	}
	path := "/v1/tasks/" + strconv.FormatInt(id, 10) + "/diff?by=lane"
	if err := c.get(ctx, path, &out); err != nil {
		return nil, err
	}
	return out.Sections, nil
}
