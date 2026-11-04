package tui

import (
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
)

func issueSyncFixture(provider, reason string) apiclient.IssueSyncStatus {
	return apiclient.IssueSyncStatus{Provider: provider, Enabled: true, Reason: reason}
}

// The overview glyph reads the bound provider's verdict (task 142.14): a
// Jira project switched off is a dash, as a GitHub one is, and a failing
// Jira sync is ✗.
func TestSyncGlyphJira(t *testing.T) {
	for _, tc := range []struct {
		sync apiclient.IssueSyncStatus
		want string
	}{
		{apiclient.IssueSyncStatus{Provider: "jira", Reason: "disabled"}, overviewDash},
		{apiclient.IssueSyncStatus{Provider: "jira", Enabled: true, Reason: "no_credential"}, "✗"},
		{apiclient.IssueSyncStatus{Provider: "jira", Enabled: true, OK: true}, "✓"},
		{apiclient.IssueSyncStatus{Provider: "none", Reason: "source_not_github"}, overviewDash},
	} {
		st := &apiclient.ProjectStats{}
		st.IssueSync.Enabled, st.IssueSync.OK, st.IssueSync.Reason = tc.sync.Enabled, tc.sync.OK, tc.sync.Reason
		if got := syncGlyph(st); got != tc.want {
			t.Errorf("%+v: glyph = %q, want %q", tc.sync, got, tc.want)
		}
	}
}

// "sync requested from …" names the rendered source, Jira's included, and
// falls back to the repo a daemon that sends no source carries.
func TestSyncedNoteNamesTheSource(t *testing.T) {
	for _, tc := range []struct {
		status apiclient.IssueSyncStatus
		want   string
	}{
		{apiclient.IssueSyncStatus{Provider: "jira", Enabled: true, Source: "acme.atlassian.net/PROJ", Reason: "pending"}, "sync requested from acme.atlassian.net/PROJ — the list updates as it lands"},
		{apiclient.IssueSyncStatus{Provider: "github", Enabled: true, Repo: "o/r", Source: "o/r"}, "sync requested from o/r — the list updates as it lands"},
		{apiclient.IssueSyncStatus{Provider: "github", Enabled: true, Repo: "o/r"}, "sync requested from o/r — the list updates as it lands"},
	} {
		if got := syncedNote(issuesSyncedMsg{status: tc.status}); got != tc.want {
			t.Errorf("%+v: note = %q, want %q", tc.status, got, tc.want)
		}
	}
}
