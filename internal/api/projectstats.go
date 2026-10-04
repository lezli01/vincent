package api

import (
	"context"
	"net/http"
	"time"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
)

// projectWithStats is a project row as GET /v1/projects?stats=true and
// GET /v1/projects/{id}?stats=true serve it (§13.2, task 132.1). It is a
// type of its own rather than an omitempty field on projectResponse so the
// default shape stays byte-identical, and so a failed count can still say
// `"stats": null` rather than vanishing.
type projectWithStats struct {
	projectResponse
	Stats *projectStatsResponse `json:"stats"`
}

type projectStatsResponse struct {
	Tasks     projectTaskStats  `json:"tasks"`
	Issues    projectIssueStats `json:"issues"`
	Chats     projectChatStats  `json:"chats"`
	IssueSync projectSyncStats  `json:"issue_sync"`
	// LastActivityAt is the newest updated_at over the project's tasks,
	// issues and chats; null when it has none.
	LastActivityAt *time.Time `json:"last_activity_at"`
}

// projectTaskStats counts every non-archived task row, lanes included, so it
// lines up with slots_used. Its `active` is therefore not an issue's
// `active`, which counts root tasks only (task 132 decision 21).
type projectTaskStats struct {
	// ByState omits zero states and never carries `archived`.
	ByState   map[string]int `json:"by_state"`
	Active    int            `json:"active"`
	Attention int            `json:"attention"`
}

type projectIssueStats struct {
	Open         int `json:"open"`
	OpenImported int `json:"open_imported"`
	Active       int `json:"active"`
}

// projectChatStats is kept apart from the task figures: chat attention is
// never folded into the task attention count (spec decision row 29).
type projectChatStats struct {
	Live          int `json:"live"`
	AwaitingInput int `json:"awaiting_input"`
}

// projectSyncStats is the stored sync health under the live config switches.
// Unlike GET /v1/projects/{id}/issues/sync it never asks git for the repo:
// a list must not run a subprocess per row.
type projectSyncStats struct {
	Enabled      bool       `json:"enabled"`
	OK           bool       `json:"ok"`
	Reason       string     `json:"reason"`
	LastSyncedAt *time.Time `json:"last_synced_at"`
}

// parseStatsParam reads the opt-in `stats` query parameter the way the task
// list reads `include_children`: absent or false is the default shape, true
// adds the object, and anything else is the caller's mistake.
func parseStatsParam(w http.ResponseWriter, r *http.Request) (withStats, ok bool) {
	switch r.URL.Query().Get("stats") {
	case "", "false":
		return false, true
	case "true":
		return true, true
	default:
		writeError(w, http.StatusBadRequest, CodeValidationFailed, "stats must be true or false")
		return false, false
	}
}

// projectStats reads every project's counts. A failed read degrades to a
// log line and ok false, which renders `"stats": null` per row — the
// slots_used precedent: the rows are in hand, and the counts are a
// decoration on them, not the answer asked for.
func (s *Server) projectStats(ctx context.Context) (map[int64]store.ProjectStats, bool) {
	read := s.statsSource
	if read == nil {
		read = s.deps.Store.ProjectStats
	}
	stats, err := read(ctx)
	if err != nil {
		s.deps.Logger.Warn("read project stats", "error", err)
		return nil, false
	}
	return stats, true
}

func (s *Server) toProjectStats(stats map[int64]store.ProjectStats, ok bool, id int64) *projectStatsResponse {
	if !ok {
		return nil
	}
	st := stats[id]
	byState := make(map[string]int, len(st.TasksByState))
	for state, n := range st.TasksByState {
		if n > 0 && state != taskstate.Archived {
			byState[string(state)] = n
		}
	}
	out := &projectStatsResponse{
		Tasks:  projectTaskStats{ByState: byState, Active: st.TasksActive, Attention: st.TasksAttention},
		Issues: projectIssueStats{Open: st.IssuesOpen, OpenImported: st.IssuesOpenImported, Active: st.IssuesActive},
		Chats:  projectChatStats{Live: st.ChatsLive, AwaitingInput: st.ChatsAwaitingInput},
	}
	if s.deps.Config != nil {
		health := renderIssueSync(s.deps.Config().GitHub, st.IssueSync)
		out.IssueSync = projectSyncStats{
			Enabled: health.Enabled, OK: health.OK, Reason: health.Reason, LastSyncedAt: health.LastSyncedAt,
		}
	}
	if st.LastActivityAt != nil {
		at := st.LastActivityAt.UTC()
		out.LastActivityAt = &at
	}
	return out
}
