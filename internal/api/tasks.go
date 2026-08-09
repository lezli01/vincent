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
	ID             int64             `json:"id"`
	ProjectID      int64             `json:"project_id"`
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
	// Warnings are non-fatal §8.2 catalog findings from creation-time
	// validation; only the POST /v1/tasks response carries them.
	Warnings []string `json:"warnings,omitempty"`
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
	return taskResponse{
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
	StepName       string   `json:"step_name"`
	StepType       string   `json:"step_type"`
	Attempt        int      `json:"attempt"`
	State          string   `json:"state"`
	Agent          *string  `json:"agent"`
	Model          *string  `json:"model"`
	Effort         *string  `json:"effort"`
	PID            *int     `json:"pid"`
	ExitCode       *int     `json:"exit_code"`
	CheckExitCode  *int     `json:"check_exit_code"`
	FailureReason  *string  `json:"failure_reason"`
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
	// ProjectName saves every client the projects join.
	ProjectName string `json:"project_name"`
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
	Priority    *int              `json:"priority"`
	Agent       *string           `json:"agent"`
	Model       *string           `json:"model"`
	Effort      *string           `json:"effort"`
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
	baseBranch := project.DefaultBranch
	if req.BaseBranch != nil && strings.TrimSpace(*req.BaseBranch) != "" {
		baseBranch = strings.TrimSpace(*req.BaseBranch)
	}
	if !s.localBranchExists(ctx, project.Path, baseBranch) {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("base_branch %q does not resolve to a local branch in %s", baseBranch, project.Path))
		return
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

	t := store.Task{
		ProjectID:        project.ID,
		Title:            title,
		WorkflowName:     entry.Name,
		WorkflowSnapshot: entry.Source,
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
	warnings, cerr := s.checkTaskCatalog(entry.Workflow, &t)
	if cerr != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, cerr)
		return
	}
	if err := s.deps.Store.CreateTask(ctx, &t); err != nil {
		s.internalError(w, "create task", err)
		return
	}
	// The branch name embeds the task id (§5.3), so it is written right
	// after the insert; the runner recomputes it if a crash lands between.
	// The write is branch-name only: the scheduler may have admitted the
	// task already, and a full-row update would stomp its state.
	t.BranchName = worktree.BranchName(t.ID, t.Title)
	if err := s.deps.Store.SetTaskBranchName(ctx, t.ID, t.BranchName); err != nil {
		s.internalError(w, "assign branch name", err)
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
			ProjectName:  names[t.ProjectID],
			StepName:     summary.stepName(t.CurrentStep),
			InputTokens:  rollups[t.ID].InputTokens,
			OutputTokens: rollups[t.ID].OutputTokens,
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
