package api

// Handoffs (spec §5.7, §13.2, task 137 decision 8, item 137.5):
// POST /v1/handoffs starts one — the handoff row, its task and the claim on
// each issue, in one transaction — and the two GETs read them back. The item
// verbs over a live handoff are 137.6's.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

// The handoff create's 409 reasons (task 137 decision 8, settled
// 2026-10-24). Each travels in details.reason under invalid_state, as every
// 409 reason does (§13.1).
const (
	// reasonIssueNotEligible is an explicit issue id the handoff may not
	// claim: missing, in another project, closed, not in `hand_off`,
	// already claimed, or its current delivery already delivered — one
	// reason, because telling them apart would leak another project's rows.
	reasonIssueNotEligible = "issue_not_eligible"
	// reasonNothingToHandOff is a selection that found no eligible issue.
	// 137.9's trigger ledger maps it to `refused`, so a dedupe key is not
	// spent on it.
	reasonNothingToHandOff = "nothing_to_hand_off"
	// reasonGrantNeedsHuman is a non-empty grant from an agent caller
	// (decision 6: "never an agent").
	reasonGrantNeedsHuman = "grant_needs_human"
)

// idempotencyHandoffRoute is the third route that honours Idempotency-Key.
// Its keys share task 040's table, recorded against the handoff's task id,
// so they cascade with the task and need no migration.
const idempotencyHandoffRoute = "/v1/handoffs"

// oneOrMany is a JSON field that takes either one string or a list of them,
// the way `select.delivery` and `select.kind` are written in a trigger.
type oneOrMany []string

// UnmarshalJSON accepts "x" as ["x"].
func (l *oneOrMany) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*l = oneOrMany{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("must be a string or a list of strings")
	}
	*l = many
	return nil
}

// handoffSelectBody is `select:` on the create body (decision 8).
type handoffSelectBody struct {
	Lane     string    `json:"lane,omitempty"`
	Delivery oneOrMany `json:"delivery,omitempty"`
	Kind     oneOrMany `json:"kind,omitempty"`
	Labels   []string  `json:"labels,omitempty"`
	Max      *int      `json:"max,omitempty"`
	Order    string    `json:"order,omitempty"`
}

// handoffCreateRequest is POST /v1/handoffs' body: the handoff's own fields,
// then the task-shaping options of POST /v1/tasks, flat (settled with the
// author 2026-10-24). The handoff task is always a root task with no issue,
// cut from the project's base, so the create fields that would say otherwise
// — issue_id, github_pull, base_branch, branch_name, existing_branch,
// merge_back — are absent here and strict decoding refuses them with a 400.
type handoffCreateRequest struct {
	ProjectID int64              `json:"project_id"`
	Workflow  string             `json:"workflow"`
	IssueIDs  []int64            `json:"issue_ids,omitempty"`
	Select    *handoffSelectBody `json:"select,omitempty"`
	Grant     []string           `json:"grant,omitempty"`
	Title     string             `json:"title,omitempty"`
	Paused    *bool              `json:"paused,omitempty"`

	Description    *string           `json:"description,omitempty"`
	Fields         map[string]string `json:"fields,omitempty"`
	Priority       *int              `json:"priority,omitempty"`
	Agent          *string           `json:"agent,omitempty"`
	Model          *string           `json:"model,omitempty"`
	Effort         *string           `json:"effort,omitempty"`
	Restricted     *bool             `json:"restricted,omitempty"`
	MaxTaskCostUSD *float64          `json:"max_task_cost_usd,omitempty"`
}

// handoffItemBody is one claimed issue on the wire.
type handoffItemBody struct {
	IssueID        int64             `json:"issue_id"`
	DeliveryTaskID *int64            `json:"delivery_task_id"`
	Delivery       *deliveryResponse `json:"delivery"`
	Outcome        string            `json:"outcome"`
	OutcomeAt      *time.Time        `json:"outcome_at"`
	OutcomeBy      string            `json:"outcome_by"`
	Note           string            `json:"note"`
}

// handoffBody is a handoff on the wire. Items is absent from a list row;
// Task only on the create, so a client can follow the task it started.
type handoffBody struct {
	ID        int64             `json:"id"`
	ProjectID int64             `json:"project_id"`
	TaskID    int64             `json:"task_id"`
	State     string            `json:"state"`
	Grant     []string          `json:"grant"`
	CreatedBy string            `json:"created_by"`
	TriggerID string            `json:"trigger_id,omitempty"`
	CreatedAt time.Time         `json:"created_at"`
	Items     []handoffItemBody `json:"items,omitempty"`
	Task      *taskResponse     `json:"task,omitempty"`
}

func renderHandoff(h *store.Handoff, items []*store.HandoffItem) handoffBody {
	grant := make([]string, 0)
	for _, c := range h.Grant() {
		grant = append(grant, string(c))
	}
	out := handoffBody{
		ID: h.ID, ProjectID: h.ProjectID, TaskID: h.TaskID, State: string(h.State), Grant: grant,
		CreatedBy: string(h.CreatedBy), TriggerID: h.TriggerID, CreatedAt: h.CreatedAt,
	}
	for _, it := range items {
		out.Items = append(out.Items, handoffItemBody{
			IssueID: it.IssueID, DeliveryTaskID: it.DeliveryTaskID, Delivery: toDeliveryResponse(it.Delivery, false),
			Outcome: string(it.Outcome), OutcomeAt: it.OutcomeAt, OutcomeBy: string(it.OutcomeBy), Note: it.Note,
		})
	}
	return out
}

// validateHandoffCreate applies the create's own rules, the ones
// prepareTaskCreate does not know, and returns the 400's message or "".
func validateHandoffCreate(req *handoffCreateRequest) string {
	if strings.TrimSpace(req.Workflow) == "" {
		return "workflow is required: there is no default handoff workflow"
	}
	if (len(req.IssueIDs) > 0) == (req.Select != nil) {
		return "exactly one of issue_ids and select is required"
	}
	seen := map[int64]bool{}
	for _, id := range req.IssueIDs {
		if id <= 0 {
			return fmt.Sprintf("issue_ids must be positive, got %d", id)
		}
		if seen[id] {
			return fmt.Sprintf("issue %d is named twice in issue_ids", id)
		}
		seen[id] = true
	}
	if sel := req.Select; sel != nil {
		if sel.Lane != "" && sel.Lane != string(issuestate.LaneHandOff) {
			return fmt.Sprintf("select.lane must be hand_off, the only lane a handoff selects from, got %q", sel.Lane)
		}
		if sel.Order != "" && sel.Order != "priority" {
			return fmt.Sprintf("select.order must be priority, got %q", sel.Order)
		}
		for _, d := range sel.Delivery {
			if !store.ValidDeliveryKind(store.DeliveryKind(d)) {
				return fmt.Sprintf("select.delivery %q must be one of: change, reply, none", d)
			}
		}
		if sel.Max != nil && *sel.Max < 1 {
			return fmt.Sprintf("select.max must be at least 1, got %d", *sel.Max)
		}
	}
	return boundTaskFields(req.Title, ptrValue(req.Description), req.Fields)
}

// handoffTitle is a handoff task's default title, naming the issue count.
func handoffTitle(n int) string {
	if n == 1 {
		return "Hand off 1 issue"
	}
	return fmt.Sprintf("Hand off %d issues", n)
}

// handleHandoffCreate implements POST /v1/handoffs. The task is built and
// validated by POST /v1/tasks' own code — prepareTaskCreate, with its
// catalog, input, restricted and container gates and MCP's bounds — so a
// handoff task and a task cannot disagree about what is acceptable; then
// store.StartHandoff creates the handoff, the task and the claims together.
func (s *Server) handleHandoffCreate(w http.ResponseWriter, r *http.Request) {
	var req handoffCreateRequest
	if !decodeJSONLimit(w, r, &req, maxLargeRequestBytes) {
		return
	}
	if msg := validateHandoffCreate(&req); msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return
	}
	grantJSON, err := store.GrantJSON(req.Grant)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("grant: %v; known capabilities: %s", err, strings.Join(capabilityNames(), ", ")))
		return
	}
	idemKey, ok := readIdempotencyKey(w, r)
	if !ok {
		return
	}
	var idemSHA string
	if idemKey != "" {
		if idemSHA, err = idempotencyDigest(&req); err != nil {
			s.internalError(w, "digest handoff create request", err)
			return
		}
		if s.replayHandoffCreate(w, r, idemKey, idemSHA) {
			return
		}
	}
	// Decision 6's "never an agent", with decision 6's own definition of an
	// agent caller — issueActor's. Best-effort, like every marker (§16).
	createdBy := store.HandoffByHuman
	if issueActor(r) == issuestate.Agent {
		createdBy = store.HandoffByAgent
		if grantJSON != "[]" {
			writeConflict(w, "only a human may give a handoff a grant; an agent may start a grant-less handoff",
				map[string]string{"reason": reasonGrantNeedsHuman})
			return
		}
	}
	// A title the caller left out is decided once the issues are counted,
	// inside the transaction; prepareTaskCreate requires one, so it
	// validates a stand-in.
	const standIn = "Hand off issues"
	title := strings.TrimSpace(req.Title)
	workflowName := strings.TrimSpace(req.Workflow)
	treq := taskCreateRequest{
		ProjectID: req.ProjectID, Workflow: &workflowName, Title: firstNonBlank(title, standIn),
		Description: req.Description, Fields: req.Fields, Priority: req.Priority,
		Agent: req.Agent, Model: req.Model, Effort: req.Effort, Paused: req.Paused,
		Restricted: req.Restricted, MaxTaskCostUSD: req.MaxTaskCostUSD,
	}
	ctx := r.Context()
	prep, ok := s.prepareTaskCreate(ctx, w, &treq, "", false)
	if !ok {
		return
	}
	t := prep.task
	if title == "" {
		t.Title = ""
	}
	resolveBranch, ok := s.handoffBranch(w, r, prep.project, &t, standIn)
	if !ok {
		return
	}
	h := &store.Handoff{GrantJSON: grantJSON, CreatedBy: createdBy}
	in := store.NewHandoff{Handoff: h, Task: &t, ResolveBranch: resolveBranch, TitleFor: handoffTitle}
	if req.Select != nil {
		in.Select = &store.HandoffSelect{
			Delivery: req.Select.Delivery, Kinds: req.Select.Kind, Labels: req.Select.Labels,
			Max: ptrValue(req.Select.Max),
		}
	} else {
		in.IssueIDs = req.IssueIDs
	}
	if idemKey != "" {
		in.Key = &store.IdempotencyKey{Method: r.Method, Path: idempotencyHandoffRoute, Key: idemKey, RequestSHA: idemSHA}
	}
	if _, err := s.deps.Store.StartHandoff(ctx, in); err != nil {
		var (
			notEligible *store.IssueNotEligibleError
			claimed     *store.BranchClaimedError
		)
		switch {
		case errors.As(err, &notEligible):
			details := map[string]string{
				"reason": reasonIssueNotEligible, "issue_id": strconv.FormatInt(notEligible.IssueID, 10),
			}
			if hid, ok, cerr := s.deps.Store.IssueClaim(ctx, req.ProjectID, notEligible.IssueID); cerr == nil && ok {
				details["handoff_id"] = strconv.FormatInt(hid, 10)
			}
			writeConflict(w, notEligible.Error(), details)
		case errors.Is(err, store.ErrNothingToHandOff):
			writeConflict(w, "the selection found no issue eligible for a handoff",
				map[string]string{"reason": reasonNothingToHandOff})
		case errors.As(err, &claimed):
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("branch %q is already claimed by task %d", claimed.Branch, claimed.TaskID))
		case errors.Is(err, store.ErrIdempotencyKeyExists):
			// The concurrent duplicate, as in handleTaskCreate: the winner's
			// handoff is the answer.
			if !s.replayHandoffCreate(w, r, idemKey, idemSHA) {
				s.internalError(w, "replay idempotent handoff create", err)
			}
		default:
			s.internalError(w, "create handoff", err)
		}
		return
	}
	if s.deps.WakeRunner != nil {
		s.deps.WakeRunner()
	}
	items, state, err := s.deps.Store.ListHandoffItems(ctx, h.ID)
	if err != nil {
		s.internalError(w, "list handoff items", err)
		return
	}
	h.State = state
	body := renderHandoff(h, items)
	resp := toTaskResponse(&t, s.snaps.get(t.ID, t.WorkflowSnapshot))
	resp.Warnings = prep.warnings
	body.Task = &resp
	writeJSON(w, http.StatusCreated, body)
}

// handoffBranch names the handoff task's branch through §5.3's chain with
// no literal — the task is cut from the project's base like any root task —
// and writes its own 400s. A name needing the id is produced in the insert
// transaction from the task's title as it stands then, which is the
// count-naming default when the caller gave none; a name that does not is
// decided here, from the stand-in.
func (s *Server) handoffBranch(
	w http.ResponseWriter, r *http.Request, project *store.Project, t *store.Task, standIn string,
) (func(int64) (string, error), bool) {
	ctx := r.Context()
	spec := s.branchSpec("", project)
	bctx := branchContext(firstNonBlank(t.Title, standIn), t.BaseBranch, t.Fields, project)
	preview, err := resolveBranchPreview(spec, bctx)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			fmt.Sprintf("branch name could not be resolved: %v", err))
		return nil, false
	}
	if msg := s.checkBranchLegality(ctx, preview.Name); msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return nil, false
	}
	if preview.NeedsID {
		return func(id int64) (string, error) {
			c := branchContext(firstNonBlank(t.Title, standIn), t.BaseBranch, t.Fields, project)
			name, _, err := worktree.ResolveBranchName(spec, c.WithID(id))
			return name, err
		}, true
	}
	if msg := s.checkBranchCollision(ctx, project.Path, preview.Name); msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return nil, false
	}
	t.BranchName = preview.Name
	return nil, true
}

// replayHandoffCreate is replayTaskCreate for POST /v1/handoffs: the key
// names the handoff's task, and the handoff is rendered as it is now, under
// a 201.
func (s *Server) replayHandoffCreate(w http.ResponseWriter, r *http.Request, key, sha string) bool {
	rec, done := s.recordedKey(w, r, key, sha, idempotencyHandoffRoute, s.deps.Store.GetIdempotencyKey)
	if rec == nil {
		return done
	}
	ctx := r.Context()
	h, err := s.deps.Store.GetHandoffByTask(ctx, rec.TaskID)
	if err != nil {
		s.internalError(w, "get handoff for idempotent replay", err)
		return true
	}
	items, _, err := s.deps.Store.ListHandoffItems(ctx, h.ID)
	if err != nil {
		s.internalError(w, "list handoff items for idempotent replay", err)
		return true
	}
	body := renderHandoff(h, items)
	if task, err := s.deps.Store.GetTask(ctx, h.TaskID); err == nil {
		resp := toTaskResponse(task, s.snaps.get(task.ID, task.WorkflowSnapshot))
		body.Task = &resp
	}
	writeJSON(w, http.StatusCreated, body)
	return true
}

// handleHandoffList implements GET /v1/handoffs: rows without items, newest
// first, filtered by ?project_id=, ?state= (repeatable) and ?issue_id=.
func (s *Server) handleHandoffList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var f store.HandoffFilter
	for _, p := range []struct {
		name string
		dst  *int64
	}{{"project_id", &f.ProjectID}, {"issue_id", &f.IssueID}} {
		v := q.Get(p.name)
		if v == "" {
			continue
		}
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil || id < 1 {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, p.name+" must be a positive integer")
			return
		}
		*p.dst = id
	}
	for _, st := range q["state"] {
		switch store.HandoffState(st) {
		case store.HandoffLive, store.HandoffWaitingState, store.HandoffSettled:
			f.States = append(f.States, store.HandoffState(st))
		default:
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("unknown handoff state %q; must be one of: live, waiting, settled", st))
			return
		}
	}
	list, err := s.deps.Store.ListHandoffs(r.Context(), f)
	if err != nil {
		s.internalError(w, "list handoffs", err)
		return
	}
	out := make([]handoffBody, 0, len(list))
	for _, h := range list {
		out = append(out, renderHandoff(h, nil))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleHandoffGet implements GET /v1/handoffs/{id}: one handoff with its
// items.
func (s *Server) handleHandoffGet(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, "handoff ids are integers")
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
	items, state, err := s.deps.Store.ListHandoffItems(ctx, id)
	if err != nil {
		s.internalError(w, "list handoff items", err)
		return
	}
	h.State = state
	writeJSON(w, http.StatusOK, renderHandoff(h, items))
}

func capabilityNames() []string {
	out := make([]string, 0, len(store.Capabilities))
	for _, c := range store.Capabilities {
		out = append(out, string(c))
	}
	return out
}
