package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// CodeWorktreeDirty is the `details.reason` of the 409 archiving returns when
// it would discard uncommitted work and force was not given (§13.2). It is
// the worktree package's own reason vocabulary, re-exported so the API's
// stable strings are all declared in one place.
const CodeWorktreeDirty = worktree.ReasonWorktreeDirty

// taskAction is one human action from spec §6, as the runner implements it.
type taskAction func(id int64) (*store.Task, error)

func (s *Server) handleTaskCancel(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, func(id int64) (*store.Task, error) {
		return s.deps.Runner.Cancel(r.Context(), id)
	})
}

func (s *Server) handleTaskPause(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, func(id int64) (*store.Task, error) {
		return s.deps.Runner.Pause(r.Context(), id)
	})
}

func (s *Server) handleTaskResume(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, func(id int64) (*store.Task, error) {
		return s.deps.Runner.Resume(r.Context(), id)
	})
}

func (s *Server) handleTaskSkip(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, func(id int64) (*store.Task, error) {
		return s.deps.Runner.Skip(r.Context(), id)
	})
}

func (s *Server) handleTaskApprove(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, func(id int64) (*store.Task, error) {
		return s.deps.Runner.Approve(r.Context(), id)
	})
}

func (s *Server) handleTaskReject(w http.ResponseWriter, r *http.Request) {
	s.runAction(w, r, func(id int64) (*store.Task, error) {
		return s.deps.Runner.Reject(r.Context(), id)
	})
}

// retryRequest is the §13.2 body of POST /v1/tasks/{id}/retry. Either field
// overrides the failed step in this task's snapshot only (§5.3).
type retryRequest struct {
	PromptOverride string `json:"prompt_override"`
	RunOverride    string `json:"run_override"`
	// BranchOverride renames the task's branch before the retry re-admits it.
	// It is what makes a `branch_exists` block recoverable at all (task 001):
	// nothing else in the API can change a branch name, so without it a blocked
	// task would be permanently dead and its transcripts orphaned.
	BranchOverride string `json:"branch_override"`
}

// retryResponse is the task as every other action renders it, plus how many
// blocked descendants the retry re-admitted (§13.2, task 090). The task
// fields stay at the top level — a retry response is still a task — and
// `retried_descendants` is always present, 0 for an ordinary blocked retry
// with nothing under it, so a client never has to tell "no cascade" from "an
// old daemon".
//
// A count and not the ids: §13.3's convention is that a client re-fetches
// what it decides it needs, and the ids under a wide fan-out are unbounded.
type retryResponse struct {
	taskResponse
	RetriedDescendants int `json:"retried_descendants"`
}

// handleTaskRetry re-admits a blocked task, or cascades from a parent parked
// in `awaiting_children` to the blocked lanes holding its join open (§6, task
// 090).
//
// Like repair it cannot go through runAction: that path's taskAction returns a
// task and nothing else, and the cascade's count is the second thing this one
// has to say.
func (s *Server) handleTaskRetry(w http.ResponseWriter, r *http.Request) {
	var req retryRequest
	if r.ContentLength != 0 && !decodeJSONLimit(w, r, &req, maxLargeRequestBytes) {
		return
	}
	for _, b := range []string{
		boundString("prompt_override", req.PromptOverride, maxPromptBytes),
		boundString("run_override", req.RunOverride, maxCommandBytes),
		boundString("branch_override", req.BranchOverride, maxNameBytes),
	} {
		if b != "" {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, b)
			return
		}
	}
	id, ok := taskIDFromPath(w, r)
	if !ok {
		return
	}
	if branch := strings.TrimSpace(req.BranchOverride); branch != "" {
		if !s.checkRetryBranchOverride(w, r, id) {
			return
		}
		if !s.renameBranchForRetry(w, r, branch) {
			return
		}
	}
	// The `on_input: require` gate again (§7.4, task 013), against the task's
	// own snapshot. Retry is the one action that re-admits a task, and the
	// repair for an `input_unsupported` block is environmental — install the
	// agent, or upgrade it back inside the verified family — so the verdict
	// is re-taken here rather than assumed from creation time. A retry that
	// would block again identically is refused with the reason instead.
	//
	// It applies to the addressed task and to nothing else. A cascade's lanes
	// deliberately skip it: a lane that would block `input_unsupported`
	// re-blocks on its own row, which is exactly what retrying it by hand
	// does today. A parked parent passes it as a matter of course — the gate
	// reads `on_input` off `agent` steps only, and the cursor is on a
	// `fan_out`.
	if !s.checkRetryInput(w, r) {
		return
	}
	// The runner is needed only from here on, which is where runAction
	// checked it when it was the caller. Moving the check up to the top of
	// the handler would turn every 400 above into a 500 on a server wired
	// without one.
	if s.deps.Runner == nil {
		s.internalError(w, "task actions", errors.New("no task runner is configured"))
		return
	}
	updated, descendants, err := s.deps.Runner.Retry(r.Context(), id,
		store.Override{Prompt: req.PromptOverride, Run: req.RunOverride})
	if err != nil {
		s.writeActionError(w, err)
		return
	}
	if req.PromptOverride != "" || req.RunOverride != "" {
		// edit+retry rewrote this task's snapshot (§6), which is the one
		// thing that can make a cached parse wrong.
		s.snaps.forget(id)
	}
	writeJSON(w, http.StatusOK, retryResponse{
		taskResponse:       toTaskResponse(updated, s.snaps.get(updated.ID, updated.WorkflowSnapshot)),
		RetriedDescendants: descendants,
	})
}

// checkRetryBranchOverride refuses `branch_override` on a parent parked in
// `awaiting_children` (task 090). Every live lane of that parent holds its
// branch as its own `base_branch`, so renaming it would re-base a fan-out
// already in flight onto a name that no longer exists — and unlike a
// `branch_exists` block there is nothing here the rename would fix.
//
// It runs ahead of renameBranchForRetry because that rename is committed
// before the action is, so a refusal afterwards would leave the branch moved.
func (s *Server) checkRetryBranchOverride(w http.ResponseWriter, r *http.Request, id int64) bool {
	task, err := s.deps.Store.GetTask(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return false
	}
	if err != nil {
		s.internalError(w, "get task", err)
		return false
	}
	if task.State == store.TaskAwaitingChildren {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, fmt.Sprintf(
			"task %d is parked on a fan_out step; branch_override would rename the branch "+
				"every live lane holds as its base_branch", id))
		return false
	}
	return true
}

// repairRequest is the §13.2 body of POST /v1/tasks/{id}/repair (§6, task
// 025). The prompt is required and literal — it is prose typed at a form, not
// a `text/template` source — and the optional triple stands in for the step
// level of §8.6's resolution chain for this one run.
type repairRequest struct {
	Prompt string  `json:"prompt"`
	Agent  *string `json:"agent"`
	Model  *string `json:"model"`
	Effort *string `json:"effort"`
}

// handleTaskRepair runs one ad-hoc repair agent in a blocked task's existing
// worktree (§13.2, task 025). Like archive it cannot go through runAction:
// the response carries the §8.2 catalog warnings a repair's own agent
// selection can raise, which taskAction has no room for.
func (s *Server) handleTaskRepair(w http.ResponseWriter, r *http.Request) {
	var req repairRequest
	if !decodeJSONLimit(w, r, &req, maxLargeRequestBytes) {
		return
	}
	if strings.TrimSpace(req.Prompt) == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"prompt is required: a repair agent needs something to be told")
		return
	}
	if b := boundString("prompt", req.Prompt, maxPromptBytes); b != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, b)
		return
	}
	if s.deps.Runner == nil {
		s.internalError(w, "task actions", errors.New("no task runner is configured"))
		return
	}
	id, ok := taskIDFromPath(w, r)
	if !ok {
		return
	}
	task, err := s.deps.Store.GetTask(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return
	}
	if err != nil {
		s.internalError(w, "get task", err)
		return
	}
	sel := store.RepairRequest{
		Prompt: req.Prompt,
		Agent:  strings.TrimSpace(ptrValue(req.Agent)),
		Model:  strings.TrimSpace(ptrValue(req.Model)),
		Effort: strings.TrimSpace(ptrValue(req.Effort)),
	}
	// The same §8.2 gate task creation applies, over the one step this run
	// will have: an unregistered agent and a known-invalid model or effort
	// are 400s, and a value no catalog knows rides back as a warning.
	warnings, cerr := s.checkRepairSelection(task, sel)
	if cerr != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, cerr)
		return
	}
	updated, err := s.deps.Runner.Repair(r.Context(), id, sel)
	if err != nil {
		s.writeActionError(w, err)
		return
	}
	resp := toTaskResponse(updated, s.snaps.get(updated.ID, updated.WorkflowSnapshot))
	resp.Warnings = warnings
	writeJSON(w, http.StatusOK, resp)
}

// checkRepairSelection validates a repair's agent/model/effort the way task
// creation validates a task's (§8.2, §8.6). The repair's own triple stands in
// for the step level, so the resolved selection is
// request > task override > the snapshot's `defaults:` > adapter default.
func (s *Server) checkRepairSelection(
	t *store.Task, req store.RepairRequest,
) (warnings []string, errMsg string) {
	if req.Agent != "" && s.deps.Agents != nil {
		if _, ok := s.deps.Agents.Get(req.Agent); !ok {
			return nil, fmt.Sprintf("unknown agent %q (available: %s)", req.Agent,
				strings.Join(s.deps.Agents.Names(), ", "))
		}
	}
	if s.deps.Catalog == nil {
		return nil, ""
	}
	var defaults agent.Level
	// A snapshot that no longer parses costs the `defaults:` level and
	// nothing else: a repair is a rescue, and refusing to launch one because
	// the snapshot rotted would take the rescue away exactly when it is
	// needed.
	if wf, _, perr := workflow.Parse([]byte(t.WorkflowSnapshot), workflow.Options{}); perr == nil {
		defaults = agent.Level{Agent: wf.Defaults.Agent, Model: wf.Defaults.Model, Effort: wf.Defaults.Effort}
	}
	sel := agent.Resolve(
		agent.Level{Agent: req.Agent, Model: req.Model, Effort: req.Effort},
		agent.Level{Agent: t.AgentOverride, Model: t.ModelOverride, Effort: t.EffortOverride},
		defaults,
	)
	cerrs, cwarns := s.deps.Catalog.Catalogs().Check(sel)
	if len(cerrs) > 0 {
		return nil, "repair: " + cerrs[0].Message
	}
	for _, f := range cwarns {
		warnings = append(warnings, "repair: "+f.Message)
	}
	return warnings, ""
}

// followUpRequest is the §13.2 body of POST /v1/tasks/{id}/follow_up (§6,
// task 027). Exactly one of the three run forms is required; the optional
// triple stands in for the step level of §8.6's chain, exactly as a repair's
// does.
//
// `prompt` and `run` are literal text — prose and a command line typed at a
// form, not `text/template` sources — and the daemon escapes them when it
// compiles the one-step workflow it runs. An operator who wants a template
// writes a workflow and names it in `workflow`.
type followUpRequest struct {
	Prompt   *string `json:"prompt"`
	Run      *string `json:"run"`
	Workflow *string `json:"workflow"`
	Agent    *string `json:"agent"`
	Model    *string `json:"model"`
	Effort   *string `json:"effort"`
}

// handleTaskFollowUp runs one more piece of work in a finished task's
// existing worktree and branch, before it is archived (§13.2, task 027).
//
// Like repair and archive it cannot go through runAction: the response
// carries the §8.2 catalog warnings the run's own agent selection can raise,
// which taskAction has no room for. Everything the handler rejects is
// something the person asking can act on — an empty form, an unknown
// workflow, a follow-up workflow that does not validate here, a model no
// catalog admits — and every one of them is a 400 rather than a task that
// blocks six seconds later.
func (s *Server) handleTaskFollowUp(w http.ResponseWriter, r *http.Request) {
	var req followUpRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if s.deps.Runner == nil {
		s.internalError(w, "task actions", errors.New("no task runner is configured"))
		return
	}
	id, ok := taskIDFromPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	task, err := s.deps.Store.GetTask(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return
	}
	if err != nil {
		s.internalError(w, "get task", err)
		return
	}
	sel, msg := followUpForm(req)
	if msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return
	}
	if sel.Agent != "" && s.deps.Agents != nil {
		if _, known := s.deps.Agents.Get(sel.Agent); !known {
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("unknown agent %q (available: %s)", sel.Agent,
					strings.Join(s.deps.Agents.Names(), ", ")))
			return
		}
	}
	wf, msg := s.compileFollowUp(ctx, task, &sel)
	if msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return
	}
	// §8.2 over the compiled document, step by step, exactly as task creation
	// checks a snapshot: an unregistered agent and a known-invalid model or
	// effort are 400s, and a value no catalog knows rides back as a warning.
	warnings, cerr := s.checkTaskCatalog(wf, task)
	if cerr != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "follow-up: "+cerr)
		return
	}
	// The `on_input: require` gate (§7.4, task 013) over the same document. A
	// follow-up workflow may declare it, and a run that could only block on
	// `input_unsupported` is refused in front of the person asking.
	if mismatch := s.inputMismatch(ctx, wf, task); mismatch != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "follow-up: "+mismatch)
		return
	}
	updated, err := s.deps.Runner.FollowUp(ctx, id, sel)
	if err != nil {
		s.writeActionError(w, err)
		return
	}
	resp := toTaskResponse(updated, s.snaps.get(updated.ID, updated.WorkflowSnapshot))
	resp.Warnings = prefixEach(warnings, "follow-up: ")
	writeJSON(w, http.StatusOK, resp)
}

// followUpForm reads which of the three §6 run forms was asked for. Exactly
// one is required: a request with none says nothing to run, and one with two
// says two different things with no rule for which wins.
func followUpForm(req followUpRequest) (store.FollowUpRequest, string) {
	var (
		out    store.FollowUpRequest
		chosen []string
	)
	if v := strings.TrimSpace(ptrValue(req.Prompt)); v != "" {
		out.Form, out.Prompt = store.FollowUpAgent, v
		chosen = append(chosen, "prompt")
	}
	if v := strings.TrimSpace(ptrValue(req.Run)); v != "" {
		out.Form, out.Run = store.FollowUpCommand, v
		chosen = append(chosen, "run")
	}
	if v := strings.TrimSpace(ptrValue(req.Workflow)); v != "" {
		out.Form, out.WorkflowName = store.FollowUpWorkflow, v
		chosen = append(chosen, "workflow")
	}
	switch len(chosen) {
	case 1:
		out.Agent = strings.TrimSpace(ptrValue(req.Agent))
		out.Model = strings.TrimSpace(ptrValue(req.Model))
		out.Effort = strings.TrimSpace(ptrValue(req.Effort))
		return out, ""
	case 0:
		return out, "a follow-up needs something to run: one of prompt, run or workflow"
	default:
		return out, fmt.Sprintf(
			"a follow-up runs one thing: %s were all given", strings.Join(chosen, ", "))
	}
}

// compileFollowUp turns a validated request into the document the engine will
// run, and writes it into sel.Workflow.
//
// The workflow form goes through exactly what task creation puts a snapshot
// through — include expansion (§7.9), fan-out tree resolution (§7.6) and a
// re-parse against the daemon's real options — for the same reason: §5.3 says
// execution uses a captured document precisely so that later edits to a
// workflow file cannot mutate a run in flight, and a follow-up is a run.
//
// The one difference is the fan-out depth budget. A follow-up on a task that
// is already a lane three levels down has three of `fan_out.max_depth`
// already spent, so the budget is re-derived from the task's own depth rather
// than taken whole (§7.6).
func (s *Server) compileFollowUp(
	ctx context.Context, task *store.Task, sel *store.FollowUpRequest,
) (*workflow.Workflow, string) {
	var (
		wf  *workflow.Workflow
		err error
	)
	if sel.Form == store.FollowUpWorkflow {
		entry, found := s.deps.Workflows.Lookup(task.ProjectID, sel.WorkflowName)
		if !found {
			return nil, fmt.Sprintf("workflow %q not found for project %d",
				sel.WorkflowName, task.ProjectID)
		}
		if !entry.Valid() {
			return nil, fmt.Sprintf("workflow %q is invalid: %s",
				sel.WorkflowName, entry.Errors.Error())
		}
		if mismatch := entry.Workflow.PlatformMismatch(workflow.HostPlatform()); mismatch != "" {
			return nil, fmt.Sprintf("workflow %q cannot run here: %s", sel.WorkflowName, mismatch)
		}
		wf = entry.Workflow
	} else {
		wf, err = taskrun.CompileFollowUp(*sel)
		if err != nil {
			return nil, err.Error()
		}
	}
	if workflow.HasInclude(wf) {
		expanded, xerr := workflow.Expand(wf, workflow.ExpandOptions{
			Lookup:   s.laneLookup(task.ProjectID),
			Limits:   workflow.IncludeLimits{MaxDepth: s.deps.Config().Include.MaxDepth},
			Override: agent.Level{Agent: sel.Agent, Model: sel.Model, Effort: sel.Effort},
		})
		if xerr != nil {
			return nil, xerr.Error()
		}
		wf = expanded
	}
	if workflow.HasFanOut(wf) {
		cfg := s.deps.Config()
		depth, derr := s.taskDepth(ctx, task.ID)
		if derr != nil {
			return nil, derr.Error()
		}
		resolved, _, terr := workflow.ResolveTree(wf, s.laneLookup(task.ProjectID),
			workflow.Limits{MaxDepth: cfg.FanOut.MaxDepth - depth, MaxTasks: cfg.FanOut.MaxTasks})
		if terr != nil {
			return nil, terr.Error()
		}
		wf = resolved
	}
	// The request stands in for the step level of §8.6's chain (decision 12),
	// so it is written into the steps that declare nothing of their own —
	// before the re-parse, so what validates is what runs.
	taskrun.ApplyFollowUpSelection(wf.Steps, *sel)
	out, merr := workflow.Marshal(wf)
	if merr != nil {
		return nil, merr.Error()
	}
	// Re-validate the finished document against the daemon's real options,
	// not just its shape: the nesting rules an include can break are decidable
	// only once the steps are in place, and the §8.1.1 platform list and
	// §8.1 id rules are checked here rather than at run time.
	revalidated, _, verr := workflow.Parse(out, s.deps.Workflows.Options())
	if verr != nil {
		return nil, fmt.Sprintf("the follow-up does not validate: %s", verr.Error())
	}
	sel.Workflow = string(out)
	return revalidated, ""
}

// taskDepth is how many fan-out levels sit above a task: 0 for a root, 1 for
// a lane of a root's fan_out step, and so on.
func (s *Server) taskDepth(ctx context.Context, id int64) (int, error) {
	ancestors, err := s.deps.Store.FanOutAncestors(ctx, id)
	if err != nil {
		return 0, err
	}
	return len(ancestors), nil
}

// prefixEach tags a list of warnings with where they came from, the way
// checkRepairSelection tags its own.
func prefixEach(in []string, prefix string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	for _, v := range in {
		out = append(out, prefix+v)
	}
	return out
}

// archiveRequest is the §13.2 body of POST /v1/tasks/{id}/archive. `force`
// is also accepted as a query parameter, matching DELETE /v1/projects/{id}.
type archiveRequest struct {
	Force bool `json:"force"`
}

// archiveResponse is the task as every other action renders it, plus what
// archive did to the branch (§13.2, task 008). The task fields stay at the top
// level — an archive response is still a task — and `branch` is omitted
// entirely when the branch step did not run, so a client that predates the
// field sees exactly what it saw before.
//
// It is reported here rather than as an event: `archived` is terminal, a
// `block_reason` would be a lie on it, and this is the one path with a human
// on the other end of the request.
type archiveResponse struct {
	taskResponse
	Branch *branchResponse `json:"branch,omitempty"`
}

// branchResponse is one branch outcome. `result` is worktree's own snake_case
// vocabulary: deleted | has_commits | unknown | error.
type branchResponse struct {
	Name   string                `json:"name"`
	Result string                `json:"result"`
	Error  string                `json:"error,omitempty"`
	Remote *remoteBranchResponse `json:"remote,omitempty"`
}

// remoteBranchResponse is the opt-in remote leg: deleted | no_upstream | error.
type remoteBranchResponse struct {
	Remote string `json:"remote,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Result string `json:"result"`
	Error  string `json:"error,omitempty"`
}

// handleTaskArchive cannot go through runAction: that path's taskAction returns
// a task and nothing else, and archive is the one action with a second thing to
// say. It reuses writeActionError so the §13.1 mapping stays in one place.
func (s *Server) handleTaskArchive(w http.ResponseWriter, r *http.Request) {
	var req archiveRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	force := req.Force || hasForce(r)
	if s.deps.Runner == nil {
		s.internalError(w, "task actions", errors.New("no task runner is configured"))
		return
	}
	id, ok := taskIDFromPath(w, r)
	if !ok {
		return
	}
	task, branch, err := s.deps.Runner.Archive(r.Context(), id, force)
	if err != nil {
		s.writeActionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, archiveResponse{
		taskResponse: toTaskResponse(task, s.snaps.get(task.ID, task.WorkflowSnapshot)),
		Branch:       toBranchResponse(branch),
	})
}

// toBranchResponse renders a branch outcome, or nil when the step never ran.
func toBranchResponse(out worktree.BranchOutcome) *branchResponse {
	if !out.Checked() {
		return nil
	}
	b := &branchResponse{Name: out.Branch, Result: out.Result, Error: out.Error}
	if out.Remote != nil {
		b.Remote = &remoteBranchResponse{
			Remote: out.Remote.Remote, Ref: out.Remote.Ref,
			Result: out.Remote.Result, Error: out.Remote.Error,
		}
	}
	return b
}

// answerRequest is the §13.2 body of POST /v1/tasks/{id}/answer (§7.4).
// Answer values accept a bare string or an array of strings, so
// single-select answers need no array ceremony.
type answerRequest struct {
	Answers map[string]answerValues `json:"answers"`
	Allow   *bool                   `json:"allow"`
}

// answerValues decodes a string or an array of strings.
type answerValues []string

func (v *answerValues) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*v = []string{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("answer values must be a string or an array of strings")
	}
	*v = many
	return nil
}

// boundAnswers bounds an answer map: §13.1's entry count on the map itself,
// on the values of any one answer, and on each question and answer string. The
// values are templated into a resumed step's prompt, so they are bounded like
// any other prompt input. The key is the agent's question text rather than a
// caller-chosen identifier (§7.4), so it takes maxAnswersKeyBytes and not the
// `fields` key bound — see the note on that constant.
func boundAnswers(answers map[string]answerValues) string {
	if msg := boundCount("answers", len(answers), maxFieldCount); msg != "" {
		return msg
	}
	for text, vals := range answers {
		if msg := boundString("answers key", text, maxAnswersKeyBytes); msg != "" {
			return msg
		}
		field := fmt.Sprintf("answers[%s]", truncKey(text))
		if msg := boundCount(field, len(vals), maxValueCount); msg != "" {
			return msg
		}
		for _, v := range vals {
			if msg := boundString(field, v, maxFieldValueBytes); msg != "" {
				return msg
			}
		}
	}
	return ""
}

func (s *Server) handleTaskAnswer(w http.ResponseWriter, r *http.Request) {
	var req answerRequest
	if !decodeJSONLimit(w, r, &req, maxLargeRequestBytes) {
		return
	}
	if b := boundAnswers(req.Answers); b != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, b)
		return
	}
	in := taskrun.AnswerInput{Allow: req.Allow}
	if req.Answers != nil {
		in.Answers = make(map[string][]string, len(req.Answers))
		for text, vals := range req.Answers {
			in.Answers[text] = vals
		}
	}
	s.runAction(w, r, func(id int64) (*store.Task, error) {
		return s.deps.Runner.Answer(r.Context(), id, in)
	})
}

// patchTaskRequest is the §13.2 body of PATCH /v1/tasks/{id}. Priority is
// the only mutable field in v1.
type patchTaskRequest struct {
	Priority *int `json:"priority"`
}

func (s *Server) handleTaskPatch(w http.ResponseWriter, r *http.Request) {
	var req patchTaskRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Priority == nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"priority is the only mutable task field")
		return
	}
	s.runAction(w, r, func(id int64) (*store.Task, error) {
		return s.deps.Runner.SetPriority(r.Context(), id, *req.Priority)
	})
}

// runAction resolves {id}, applies the action, and renders the task or the
// §13.1 error the action produced.
func (s *Server) runAction(w http.ResponseWriter, r *http.Request, action taskAction) {
	if s.deps.Runner == nil {
		s.internalError(w, "task actions", errors.New("no task runner is configured"))
		return
	}
	id, ok := taskIDFromPath(w, r)
	if !ok {
		return
	}
	task, err := action(id)
	if err != nil {
		s.writeActionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTaskResponse(task, s.snaps.get(task.ID, task.WorkflowSnapshot)))
}

// writeActionError maps the errors an action can produce onto §13.1 status
// codes. A 409 always reports what was actually found, so a client can
// re-issue against the state it did not expect.
func (s *Server) writeActionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())

	case isInvalidAction(err):
		e, _ := taskrun.AsInvalidAction(err)
		writeConflict(w, e.Error(),
			map[string]string{"state": string(e.State)})

	case isStateConflict(err):
		e, _ := store.AsStateConflict(err)
		writeConflict(w, e.Error(),
			map[string]string{"state": string(e.Got)})

	case isOverrideMismatch(err):
		e, _ := taskrun.AsOverrideMismatch(err)
		writeError(w, http.StatusBadRequest, CodeValidationFailed, e.Error())

	// A repair with nothing to say (task 025). The handler catches an empty
	// prompt in front of the runner; this covers the one that arrives as
	// whitespace the runner trims away.
	case isRepairPrompt(err):
		e, _ := taskrun.AsRepairPrompt(err)
		writeError(w, http.StatusBadRequest, CodeValidationFailed, e.Error())

	// A follow-up that describes nothing to run, or an `edit + retry` aimed
	// at a follow-up step, which is not part of the snapshot there is
	// anything to edit in (task 027).
	case isFollowUpRequest(err):
		e, _ := taskrun.AsFollowUpRequest(err)
		writeError(w, http.StatusBadRequest, CodeValidationFailed, e.Error())

	case isFollowUpOverride(err):
		e, _ := taskrun.AsFollowUpOverride(err)
		writeError(w, http.StatusBadRequest, CodeValidationFailed, e.Error())

	// An `edit + retry` aimed at a parent parked in `awaiting_children`. Its
	// cursor is on a `fan_out` step, which has neither a prompt nor a command
	// for an override to rewrite — the edit belongs on the blocked lane (task
	// 090).
	case isParkedOverride(err):
		e, _ := taskrun.AsParkedOverride(err)
		writeError(w, http.StatusBadRequest, CodeValidationFailed, e.Error())

	// A structurally mismatched answer is untranslatable to the live agent
	// session; the request never reaches the task (§7.4, §13.2).
	case isAnswerValidation(err):
		e, _ := taskrun.AsAnswerValidation(err)
		writeError(w, http.StatusBadRequest, CodeValidationFailed, e.Error())

	// Archive removes the worktree before transitioning, so a refusal here
	// means the task is untouched — `worktree_dirty` is the one a client
	// resolves by re-sending with force (§13.2).
	case worktree.ReasonOf(err) != "":
		writeConflict(w, err.Error(),
			map[string]string{"reason": worktree.ReasonOf(err)})

	default:
		s.internalError(w, "task action", err)
	}
}

func isInvalidAction(err error) bool {
	_, ok := taskrun.AsInvalidAction(err)
	return ok
}

func isStateConflict(err error) bool {
	_, ok := store.AsStateConflict(err)
	return ok
}

func isOverrideMismatch(err error) bool {
	_, ok := taskrun.AsOverrideMismatch(err)
	return ok
}

func isFollowUpRequest(err error) bool {
	_, ok := taskrun.AsFollowUpRequest(err)
	return ok
}

func isFollowUpOverride(err error) bool {
	_, ok := taskrun.AsFollowUpOverride(err)
	return ok
}

func isParkedOverride(err error) bool {
	_, ok := taskrun.AsParkedOverride(err)
	return ok
}

func isRepairPrompt(err error) bool {
	_, ok := taskrun.AsRepairPrompt(err)
	return ok
}

func isAnswerValidation(err error) bool {
	_, ok := taskrun.AsAnswerValidation(err)
	return ok
}
