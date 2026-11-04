package api

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/doctor"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/issuesource"
	"github.com/lezli01/vincent/internal/store"
)

// The issue sync status routes (spec §5.6, §13.2, task 130.8, task 142.7).
// The daemon's per-provider sync loop imports and refreshes a project's
// issues from the provider its issue source resolves to (GitHub or Jira) and
// records how that went in the store's issue_sync_state row; these routes
// read that row back, and the POST asks for a sync now. Neither ever calls a
// provider itself — the importer is the only thing that does.

// The sync reasons this file computes from the live config and the
// project's resolution rather than from the stored row. They are not
// internal/reasons rows: a sync is not a task, and nothing about it blocks
// one.
const (
	// syncReasonDisabled is `github.enabled: false` on a GitHub row.
	syncReasonDisabled = "github_disabled"
	// syncReasonJiraDisabled is `jira.enabled: false` on a Jira row: the
	// provider-neutral spelling, since "github_disabled" would blame a
	// switch that has nothing to do with a Jira project.
	syncReasonJiraDisabled = issuesource.ReasonDisabled
	// syncReasonPollDisabled is the provider's `poll_interval: 0`: the
	// integration is on, the background tick is not.
	syncReasonPollDisabled = "poll_disabled"
	// syncReasonPending is a github or jira project the importer has not
	// attempted yet.
	syncReasonPending = "pending"
	// syncReasonNotGitHub is a derived project whose origin is not a
	// github.com remote, or an explicit github binding on such an origin:
	// the daemon's spelling (internal/daemon/issuesync.go), derived here for
	// a project that has no row because the daemon never writes one for a
	// project that resolves to none (task 142.7).
	syncReasonNotGitHub = "not_github"
	// syncReasonSourceNotGitHub is a project explicitly bound to none.
	syncReasonSourceNotGitHub = "source_not_github"
)

// issueSyncStatusBody is GET and POST /v1/projects/{id}/issues/sync.
type issueSyncStatusBody struct {
	// Enabled is whether the provider's sync loop imports at all: its
	// `enabled` and a non-zero `poll_interval` (github's or jira's); false
	// for a project whose source resolves to none.
	Enabled  bool   `json:"enabled"`
	Provider string `json:"provider"`
	Repo     string `json:"repo"`
	// Site is the bound Jira site when the project's issue source is jira
	// (task 142.6 decision 5): shown wherever sync status is, so a binding
	// entered on the wrong site is visible before a token is sent there.
	Site string `json:"site,omitempty"`
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
// nil when it has none. The config folded is the row's provider's — a Jira
// row is never reported github_disabled because github.enabled is false —
// or, with no row, the resolved provider's. A row that exists only because a
// sync was requested (no attempt yet) is still pending, not ok: nothing has
// been imported.
//
// With no row the reason is derived from res rather than reported pending:
// the daemon never writes a row for a project that resolves to none, so a
// row-less project is "not attempted yet" only when it has a provider to
// attempt it with.
func renderIssueSync(cfg config.Config, res issuesource.Resolution, st *store.IssueSyncState) issueSyncStatusBody {
	provider := res.Provider
	if st != nil {
		provider = st.Provider
		if provider == "" {
			// A row written before the provider column was stamped is a
			// GitHub import's: there was no other kind.
			provider = store.IssueProviderGitHub
		}
	}
	out := issueSyncStatusBody{Provider: provider}
	if st != nil {
		out.Repo = st.Repo
		out.LastSyncedAt = st.LastOKAt
		out.OK = st.OK
		out.Reason = st.Reason
		out.ImportComplete = st.ImportComplete
		out.RateLimitedUntil = st.RateLimitedUntil
	}
	var enabled bool
	var poll config.Duration
	switch provider {
	case store.IssueProviderGitHub:
		out.Enabled, enabled, poll = cfg.GitHub.Polls(), cfg.GitHub.Enabled, cfg.GitHub.PollInterval
	case store.IssueProviderJira:
		out.Enabled, enabled, poll = cfg.Jira.Polls(), cfg.Jira.Enabled, cfg.Jira.PollInterval
	default:
		// none: no loop is this project's, so no switch applies. Only a
		// row-less project gets here (the daemon writes none for it).
		out.OK, out.Reason = false, syncReasonNotGitHub
		if res.Explicit {
			out.Reason = syncReasonSourceNotGitHub
		}
		return out
	}
	switch {
	case !enabled && provider == store.IssueProviderJira:
		out.OK, out.Reason = false, syncReasonJiraDisabled
	case !enabled:
		out.OK, out.Reason = false, syncReasonDisabled
	case poll <= 0:
		out.OK, out.Reason = false, syncReasonPollDisabled
	case st == nil && provider == store.IssueProviderGitHub && !res.OriginGitHub:
		// An explicit github binding on an origin that names no github.com
		// repository: the importer will say not_github on its first pass, so
		// say it now rather than promise a sync that cannot happen.
		out.OK, out.Reason = false, syncReasonNotGitHub
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
	res := s.issueResolution(ctx, project)
	out := renderIssueSync(s.deps.Config(), res, st)
	counts, err := s.deps.Store.CountIssueWrites(ctx, project.ID)
	if err != nil {
		return issueSyncStatusBody{}, err
	}
	out.WritesPending, out.WritesFailed, out.WritesConflict = counts.Pending, counts.Failed, counts.Conflict
	if project.IssueSource.Provider == store.IssueProviderJira {
		out.Site = project.IssueSource.JiraSite
	}
	if out.Repo == "" && res.Provider == store.IssueProviderGitHub && res.OriginGitHub && res.Repo != (github.Repo{}) {
		out.Repo = res.Repo.String()
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
// with the status as it stands. The request is stamped with the project's
// resolved provider, which is how the daemon chooses which provider's pass
// to wake (task 142.7); a project that resolves to none — or to github on an
// origin that is not a github.com remote — has no pass to wake,
// so nothing is recorded — no row is created for it — and the answer is the
// derived not_github or source_not_github status — the sync itself lands on the importer's
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
	res := s.issueResolution(ctx, project)
	// github pinned on an origin that names no github.com repository has
	// no pass to wake either (review F2): the daemon's GitHub pass stands
	// down on it, so a recorded request would never be served and the
	// project would read pending for ever instead of not_github. Without a
	// git runner the origin reads as GitHub's (issueResolution), so this
	// only fires on an origin actually read.
	if res.Provider == store.IssueProviderNone || (res.Provider == store.IssueProviderGitHub && !res.OriginGitHub) {
		out, err := s.issueSyncStatus(ctx, project)
		if err != nil {
			s.internalError(w, "read issue sync state", err)
			return
		}
		writeJSON(w, http.StatusAccepted, out)
		return
	}
	if err := s.deps.Store.RequestIssueSync(ctx, project.ID, res.Provider); err != nil {
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
	cfg := s.deps.Config()
	for i := range states {
		st := &states[i]
		name := ""
		if p, err := s.deps.Store.GetProject(ctx, st.ProjectID); err == nil {
			name = p.Name
		}
		// A stored row carries its provider, and a row is folded by it
		// alone; the resolution only matters to a project with no row.
		body := renderIssueSync(cfg, issuesource.Resolution{Provider: st.Provider}, st)
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
