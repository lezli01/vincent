package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/doctor"
	"github.com/lezli01/vincent/internal/store"
)

// The issue sync status routes (spec §5.6, §13.2, task 130.8). The daemon's
// reconciler tick imports and refreshes a GitHub-based project's issues and
// records how that went in the store's issue_sync_state row; these routes
// read that row back, and the POST asks for a sync now. Neither ever calls
// GitHub itself — the importer is the only thing that does.

// The sync reasons this file computes from the live config rather than from
// the stored row. They are not internal/reasons rows: a sync is not a task,
// and nothing about it blocks one.
const (
	// syncReasonDisabled is `github.enabled: false`.
	syncReasonDisabled = "github_disabled"
	// syncReasonPollDisabled is `github.poll_interval: 0`: the integration
	// is on, the background tick is not.
	syncReasonPollDisabled = "poll_disabled"
	// syncReasonPending is a project the importer has not attempted yet.
	syncReasonPending = "pending"
)

// issueSyncProvider is the one provider an import comes from today.
const issueSyncProvider = "github"

// issueSyncStatusBody is GET and POST /v1/projects/{id}/issues/sync.
type issueSyncStatusBody struct {
	// Enabled is whether the reconciler tick imports at all:
	// github.enabled and a non-zero github.poll_interval.
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider"`
	Repo     string `json:"repo"`
	// LastSyncedAt is when an import last succeeded; omitted when none has.
	LastSyncedAt *time.Time `json:"last_synced_at,omitempty"`
	OK           bool       `json:"ok"`
	// Reason names why OK is false. The config switches take precedence
	// over whatever the importer last stored.
	Reason string `json:"reason,omitempty"`
	// ImportComplete is whether the first full import has finished.
	ImportComplete   bool       `json:"import_complete"`
	RateLimitedUntil *time.Time `json:"rate_limited_until,omitempty"`
	// WritesPending, WritesFailed and WritesConflict count the project's
	// imported issues whose newest state write-back is waiting, gave up,
	// or found GitHub changed first (task 130.10).
	WritesPending  int `json:"writes_pending"`
	WritesFailed   int `json:"writes_failed"`
	WritesConflict int `json:"writes_conflict"`
}

// renderIssueSync folds the live config over a project's stored sync row,
// nil when it has none. A row that exists only because a sync was requested
// (no attempt yet) is still pending, not ok: nothing has been imported.
func renderIssueSync(gh config.GitHub, st *store.IssueSyncState) issueSyncStatusBody {
	out := issueSyncStatusBody{Enabled: gh.Polls(), Provider: issueSyncProvider}
	if st != nil {
		if st.Provider != "" {
			out.Provider = st.Provider
		}
		out.Repo = st.Repo
		out.LastSyncedAt = st.LastOKAt
		out.OK = st.OK
		out.Reason = st.Reason
		out.ImportComplete = st.ImportComplete
		out.RateLimitedUntil = st.RateLimitedUntil
	}
	switch {
	case !gh.Enabled:
		out.OK, out.Reason = false, syncReasonDisabled
	case gh.PollInterval <= 0:
		out.OK, out.Reason = false, syncReasonPollDisabled
	case st == nil || st.LastAttemptAt == nil:
		out.OK, out.Reason = false, syncReasonPending
	case out.OK:
		out.Reason = ""
	}
	return out
}

// issueSyncStatus reads a project's status. The repo falls back to the one
// its origin names (a local git call, never a GitHub one) while the importer
// has not recorded one.
func (s *Server) issueSyncStatus(ctx context.Context, project *store.Project) (issueSyncStatusBody, error) {
	st, err := s.deps.Store.GetIssueSyncState(ctx, project.ID)
	if errors.Is(err, store.ErrNotFound) {
		st, err = nil, nil
	}
	if err != nil {
		return issueSyncStatusBody{}, err
	}
	out := renderIssueSync(s.deps.Config().GitHub, st)
	counts, err := s.deps.Store.CountIssueWrites(ctx, project.ID)
	if err != nil {
		return issueSyncStatusBody{}, err
	}
	out.WritesPending, out.WritesFailed, out.WritesConflict = counts.Pending, counts.Failed, counts.Conflict
	if out.Repo == "" {
		if repo, ok := s.githubRepo(ctx, project); ok {
			out.Repo = repo.String()
		}
	}
	return out, nil
}

// handleIssueSyncStatus implements GET /v1/projects/{id}/issues/sync.
func (s *Server) handleIssueSyncStatus(w http.ResponseWriter, r *http.Request) {
	project, ok := s.projectFromPath(w, r)
	if !ok {
		return
	}
	out, err := s.issueSyncStatus(r.Context(), project)
	if err != nil {
		s.internalError(w, "read issue sync state", err)
		return
	}
	writeJSON(w, http.StatusOK, out)
}

// handleIssueSyncNow implements POST /v1/projects/{id}/issues/sync: sync
// now. It only records the request and wakes the importer, so it answers 202
// with the status as it stands — the sync itself lands on the importer's
// goroutine and announces itself with issue.sync_changed when it changes the
// verdict. With a switch off the request is recorded and the status names
// the switch; nothing is imported until it is turned back on. The same
// request wakes the state write-back drain (task 130.10), which is why this
// route is a human's only (decision 15.8).
func (s *Server) handleIssueSyncNow(w http.ResponseWriter, r *http.Request) {
	project, ok := s.projectFromPath(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	if err := s.deps.Store.RequestIssueSync(ctx, project.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, CodeNotFound, "project not found")
			return
		}
		s.internalError(w, "request issue sync", err)
		return
	}
	out, err := s.issueSyncStatus(ctx, project)
	if err != nil {
		s.internalError(w, "read issue sync state", err)
		return
	}
	writeJSON(w, http.StatusAccepted, out)
}

// fillIssueSync adds each project's sync health to doctor's GitHub row. Like
// the rest of that row it is information, never a Problem; a failed read
// leaves the list empty rather than failing the report.
func (s *Server) fillIssueSync(ctx context.Context, rep *doctor.Report) {
	if s.deps.Store == nil {
		return
	}
	states, err := s.deps.Store.ListIssueSyncStates(ctx)
	if err != nil {
		s.deps.Logger.Warn("doctor: list issue sync states", "error", err)
		return
	}
	gh := s.deps.Config().GitHub
	for i := range states {
		st := &states[i]
		name := ""
		if p, err := s.deps.Store.GetProject(ctx, st.ProjectID); err == nil {
			name = p.Name
		}
		body := renderIssueSync(gh, st)
		counts, err := s.deps.Store.CountIssueWrites(ctx, st.ProjectID)
		if err != nil {
			s.deps.Logger.Warn("doctor: count issue writes", "project", st.ProjectID, "error", err)
		}
		rep.GitHub.Sync = append(rep.GitHub.Sync, doctor.ProjectIssueSync{
			ProjectID:      st.ProjectID,
			Project:        name,
			OK:             body.OK,
			Reason:         body.Reason,
			LastSyncedAt:   body.LastSyncedAt,
			ImportComplete: body.ImportComplete,
			WritesPending:  counts.Pending,
			WritesFailed:   counts.Failed,
			WritesConflict: counts.Conflict,
		})
	}
}
