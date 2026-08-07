package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

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
	BlockReason    *string           `json:"block_reason"`
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
}

func toTaskResponse(t *store.Task) taskResponse {
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
		BlockReason:      nilIfEmpty(t.BlockReason),
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
	ID             int64    `json:"id"`
	StepIndex      int      `json:"step_index"`
	StepID         string   `json:"step_id"`
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
	StartedAt      string   `json:"started_at"`
	FinishedAt     *string  `json:"finished_at"`
}

func toStepRunResponse(r *store.StepRun) stepRunResponse {
	return stepRunResponse{
		ID:             r.ID,
		StepIndex:      r.StepIndex,
		StepID:         r.StepID,
		StepType:       r.StepType,
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
		StartedAt:      r.StartedAt.UTC().Format(time.RFC3339),
		FinishedAt:     timePtr(r.FinishedAt),
	}
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
// resolved through the registry and snapshotted onto the task (§5.3); the
// optional agent/model/effort override is validated per the T1.7–T1.9
// decision: the agent must name a registered adapter, model/effort are free
// text until T2.11's catalog validation.
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
	if err := s.deps.Store.CreateTask(ctx, &t); err != nil {
		s.internalError(w, "create task", err)
		return
	}
	// The branch name embeds the task id (§5.3), so it is written right
	// after the insert; the runner recomputes it if a crash lands between.
	t.BranchName = worktree.BranchName(t.ID, t.Title)
	if err := s.deps.Store.UpdateTask(ctx, &t); err != nil {
		s.internalError(w, "assign branch name", err)
		return
	}
	if s.deps.WakeRunner != nil {
		s.deps.WakeRunner()
	}
	writeJSON(w, http.StatusCreated, toTaskResponse(&t))
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
	tasks, err := s.deps.Store.ListTasks(r.Context(), filter)
	if err != nil {
		s.internalError(w, "list tasks", err)
		return
	}
	out := make([]taskResponse, 0, len(tasks))
	for i := range tasks {
		out = append(out, toTaskResponse(&tasks[i]))
	}
	writeJSON(w, http.StatusOK, out)
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
	resp := toTaskResponse(t)
	resp.Snapshot = t.WorkflowSnapshot
	resp.Steps = make([]stepRunResponse, 0, len(runs))
	for i := range runs {
		resp.Steps = append(resp.Steps, toStepRunResponse(&runs[i]))
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
	out := make([]stepRunResponse, 0, len(runs))
	for i := range runs {
		out = append(out, toStepRunResponse(&runs[i]))
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
	var offset int64
	if v := r.URL.Query().Get("offset"); v != "" {
		offset, err = strconv.ParseInt(v, 10, 64)
		if err != nil || offset < 0 {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "offset must be a non-negative integer")
			return
		}
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
	offset = min(offset, size)
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		s.internalError(w, "seek transcript", err)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Next-Offset", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, f)
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
