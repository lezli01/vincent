package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/mcp"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// firstNonBlank returns the first value that is not empty after trimming.
func firstNonBlank(values ...string) string {
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

// taskOrigin records which definition a task was created from (§5.3, task
// 043). The entry is the one Lookup's shadowing walk just returned, so this
// costs no second registry read, and the value is frozen here: nothing
// recomputes it afterwards, which is what lets it still answer "did a project
// `adhoc.yaml` stand in for the built-in" months later.
func taskOrigin(entry workflow.Entry, projectPath, globalDir string) *store.WorkflowOrigin {
	o := entry.Origin(projectPath, globalDir)
	return &store.WorkflowOrigin{Scope: string(o.Scope), File: o.File, Digest: o.Digest}
}

// ptrValue dereferences an optional string field, treating nil as empty.
func ptrValue(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// taskResponse is the JSON shape of a task (spec §5.3). Optional fields
// render as null.
type taskResponse struct {
	ID        int64 `json:"id"`
	ProjectID int64 `json:"project_id"`
	// ProjectName saves every client the projects join. It lives on the
	// shared response rather than on the list row because the detail endpoint
	// needs it too — omitting it there left every client's ProjectName empty
	// and `vincent task show` printing a blank project (T4.4 finding).
	ProjectName    string            `json:"project_name,omitempty"`
	Title          string            `json:"title"`
	Description    string            `json:"description"`
	Fields         map[string]string `json:"fields,omitempty"`
	Workflow       string            `json:"workflow"`
	Snapshot       string            `json:"workflow_snapshot,omitempty"` // detail view only
	BaseBranch     string            `json:"base_branch"`
	BranchName     string            `json:"branch_name"`
	WorktreePath   *string           `json:"worktree_path"`
	Priority       int               `json:"priority"`
	AgentOverride  *string           `json:"agent_override"`
	ModelOverride  *string           `json:"model_override"`
	EffortOverride *string           `json:"effort_override"`
	State          string            `json:"state"`
	CurrentStep    int               `json:"current_step"`
	// StepTotal is the snapshot's step count — the n in k/n. Zero when the
	// snapshot could not be parsed. It lives here rather than on the list DTO
	// alone so the detail view can render k/n without re-parsing the snapshot.
	StepTotal   int     `json:"step_total"`
	BlockReason *string `json:"block_reason"`
	// AdmitNotBefore and QueuedReason describe a queued task waiting on
	// something other than a free slot (§11, task 003) — an agent usage window
	// today. Both are null for every other task, so the pair is additive and
	// changes nothing for a client that ignores it. They are deliberately not
	// folded into block_reason, which means "set while blocked" (§14).
	AdmitNotBefore *string `json:"admit_not_before"`
	QueuedReason   *string `json:"queued_reason"`
	// Warnings are non-fatal §8.2 catalog findings from creation-time
	// validation; only the POST /v1/tasks response carries them.
	Warnings []string `json:"warnings,omitempty"`
	// ParentTaskID, LaneID and LaneOrder identify a fan-out lane (§7.6, task
	// 014). All null for a root task, which is every task that is not a lane.
	ParentTaskID *int64  `json:"parent_task_id"`
	LaneID       *string `json:"lane_id"`
	LaneOrder    *int    `json:"lane_order"`
	// Loop is the §7.8 rollup, present only while a `loop` step is the
	// current one. Derived per request from the step's rows, never stored: a
	// counter would be the persisted loop cursor decision 7 declined, and
	// §12.4 recovery would have to reconcile it.
	Loop *loopResponse `json:"loop,omitempty"`
	// Children is the §13.2 subtree rollup, present on the detail endpoint
	// whenever the task has lanes. Derived per request from one recursive
	// CTE, never stored: a counter would be a second truth that drifts.
	Children *childrenResponse `json:"children,omitempty"`
	// PendingInput is the normalized §7.4 input request while the task is
	// awaiting_input — embedded verbatim as the engine persisted it.
	PendingInput json.RawMessage `json:"pending_input,omitempty"`
	// PauseRequested is a pause accepted but not yet in effect (§6).
	PauseRequested bool `json:"pause_requested"`
	// GitHubIssue is the issue snapshot this task was created from (§5.3,
	// task 035); null for every task created without one. It is served as
	// captured and never refreshed — clients render it as history, not as the
	// issue's current state.
	GitHubIssue *github.Issue `json:"github_issue"`
	// GitHubPull is this task's pull-request link (§5.3, task 052); null for
	// a task no pull request has ever matched. It is a **pointer** — repo,
	// number, who linked it, and whether a human unlinked it — never a
	// snapshot: everything renderable about the pull request is served live
	// by GET /v1/tasks/{id}/github/pull, which is what lets a task still name
	// a pull request that has since merged.
	GitHubPull *github.PullLink `json:"github_pull"`
	// WorkflowOrigin is where the task's workflow definition came from (§5.3,
	// task 043): scope, scope-relative file and source digest, or `derived`
	// naming the parent of a fan-out lane. Null for a task created before the
	// origin was recorded, which clients render as `unknown` — never as a
	// synthesized scope, and never re-derived from today's registry.
	WorkflowOrigin *store.WorkflowOrigin `json:"workflow_origin"`
	// AvailableActions are the §6 human actions valid from the current
	// state. Derived, never stored: clients render an action bar from this
	// rather than restating the state machine.
	AvailableActions []string          `json:"available_actions"`
	CreatedAt        string            `json:"created_at"`
	UpdatedAt        string            `json:"updated_at"`
	StartedAt        *string           `json:"started_at"`
	FinishedAt       *string           `json:"finished_at"`
	ArchivedAt       *string           `json:"archived_at"`
	Steps            []stepRunResponse `json:"steps,omitempty"` // detail view only
	// WorkflowSteps is the snapshot's step list (detail view only): the text
	// edit+retry prefills an editor with, and a manual step's instructions.
	// It reflects earlier edit+retry rewrites, because the snapshot is this
	// task's execution truth (§5.3).
	WorkflowSteps []snapshotStepResponse `json:"workflow_steps,omitempty"`
	// SourceChatID names the chat this task was handed off from (§5.5, task
	// 074). It is the reverse of the one stored edge, `chats.handoff_task_id`,
	// read as a single indexed query per list rather than per rendered task.
	SourceChatID *int64 `json:"source_chat_id,omitempty"`
}

// snapshotStepResponse is one step of the task's snapshot (spec §13.2).
type snapshotStepResponse struct {
	Index        int    `json:"index"`
	ID           string `json:"id"`
	Type         string `json:"type"`
	Prompt       string `json:"prompt,omitempty"`
	Run          string `json:"run,omitempty"`
	Instructions string `json:"instructions,omitempty"`
	// ResolvedFrom is the chain of workflows this step was spliced through
	// (§7.9), outermost first. Absent for a step the task's own workflow
	// wrote.
	ResolvedFrom []string `json:"resolved_from,omitempty"`
}

// workflowSteps renders the parsed snapshot for the detail response. Nil for
// a snapshot that would not parse — the same degradation step_total already
// takes.
func workflowSteps(summary snapshotSummary) []snapshotStepResponse {
	if len(summary.steps) == 0 {
		return nil
	}
	out := make([]snapshotStepResponse, 0, len(summary.steps))
	for _, s := range summary.steps {
		out = append(out, snapshotStepResponse{
			Index:        s.index,
			ID:           s.id,
			Type:         s.stepType,
			Prompt:       s.prompt,
			Run:          s.run,
			Instructions: s.instructions,
			ResolvedFrom: s.resolvedFrom,
		})
	}
	return out
}

func toTaskResponse(t *store.Task, summary snapshotSummary) taskResponse {
	var laneID *string
	var laneOrder *int
	if t.ParentTaskID != nil {
		id, order := t.LaneID, t.LaneOrder
		laneID, laneOrder = &id, &order
	}
	return taskResponse{
		ParentTaskID:     t.ParentTaskID,
		LaneID:           laneID,
		LaneOrder:        laneOrder,
		ID:               t.ID,
		ProjectID:        t.ProjectID,
		Title:            t.Title,
		Description:      t.Description,
		Fields:           t.Fields,
		Workflow:         t.WorkflowName,
		BaseBranch:       t.BaseBranch,
		BranchName:       t.BranchName,
		WorktreePath:     nilIfEmpty(t.WorktreePath),
		Priority:         t.Priority,
		AgentOverride:    nilIfEmpty(t.AgentOverride),
		ModelOverride:    nilIfEmpty(t.ModelOverride),
		EffortOverride:   nilIfEmpty(t.EffortOverride),
		State:            string(t.State),
		CurrentStep:      t.CurrentStep,
		StepTotal:        summary.stepTotal,
		BlockReason:      nilIfEmpty(t.BlockReason),
		AdmitNotBefore:   timePtr(t.AdmitNotBefore),
		QueuedReason:     nilIfEmpty(t.QueuedReason),
		PendingInput:     rawIfNotEmpty(t.PendingInputJSON),
		GitHubIssue:      t.GitHubIssue,
		GitHubPull:       t.GitHubPull,
		WorkflowOrigin:   t.WorkflowOrigin,
		PauseRequested:   t.PauseRequested,
		AvailableActions: availableActions(t.State),
		CreatedAt:        t.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:        t.UpdatedAt.UTC().Format(time.RFC3339),
		StartedAt:        timePtr(t.StartedAt),
		FinishedAt:       timePtr(t.FinishedAt),
		ArchivedAt:       timePtr(t.ArchivedAt),
	}
}

// availableActions renders the §6 human actions valid from a state. Always a
// list, never null: a terminal task has none, and a client should not have
// to distinguish that from a missing field.
func availableActions(s store.TaskState) []string {
	actions := taskstate.HumanActionsFrom(s)
	out := make([]string, 0, len(actions))
	for _, a := range actions {
		out = append(out, string(a))
	}
	return out
}

// stepRunResponse is the JSON shape of one step-run attempt (spec §5.4).
type stepRunResponse struct {
	ID        int64  `json:"id"`
	StepIndex int    `json:"step_index"`
	StepID    string `json:"step_id"`
	// StepName is the snapshot's display name for this attempt's step, so a
	// timeline reads in the workflow author's words rather than in step ids.
	StepName string `json:"step_name"`
	StepType string `json:"step_type"`
	Attempt  int    `json:"attempt"`
	// Iteration is which pass of an enclosing `loop` produced this attempt —
	// 1-based, and 0 for every row outside a loop (§7.8). Body steps share
	// the loop's step_index, so this and step_id together are what tell two
	// of them apart.
	Iteration int `json:"iteration"`
	// LoopItem is the `for_each` item that iteration ran on; null for a
	// `count:` loop and outside a loop.
	LoopItem *string `json:"loop_item"`
	// LoopTotal is how many iterations the admission that wrote this row
	// planned to run — the `for_each` list's real length, which the snapshot
	// does not have. 0 for a row outside a loop, and for one written before
	// the column existed.
	LoopTotal     int     `json:"loop_total"`
	State         string  `json:"state"`
	Agent         *string `json:"agent"`
	Model         *string `json:"model"`
	Effort        *string `json:"effort"`
	PID           *int    `json:"pid"`
	ExitCode      *int    `json:"exit_code"`
	CheckExitCode *int    `json:"check_exit_code"`
	FailureReason *string `json:"failure_reason"`
	// SkipReason says why a `skipped` attempt was skipped: "condition" for a
	// false `if:` guard (§7.7), null for the human `skip` action (§6). The
	// two share one state, so this is what tells a timeline which it is.
	SkipReason    *string `json:"skip_reason"`
	ResultSummary string  `json:"result_summary"`
	// StatusMessage is what the step said about *itself* (§5.4, task 036):
	// free text its own process set through
	// POST /v1/tasks/{id}/steps/{step_id}/status. Null when it said nothing,
	// which is every step type that runs no process and every `agent` or
	// `command` step that was never asked to report.
	//
	// It is not a failure cause and must not be rendered as one: a step
	// killed on `timeout` can carry a message it set half an hour earlier,
	// and `failure_reason` is the daemon's verdict.
	StatusMessage  *string  `json:"status_message"`
	TranscriptPath *string  `json:"transcript_path"`
	InputTokens    *int64   `json:"input_tokens"`
	OutputTokens   *int64   `json:"output_tokens"`
	CostUSD        *float64 `json:"cost_usd"`
	// InputWaitMS is the time this attempt spent in awaiting_input (§7.4);
	// excluded from duration metrics (§17).
	InputWaitMS int64 `json:"input_wait_ms"`
	// PromptOverride/RunOverride report that a human edited this attempt's
	// prompt or command before retrying it (§6 edit+retry). Booleans, not the
	// text: a timeline flags the edit, and the text itself lives in the task's
	// workflow_snapshot, which is the execution truth (§5.3).
	PromptOverride bool    `json:"prompt_override"`
	RunOverride    bool    `json:"run_override"`
	StartedAt      string  `json:"started_at"`
	FinishedAt     *string `json:"finished_at"`
}

// listTaskResponse is one row of GET /v1/tasks: the task plus the derived
// columns a board renders (§15). They live here rather than on taskResponse
// because the detail endpoint already serves the same numbers per attempt in
// steps[], and a rollup there would be a second, redundant answer.
type listTaskResponse struct {
	taskResponse
	// StepName is the current step's name (or id), empty for a task whose
	// cursor has run past the last step. Distinct from a step row's own
	// step_name, which names *that* attempt's step.
	StepName string `json:"step_name"`
	// CostUSD sums every attempt (§17); null when no attempt reported a
	// cost, which is not the same as free.
	CostUSD      *float64 `json:"cost_usd"`
	InputTokens  int64    `json:"input_tokens"`
	OutputTokens int64    `json:"output_tokens"`
	// StatusMessage is the newest step run's status (§5.4, task 036),
	// denormalized here the way step_name and cost_usd are: a board reads
	// this endpoint and never fetches step rows. Null when the newest
	// attempt said nothing — which is deliberate, so a finished step's line
	// does not linger beside the next one.
	StatusMessage *string `json:"status_message"`
}

func toStepRunResponse(r *store.StepRun, summary snapshotSummary) stepRunResponse {
	return stepRunResponse{
		ID:             r.ID,
		StepIndex:      r.StepIndex,
		StepID:         r.StepID,
		StepName:       summary.stepName(r.StepIndex),
		StepType:       r.StepType,
		Iteration:      r.Iteration,
		LoopItem:       nilIfEmpty(r.LoopItem),
		LoopTotal:      r.LoopTotal,
		PromptOverride: r.PromptOverride != "",
		RunOverride:    r.RunOverride != "",
		Attempt:        r.Attempt,
		State:          string(r.State),
		Agent:          nilIfEmpty(r.Agent),
		Model:          nilIfEmpty(r.Model),
		Effort:         nilIfEmpty(r.Effort),
		PID:            r.PID,
		ExitCode:       r.ExitCode,
		CheckExitCode:  r.CheckExitCode,
		FailureReason:  nilIfEmpty(r.FailureReason),
		SkipReason:     nilIfEmpty(r.SkipReason),
		ResultSummary:  r.ResultSummary,
		StatusMessage:  nilIfEmpty(r.StatusMessage),
		TranscriptPath: nilIfEmpty(r.TranscriptPath),
		InputTokens:    r.InputTokens,
		OutputTokens:   r.OutputTokens,
		CostUSD:        r.CostUSD,
		InputWaitMS:    r.InputWaitMS,
		StartedAt:      r.StartedAt.UTC().Format(time.RFC3339),
		FinishedAt:     timePtr(r.FinishedAt),
	}
}

// rawIfNotEmpty embeds stored JSON verbatim; "" renders as an absent field.
func rawIfNotEmpty(s string) json.RawMessage {
	if s == "" {
		return nil
	}
	return json.RawMessage(s)
}

type taskCreateRequest struct {
	ProjectID   int64             `json:"project_id"`
	Workflow    *string           `json:"workflow"`
	Title       string            `json:"title"`
	Description *string           `json:"description"`
	Fields      map[string]string `json:"fields"`
	BaseBranch  *string           `json:"base_branch"`
	// BranchName names this task's branch outright, overriding the project and
	// config templates (§5.3, task 001). It is used verbatim, never rendered:
	// it is a name the user typed for one task, so a stray brace belongs to the
	// name rather than being a template error.
	BranchName *string `json:"branch_name"`
	Priority   *int    `json:"priority"`
	Agent      *string `json:"agent"`
	Model      *string `json:"model"`
	Effort     *string `json:"effort"`
	// GitHubIssue creates this task from a GitHub issue (§13.2, task 035).
	// The daemon fetches it, prefills the task from it and persists the
	// snapshot on the row; **any value the request supplies explicitly wins**
	// over the issue-derived one, which is what makes `--github-issue N` and
	// the TUI's previewed prefill produce the same stored task (decision 2).
	GitHubIssue *int `json:"github_issue"`
	// GitHubPull creates this task **from** a GitHub pull request, and runs
	// it on that pull request's head branch (§13.2, task 064). The daemon
	// resolves it, prefills title, description and a declared `pull` field
	// from it, writes the `human` link, and makes the head branch the task's
	// branch — the top of §5.3's naming chain, above even a typed literal
	// (decision 1). Explicit values win over prefilled ones exactly as they
	// do for `github_issue`.
	//
	// Naming both `github_issue` and `github_pull` is refused: two prefills
	// would fight over the same title and description, and there is no
	// defensible order.
	GitHubPull *int `json:"github_pull"`
}

// boundTaskCreate applies §13.1's size bounds to a task-create body. It is
// shared with the handoff route (task 074) for the reason prepareTaskCreate
// is: two copies would drift into a body one route accepts and the other
// rejects.
func boundTaskCreate(req *taskCreateRequest) string {
	for _, b := range []string{
		boundTaskFields(req.Title, ptrValue(req.Description), req.Fields),
		boundString("base_branch", ptrValue(req.BaseBranch), maxNameBytes),
		boundString("branch_name", ptrValue(req.BranchName), maxNameBytes),
	} {
		if b != "" {
			return b
		}
	}
	return ""
}

// preparedTask is a task-create body that has passed every validation §13.2
// applies before a branch name is decided: the project, the resolved and
// expanded workflow, and the task row built from both.
//
// It stops short of the branch because that is exactly where the two callers
// differ. POST /v1/tasks resolves a name through §5.3's chain; a handoff
// (task 074) inherits the chat's verbatim and has no chain to walk.
type preparedTask struct {
	project  *store.Project
	task     store.Task
	workflow *workflow.Workflow
	pull     *github.PullRequest
	warnings []string
}

// prepareTaskCreate is the whole of POST /v1/tasks' validation up to the
// branch: project, workflow resolution and snapshot, include expansion,
// fan-out resolution, the GitHub prefills, declared fields, the base branch,
// the agent/model/effort override, MCP provenance and the four capability
// gates.
//
// It is factored out rather than copied because `POST /v1/chats/{id}/handoff`
// must accept exactly the task POST /v1/tasks accepts (task 074 decision 1):
// a second copy would drift, and the drift would surface as a task the handoff
// route takes and the create route refuses. It writes its own 400s, the way
// applyIssuePrefill beside it does, and reports false once it has.
//
// inheritedBase, when non-empty, replaces the request's `base_branch` and is
// taken verbatim: a handoff's base branch is a fact about a worktree that
// already exists, not a name to validate against the repository as it is now.
func (s *Server) prepareTaskCreate(
	ctx context.Context, w http.ResponseWriter, req *taskCreateRequest, inheritedBase string,
) (*preparedTask, bool) {
	project, err := s.deps.Store.GetProject(ctx, req.ProjectID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("project %d not found", req.ProjectID))
		return nil, false
	}
	if err != nil {
		s.internalError(w, "get project", err)
		return nil, false
	}
	// Workflow resolution (§5.3): the named workflow, else the project's
	// default, else the built-in adhoc. The entry's source becomes the
	// task's snapshot, so later edits to the file never touch this task.
	workflowName := firstNonBlank(ptrValue(req.Workflow), project.DefaultWorkflow, workflow.AdhocName)
	entry, ok := s.deps.Workflows.Lookup(project.ID, workflowName)
	if !ok {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("workflow %q not found for project %d", workflowName, project.ID))
		return nil, false
	}
	if !entry.Valid() {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("workflow %q is invalid: %s", workflowName, entry.Errors.Error()))
		return nil, false
	}
	// Platform restriction (§8.1.1, task 010). Rejected at creation rather than
	// at admission: a task that can never run should not reach the board, and
	// the human asking for it is right here to be told why.
	if mismatch := entry.Workflow.PlatformMismatch(workflow.HostPlatform()); mismatch != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("workflow %q cannot run here: %s", workflowName, mismatch))
		return nil, false
	}
	// The GitHub issue prefill (task 035), before field validation because it
	// is one of the things being validated, and before the title check
	// because filling the title in is half of what it is for.
	if req.GitHubIssue != nil && req.GitHubPull != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"github_issue and github_pull cannot both be given: they would prefill the same fields from different sources")
		return nil, false
	}
	issue, ok := s.applyIssuePrefill(ctx, w, project, entry.Workflow, req)
	if !ok {
		return nil, false
	}
	// The pull-request prefill (task 064), in the same place and for the same
	// reasons: before field validation because it is one of the things being
	// validated, and before the title check because filling the title in is
	// half of what it is for.
	pull, pullRepo, ok := s.applyPullPrefill(ctx, w, project, entry.Workflow, req)
	if !ok {
		return nil, false
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "title is required")
		return nil, false
	}
	// Workflow-declared fields (§8.1.2, task 022) are a contract on the
	// selected root workflow. The map remains open: ValidateTaskFields checks
	// only names the workflow declared, so existing custom metadata continues
	// to pass through and be snapshotted on the task unchanged.
	//
	// PrepareTaskFields runs first (task 058): it substitutes a *required*
	// field's declared default for an omitted key and canonicalizes a
	// multi-valued enum to declared order, so the task row records the value
	// that actually applied and every client produces the same string for the
	// same selection. It never invents an optional field's default — an
	// optional key the caller omitted stays absent from .Task.Fields.
	req.Fields = entry.Workflow.PrepareTaskFields(req.Fields)
	if fieldErrs := entry.Workflow.ValidateTaskFields(req.Fields); len(fieldErrs) > 0 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, fieldErrs.Error())
		return nil, false
	}
	// An inherited base branch skips the repository check on purpose: it
	// names the commit an existing worktree was cut from, and a task that
	// adopts that worktree is not about to cut another. Refusing it because
	// the branch has since been deleted locally would reject a handoff for a
	// fact that no longer has any bearing on it (task 074).
	baseBranch := inheritedBase
	if baseBranch == "" {
		baseBranch = project.DefaultBranch
		if req.BaseBranch != nil && strings.TrimSpace(*req.BaseBranch) != "" {
			baseBranch = strings.TrimSpace(*req.BaseBranch)
		}
		if !s.localBranchExists(ctx, project.Path, baseBranch) {
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("base_branch %q does not resolve to a local branch in %s", baseBranch, project.Path))
			return nil, false
		}
	}
	var agentOverride string
	if req.Agent != nil && strings.TrimSpace(*req.Agent) != "" {
		agentOverride = strings.TrimSpace(*req.Agent)
		if s.deps.Agents != nil {
			if _, ok := s.deps.Agents.Get(agentOverride); !ok {
				writeError(w, http.StatusBadRequest, CodeValidationFailed,
					fmt.Sprintf("unknown agent %q (available: %s)", agentOverride,
						strings.Join(s.deps.Agents.Names(), ", ")))
				return nil, false
			}
		}
	}

	// Include expansion (§7.9, task 019). Every `type: include` is spliced
	// into the snapshot **now**, for the reason the fan-out tree below is
	// resolved now: §5.3 says execution uses the snapshot precisely so later
	// edits to a workflow file cannot mutate an in-flight task.
	//
	// It runs before fan-out resolution because an include can *bring* a
	// fan_out step, and that step's lanes need resolving like any other.
	//
	// The task's own override goes in because it outranks a callee's
	// `defaults:` in §8.6's order (decision 7); it is read here rather than
	// from the task row below because expansion has to happen before the row
	// is built.
	wf, snapshot := entry.Workflow, entry.Source
	if workflow.HasInclude(wf) {
		expanded, xerr := workflow.Expand(wf, workflow.ExpandOptions{
			Lookup: s.laneLookup(project.ID),
			Limits: workflow.IncludeLimits{MaxDepth: s.deps.Config().Include.MaxDepth},
			Override: agent.Level{
				Agent:  agentOverride,
				Model:  strings.TrimSpace(ptrValue(req.Model)),
				Effort: strings.TrimSpace(ptrValue(req.Effort)),
			},
		})
		if xerr != nil {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, xerr.Error())
			return nil, false
		}
		// Re-validate the expansion, not just its shape. The nesting rules an
		// include can break — no `loop` inside a `loop`, no `fan_out` inside a
		// `parallel` — are decidable only once the steps are in place
		// (decision 9), and so is the §8.2 catalog check over a callee's
		// materialised agent fields.
		out, merr := workflow.Marshal(expanded)
		if merr != nil {
			s.internalError(w, "re-encode expanded snapshot", merr)
			return nil, false
		}
		revalidated, _, verr := workflow.Parse(out, s.deps.Workflows.Options())
		if verr != nil {
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("workflow %q does not validate once its includes are expanded: %s",
					workflowName, verr.Error()))
			return nil, false
		}
		wf, snapshot = revalidated, string(out)
	}

	// Fan-out tree resolution (§7.6, task 014 decisions 4, 5). Every lane
	// naming a registry workflow is resolved into the snapshot **now**, so
	// the tree's shape is fixed at creation and later edits to those files
	// cannot mutate this task. The bounds are checked here for the same
	// reason: a depth-3 explosion should be a 400 in front of the person
	// typing, not two hundred worktrees discovered six hours later.
	if workflow.HasFanOut(wf) {
		cfg := s.deps.Config()
		resolved, _, terr := workflow.ResolveTree(wf, s.laneLookup(project.ID),
			workflow.Limits{MaxDepth: cfg.FanOut.MaxDepth, MaxTasks: cfg.FanOut.MaxTasks})
		if terr != nil {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, terr.Error())
			return nil, false
		}
		out, merr := workflow.Marshal(resolved)
		if merr != nil {
			s.internalError(w, "re-encode resolved fan-out snapshot", merr)
			return nil, false
		}
		wf, snapshot = resolved, string(out)
	}

	t := store.Task{
		ProjectID:        project.ID,
		Title:            title,
		WorkflowName:     entry.Name,
		WorkflowSnapshot: snapshot,
		WorkflowOrigin:   taskOrigin(entry, project.Path, s.deps.Workflows.GlobalDir()),
		BaseBranch:       baseBranch,
		Fields:           req.Fields,
		AgentOverride:    agentOverride,
		State:            store.TaskQueued,
		GitHubIssue:      issue,
	}
	if pull != nil {
		// The link is written **at creation**, as `human` (decision 7): the
		// pull-requests takeover then reads "claimed" immediately rather than
		// up to `github.poll_interval` later, and 052 decision 2's reconciler
		// will not overwrite a human link. `Branch` is what records that this
		// task's branch came from the pull request — admission, archive and
		// the retry guard all read it (decision 8).
		t.GitHubPull = &github.PullLink{
			Repo:     pullRepo.String(),
			Number:   pull.Number,
			Source:   github.SourceHuman,
			Branch:   true,
			Fork:     pull.Fork(),
			LinkedAt: time.Now().UTC(),
		}
	}
	// §13.4 provenance and its bounds (task 057 decision 7). Both only apply
	// to a task an agent step created over MCP; every other caller leaves
	// created_by_task_id NULL and is bounded by nothing new.
	if creator, viaMCP := mcp.CreatorTaskID(ctx); viaMCP {
		if msg := s.checkMCPBounds(ctx, creator); msg != "" {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
			return nil, false
		}
		t.CreatedByTaskID = &creator
	}
	if req.Description != nil {
		t.Description = *req.Description
	}
	if req.Priority != nil {
		t.Priority = *req.Priority
	}
	if req.Model != nil {
		t.ModelOverride = strings.TrimSpace(*req.Model)
	}
	if req.Effort != nil {
		t.EffortOverride = strings.TrimSpace(*req.Effort)
	}
	warnings, cerr := s.checkTaskCatalog(wf, &t)
	if cerr != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, cerr)
		return nil, false
	}
	// The `on_input: require` gate (§7.4, task 013), after the catalog check
	// so a task with two problems reports the model one first — that is the
	// field the human just typed.
	if mismatch := s.inputMismatch(ctx, wf, &t); mismatch != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, mismatch)
		return nil, false
	}
	// The `permission_mode: restricted` gate (§9.4, task 041), last of the
	// three capability checks for the same reason the input one is second:
	// the model and the input policy are fields the human just typed, and a
	// platform fact is the least surprising thing to be told about.
	if mismatch := s.restrictedMismatch(wf, &t); mismatch != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, mismatch)
		return nil, false
	}
	// The container gate (§16, task 061 decision 3), after the three
	// capability checks: it is a fact about the host and the daemon's config,
	// not about a field the human just typed, so it is the least surprising
	// thing to be told about last.
	if mismatch := s.containerMismatch(ctx, wf); mismatch != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, mismatch)
		return nil, false
	}
	return &preparedTask{project: project, task: t, workflow: wf, pull: pull, warnings: warnings}, true
}

// handleTaskCreate implements POST /v1/tasks (spec §13.2). The workflow is
// resolved through the registry and snapshotted onto the task (§5.3). The
// optional agent/model/effort override is validated per §8.2 with the
// override applied to every agent step's resolved triple: the agent must
// name a registered adapter, a known-invalid model/effort is a 400, and a
// catalog-unknown value passes with a warning on the 201 body (T2.11).
func (s *Server) handleTaskCreate(w http.ResponseWriter, r *http.Request) {
	var req taskCreateRequest
	// The large bound: a task's description and fields are what the workflow
	// templates into an agent's prompt, so this body legitimately carries prose
	// (§13.1, amended 2026-08-25).
	if !decodeJSONLimit(w, r, &req, maxLargeRequestBytes) {
		return
	}
	if b := boundTaskCreate(&req); b != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, b)
		return
	}
	// Replay protection (§13.1, task 040). The digest is taken here, over the
	// request as it arrived and before applyIssuePrefill mutates it below, so
	// an issue edited between two identical sends cannot manufacture a 409.
	idemKey, ok := readIdempotencyKey(w, r)
	if !ok {
		return
	}
	var idemSHA string
	if idemKey != "" {
		var derr error
		if idemSHA, derr = idempotencyDigest(&req); derr != nil {
			s.internalError(w, "digest task create request", derr)
			return
		}
		if s.replayTaskCreate(w, r, idemKey, idemSHA) {
			return
		}
	}
	ctx := r.Context()
	prep, ok := s.prepareTaskCreate(ctx, w, &req, "")
	if !ok {
		return
	}
	project, t, pull, warnings := prep.project, prep.task, prep.pull, prep.warnings
	var branchName string
	if req.BranchName != nil {
		branchName = strings.TrimSpace(*req.BranchName)
	}

	// Branch naming (§5.3, task 001): `default < config.yaml < project < literal`.
	// Resolve before the insert so a bad name is a 400 rather than a task that
	// blocks later, and so the git-side checks run with no transaction open.
	spec := s.branchSpec(branchName, project)
	if pull != nil {
		spec.Pull = pull.HeadBranch
	}
	bctx := branchContext(t.Title, t.BaseBranch, t.Fields, project)
	preview, err := resolveBranchPreview(spec, bctx)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("branch name could not be resolved: %v", err))
		return
	}
	// Legality is id-independent, so it is checked on both paths. Collision only
	// on the path whose name is final; see placeholderTaskID.
	if msg := s.checkBranchLegality(ctx, preview.Name); msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return
	}
	var resolveBranch func(int64) (string, error)
	if preview.NeedsID {
		// The name needs the id, so it is produced inside the insert transaction.
		resolveBranch = func(id int64) (string, error) {
			name, _, err := worktree.ResolveBranchName(spec, bctx.WithID(id))
			return name, err
		}
	} else {
		// A pull-request task is exempt from the collision check: its branch
		// is *expected* to exist, and refusing it would make the feature
		// unusable for anyone who has already looked at the pull request
		// (decision 2). The in-transaction claim check below still runs, so
		// two live vincent tasks on one head branch remain a 400.
		if pull == nil {
			if msg := s.checkBranchCollision(ctx, project.Path, preview.Name); msg != "" {
				writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
				return
			}
		}
		t.BranchName = preview.Name
	}
	var key *store.IdempotencyKey
	if idemKey != "" {
		key = &store.IdempotencyKey{
			Method: r.Method, Path: idempotencyRoute, Key: idemKey, RequestSHA: idemSHA,
		}
	}
	if err := s.deps.Store.CreateTaskWithKey(ctx, &t, resolveBranch, key); err != nil {
		var claimed *store.BranchClaimedError
		switch {
		case errors.As(err, &claimed):
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("branch %q is already claimed by task %d",
					claimed.Branch, claimed.TaskID))
		case errors.Is(err, store.ErrIdempotencyKeyExists):
			// The concurrent duplicate: another request carrying this key
			// committed while this one was doing its work. The store's single
			// writer serialized the two transactions, this one's task insert
			// rolled back with the key insert that lost, and the winner's task
			// is the answer. replayTaskCreate cannot come back false here —
			// the row it looks for is the one that just rejected this insert.
			if !s.replayTaskCreate(w, r, idemKey, idemSHA) {
				s.internalError(w, "replay idempotent create", err)
			}
		default:
			s.internalError(w, "create task", err)
		}
		return
	}
	if s.deps.WakeRunner != nil {
		s.deps.WakeRunner()
	}
	resp := toTaskResponse(&t, s.snaps.get(t.ID, t.WorkflowSnapshot))
	resp.Warnings = warnings
	writeJSON(w, http.StatusCreated, resp)
}

// checkTaskCatalog applies the §8.2 cross-catalog check to every agent
// step's resolved triple with the task-level override in place. A
// known-invalid value yields a non-empty error message (→ 400); values no
// catalog knows come back as warnings for the 201 body. Never probes: the
// catalogs are the cache's primed-or-curated view (T2.11).
func (s *Server) checkTaskCatalog(wf *workflow.Workflow, t *store.Task) (warnings []string, errMsg string) {
	if s.deps.Catalog == nil || wf == nil {
		return nil, ""
	}
	catalogs := s.deps.Catalog.Catalogs()
	override := agent.Level{Agent: t.AgentOverride, Model: t.ModelOverride, Effort: t.EffortOverride}
	defaults := agent.Level{Agent: wf.Defaults.Agent, Model: wf.Defaults.Model, Effort: wf.Defaults.Effort}
	for i, step := range wf.Steps {
		if step.Type != workflow.StepAgent {
			continue
		}
		sel := agent.Resolve(
			agent.Level{Agent: step.Agent, Model: step.Model, Effort: step.Effort},
			override, defaults,
		)
		cerrs, cwarns := catalogs.Check(sel)
		if len(cerrs) > 0 {
			return nil, fmt.Sprintf("steps[%d] (%s): %s", i, step.ID, cerrs[0].Message)
		}
		for _, f := range cwarns {
			warnings = append(warnings, fmt.Sprintf("steps[%d] (%s): %s", i, step.ID, f.Message))
		}
	}
	return warnings, ""
}

// inputMismatch applies the `on_input: require` gate to a task about to be
// created or edited (§7.4, task 013): every step that requires mid-run input
// must resolve to an agent that can provide it.
//
// The verdict is the probed one, so claude's version gate counts — but only a
// *positive* "does not support input" refuses anything. A missing binary or a
// probe that did not answer lets the task through, because §9.6's
// degrade-never-block rule outranks this gate: one cold-logon probe timeout
// must not start refusing task creation against a healthy CLI (T4.22). The
// engine's pre-flight is the backstop for what a probe could not say here.
// laneLookup resolves a lane's workflow name through the registry, applying
// the same builtin < global < project shadowing every other lookup does
// (§5.2). An invalid workflow is reported as not found: a lane cannot be cut
// from a file that does not parse, and the registry's own errors are already
// visible on GET /v1/workflows.
func (s *Server) laneLookup(projectID int64) workflow.LookupFunc {
	return func(name string) (*workflow.Workflow, bool) {
		entry, ok := s.deps.Workflows.Lookup(projectID, name)
		if !ok || !entry.Valid() {
			return nil, false
		}
		return entry.Workflow, true
	}
}

// restrictedMismatch applies the `permission_mode: restricted` gate to a task
// about to be created (§9.4, task 041): every step that runs restricted must
// resolve to an adapter that can restrict on this host.
//
// Unlike inputMismatch it reads the *static* catalog rather than a probe, so
// it spawns nothing and answers the same on a machine with no CLI installed:
// cursor cannot restrict on Windows whether or not cursor-agent is there
// (§9.7). Only a positive "cannot" refuses; an unregistered agent and an
// adapter that states no level both let the task through.
//
// Retries are deliberately not gated. The decision was creation-time
// enforcement, not creation-plus-admission: a retry that would reproduce the
// condition is caught by the engine's restricted_unsupported backstop, which
// is what makes the pair defence in depth rather than one check written twice.
func (s *Server) restrictedMismatch(wf *workflow.Workflow, t *store.Task) string {
	if s.deps.Catalog == nil || wf == nil {
		return ""
	}
	catalogs := s.deps.Catalog.Catalogs()
	override := agent.Level{Agent: t.AgentOverride, Model: t.ModelOverride, Effort: t.EffortOverride}
	return wf.RestrictedMismatch(override, func(name string) bool {
		return !catalogs.RestrictedPossible(name)
	})
}

func (s *Server) inputMismatch(ctx context.Context, wf *workflow.Workflow, t *store.Task) string {
	if s.deps.Catalog == nil || wf == nil {
		return ""
	}
	override := agent.Level{Agent: t.AgentOverride, Model: t.ModelOverride, Effort: t.EffortOverride}
	return wf.InputMismatch(override, func(name string) bool {
		return s.deps.Catalog.InputVerdict(ctx, name) == agent.InputUnsupported
	})
}

// checkRetryInput re-applies the task-013 gate before a retry re-admits a
// task, reading the agent selection out of the task's own snapshot (§5.3).
// It writes the error and returns false when the retry would only reproduce
// an `input_unsupported` block; a task whose snapshot no longer parses is
// left alone, because the engine reports that as `invalid_snapshot` and two
// errors for one cause is one too many.
func (s *Server) checkRetryInput(w http.ResponseWriter, r *http.Request) bool {
	if s.deps.Catalog == nil {
		return true
	}
	ctx := r.Context()
	id, ok := taskIDFromPath(w, r)
	if !ok {
		return false
	}
	task, err := s.deps.Store.GetTask(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("task %d not found", id))
		return false
	}
	if err != nil {
		s.internalError(w, "get task", err)
		return false
	}
	wf, _, perr := workflow.Parse([]byte(task.WorkflowSnapshot), workflow.Options{})
	if perr != nil {
		return true
	}
	if mismatch := s.inputMismatch(ctx, wf, task); mismatch != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, mismatch)
		return false
	}
	return true
}

// childrenResponse is the fan-out subtree rollup (§13.2, task 014
// decision 13). Ids rather than objects for the blocked and gated lanes,
// which is how §13.3 hands out everything else: the client re-fetches what it
// decides it needs.
type childrenResponse struct {
	Total   int            `json:"total"`
	Settled int            `json:"settled"`
	ByState map[string]int `json:"by_state"`
	// Blocked and AwaitingGate are the lanes holding the join open on a
	// human — the cost paid for hiding lanes from the task list.
	Blocked      []int64 `json:"blocked"`
	AwaitingGate []int64 `json:"awaiting_gate"`
}

func toChildrenResponse(r store.ChildrenRollup) *childrenResponse {
	out := &childrenResponse{
		Total:        r.Total,
		Settled:      r.Settled,
		ByState:      make(map[string]int, len(r.ByState)),
		Blocked:      r.Blocked,
		AwaitingGate: r.AwaitingGate,
	}
	for state, n := range r.ByState {
		out.ByState[string(state)] = n
	}
	if out.Blocked == nil {
		out.Blocked = []int64{}
	}
	if out.AwaitingGate == nil {
		out.AwaitingGate = []int64{}
	}
	return out
}

// loopResponse is the §7.8 loop rollup — the shape of the §13.2 children
// rollup, one level cheaper. It exists so a board can render `loop 4/10`
// without a client re-deriving iteration numbers from the step rows, and it
// is deliberately not a new event type: ten iterations of a four-step body
// would put forty durable events on the stream to say what forty rows
// already say (task 016 decision 14).
type loopResponse struct {
	// Driver is "count" or "for_each".
	Driver string `json:"driver"`
	// Iteration is the pass in progress — the highest a row carries, or 0
	// before the first one starts.
	Iteration int `json:"iteration"`
	// MaxIterations is the largest iteration this loop could reach: the
	// `count:` itself, or the ceiling a `for_each` is bounded by, whose real
	// length is only known at run time.
	MaxIterations int `json:"max_iterations"`
	// Item is the `for_each` item the current iteration is running on; empty
	// for a `count:` loop.
	Item string `json:"item,omitempty"`
	// Total is the loop's *real* extent — how many iterations the admission
	// that is running it planned. For a `for_each` that is the derived list's
	// length, which the snapshot cannot know: the list is rendered at run
	// time, so a snapshot-derived denominator is the ceiling and not the
	// loop's own number. Absent (0) until a row records one, because before
	// the first iteration there is nothing to report and a guess would read
	// like an answer.
	//
	// MaxIterations keeps its own meaning beside it — the bound the loop
	// would block on. They are two numbers and neither stands in for the
	// other.
	Total int `json:"total,omitempty"`
	// BodyStep, BodyIndex and BodyTotal name the body step the current
	// iteration is on and its 1-based place in the body. The outer `step k/n`
	// counts a whole loop as one step, so without these "where is this task"
	// stops at the loop's own name.
	//
	// All three are absent together, and only together: a row whose step id
	// is not one of the snapshot's body ids — a repair row, or any row at all
	// once the snapshot no longer parses — gets no clause rather than a
	// position counted against a body that is not the one it ran in.
	BodyStep  string `json:"body_step,omitempty"`
	BodyIndex int    `json:"body_index,omitempty"`
	BodyTotal int    `json:"body_total,omitempty"`
}

// loopRollup builds the §7.8 rollup for a task whose current step is a loop,
// and returns nil for every other task. The extra query is why it is gated on
// the snapshot rather than run for every row: the summary is cached, so a
// task that is not in a loop costs a map lookup.
func (s *Server) loopRollup(ctx context.Context, t *store.Task, summary snapshotSummary) *loopResponse {
	def := summary.loopAt(t.CurrentStep)
	if def == nil {
		return nil
	}
	out := &loopResponse{Driver: def.driver, MaxIterations: def.total}
	runs, err := s.deps.Store.ListStepRunsAt(ctx, t.ID, t.CurrentStep)
	if err != nil {
		s.deps.Logger.Error("loop rollup", "task", t.ID, "error", err)
		return out
	}
	// The newest row of the highest iteration is the loop's current position:
	// its iteration and item, the extent its admission planned, and the body
	// step it is on.
	var current *store.StepRun
	for i := range runs {
		if run := &runs[i]; run.Iteration >= out.Iteration {
			out.Iteration, out.Item, current = run.Iteration, run.LoopItem, run
		}
	}
	if current == nil {
		// No rows: the loop has not started an iteration, so there is no
		// extent to report. Falling back to the ceiling here would put a
		// denominator on the wire that no admission ever planned.
		return out
	}
	// A row written before migration 0026 carries no extent; the snapshot's
	// bound is the only number left, and it is the one the rollup reported
	// for that row's whole lifetime.
	if out.Total = current.LoopTotal; out.Total == 0 {
		out.Total = def.total
	}
	for i, id := range def.body {
		if id == current.StepID {
			out.BodyStep, out.BodyIndex, out.BodyTotal = id, i+1, len(def.body)
			break
		}
	}
	return out
}

func (s *Server) handleTaskList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := store.TaskFilter{State: store.TaskState(q.Get("state"))}
	for name, dst := range map[string]*int{"limit": &filter.Limit, "offset": &filter.Offset} {
		if v := q.Get(name); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				writeError(w, http.StatusBadRequest, CodeValidationFailed,
					fmt.Sprintf("%s must be a non-negative integer", name))
				return
			}
			*dst = n
		}
	}
	if v := q.Get("project_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "project_id must be an integer")
			return
		}
		filter.ProjectID = id
	}
	// Fan-out lanes are excluded by default (§13.2, task 014 decision 13): a
	// list is the work someone asked for, and a 64-task tree would bury it.
	// `parent_id` drills into one parent's lanes; `include_children` is the
	// flat everything.
	if v := q.Get("parent_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "parent_id must be an integer")
			return
		}
		filter.ParentID = id
	}
	switch v := q.Get("include_children"); v {
	case "", "false":
	case "true":
		filter.Children = store.ChildrenInclude
	default:
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"include_children must be true or false")
		return
	}
	switch v := q.Get("archived"); v {
	case "", "false":
		filter.Archived = store.ArchivedExclude
	case "true":
		filter.Archived = store.ArchivedOnly
	case "all":
		filter.Archived = store.ArchivedAll
	default:
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"archived must be one of: false, true, all")
		return
	}
	ctx := r.Context()
	tasks, err := s.deps.Store.ListTasks(ctx, filter)
	if err != nil {
		s.internalError(w, "list tasks", err)
		return
	}
	out, err := s.toListResponse(ctx, tasks)
	if err != nil {
		s.internalError(w, "list tasks", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// toListResponse decorates tasks with the board columns: project names, the
// snapshot's step count and current step name, and the cost/token rollups.
// Three queries total regardless of task count — the point of the endpoint
// is that a board never has to fan out per row.
//
// The §7.8 loop rollup is the one per-row query, and it is taken only for a
// task whose *cached* snapshot says its current step is a loop. That is what
// keeps "a board never fans out" true in practice: a board of fifty tasks
// none of which is looping still costs three queries, and one that is
// looping is the row a person is watching iterate.
func (s *Server) toListResponse(ctx context.Context, tasks []store.Task) ([]listTaskResponse, error) {
	ids := make([]int64, 0, len(tasks))
	for i := range tasks {
		ids = append(ids, tasks[i].ID)
	}
	rollups, err := s.deps.Store.TaskRollups(ctx, ids)
	if err != nil {
		return nil, err
	}
	statuses, err := s.deps.Store.LatestStepStatuses(ctx, ids)
	if err != nil {
		return nil, err
	}
	projects, err := s.deps.Store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(projects))
	for i := range projects {
		names[projects[i].ID] = projects[i].Name
	}
	// One indexed query over the handed-off chats, turned into a map, rather
	// than a `source_chat_id` column on every task row (task 074 decision 2).
	sources, err := s.deps.Store.SourceChatIDs(ctx)
	if err != nil {
		return nil, err
	}

	out := make([]listTaskResponse, 0, len(tasks))
	for i := range tasks {
		t := &tasks[i]
		summary := s.snaps.get(t.ID, t.WorkflowSnapshot)
		row := listTaskResponse{
			taskResponse: toTaskResponse(t, summary),
			StepName:     summary.stepName(t.CurrentStep),
			InputTokens:  rollups[t.ID].InputTokens,
			OutputTokens: rollups[t.ID].OutputTokens,
		}
		row.Loop = s.loopRollup(ctx, t, summary)
		row.ProjectName = names[t.ProjectID]
		row.StatusMessage = nilIfEmpty(statuses[t.ID])
		if chatID, ok := sources[t.ID]; ok {
			row.SourceChatID = &chatID
		}
		if ru := rollups[t.ID]; ru.HasCost {
			cost := ru.CostUSD
			row.CostUSD = &cost
		}
		out = append(out, row)
	}
	return out, nil
}

func (s *Server) handleTaskGet(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	runs, err := s.deps.Store.ListStepRuns(r.Context(), t.ID)
	if err != nil {
		s.internalError(w, "list step runs", err)
		return
	}
	summary := s.snaps.get(t.ID, t.WorkflowSnapshot)
	resp := toTaskResponse(t, summary)
	resp.Snapshot = t.WorkflowSnapshot
	resp.WorkflowSteps = workflowSteps(summary)
	// The list endpoint has always carried project_name; the detail endpoint
	// never did, so every client's TaskDetail.ProjectName was silently empty
	// and `vincent task show` printed a blank project. One row read is cheaper
	// than making each client join it back (T4.4 walkthrough finding).
	if p, err := s.deps.Store.GetProject(r.Context(), t.ProjectID); err == nil {
		resp.ProjectName = p.Name
	}
	// The reverse of `chats.handoff_task_id` (task 074 decision 2): one
	// indexed lookup, so the detail view can link back to the conversation
	// this task's workspace came out of.
	if chatID, err := s.deps.Store.SourceChatID(r.Context(), t.ID); err == nil && chatID != 0 {
		resp.SourceChatID = &chatID
	}
	// The children rollup (§13.2, task 014 decisions 13, 27). Present
	// whenever the task has lanes, in any state — a parent mid-join, or one
	// already done, still wants its lane ids reachable from one GET, and
	// gating the field on a state would force a client to know the state to
	// know whether to look.
	resp.Loop = s.loopRollup(r.Context(), t, summary)
	if rollup, err := s.deps.Store.ChildrenOf(r.Context(), t.ID); err != nil {
		s.deps.Logger.Error("children rollup", "task", t.ID, "error", err)
	} else if rollup.Total > 0 {
		resp.Children = toChildrenResponse(rollup)
	}
	resp.Steps = make([]stepRunResponse, 0, len(runs))
	for i := range runs {
		resp.Steps = append(resp.Steps, toStepRunResponse(&runs[i], summary))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleTaskSteps(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	runs, err := s.deps.Store.ListStepRuns(r.Context(), t.ID)
	if err != nil {
		s.internalError(w, "list step runs", err)
		return
	}
	summary := s.snaps.get(t.ID, t.WorkflowSnapshot)
	out := make([]stepRunResponse, 0, len(runs))
	for i := range runs {
		out = append(out, toStepRunResponse(&runs[i], summary))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTranscript implements the ranged raw-JSONL transcript endpoint
// (spec §13.2): ?offset= is a byte offset, X-Next-Offset tells clients where
// to resume for the transcript-then-follow pattern (§13.3).
func (s *Server) handleTranscript(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	runID, err := strconv.ParseInt(r.PathValue("run_id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "step run ids are integers")
		return
	}
	run, err := s.deps.Store.GetStepRun(r.Context(), runID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && run.TaskID != t.ID) {
		writeError(w, http.StatusNotFound, CodeNotFound,
			fmt.Sprintf("step run %d not found on task %d", runID, t.ID))
		return
	}
	if err != nil {
		s.internalError(w, "get step run", err)
		return
	}
	rng, ok := transcriptRange(w, r)
	if !ok {
		return
	}
	if run.TranscriptPath == "" {
		writeError(w, http.StatusNotFound, CodeNotFound, "step run has no transcript")
		return
	}
	f, err := os.Open(run.TranscriptPath)
	if err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "transcript file is gone (pruned or removed)")
		return
	}
	defer func() { _ = f.Close() }()
	s.serveTranscriptFile(w, f, rng, run.Agent, run.ID)
}

// handleTaskDiff implements GET /v1/tasks/{id}/diff: the worktree against
// merge-base(base, HEAD) — committed + staged + unstaged tracked changes;
// untracked files excluded (documented limitation, phase 1 decision).
//
// The base is the commit the task actually started at when the task recorded
// one (§5.3, task 056), and the branch name only when it did not. A task cut
// from a fetched upstream tip is *ahead* of the local base branch, so a
// merge-base against the name resolves to the stale local commit and the
// reviewer reads every upstream change the fetch brought in as the task's own
// work.
func (s *Server) handleTaskDiff(w http.ResponseWriter, r *http.Request) {
	t, ok := s.taskFromPath(w, r)
	if !ok {
		return
	}
	if t.WorktreePath == "" {
		writeError(w, http.StatusConflict, CodeInvalidState, "task has no worktree yet")
		return
	}
	if _, err := os.Stat(t.WorktreePath); err != nil {
		writeError(w, http.StatusConflict, CodeInvalidState, "worktree no longer exists")
		return
	}
	ctx := r.Context()
	base := t.BaseBranch
	if t.BaseSHA != "" {
		base = t.BaseSHA
	}
	mergeBase, err := s.git(ctx, t.WorktreePath, "merge-base", base, "HEAD")
	if err != nil {
		writeError(w, http.StatusConflict, CodeInvalidState,
			fmt.Sprintf("cannot compute merge-base with %q: %v", base, err))
		return
	}
	diff, err := s.git(ctx, t.WorktreePath, "diff", mergeBase)
	if err != nil {
		writeError(w, http.StatusConflict, CodeInvalidState, fmt.Sprintf("git diff failed: %v", err))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if diff != "" {
		_, _ = io.WriteString(w, diff+"\n")
	}
}

// taskFromPath resolves the {id} path segment to a task, writing the
// 404 response itself when it cannot.
// taskIDFromPath resolves the {id} path segment, writing the 404 itself when
// it is not an integer.
func taskIDFromPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "task ids are integers")
		return 0, false
	}
	return id, true
}

func (s *Server) taskFromPath(w http.ResponseWriter, r *http.Request) (*store.Task, bool) {
	id, ok := taskIDFromPath(w, r)
	if !ok {
		return nil, false
	}
	t, err := s.deps.Store.GetTask(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("task %d not found", id))
		return nil, false
	}
	if err != nil {
		s.internalError(w, "get task", err)
		return nil, false
	}
	return t, true
}

func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func timePtr(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.UTC().Format(time.RFC3339)
	return &v
}

// checkMCPBounds enforces `mcp.max_depth` and `mcp.max_tasks` on the chain the
// creating task belongs to (§12.3, §13.4 — task 057 decision 7). It returns
// the refusal message, or "" to proceed.
//
// Enforced at creation, like §7.6's fan-out bounds and §7.9's include bound,
// and for the same reason: the cheapest moment to refuse a runaway is before
// the worktree exists. What is different is that the chain it walks is not in
// any snapshot — it is discovered one insert at a time, which is exactly why
// neither existing bound covers it.
func (s *Server) checkMCPBounds(ctx context.Context, creator int64) string {
	cfg := s.deps.Config()
	// +1 for the task about to be inserted: the ancestry is the chain behind
	// the creator, and the creator itself is a level.
	ancestors, err := s.deps.Store.MCPAncestry(ctx, creator, cfg.MCP.MaxDepth+2)
	if err != nil {
		s.deps.Logger.Warn("walk mcp ancestry", "task", creator, "error", err)
		return ""
	}
	depth := len(ancestors) + 1
	if depth >= cfg.MCP.MaxDepth {
		return fmt.Sprintf(
			"mcp.max_depth is %d and task %d is already %d levels deep in MCP-created tasks",
			cfg.MCP.MaxDepth, creator, depth)
	}
	root := creator
	if len(ancestors) > 0 {
		root = ancestors[len(ancestors)-1]
	}
	n, err := s.deps.Store.MCPChainSize(ctx, root)
	if err != nil {
		s.deps.Logger.Warn("count mcp chain", "task", root, "error", err)
		return ""
	}
	if n >= cfg.MCP.MaxTasks {
		return fmt.Sprintf(
			"mcp.max_tasks is %d and the MCP creation chain rooted at task %d already holds %d",
			cfg.MCP.MaxTasks, root, n)
	}
	return ""
}
