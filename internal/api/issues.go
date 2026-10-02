package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/lezli01/vincent/internal/issues"
	"github.com/lezli01/vincent/internal/issuestate"
	"github.com/lezli01/vincent/internal/mcp"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// The issue routes (spec §5.6, §13.2, task 130.3). Every write goes through
// internal/issues, the one validated write path; this file maps its errors
// onto §13.1's envelope and nothing more.
//
// The 409 reasons below are details.reason values, not codes: every 409 is
// CodeInvalidState (task 040's rule).
const (
	// issueReasonChanged is a PATCH whose `version` is no longer the stored
	// one. details.issue carries the issue as it is now, so the client can
	// rebase its edit without a second read.
	issueReasonChanged = "issue_changed"
	// issueReasonMirrored is a PATCH of an imported issue's title, body or
	// labels (task 130.3 decision 2): those mirror the remote.
	issueReasonMirrored = "issue_mirrored"
)

// issueSourceBody is where an imported issue came from; null for a local one.
type issueSourceBody struct {
	Provider string `json:"provider"`
	Repo     string `json:"repo"`
	Number   int    `json:"number,omitempty"`
	URL      string `json:"url,omitempty"`
	// RemoteState is the state the remote last reported, read from the
	// stored remote payload; omitted when it carries none.
	RemoteState string `json:"remote_state,omitempty"`
}

// issueRowBody is an issue as GET /v1/issues lists it: everything but the
// body, which is unbounded prose a list never renders.
type issueRowBody struct {
	ID              int64            `json:"id"`
	ProjectID       int64            `json:"project_id"`
	Title           string           `json:"title"`
	State           string           `json:"state"`
	CloseReason     string           `json:"close_reason,omitempty"`
	DuplicateOf     *int64           `json:"duplicate_of,omitempty"`
	Kind            string           `json:"kind"`
	Priority        int              `json:"priority"`
	Author          string           `json:"author"`
	CreatedByTaskID *int64           `json:"created_by_task_id,omitempty"`
	Labels          []string         `json:"labels"`
	Source          *issueSourceBody `json:"source"`
	// Active is whether a root task created from the issue is unsettled;
	// TaskCount counts those root tasks (task 130 decision 5).
	Active    bool       `json:"active"`
	TaskCount int        `json:"task_count"`
	Version   int64      `json:"version"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	ClosedAt  *time.Time `json:"closed_at,omitempty"`
}

// issueTasksBody is the root tasks an issue started.
type issueTasksBody struct {
	Count     int     `json:"count"`
	ActiveIDs []int64 `json:"active_ids"`
}

// issueBody is one issue in full: the row plus its body, what a human may do
// next, its root tasks, and which fields a PATCH may touch.
type issueBody struct {
	issueRowBody
	Body             string              `json:"body"`
	AvailableActions []issuestate.Action `json:"available_actions"`
	Tasks            issueTasksBody      `json:"tasks"`
	Editable         []string            `json:"editable"`
	// Prefill is what creating a task from this issue with `issue_id` would
	// fill in (task 130.7), present only on GET /v1/issues/{id}?workflow=W:
	// the declared-field half is a fact about a workflow. It is computed by
	// the same function POST /v1/tasks runs (task 035 decision 2's kept
	// principle), so the preview is what the create stores.
	Prefill *githubPrefill `json:"prefill,omitempty"`
}

func renderIssueRow(iss *store.Issue) issueRowBody {
	labels := iss.Labels
	if labels == nil {
		labels = []string{}
	}
	row := issueRowBody{
		ID:              iss.ID,
		ProjectID:       iss.ProjectID,
		Title:           iss.Title,
		State:           string(issuestate.Normalize(iss.State)),
		CloseReason:     string(iss.CloseReason),
		DuplicateOf:     iss.DuplicateOfIssueID,
		Kind:            iss.Kind,
		Priority:        iss.Priority,
		Author:          iss.Author,
		CreatedByTaskID: iss.CreatedByTaskID,
		Labels:          labels,
		Active:          iss.Active,
		TaskCount:       iss.TaskCount,
		Version:         iss.Version,
		CreatedAt:       iss.CreatedAt,
		UpdatedAt:       iss.UpdatedAt,
		ClosedAt:        iss.ClosedAt,
	}
	if issues.Mirrored(iss) {
		r := iss.Remote
		row.Source = &issueSourceBody{
			Provider:    r.Provider,
			Repo:        r.Repo,
			Number:      r.Number,
			URL:         r.URL,
			RemoteState: remoteState(r.RemoteJSON),
		}
	}
	return row
}

// remoteState reads the `state` a provider's stored payload carries, "" when
// it carries none or is not JSON.
func remoteState(raw string) string {
	if raw == "" {
		return ""
	}
	var v struct {
		State string `json:"state"`
	}
	if json.Unmarshal([]byte(raw), &v) != nil {
		return ""
	}
	return strings.ToLower(v.State)
}

// renderIssue renders iss in full. The active task ids are a second read; a
// failure there degrades to an empty list with a log line rather than
// turning a read that already succeeded into a 500.
func (s *Server) renderIssue(ctx context.Context, iss *store.Issue) issueBody {
	ids, err := s.deps.Store.ActiveIssueTaskIDs(ctx, iss.ID)
	if err != nil {
		s.deps.Logger.Warn("list issue tasks", "issue", iss.ID, "error", err)
		ids = []int64{}
	}
	actions := issuestate.HumanActionsFrom(iss.State)
	if actions == nil {
		actions = []issuestate.Action{}
	}
	return issueBody{
		issueRowBody:     renderIssueRow(iss),
		Body:             iss.Body,
		AvailableActions: actions,
		Tasks:            issueTasksBody{Count: iss.TaskCount, ActiveIDs: ids},
		Editable:         issues.Editable(iss),
	}
}

// issueActor is who a request writes as: an MCP tool call is an agent, every
// other API call a human. Telling a step's CLI call from a person's is task
// 130 open question 6, not this route's.
func issueActor(ctx context.Context) issuestate.Actor {
	if mcp.ViaTool(ctx) {
		return issuestate.Agent
	}
	return issuestate.Human
}

// osUsername is the name of the user the daemon runs as, read once. An HTTP
// create records it as the issue's author (task 130.3 decision 1): every
// client of this daemon is that user, so it is the honest answer, and it is
// not a field a client could set to something else.
var osUsername = sync.OnceValue(func() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	for _, env := range []string{"USER", "USERNAME"} {
		if v := os.Getenv(env); v != "" {
			return v
		}
	}
	return "unknown"
})

// issueAuthor derives a create's author and MCP provenance from the request
// context, never from the body: a step's call is `task N`, any other MCP call
// `agent`, and everything else the daemon's own OS user.
func issueAuthor(ctx context.Context) (author string, createdBy *int64) {
	if creator, ok := mcp.CreatorTaskID(ctx); ok {
		return fmt.Sprintf("task %d", creator), &creator
	}
	if mcp.ViaTool(ctx) {
		return string(issuestate.Agent), nil
	}
	return osUsername(), nil
}

// writeIssueError maps an internal/issues or store error onto the envelope.
// A stale-version 409 re-reads the issue, so it carries the current one.
func (s *Server) writeIssueError(w http.ResponseWriter, r *http.Request, id int64, what string, err error) {
	var verr *issues.ValidationError
	switch {
	case errors.As(err, &verr):
		writeError(w, http.StatusBadRequest, CodeValidationFailed, verr.Error())
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
	case errors.Is(err, issues.ErrMirrored):
		writeConflict(w, err.Error(), map[string]string{"reason": issueReasonMirrored})
	case errors.Is(err, store.ErrInvalidIssueAction):
		state := ""
		if cur, gerr := s.deps.Store.GetIssue(r.Context(), id); gerr == nil {
			state = string(issuestate.Normalize(cur.State))
		}
		writeConflict(w, fmt.Sprintf("issue %d: action not allowed in state %s", id, state),
			map[string]string{"state": state})
	case errors.Is(err, store.ErrIssueChanged):
		cur, gerr := s.deps.Store.GetIssue(r.Context(), id)
		if gerr != nil {
			s.writeIssueError(w, r, id, what, gerr)
			return
		}
		// details.issue is an object, which errorDetail's string map cannot
		// carry; the envelope is otherwise writeConflict's exactly.
		writeJSON(w, http.StatusConflict, map[string]any{"error": map[string]any{
			"code":    CodeInvalidState,
			"message": fmt.Sprintf("issue %d changed since it was read (now at version %d)", id, cur.Version),
			"details": map[string]any{"reason": issueReasonChanged, "issue": s.renderIssue(r.Context(), cur)},
		}})
	default:
		s.internalError(w, what, err)
	}
}

// issueIDFromPath parses {id}, writing the 400 itself.
func issueIDFromPath(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "issue id must be an integer")
		return 0, false
	}
	return id, true
}

// handleIssueList implements GET /v1/issues. The `q` parameter is a
// server-side search, unlike §13.2's pickers: an issue set is unbounded in a
// way a project or workflow list is not (task 130.3, spec §13.2 amendment).
func (s *Server) handleIssueList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var f store.IssueFilter
	if v := q.Get("project_id"); v != "" {
		id, err := strconv.ParseInt(v, 10, 64)
		if err != nil {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "project_id must be an integer")
			return
		}
		f.ProjectID = id
	}
	for _, st := range q["state"] {
		if !isIssueState(st) {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, fmt.Sprintf("unknown issue state %q", st))
			return
		}
		f.States = append(f.States, issuestate.State(st))
	}
	f.Labels = q["label"]
	f.Kind = q.Get("kind")
	f.Text = q.Get("q")
	switch v := q.Get("source"); v {
	case "", "local", "github":
		f.Source = v
	default:
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "source must be one of: local, github")
		return
	}
	switch v := q.Get("sort"); v {
	case "", store.IssueSortUpdated, store.IssueSortCreated:
		f.Sort = v
	default:
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "sort must be one of: updated, created")
		return
	}
	for _, p := range []struct {
		name string
		dst  *int
	}{{"limit", &f.Limit}, {"offset", &f.Offset}} {
		v := q.Get(p.name)
		if v == "" {
			continue
		}
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				p.name+" must be a non-negative integer")
			return
		}
		*p.dst = n
	}
	list, err := issues.New(s.deps.Store).List(r.Context(), f)
	if err != nil {
		s.internalError(w, "list issues", err)
		return
	}
	out := make([]issueRowBody, 0, len(list))
	for _, iss := range list {
		out = append(out, renderIssueRow(iss))
	}
	writeJSON(w, http.StatusOK, out)
}

func isIssueState(v string) bool {
	for _, st := range issuestate.States {
		if string(st) == v {
			return true
		}
	}
	return false
}

// issueCreateRequest is POST /v1/issues' body. Every optional field is
// omitempty, so a field added later does not move the idempotency digest of
// a request that does not send it (task 130 decision 7).
type issueCreateRequest struct {
	ProjectID int64    `json:"project_id"`
	Title     string   `json:"title"`
	Body      string   `json:"body,omitempty"`
	Labels    []string `json:"labels,omitempty"`
	Kind      string   `json:"kind,omitempty"`
	Priority  int      `json:"priority,omitempty"`
}

// handleIssueCreate implements POST /v1/issues. The author is the daemon's
// to derive (decision 1), and an Idempotency-Key makes the create replayable
// exactly as POST /v1/tasks' is (§13.1).
func (s *Server) handleIssueCreate(w http.ResponseWriter, r *http.Request) {
	var req issueCreateRequest
	// The large tier: an issue's body is prose that ends up in a prompt, as
	// a task's description does.
	if !decodeJSONLimit(w, r, &req, maxLargeRequestBytes) {
		return
	}
	if msg := boundIssueText(&req.Title, &req.Body); msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return
	}
	if req.ProjectID <= 0 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "project_id is required")
		return
	}
	idemKey, ok := readIdempotencyKey(w, r)
	if !ok {
		return
	}
	var idemSHA string
	if idemKey != "" {
		var derr error
		if idemSHA, derr = idempotencyDigest(&req); derr != nil {
			s.internalError(w, "digest issue create request", derr)
			return
		}
		if s.replayIssueCreate(w, r, idemKey, idemSHA) {
			return
		}
	}
	ctx := r.Context()
	if _, err := s.deps.Store.GetProject(ctx, req.ProjectID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, CodeNotFound, fmt.Sprintf("project %d not found", req.ProjectID))
			return
		}
		s.internalError(w, "get project", err)
		return
	}
	author, createdBy := issueAuthor(ctx)
	in := issues.CreateInput{
		ProjectID:       req.ProjectID,
		Title:           req.Title,
		Body:            req.Body,
		Kind:            req.Kind,
		Author:          author,
		Priority:        req.Priority,
		Labels:          req.Labels,
		CreatedByTaskID: createdBy,
	}
	if idemKey != "" {
		in.Key = &store.IdempotencyKey{
			Method: r.Method, Path: idempotencyIssueRoute, Key: idemKey, RequestSHA: idemSHA,
		}
	}
	iss, err := issues.New(s.deps.Store).Create(ctx, issueActor(ctx), in)
	if errors.Is(err, store.ErrIdempotencyKeyExists) {
		// The concurrent duplicate, as in handleTaskCreate: the winner's
		// issue is the answer.
		if !s.replayIssueCreate(w, r, idemKey, idemSHA) {
			s.internalError(w, "replay idempotent issue create", err)
		}
		return
	}
	if err != nil {
		s.writeIssueError(w, r, 0, "create issue", err)
		return
	}
	writeJSON(w, http.StatusCreated, s.renderIssue(ctx, iss))
}

// boundIssueText holds an issue's title and body to a task's title and
// description bounds (§13.1): both reach a prompt the same way.
func boundIssueText(title, body *string) string {
	if title != nil {
		if msg := boundString("title", *title, maxTitleBytes); msg != "" {
			return msg
		}
	}
	if body != nil {
		if msg := boundString("body", *body, maxDescriptionBytes); msg != "" {
			return msg
		}
	}
	return ""
}

// handleIssueGet implements GET /v1/issues/{id}.
func (s *Server) handleIssueGet(w http.ResponseWriter, r *http.Request) {
	id, ok := issueIDFromPath(w, r)
	if !ok {
		return
	}
	iss, err := s.deps.Store.GetIssue(r.Context(), id)
	if err != nil {
		s.writeIssueError(w, r, id, "get issue", err)
		return
	}
	body := s.renderIssue(r.Context(), iss)
	// The prefill preview. An unknown workflow is a 400 rather than a silent
	// "no prefill", for the reason the GitHub issues listing gives: a
	// preview of nothing would look like an issue with no metadata.
	if name := strings.TrimSpace(r.URL.Query().Get("workflow")); name != "" {
		var wf *workflow.Workflow
		if s.deps.Workflows != nil {
			if entry, found := s.deps.Workflows.Lookup(iss.ProjectID, name); found && entry.Valid() {
				wf = entry.Workflow
			}
		}
		if wf == nil {
			writeError(w, http.StatusBadRequest, CodeValidationFailed,
				fmt.Sprintf("workflow %q not found for project %d", name, iss.ProjectID))
			return
		}
		prefill := vincentIssuePrefill(store.NewIssueSnapshot(iss, time.Now()), wf)
		body.Prefill = &prefill
	}
	writeJSON(w, http.StatusOK, body)
}

// issuePatchRequest is PATCH /v1/issues/{id}'s body. `state` is deliberately
// not a field — close and reopen are their own routes — so sending it is the
// unknown-field 400.
type issuePatchRequest struct {
	Version      *int64    `json:"version"`
	Title        *string   `json:"title"`
	Body         *string   `json:"body"`
	Labels       *[]string `json:"labels"`
	AddLabels    []string  `json:"add_labels"`
	RemoveLabels []string  `json:"remove_labels"`
	Kind         *string   `json:"kind"`
	Priority     *int      `json:"priority"`
}

// handleIssuePatch implements PATCH /v1/issues/{id}: a compare-and-set on
// `version` (task 130 decision 11 — a departure from §12.3's "no ETag",
// recorded there), with fields and labels landing as one write.
func (s *Server) handleIssuePatch(w http.ResponseWriter, r *http.Request) {
	id, ok := issueIDFromPath(w, r)
	if !ok {
		return
	}
	var req issuePatchRequest
	if !decodeJSONLimit(w, r, &req, maxLargeRequestBytes) {
		return
	}
	if req.Version == nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"version is required: send the version you read")
		return
	}
	if req.Title == nil && req.Body == nil && req.Labels == nil && req.AddLabels == nil &&
		req.RemoveLabels == nil && req.Kind == nil && req.Priority == nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "patch changes nothing: send at least one field")
		return
	}
	if req.Labels != nil && (req.AddLabels != nil || req.RemoveLabels != nil) {
		writeError(w, http.StatusBadRequest, CodeValidationFailed,
			"labels replaces the set and cannot be combined with add_labels or remove_labels")
		return
	}
	if msg := boundIssueText(req.Title, req.Body); msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return
	}
	ctx := r.Context()
	iss, err := issues.New(s.deps.Store).Update(ctx, issueActor(ctx), id, *req.Version, store.IssuePatch{
		Title:        req.Title,
		Body:         req.Body,
		Kind:         req.Kind,
		Priority:     req.Priority,
		Labels:       req.Labels,
		AddLabels:    req.AddLabels,
		RemoveLabels: req.RemoveLabels,
	})
	if err != nil {
		s.writeIssueError(w, r, id, "update issue", err)
		return
	}
	writeJSON(w, http.StatusOK, s.renderIssue(ctx, iss))
}

// issueCloseRequest is POST /v1/issues/{id}/close's body; an empty body is a
// close as completed.
type issueCloseRequest struct {
	Reason      issuestate.Reason `json:"reason"`
	DuplicateOf *int64            `json:"duplicate_of"`
}

// handleIssueClose implements POST /v1/issues/{id}/close.
func (s *Server) handleIssueClose(w http.ResponseWriter, r *http.Request) {
	id, ok := issueIDFromPath(w, r)
	if !ok {
		return
	}
	var req issueCloseRequest
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	iss, err := issues.New(s.deps.Store).Close(ctx, issueActor(ctx), id, req.Reason, req.DuplicateOf)
	if err != nil {
		s.writeIssueError(w, r, id, "close issue", err)
		return
	}
	writeJSON(w, http.StatusOK, s.renderIssue(ctx, iss))
}

// handleIssueReopen implements POST /v1/issues/{id}/reopen. Its body, if
// any, is `{}`.
func (s *Server) handleIssueReopen(w http.ResponseWriter, r *http.Request) {
	id, ok := issueIDFromPath(w, r)
	if !ok {
		return
	}
	var req struct{}
	if r.ContentLength != 0 && !decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	iss, err := issues.New(s.deps.Store).Reopen(ctx, issueActor(ctx), id)
	if err != nil {
		s.writeIssueError(w, r, id, "reopen issue", err)
		return
	}
	writeJSON(w, http.StatusOK, s.renderIssue(ctx, iss))
}

// handleIssueDelete implements DELETE /v1/issues/{id}: permanent, in any
// state (task 130 decision 6). An imported issue leaves its tombstone so sync
// never imports it again, upstream is never touched, and tasks created from
// it keep running with issue_id NULL. Not an MCP tool (§13.4), on task 092's
// line.
func (s *Server) handleIssueDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := issueIDFromPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if err := issues.New(s.deps.Store).Delete(ctx, issueActor(ctx), id); err != nil {
		s.writeIssueError(w, r, id, "delete issue", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// issueLabelBody is one entry of a project's label catalogue.
type issueLabelBody struct {
	Name        string `json:"name"`
	Color       string `json:"color,omitempty"`
	Description string `json:"description,omitempty"`
	Source      string `json:"source"`
	IssueCount  int    `json:"issue_count"`
}

// handleProjectIssueLabels implements GET /v1/projects/{id}/issue-labels: the
// project's label catalogue, each label with how many issues carry it.
func (s *Server) handleProjectIssueLabels(w http.ResponseWriter, r *http.Request) {
	p, ok := s.projectFromPath(w, r)
	if !ok {
		return
	}
	labels, err := s.deps.Store.ListLabels(r.Context(), p.ID)
	if err != nil {
		s.internalError(w, "list labels", err)
		return
	}
	out := make([]issueLabelBody, 0, len(labels))
	for _, l := range labels {
		out = append(out, issueLabelBody{
			Name: l.Name, Color: l.Color, Description: l.Description, Source: l.Source, IssueCount: l.IssueCount,
		})
	}
	writeJSON(w, http.StatusOK, out)
}
