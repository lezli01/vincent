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
}

// snapshotStepResponse is one step of the task's snapshot (spec §13.2).
type snapshotStepResponse struct {
	Index        int    `json:"index"`
	ID           string `json:"id"`
	Type         string `json:"type"`
	Prompt       string `json:"prompt,omitempty"`
	Run          string `json:"run,omitempty"`
	Instructions string `json:"instructions,omitempty"`
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
	LoopItem      *string `json:"loop_item"`
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
	SkipReason     *string  `json:"skip_reason"`
	ResultSummary  string   `json:"result_summary"`
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
}

// handleTaskCreate implements POST /v1/tasks (spec §13.2). The workflow is
// resolved through the registry and snapshotted onto the task (§5.3). The
// optional agent/model/effort override is validated per §8.2 with the
// override applied to every agent step's resolved triple: the agent must
// name a registered adapter, a known-invalid model/effort is a 400, and a
// catalog-unknown value passes with a warning on the 201 body (T2.11).
func (s *Server) handleTaskCreate(w http.ResponseWriter, r *http.Request) {
	var req taskCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	project, err := s.deps.Store.GetProject(ctx, req.ProjectID)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("project %d not found", req.ProjectID))
		return
	}
	if err != nil {
		s.internalError(w, "get project", err)
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "title is required")
		return
	}
	// Workflow resolution (§5.3): the named workflow, else the project's
	// default, else the built-in adhoc. The entry's source becomes the
	// task's snapshot, so later edits to the file never touch this task.
	workflowName := firstNonBlank(ptrValue(req.Workflow), project.DefaultWorkflow, workflow.AdhocName)
	entry, ok := s.deps.Workflows.Lookup(project.ID, workflowName)
	if !ok {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("workflow %q not found for project %d", workflowName, project.ID))
		return
	}
	if !entry.Valid() {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("workflow %q is invalid: %s", workflowName, entry.Errors.Error()))
		return
	}
	// Platform restriction (§8.1.1, task 010). Rejected at creation rather than
	// at admission: a task that can never run should not reach the board, and
	// the human asking for it is right here to be told why.
	if mismatch := entry.Workflow.PlatformMismatch(workflow.HostPlatform()); mismatch != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("workflow %q cannot run here: %s", workflowName, mismatch))
		return
	}
	baseBranch := project.DefaultBranch
	if req.BaseBranch != nil && strings.TrimSpace(*req.BaseBranch) != "" {
		baseBranch = strings.TrimSpace(*req.BaseBranch)
	}
	if !s.localBranchExists(ctx, project.Path, baseBranch) {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("base_branch %q does not resolve to a local branch in %s", baseBranch, project.Path))
		return
	}
	var branchName string
	if req.BranchName != nil {
		branchName = strings.TrimSpace(*req.BranchName)
	}
	var agentOverride string
	if req.Agent != nil && strings.TrimSpace(*req.Agent) != "" {
		agentOverride = strings.TrimSpace(*req.Agent)
		if s.deps.Agents != nil {
			if _, ok := s.deps.Agents.Get(agentOverride); !ok {
				writeError(w, http.StatusBadRequest, CodeValidationFailed,
					fmt.Sprintf("unknown agent %q (available: %s)", agentOverride,
						strings.Join(s.deps.Agents.Names(), ", ")))
				return
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
			return
		}
		// Re-validate the expansion, not just its shape. The nesting rules an
		// include can break — no `loop` inside a `loop`, no `fan_out` inside a
		// `parallel` — are decidable only once the steps are in place
		// (decision 9), and so is the §8.2 catalog check over a callee's
		// materialised agent fields.
		out, merr := workflow.Marshal(expanded)
		if merr != nil {
			s.internalError(w, "re-encode expanded snapshot", merr)
			return
		}
		revalidated, _, verr := workflow.Parse(out, s.deps.Workflows.Options())
		if verr != nil {
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("workflow %q does not validate once its includes are expanded: %s",
					workflowName, verr.Error()))
			return
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
			return
		}
		out, merr := workflow.Marshal(resolved)
		if merr != nil {
			s.internalError(w, "re-encode resolved fan-out snapshot", merr)
			return
		}
		wf, snapshot = resolved, string(out)
	}

	t := store.Task{
		ProjectID:        project.ID,
		Title:            title,
		WorkflowName:     entry.Name,
		WorkflowSnapshot: snapshot,
		BaseBranch:       baseBranch,
		Fields:           req.Fields,
		AgentOverride:    agentOverride,
		State:            store.TaskQueued,
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
		return
	}
	// The `on_input: require` gate (§7.4, task 013), after the catalog check
	// so a task with two problems reports the model one first — that is the
	// field the human just typed.
	if mismatch := s.inputMismatch(ctx, wf, &t); mismatch != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, mismatch)
		return
	}
	// Branch naming (§5.3, task 001): `default < config.yaml < project < literal`.
	// Resolve before the insert so a bad name is a 400 rather than a task that
	// blocks later, and so the git-side checks run with no transaction open.
	spec := s.branchSpec(branchName, project)
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
		if msg := s.checkBranchCollision(ctx, project.Path, preview.Name); msg != "" {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
			return
		}
		t.BranchName = preview.Name
	}
	if err := s.deps.Store.CreateTask(ctx, &t, resolveBranch); err != nil {
		var claimed *store.BranchClaimedError
		if errors.As(err, &claimed) {
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("branch %q is already claimed by task %d",
					claimed.Branch, claimed.TaskID))
			return
		}
		s.internalError(w, "create task", err)
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
	for i := range runs {
		if run := &runs[i]; run.Iteration >= out.Iteration {
			out.Iteration, out.Item = run.Iteration, run.LoopItem
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
	projects, err := s.deps.Store.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	names := make(map[int64]string, len(projects))
	for i := range projects {
		names[projects[i].ID] = projects[i].Name
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
	q := r.URL.Query()
	rawOffset, rawTail := q.Get("offset"), q.Get("tail")
	if rawOffset != "" && rawTail != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"offset and tail are mutually exclusive")
		return
	}
	var offset, tail int64
	if rawOffset != "" {
		offset, err = strconv.ParseInt(rawOffset, 10, 64)
		if err != nil || offset < 0 {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "offset must be a non-negative integer")
			return
		}
	}
	if rawTail != "" {
		tail, err = strconv.ParseInt(rawTail, 10, 64)
		if err != nil || tail < 0 {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "tail must be a non-negative integer")
			return
		}
	}
	normalized := false
	switch format := q.Get("format"); format {
	case "", "raw":
	case "normalized":
		normalized = true
	default:
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"format must be raw or normalized")
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
	fi, err := f.Stat()
	if err != nil {
		s.internalError(w, "stat transcript", err)
		return
	}
	size := fi.Size()

	// The body always covers whole records: it ends at the last newline, and
	// a tail window starts at the beginning of the record its byte count
	// lands in (§13.2).
	end, err := lineBoundary(f, size)
	if err != nil {
		s.internalError(w, "scan transcript", err)
		return
	}
	start := min(offset, size)
	if rawTail != "" {
		if start, err = lineBoundary(f, size-tail); err != nil {
			s.internalError(w, "scan transcript", err)
			return
		}
	}
	start = min(start, end)

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Next-Offset", strconv.FormatInt(end, 10))
	w.WriteHeader(http.StatusOK)
	section := io.NewSectionReader(f, start, end-start)
	if !normalized {
		_, _ = io.Copy(w, section)
		return
	}
	if err := normalizeTranscript(w, section, s.transcriptParser(run.Agent)); err != nil {
		// The status is already on the wire; the client sees a short body and
		// resumes from the offset it was given, which is still line-aligned.
		s.deps.Logger.Error("normalize transcript", "run_id", run.ID, "error", err)
	}
}

// handleTaskDiff implements GET /v1/tasks/{id}/diff: the worktree against
// merge-base(base, HEAD) — committed + staged + unstaged tracked changes;
// untracked files excluded (documented limitation, phase 1 decision).
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
	mergeBase, err := s.git(ctx, t.WorktreePath, "merge-base", t.BaseBranch, "HEAD")
	if err != nil {
		writeError(w, http.StatusConflict, CodeInvalidState,
			fmt.Sprintf("cannot compute merge-base with %q: %v", t.BaseBranch, err))
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
