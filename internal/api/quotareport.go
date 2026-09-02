package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/store"
)

// agentQuotaReportRequest is the body of `POST /v1/agents/{name}/quota`
// (§9.6, task 082): a usage reading a source is *pushing*, because there is no
// probe that could go and fetch it.
//
// claude is the case that needs this. Its usage windows reach vincent through
// the status line — the CLI runs `vincent statusline` to draw itself and hands
// that process the numbers — so the reading arrives at a moment the daemon did
// not choose. codex, whose app-server answers on request, needs none of this
// and goes through agent.QuotaReporter instead.
type agentQuotaReportRequest struct {
	Source  string                   `json:"source"`
	Windows []agentQuotaReportWindow `json:"windows"`
}

type agentQuotaReportWindow struct {
	Name        string  `json:"name"`
	UsedPercent float64 `json:"used_percent"`
	Window      string  `json:"window"`
	// ResetsAt is optional: a source that did not name a reset omits it, and
	// the reading is served with `resets_at_reported: false`. It is a pointer
	// rather than a bare time.Time because those are different statements and
	// a zero-value time on the wire would be a claim about the year 1.
	ResetsAt *time.Time `json:"resets_at,omitempty"`
}

// handleAgentQuotaReport records a pushed usage reading (204 No Content).
//
// It is registered beside the other /v1/agents rows so it rides the same
// recover → log → auth chain and the same bearer token as everything else: a
// status line runs as the user, reads the token the way any client does, and
// gets no special path into the daemon.
//
// The reading lands in the catalog cache and nowhere else — no table, no
// migration (task 082 decision 4) — so it is exactly as durable as the daemon
// and a restart drops it until the next render pushes again. That is the
// intended trade: a percentage nothing has confirmed since the daemon started
// is one vincent should not be showing.
func (s *Server) handleAgentQuotaReport(w http.ResponseWriter, r *http.Request) {
	if s.deps.Catalog == nil {
		s.internalError(w, "agent catalog", errors.New("no agent catalog is configured"))
		return
	}
	name := r.PathValue("name")
	var req agentQuotaReportRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if msg := validateQuotaReport(req); msg != "" {
		writeError(w, http.StatusBadRequest, CodeValidationFailed, msg)
		return
	}
	// The 404 is decided before SetQuota rather than from its return, because
	// its false is overloaded: "no such adapter" and "you told me that
	// already" are the same bool and only one of them is an error.
	if !s.knownAgent(name) {
		writeError(w, http.StatusNotFound, CodeNotFound,
			fmt.Sprintf("unknown agent %q", name))
		return
	}
	q := &agent.ReportedQuota{
		Source:     req.Source,
		ReportedAt: time.Now().UTC(),
		Windows:    make([]agent.ReportedWindow, 0, len(req.Windows)),
	}
	for _, win := range req.Windows {
		rw := agent.ReportedWindow{
			Name:        win.Name,
			UsedPercent: win.UsedPercent,
			Window:      win.Window,
		}
		if win.ResetsAt != nil {
			rw.ResetsAt = win.ResetsAt.UTC()
		}
		q.Windows = append(q.Windows, rw)
	}
	if s.deps.Catalog.SetQuota(name, q) {
		s.emitQuotaChanged(r, name, q)
	}
	w.WriteHeader(http.StatusNoContent)
}

// knownAgent reports whether the catalog serves this adapter.
func (s *Server) knownAgent(name string) bool {
	for _, n := range s.deps.Catalog.Names() {
		if n == name {
			return true
		}
	}
	return false
}

// validateQuotaReport rejects a body that says nothing usable. It is
// deliberately thin: this route's job is to accept whatever a vendor's surface
// reports, and a percentage over 100 is a reading, not a malformed body.
func validateQuotaReport(req agentQuotaReportRequest) string {
	if req.Source == "" {
		return "source is required"
	}
	if len(req.Windows) == 0 {
		return "windows must name at least one window"
	}
	for i, w := range req.Windows {
		if w.Name == "" {
			return fmt.Sprintf("windows[%d].name is required", i)
		}
		if w.UsedPercent < 0 {
			return fmt.Sprintf("windows[%d].used_percent must not be negative", i)
		}
	}
	return ""
}

// emitQuotaChanged appends the durable `agent.quota_changed` event for a
// reading that actually changed something (§13.3).
//
// The payload is the shape store's own upsert path builds, field for field: a
// subscriber must not be able to tell whether the news came from a `usage_limit`
// stop or from a status line, only what it now says. `scheduler.WakeOn` is
// false for this type and stays false — quota is display, never admission
// (task 026), and a status line re-rendering must not spin the scheduler.
//
// A failed append is logged, not returned: the reading is already in the
// catalog and the next poll of /v1/agents carries it. Losing the nudge is not
// worth failing a push the user cannot see or retry.
func (s *Server) emitQuotaChanged(r *http.Request, name string, q *agent.ReportedQuota) {
	if s.deps.Store == nil {
		return
	}
	spent := false
	var resets *string
	if len(q.Windows) > 0 {
		t := tightestWindow(q.Windows)
		spent = t.UsedPercent >= 100
		if !t.ResetsAt.IsZero() {
			at := t.ResetsAt.UTC().Format(time.RFC3339)
			resets = &at
		}
	}
	payload, err := json.Marshal(map[string]any{
		"agent": name, "spent": spent, "resets_at": resets, "source": &q.Source,
	})
	if err != nil {
		s.deps.Logger.Warn("marshal agent quota event", "agent", name, "error", err)
		return
	}
	ev := &store.Event{Type: store.EventAgentQuotaChanged, Payload: payload}
	if err := s.deps.Store.AppendEvent(r.Context(), ev); err != nil {
		s.deps.Logger.Warn("append agent quota event", "agent", name, "error", err)
	}
}
