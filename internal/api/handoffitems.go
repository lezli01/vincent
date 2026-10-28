package api

// The handoff item verbs (spec §5.7, §13.2, task 137.6): deliver, return,
// wait and release, POST /v1/handoffs/{id}/items/{issue_id}/{verb}. Each
// resolves one item, a comma-separated list of them, or — `all` — every
// unresolved one, in one store transaction with every side effect it asks
// for (task 137 decision 4, amended 2026-10-28): a refused side effect
// records nothing.

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/lezli01/vincent/internal/issues"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
)

// reasonHandoffItemResolved is the 409 for a verb on an item that is
// already delivered, returned or released with another outcome — or not
// waiting, on a handoff that is no longer live. details.outcome is the
// item's outcome. A repeat of the item's own outcome is a no-op instead.
const reasonHandoffItemResolved = "handoff_item_resolved"

// handoffCloseNone is deliver's `close: none`: the issue is left open.
const handoffCloseNone = "none"

// handoffItemRequest is a verb's body. Each verb takes the fields it names
// and refuses the others with a 400, so a flag meant for one verb is never
// silently dropped by another.
type handoffItemRequest struct {
	Note    string `json:"note"`
	Comment string `json:"comment"`
	// Close is deliver's: completed, not_planned, duplicate or none. Empty
	// is the delivery's suggested close_reason, else completed.
	Close string `json:"close"`
	// FollowUp, Paused, Side and Workflow are return's.
	FollowUp string `json:"follow_up"`
	Paused   bool   `json:"paused"`
	Side     bool   `json:"side"`
	Workflow string `json:"workflow"`
}

// validate applies the verb's own rules and returns the 400's message or "".
func (req *handoffItemRequest) validate(outcome store.HandoffOutcome) string {
	for _, b := range []string{
		boundString("note", req.Note, maxDescriptionBytes),
		boundString("comment", req.Comment, maxDescriptionBytes),
		boundString("follow_up", req.FollowUp, maxDescriptionBytes),
		boundString("workflow", req.Workflow, maxNameBytes),
	} {
		if b != "" {
			return b
		}
	}
	req.FollowUp, req.Workflow = strings.TrimSpace(req.FollowUp), strings.TrimSpace(req.Workflow)
	if outcome != store.HandoffDelivered && req.Close != "" {
		return "close is deliver's alone"
	}
	if req.Close != "" && req.Close != handoffCloseNone {
		if _, err := issuestate.ResolveReason(issuestate.Close, issuestate.Reason(req.Close)); err != nil {
			return fmt.Sprintf("close must be one of completed, not_planned, duplicate or none; got %q", req.Close)
		}
	}
	if outcome != store.HandoffReturned && (req.FollowUp != "" || req.Paused || req.Side || req.Workflow != "") {
		return "follow_up, paused, side and workflow are return's alone"
	}
	if req.FollowUp == "" && (req.Paused || req.Side) {
		return "paused and side need follow_up"
	}
	if req.Workflow != "" && !req.Side {
		return "workflow needs side"
	}
	return ""
}

// handleHandoffItemVerb returns the handler of one verb.
func (s *Server) handleHandoffItemVerb(outcome store.HandoffOutcome) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.handoffItemVerb(w, r, outcome)
	}
}

func (s *Server) handoffItemVerb(w http.ResponseWriter, r *http.Request, outcome store.HandoffOutcome) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "handoff ids are integers")
		return
	}
	v := store.HandoffVerb{HandoffID: id, Outcome: outcome}
	if sel := strings.TrimSpace(r.PathValue("issue_id")); sel == "all" {
		v.All = true
	} else {
		for part := range strings.SplitSeq(sel, ",") {
			n, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64)
			if err != nil || n <= 0 {
				writeError(w, http.StatusBadRequest, CodeValidationFailed,
					"issue_id is an issue id, a comma-separated list of them, or all")
				return
			}
			v.IssueIDs = append(v.IssueIDs, n)
		}
	}
	var req handoffItemRequest
	if r.ContentLength != 0 && !decodeJSONLimit(w, r, &req, maxLargeRequestBytes) {
		return
	}
	if msg := req.validate(outcome); msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return
	}
	ctx := r.Context()
	h, err := s.deps.Store.GetHandoff(ctx, id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("handoff %d not found", id))
		return
	}
	if err != nil {
		s.internalError(w, "get handoff", err)
		return
	}
	v.IssueActor = issueActor(r)
	v.By = store.HandoffOutcomeBy(v.IssueActor)
	if acting := actingTaskID(r); acting != nil && *acting == h.TaskID {
		v.By = store.HandoffByHandoff
	}
	v.Note = strings.TrimSpace(req.Note)
	if v.Comment = strings.TrimSpace(req.Comment); v.Comment != "" {
		v.CommentAuthor, _ = issueAuthor(ctx)
	}
	if outcome == store.HandoffDelivered && req.Close != handoffCloseNone {
		v.Close, v.CloseReason = true, issuestate.Reason(req.Close)
	}
	if !s.grantHandoffItemWrites(ctx, w, r, &v) {
		return
	}
	v.Check = handoffItemCheck(v)
	if req.FollowUp != "" && !s.prepareHandoffReturn(ctx, w, r, &v, &req) {
		return
	}
	res, err := s.deps.Store.ResolveHandoffItems(ctx, v)
	if err != nil {
		s.writeHandoffItemError(w, err)
		return
	}
	if (len(v.FollowUps) > 0 || len(v.SideTasks) > 0) && s.deps.WakeRunner != nil {
		s.deps.WakeRunner()
	}
	items, state, err := s.deps.Store.ListHandoffItems(ctx, id)
	if err != nil {
		s.internalError(w, "list handoff items", err)
		return
	}
	res.Handoff.State = state
	writeJSON(w, http.StatusOK, renderHandoff(res.Handoff, items))
}

// grantHandoffItemWrites asks the forge-write guard (task 137.7) about an
// agent's comment and close on each item the verb will move, as `vincent
// issue comment` and `issue close` ask it: a write that reaches the forge —
// a comment on a live-remote thread, a close of an open issue that writes
// back — needs a grant naming store.CapIssueComment or store.CapIssueClose,
// and a granted one is made as issuestate.Handoff. It reads before the
// transaction, which cannot call the guard; handoffItemCheck asks again
// inside it, so an import landing between the two is refused, never let
// through. A refusal is written to w and reported as false.
func (s *Server) grantHandoffItemWrites(ctx context.Context, w http.ResponseWriter, r *http.Request, v *store.HandoffVerb) bool {
	if v.IssueActor != issuestate.Agent || (v.Comment == "" && !v.Close) {
		return true
	}
	items, err := s.deps.Store.HandoffVerbItems(ctx, *v)
	if err != nil {
		s.writeHandoffItemError(w, err)
		return false
	}
	svc := issues.New(s.deps.Store)
	for _, it := range items {
		if it.Outcome == v.Outcome {
			continue
		}
		iss, err := s.deps.Store.GetIssue(ctx, it.IssueID)
		if err != nil {
			s.writeHandoffItemError(w, err)
			return false
		}
		var caps []store.Capability
		if v.Comment != "" && issues.ThreadMirrored(iss) {
			caps = append(caps, store.CapIssueComment)
		}
		if v.Close && issues.WritesBack(iss) && issuestate.Normalize(iss.State) != issuestate.Closed {
			caps = append(caps, store.CapIssueClose)
		}
		for _, c := range caps {
			verdict, err := svc.ForgeWrite(ctx, forgeCaller(r), c, issues.Target{IssueID: iss.ID})
			if err != nil {
				s.writeHandoffItemError(w, err)
				return false
			}
			if v.CommentCloseActors == nil {
				v.CommentCloseActors = map[int64]issuestate.Actor{}
			}
			v.CommentCloseActors[iss.ID] = verdict.By
		}
	}
	return true
}

// handoffItemCheck is the verb's per-issue refusal, asked inside the
// transaction before anything is written: the forge-write rule of a step's
// `issue close` and `issue comment` holds for the same writes made through a
// verb (task 137 decision 1). An agent's write that reaches the forge passes
// only where grantHandoffItemWrites found a grant; a human's comment on a
// live-remote thread stays refused, as on the issue route.
func handoffItemCheck(v store.HandoffVerb) func(*store.Issue) error {
	return func(iss *store.Issue) error {
		by := v.IssueActor
		if a, ok := v.CommentCloseActors[iss.ID]; ok {
			by = a
		}
		if v.Comment != "" && issues.ThreadMirrored(iss) &&
			(by != issuestate.Handoff || !issues.WritesBack(iss)) {
			if by == issuestate.Agent {
				return fmt.Errorf("issue %d: %w", iss.ID, issues.ErrForgeWriteNeedsHuman)
			}
			return fmt.Errorf("issue %d: %w", iss.ID, issues.ErrMirrored)
		}
		if v.Close && by == issuestate.Agent && issues.WritesBack(iss) &&
			issuestate.Normalize(iss.State) != issuestate.Closed {
			return fmt.Errorf("issue %d: %w", iss.ID, issues.ErrForgeWriteNeedsHuman)
		}
		return nil
	}
}

// prepareHandoffReturn prepares a return's follow-up — or, with side, its
// side task — for every item the verb will move (task 137.6 decisions 2
// and 3), through the checks POST /v1/tasks/{id}/follow_up and POST
// /v1/tasks apply. A refusal is written to w and reported as false.
func (s *Server) prepareHandoffReturn(
	ctx context.Context, w http.ResponseWriter, r *http.Request, v *store.HandoffVerb, req *handoffItemRequest,
) bool {
	if s.deps.Runner == nil {
		s.internalError(w, "handoff return", errors.New("no task runner is configured"))
		return false
	}
	items, err := s.deps.Store.HandoffVerbItems(ctx, *v)
	if err != nil {
		s.writeHandoffItemError(w, err)
		return false
	}
	for _, it := range items {
		if it.Outcome == store.HandoffReturned {
			continue
		}
		latest, err := s.deps.Store.LatestIssueMainTask(ctx, it.IssueID)
		if errors.Is(err, store.ErrNotFound) {
			writeConflict(w, fmt.Sprintf("issue %d has no main task to follow up", it.IssueID), nil)
			return false
		}
		if err != nil {
			s.internalError(w, "latest issue main task", err)
			return false
		}
		if req.Side {
			side, ok := s.prepareHandoffSideTask(ctx, w, r, latest, req)
			if !ok {
				return false
			}
			if v.SideTasks == nil {
				v.SideTasks = map[int64]*store.HandoffSideTask{}
			}
			v.SideTasks[it.IssueID] = side
			continue
		}
		fu, ok := s.prepareHandoffFollowUp(ctx, w, r, latest, req)
		if !ok {
			return false
		}
		if v.FollowUps == nil {
			v.FollowUps = map[int64]*store.HandoffFollowUp{}
		}
		v.FollowUps[it.IssueID] = fu
	}
	return true
}

// prepareHandoffFollowUp is handleTaskFollowUp short of the swap, for the
// prompt form: the same refusals, in the same order.
func (s *Server) prepareHandoffFollowUp(
	ctx context.Context, w http.ResponseWriter, r *http.Request, task *store.Task, req *handoffItemRequest,
) (*store.HandoffFollowUp, bool) {
	if s.refuseIssueWorktreeMoved(ctx, w, task) {
		return nil, false
	}
	prompt := req.FollowUp
	sel, msg := followUpForm(followUpRequest{Prompt: &prompt})
	if msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return nil, false
	}
	wf, msg := s.compileFollowUp(ctx, task, &sel)
	if msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return nil, false
	}
	if _, cerr := s.checkTaskCatalog(wf, task); cerr != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "follow-up: "+cerr)
		return nil, false
	}
	if mismatch := s.inputMismatch(ctx, wf, task); mismatch != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "follow-up: "+mismatch)
		return nil, false
	}
	sel.ActingTaskID = actingTaskID(r)
	from, to, ch, err := s.deps.Runner.PrepareFollowUp(ctx, task.ID, sel, req.Paused)
	if err != nil {
		s.writeActionError(w, err)
		return nil, false
	}
	return &store.HandoffFollowUp{TaskID: task.ID, From: from, To: to, Change: ch}, true
}

// prepareHandoffSideTask is a return's `--side` task (decision 3): a root
// task on the issue with merge_back, running the workflow the latest main
// task ran — its source side task's, when that is a merge-back — or the
// one named, with the prompt as its description. It is prepared as POST
// /v1/tasks prepares one.
func (s *Server) prepareHandoffSideTask(
	ctx context.Context, w http.ResponseWriter, r *http.Request, latest *store.Task, req *handoffItemRequest,
) (*store.HandoffSideTask, bool) {
	workflowName := req.Workflow
	if workflowName == "" {
		workflowName = latest.WorkflowName
		if latest.MergeSourceTaskID != nil {
			source, err := s.deps.Store.GetTask(ctx, *latest.MergeSourceTaskID)
			if err != nil {
				s.internalError(w, "get merge-back source task", err)
				return nil, false
			}
			workflowName = source.WorkflowName
		}
	}
	prompt, paused := req.FollowUp, req.Paused
	create := taskCreateRequest{
		ProjectID:   latest.ProjectID,
		Workflow:    &workflowName,
		Description: &prompt,
		IssueID:     latest.IssueID,
		MergeBack:   &mergeBackBody{},
		Paused:      &paused,
	}
	if b := boundTaskCreate(&create); b != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, b)
		return nil, false
	}
	prep, ok := s.prepareTaskCreate(ctx, w, &create, "", false)
	if !ok || !refuseHandoffRole(w, prep.workflow) {
		return nil, false
	}
	t := prep.task
	resolveBranch, ok := s.newTaskBranch(ctx, w, prep, &t, "", false)
	if !ok {
		return nil, false
	}
	t.ActingTaskID = actingTaskID(r)
	return &store.HandoffSideTask{Task: &t, ResolveBranch: resolveBranch}, true
}

// writeHandoffItemError maps a verb's store and issue errors onto the
// envelope.
func (s *Server) writeHandoffItemError(w http.ResponseWriter, err error) {
	var (
		resolved *store.HandoffItemResolvedError
		claimed  *store.BranchClaimedError
	)
	switch {
	case errors.As(err, &resolved):
		writeConflict(w, resolved.Error(), map[string]string{
			"reason": reasonHandoffItemResolved, "outcome": string(resolved.Outcome),
			"issue_id": strconv.FormatInt(resolved.IssueID, 10),
		})
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
	case errors.Is(err, issues.ErrMirrored):
		writeConflict(w, err.Error(), map[string]string{"reason": issueReasonMirrored})
	case errors.Is(err, issues.ErrForgeWriteNeedsHuman):
		writeConflict(w, err.Error(), map[string]string{"reason": issueReasonForgeWrite})
	case errors.Is(err, store.ErrInvalidIssueAction):
		writeConflict(w, err.Error(), nil)
	case writeIssueInHandoff(w, err):
	case writeIssueWorktreeError(w, err):
	case errors.As(err, &claimed):
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("branch %q is already claimed by task %d", claimed.Branch, claimed.TaskID))
	case isTaskLocked(err), isInvalidAction(err), isStateConflict(err):
		s.writeActionError(w, err)
	default:
		s.internalError(w, "resolve handoff items", err)
	}
}
