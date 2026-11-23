package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/reasons"
)

// The issue sync's health, said (#1069). The status was fetched on every
// listing and only `S`'s gate read it, so a project whose import was failing
// looked like a healthy one.
//
// The words for the sync's reasons live here rather than in internal/reasons:
// that catalogue is the task and step reasons, held by its tests to the
// Reason* constants and to docs/reference/task-lifecycle.md's tables. A
// reason this table does not know falls back to it, and then to the code.
var syncReasonWords = map[string]string{ //nolint:gosec // G101: reason words, not credentials
	"github_disabled":   "GitHub is switched off in config.yaml",
	"disabled":          "the issue source is switched off in config.yaml",
	"poll_disabled":     "polling is off (poll_interval is 0)",
	"pending":           "the first import has not run yet",
	"not_github":        "the project's origin is not a github.com repository",
	"source_not_github": "the project has no issue source",
	"no_credential":     "no credential can authenticate",
	"no_client":         "the provider's client is not available",
	"origin_changed":    "the project's origin changed since the last import",
	"binding_changed":   "the project's issue source changed since the last import",
	"rate_limited":      "the provider's rate limit is spent",
	"unauthorized":      "the credential was rejected",
	"forbidden":         "the credential may not read these issues",
	"not_found":         "the repository or project was not found",
	"timeout":           "the provider did not answer in time",
	"unreachable":       "the provider could not be reached",
	"bad_response":      "the provider's answer did not parse",
}

// syncReason words a sync reason.
func syncReason(reason string) string {
	if w, ok := syncReasonWords[reason]; ok {
		return w
	}
	if e := reasons.Explain(reason); e.Title != reason {
		return e.Title
	}
	return strings.ReplaceAll(reason, "_", " ")
}

// syncSummary is the issue source's health for the board's summary edge:
// where it imports from, when it last did, and the write-backs that are
// waiting, failed or conflicted. Empty for a project with no source.
func syncSummary(s apiclient.IssueSyncStatus, now time.Time) (text string, bad bool) {
	if noSyncPass(s) {
		return "", false
	}
	source := firstNonEmpty(s.Source, s.Repo, s.ProviderName, s.Provider)
	parts := []string{source}
	switch {
	case !s.OK && s.Reason == "rate_limited" && s.RateLimitedUntil != nil:
		parts = append(parts, "rate limited until "+s.RateLimitedUntil.Local().Format("15:04"))
		bad = true
	case !s.OK && s.Reason != "":
		parts = append(parts, syncReason(s.Reason))
		bad = s.Reason != "pending"
	case s.LastSyncedAt != nil:
		parts = append(parts, "synced "+syncAgo(now, *s.LastSyncedAt))
	}
	if n := s.WritesFailed; n > 0 {
		parts = append(parts, fmt.Sprintf("%d write-back failed", n))
		bad = true
	}
	if n := s.WritesConflict; n > 0 {
		parts = append(parts, fmt.Sprintf("%d write-back conflicted", n))
		bad = true
	}
	if n := s.WritesPending; n > 0 {
		parts = append(parts, fmt.Sprintf("%d write-back pending", n))
	}
	return strings.Join(parts, " · "), bad
}

// syncAgo is how long ago the last import was, in words.
func syncAgo(now, at time.Time) string {
	d := now.Sub(at)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// syncOutcome is how a pass `S` asked for at asked ended, read off a later
// status: done is false while it has not visibly ended.
func syncOutcome(s apiclient.IssueSyncStatus, asked time.Time) (note string, bad, done bool) {
	if asked.IsZero() {
		return "", false, false
	}
	source := firstNonEmpty(s.Source, s.Repo, s.ProviderName, s.Provider)
	switch {
	case s.LastSyncedAt != nil && !s.LastSyncedAt.Before(asked):
		note = "synced from " + source
		if n := s.WritesFailed + s.WritesConflict; n > 0 {
			return note + fmt.Sprintf(" · %d write-back need attention", n), true, true
		}
		return note, false, true
	case !s.OK && s.Reason != "" && s.Reason != "pending":
		return "the sync from " + source + " did not finish: " + syncReason(s.Reason), true, true
	}
	return "", false, false
}
