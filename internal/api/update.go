package api

import (
	"net/http"
	"time"

	"github.com/lezli01/vincent/internal/release"
	"github.com/lezli01/vincent/internal/version"
)

// GET /v1/update — the daemon's cached release check (task 055, spec §13.2).
//
// It serves the cache and nothing else. There is deliberately no `?refresh=`
// parameter: the whole promise of `update.check: false` is that the daemon
// makes no outbound request, and a parameter that makes one on demand hands
// any client the ability to break that. `vincent update --check` queries the
// feed directly instead, which also makes it work before the first poll and
// with no daemon running at all (decision 3).

// updateResponse is the GET /v1/update body.
//
// `checked_at` is null and `latest` empty until a poll succeeds. That is the
// never-polled state and it is distinct from "no update available": a daemon
// that started ten seconds ago does not know yet, and a client rendering the
// two the same way claims a check that never happened.
type updateResponse struct {
	// Enabled is whether the background poll is configured to run, as of the
	// last tick.
	Enabled bool `json:"enabled"`
	// CurrentVersion is the running daemon's build, so a client can render
	// the comparison without a second request to /v1/info.
	CurrentVersion string `json:"current_version"`
	// LatestVersion is the newest stable release seen, empty until a poll
	// succeeds. Prereleases never appear here.
	LatestVersion string `json:"latest_version"`
	// UpdateAvailable is the daemon's own verdict. It is computed here, not
	// left to the client, so every client agrees — and so the `dev`-build
	// rule (a build from source is never "behind") lives in one place.
	UpdateAvailable bool       `json:"update_available"`
	PublishedAt     *time.Time `json:"published_at"`
	ReleaseURL      string     `json:"release_url,omitempty"`
	CheckedAt       *time.Time `json:"checked_at"`
	// Error is why the last poll failed, empty when it worked. A check that
	// is quietly failing looks identical to one that has nothing to report,
	// and `vincent doctor` needs to be able to tell a user which it is.
	Error string `json:"error,omitempty"`
}

func (s *Server) handleUpdate(w http.ResponseWriter, _ *http.Request) {
	var st release.Status
	if s.deps.UpdateStatus != nil {
		st = s.deps.UpdateStatus()
	}
	current := version.Version()
	out := updateResponse{
		Enabled:         st.Enabled,
		CurrentVersion:  current,
		LatestVersion:   st.Latest.Version,
		UpdateAvailable: st.UpdateAvailable(current),
		ReleaseURL:      st.Latest.URL,
		Error:           st.Error,
	}
	if !st.CheckedAt.IsZero() {
		t := st.CheckedAt.UTC()
		out.CheckedAt = &t
	}
	if !st.Latest.PublishedAt.IsZero() {
		t := st.Latest.PublishedAt.UTC()
		out.PublishedAt = &t
	}
	writeJSON(w, http.StatusOK, out)
}
