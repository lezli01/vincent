package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/trigger"
	"github.com/lezli01/vincent/internal/workflow"
)

// The trigger routes (§13.2, task 096). The daemon owns every file end to end,
// as it does a workflow's (task 065): a create writes a daemon-rendered
// starter, an edit is workflow.Edit operations carrying a version token, and
// the registry reloads before the answer so a GET issued the instant a write
// returns sees it.
//
// Three families, with three MCP postures (decision 22, decision 31G):
//
//   - the reads, `validate`, and both dry runs are ordinary tools — a dry run
//     fires nothing, and `poll` runs only a command the user configured;
//   - POST, PATCH and DELETE are excluded: an agent must not author or arm a
//     trigger that starts agents, and enabling one is a PATCH;
//   - the ingress route is excluded too: an agent that can inject events can
//     start agents.

// triggerPoll is one trigger's poll health as the view renders it.
type triggerPoll struct {
	// Seeded says the trigger has a cursor: its next poll judges rather than
	// seeds.
	Seeded     bool    `json:"seeded"`
	LastPollAt *string `json:"last_poll_at"`
	OK         bool    `json:"ok"`
	Error      string  `json:"error,omitempty"`
	LastFireAt *string `json:"last_fire_at"`
}

// triggerSummary is one row of GET /v1/triggers.
type triggerSummary struct {
	ID      string           `json:"id"`
	File    string           `json:"file"`
	Version string           `json:"version"`
	Valid   bool             `json:"valid"`
	Errors  []workflow.Error `json:"errors"`
	Enabled bool             `json:"enabled"`
	// Armed is enabled, valid and `triggers.enabled` on (decision 16);
	// DisarmedReason says which of those is missing.
	Armed          bool        `json:"armed"`
	DisarmedReason string      `json:"disarmed_reason,omitempty"`
	SourceType     string      `json:"source_type,omitempty"`
	ActionType     string      `json:"action_type,omitempty"`
	ProjectID      int64       `json:"project_id,omitempty"`
	OnFire         string      `json:"on_fire,omitempty"`
	Permission     string      `json:"permission,omitempty"`
	Poll           triggerPoll `json:"poll"`
}

// triggerListResponse is GET /v1/triggers.
type triggerListResponse struct {
	// Enabled is `triggers.enabled`: the view's global-off banner.
	Enabled  bool             `json:"enabled"`
	Dir      string           `json:"dir"`
	Triggers []triggerSummary `json:"triggers"`
}

// triggerDetail is GET /v1/triggers/{id}: the summary, the file's bytes and
// its parsed definition (null when it does not validate).
type triggerDetail struct {
	triggerSummary
	Source     string              `json:"source"`
	Definition *trigger.Definition `json:"definition"`
}

// triggerDelivery is one ledger row on the wire.
type triggerDelivery struct {
	ID        int64  `json:"id"`
	TriggerID string `json:"trigger_id"`
	EventID   string `json:"event_id"`
	DedupeKey string `json:"dedupe_key"`
	// ConcurrencyKey is the `overrun:` group this event was judged in, absent
	// for a trigger that declares none; SupersededTaskID the task a
	// `cancel_previous` fire replaced (task 121).
	ConcurrencyKey   string `json:"concurrency_key,omitempty"`
	Outcome          string `json:"outcome"`
	TaskID           *int64 `json:"task_id"`
	SupersededTaskID *int64 `json:"superseded_task_id,omitempty"`
	Detail           string `json:"detail,omitempty"`
	CreatedAt        string `json:"created_at"`
}

// triggersReady writes a 500 for a server wired without triggers.
func (s *Server) triggersReady(w http.ResponseWriter) bool {
	if s.deps.Triggers == nil || s.deps.TriggerRegistry == nil || s.deps.TriggerWriter == nil {
		s.internalError(w, "triggers", errors.New("triggers are not wired in this server"))
		return false
	}
	return true
}

func timeString(t *time.Time) *string {
	if t == nil {
		return nil
	}
	v := t.UTC().Format(time.RFC3339Nano)
	return &v
}

func (s *Server) summarize(e *trigger.Entry, c *store.TriggerCursor) triggerSummary {
	out := triggerSummary{
		ID: e.ID, File: e.File, Version: e.Version, Valid: e.Valid(),
		Errors: []workflow.Error{}, Armed: s.deps.Triggers.Armed(e),
		DisarmedReason: s.deps.Triggers.DisarmedReason(e),
	}
	if len(e.Errors) > 0 {
		out.Errors = e.Errors
	}
	if d := e.Def; d != nil {
		out.Enabled, out.SourceType, out.ActionType = d.Enabled, d.Source.Type, d.Action.Type
		out.ProjectID, out.OnFire = d.Source.Project, d.EffectiveOnFire()
		if d.Action.Type == trigger.ActionCreateTask {
			out.Permission = d.EffectivePermission()
		}
	}
	if c != nil {
		out.Poll = triggerPoll{
			Seeded: c.Cursor != nil, LastPollAt: timeString(c.LastPollAt), OK: c.LastPollOK,
			Error: c.LastPollError, LastFireAt: timeString(c.LastFireAt),
		}
	}
	return out
}

// handleTriggerList serves GET /v1/triggers.
func (s *Server) handleTriggerList(w http.ResponseWriter, r *http.Request) {
	if !s.triggersReady(w) {
		return
	}
	cursors, err := s.deps.Store.ListTriggerCursors(r.Context())
	if err != nil {
		s.internalError(w, "triggers: list cursors", err)
		return
	}
	out := triggerListResponse{
		Enabled: s.deps.Config().Triggers.Enabled, Dir: s.deps.TriggerRegistry.Dir(),
		Triggers: []triggerSummary{},
	}
	for _, e := range s.deps.TriggerRegistry.List() {
		out.Triggers = append(out.Triggers, s.summarize(&e, cursors[e.ID]))
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTriggerGet serves GET /v1/triggers/{id}.
func (s *Server) handleTriggerGet(w http.ResponseWriter, r *http.Request) {
	if !s.triggersReady(w) {
		return
	}
	e, ok := s.deps.TriggerRegistry.Get(r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "no trigger named "+r.PathValue("id"))
		return
	}
	c, err := s.deps.Store.GetTriggerCursor(r.Context(), e.ID)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.internalError(w, "triggers: get cursor", err)
		return
	}
	writeJSON(w, http.StatusOK, triggerDetail{
		triggerSummary: s.summarize(&e, c), Source: string(e.Source), Definition: e.Def,
	})
}

// handleTriggerSchema serves GET /v1/triggers/schema (decision 19).
func (s *Server) handleTriggerSchema(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, trigger.SchemaDescriptor())
}

// triggerValidateRequest is POST /v1/triggers/validate.
type triggerValidateRequest struct {
	Source string `json:"source"`
	// ID, when set, is the file stem the document's id must match.
	ID string `json:"id,omitempty"`
}

type triggerValidateResponse struct {
	Valid  bool             `json:"valid"`
	Errors []workflow.Error `json:"errors"`
}

// handleTriggerValidate serves POST /v1/triggers/validate.
func (s *Server) handleTriggerValidate(w http.ResponseWriter, r *http.Request) {
	var req triggerValidateRequest
	if !decodeJSONLimit(w, r, &req, maxLargeRequestBytes) {
		return
	}
	_, errs := trigger.Parse([]byte(req.Source), req.ID)
	out := triggerValidateResponse{Valid: len(errs) == 0, Errors: []workflow.Error{}}
	if len(errs) > 0 {
		out.Errors = errs
	}
	writeJSON(w, http.StatusOK, out)
}

// triggerCreateRequest is POST /v1/triggers: what the view's create prompt
// collects. The daemon renders the starter — disabled, with no on_fire line —
// and the form edits it from there.
type triggerCreateRequest struct {
	ID           string   `json:"id"`
	ProjectID    int64    `json:"project_id"`
	PollInterval string   `json:"poll_interval,omitempty"`
	Command      []string `json:"command,omitempty"`
	Workflow     string   `json:"workflow,omitempty"`
	Title        string   `json:"title,omitempty"`
}

// triggerWriteResponse answers every write.
type triggerWriteResponse struct {
	ID      string           `json:"id"`
	File    string           `json:"file"`
	Version string           `json:"version"`
	Errors  []workflow.Error `json:"errors"`
}

// handleTriggerCreate serves POST /v1/triggers.
func (s *Server) handleTriggerCreate(w http.ResponseWriter, r *http.Request) {
	if !s.triggersReady(w) {
		return
	}
	var req triggerCreateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := trigger.ValidID(req.ID); err != nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error())
		return
	}
	if req.ProjectID < 1 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "project_id is required")
		return
	}
	if !s.projectExists(w, r, req.ProjectID) {
		return
	}
	version, err := s.deps.TriggerWriter.Create(req.ID, trigger.Starter(trigger.StarterSpec{
		ID: req.ID, Project: req.ProjectID, PollInterval: req.PollInterval,
		Command: req.Command, Workflow: req.Workflow, Title: req.Title,
	}))
	if err != nil {
		s.writeTriggerError(w, "triggers: create", err)
		return
	}
	s.deps.TriggerRegistry.Reload()
	path, _ := s.deps.TriggerWriter.Path(req.ID)
	writeJSON(w, http.StatusCreated, triggerWriteResponse{ID: req.ID, File: path, Version: version, Errors: []workflow.Error{}})
}

// triggerPatchRequest is PATCH /v1/triggers/{id}.
type triggerPatchRequest struct {
	Version string        `json:"version"`
	Ops     []workflow.Op `json:"ops"`
}

// handleTriggerPatch serves PATCH /v1/triggers/{id}. A patch whose result
// does not validate is refused with the file byte-identical, and a stale
// version is a 409 carrying the current one.
func (s *Server) handleTriggerPatch(w http.ResponseWriter, r *http.Request) {
	if !s.triggersReady(w) {
		return
	}
	var req triggerPatchRequest
	if !decodeJSONLimit(w, r, &req, maxLargeRequestBytes) {
		return
	}
	if req.Version == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "version is required")
		return
	}
	if len(req.Ops) == 0 {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "ops must not be empty")
		return
	}
	id := r.PathValue("id")
	if !triggerIDFromPath(w, id) {
		return
	}
	version, err := s.deps.TriggerWriter.Patch(id, req.Version, req.Ops)
	if err != nil {
		s.writeTriggerError(w, "triggers: patch", err)
		return
	}
	s.deps.TriggerRegistry.Reload()
	path, _ := s.deps.TriggerWriter.Path(id)
	writeJSON(w, http.StatusOK, triggerWriteResponse{ID: id, File: path, Version: version, Errors: []workflow.Error{}})
}

// handleTriggerDelete serves DELETE /v1/triggers/{id}?version=. The ledger is
// kept (decision 21) and the cursor goes with the file (decision 16), which
// the registry reload below reports to the manager.
func (s *Server) handleTriggerDelete(w http.ResponseWriter, r *http.Request) {
	if !s.triggersReady(w) {
		return
	}
	version := r.URL.Query().Get("version")
	if version == "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "version is required")
		return
	}
	if !triggerIDFromPath(w, r.PathValue("id")) {
		return
	}
	if err := s.deps.TriggerWriter.Delete(r.PathValue("id"), version); err != nil {
		s.writeTriggerError(w, "triggers: delete", err)
		return
	}
	s.deps.TriggerRegistry.Reload()
	w.WriteHeader(http.StatusNoContent)
}

// triggerTestRequest is POST /v1/triggers/{id}/test.
type triggerTestRequest struct {
	Event map[string]any `json:"event"`
}

// handleTriggerTest serves POST /v1/triggers/{id}/test: the supplied event
// through the real pipeline, writing nothing.
func (s *Server) handleTriggerTest(w http.ResponseWriter, r *http.Request) {
	if !s.triggersReady(w) {
		return
	}
	var req triggerTestRequest
	if !decodeJSONLimit(w, r, &req, maxLargeRequestBytes) {
		return
	}
	if req.Event == nil {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "event is required: a JSON object")
		return
	}
	j, err := s.deps.Triggers.Test(r.Context(), r.PathValue("id"), trigger.Event(req.Event))
	if err != nil {
		s.writeTriggerError(w, "triggers: test", err)
		return
	}
	writeJSON(w, http.StatusOK, j)
}

// handleTriggerPoll serves POST /v1/triggers/{id}/poll: the source run once
// for real, judged, and nothing recorded.
func (s *Server) handleTriggerPoll(w http.ResponseWriter, r *http.Request) {
	if !s.triggersReady(w) {
		return
	}
	out, err := s.deps.Triggers.PollDry(r.Context(), r.PathValue("id"))
	var pe *trigger.PollError
	if errors.As(err, &pe) {
		writeJSON(w, http.StatusOK, trigger.DryPoll{Events: []*trigger.Judgement{}, Error: pe.Error()})
		return
	}
	if err != nil {
		s.writeTriggerError(w, "triggers: poll", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

type triggerDeliveriesResponse struct {
	Deliveries []triggerDelivery `json:"deliveries"`
}

// handleTriggerDeliveries serves GET /v1/triggers/{id}/deliveries?limit=. The
// ledger outlives the file, so an id with no file still lists.
func (s *Server) handleTriggerDeliveries(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := trigger.ValidID(id); err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return
	}
	limit := 100
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 1000 {
			writeError(w, http.StatusBadRequest, CodeValidationFailed, "limit must be an integer from 1 to 1000")
			return
		}
		limit = n
	}
	rows, err := s.deps.Store.ListTriggerDeliveries(r.Context(), id, limit)
	if err != nil {
		s.internalError(w, "triggers: list deliveries", err)
		return
	}
	out := triggerDeliveriesResponse{Deliveries: make([]triggerDelivery, 0, len(rows))}
	for _, d := range rows {
		out.Deliveries = append(out.Deliveries, triggerDelivery{
			ID: d.ID, TriggerID: d.TriggerID, EventID: d.EventID, DedupeKey: d.DedupeKey,
			ConcurrencyKey: d.ConcurrencyKey, Outcome: d.Outcome, TaskID: d.TaskID,
			SupersededTaskID: d.SupersededTaskID, Detail: d.Detail,
			CreatedAt: d.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	writeJSON(w, http.StatusOK, out)
}

// handleTriggerEvents serves POST /v1/triggers/{id}/events: the `type: http`
// ingress (096.5). The body is read raw under §13.1's 4 MiB tier — a webhook
// payload routinely exceeds the 64 KiB default, and the signature is over the
// exact bytes, so nothing may decode it first.
func (s *Server) handleTriggerEvents(w http.ResponseWriter, r *http.Request) {
	if !s.triggersReady(w) {
		return
	}
	id := r.PathValue("id")
	if _, ok := s.deps.TriggerRegistry.Get(id); !ok {
		writeError(w, http.StatusNotFound, CodeNotFound, "no trigger named "+id)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxLargeRequestBytes))
	if err != nil {
		decodeFailed(w, err, maxLargeRequestBytes)
		return
	}
	del, err := s.deps.Triggers.Ingest(r.Context(), id, r.Header, body)
	if err != nil {
		s.writeTriggerError(w, "triggers: ingest", err)
		return
	}
	writeJSON(w, http.StatusOK, del)
}

// triggerIDFromPath answers 404 for an id no trigger file could have: a slug
// is the only thing that can name a file in the directory (ValidID).
func triggerIDFromPath(w http.ResponseWriter, id string) bool {
	if err := trigger.ValidID(id); err != nil {
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
		return false
	}
	return true
}

// writeTriggerError maps the trigger package's errors onto §13.1.
func (s *Server) writeTriggerError(w http.ResponseWriter, what string, err error) {
	var (
		invalid  *trigger.InvalidError
		stale    *trigger.StaleError
		disarmed *trigger.DisarmedError
		badEvent *trigger.EventError
		poll     *trigger.PollError
	)
	switch {
	case errors.Is(err, trigger.ErrNotFound):
		writeError(w, http.StatusNotFound, CodeNotFound, err.Error())
	case errors.As(err, &invalid):
		b, _ := json.Marshal(invalid.Errors)
		writeJSON(w, http.StatusBadRequest, errorBody{Error: errorDetail{
			Code: CodeValidationFailed, Message: invalid.Error(),
			Details: map[string]string{"errors": string(b)},
		}})
	case errors.As(err, &stale):
		writeConflict(w, err.Error(), map[string]string{"version": stale.Current})
	case errors.Is(err, trigger.ErrExists):
		writeConflict(w, err.Error(), nil)
	case errors.As(err, &disarmed):
		writeConflict(w, err.Error(), map[string]string{"reason": disarmed.Reason})
	case errors.Is(err, trigger.ErrSignature):
		writeError(w, http.StatusUnauthorized, CodeUnauthorized, err.Error())
	case errors.Is(err, trigger.ErrNotHTTP), errors.Is(err, trigger.ErrNoPoll), errors.As(err, &badEvent):
		writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error())
	case errors.As(err, &poll):
		writeError(w, http.StatusBadRequest, CodeValidationFailed, err.Error())
	default:
		s.internalError(w, what, err)
	}
}
