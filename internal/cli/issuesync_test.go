package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
)

// TestGitHubStatusRowsNameImportSync: `vincent github status` reports the
// credential as what issue import and sync would use (task 130.8), not as a
// picker's read.
func TestGitHubStatusRowsNameImportSync(t *testing.T) {
	out := joinRows(githubStatusRows(apiclient.GitHubStatus{Enabled: true, Repo: "o/r", Available: true, Via: "gh"}))
	if !strings.Contains(out, "issue import/sync\tavailable via gh") || !strings.Contains(out, "repo\to/r") {
		t.Errorf("available status:\n%s", out)
	}
	out = joinRows(githubStatusRows(apiclient.GitHubStatus{Enabled: true, Message: "not a GitHub repository"}))
	if !strings.Contains(out, "issue import/sync\tunavailable: not a GitHub repository") {
		t.Errorf("unavailable status:\n%s", out)
	}
}

// TestIssueSyncRows: the human rendering names every field of the status,
// and a request made while a switch is off says nothing will happen.
func TestIssueSyncRows(t *testing.T) {
	at := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	out := joinRows(issueSyncRows(apiclient.IssueSyncStatus{
		Enabled: true, Repo: "o/r", LastSyncedAt: &at, OK: true, ImportComplete: true,
	}, true))
	for _, want := range []string{"enabled\tyes", "repo\to/r", "last synced\t2026-", "ok\tyes", "reason\t-", "import complete\tyes", "sync\trequested"} {
		if !strings.Contains(out, want) {
			t.Errorf("ok status does not show %q:\n%s", want, out)
		}
	}
	out = joinRows(issueSyncRows(apiclient.IssueSyncStatus{Repo: "o/r", Reason: "github_disabled"}, true))
	for _, want := range []string{"enabled\tno", "last synced\tnever", "reason\tgithub_disabled", "requested, but import is off (github_disabled)"} {
		if !strings.Contains(out, want) {
			t.Errorf("disabled status does not show %q:\n%s", want, out)
		}
	}
	// A project with no provider pass records no request at all (review
	// F3): the row must not claim one, whether or not GitHub's switch is on.
	for _, s := range []apiclient.IssueSyncStatus{
		{Provider: "none", Reason: "not_github"},
		{Provider: "none", Reason: "source_not_github"},
		{Provider: "github", Enabled: true, Reason: "not_github"},
	} {
		out := joinRows(issueSyncRows(s, true))
		if !strings.Contains(out, "sync\tnothing to sync ("+s.Reason+")") || strings.Contains(out, "requested") {
			t.Errorf("%+v: sync now claims a request:\n%s", s, out)
		}
	}
	if out := joinRows(issueSyncRows(apiclient.IssueSyncStatus{Enabled: true}, false)); strings.Contains(out, "requested") {
		t.Errorf("--status claims a request:\n%s", out)
	}
}

// TestIssueSyncRowsNameTheSource: the provider and source rows print the
// daemon's renderings (task 142.14), a dash for a project with none, and
// the help no longer promises GitHub alone.
func TestIssueSyncRowsNameTheSource(t *testing.T) {
	out := joinRows(issueSyncRows(apiclient.IssueSyncStatus{
		Enabled: true, Provider: "jira", ProviderName: "Jira", Source: "acme.atlassian.net/PROJ", Reason: "pending",
	}, true))
	for _, want := range []string{"provider\tJira", "source\tacme.atlassian.net/PROJ", "repo\t-"} {
		if !strings.Contains(out, want) {
			t.Errorf("jira status does not show %q:\n%s", want, out)
		}
	}
	if p, src, r := strings.Index(out, "provider\t"), strings.Index(out, "source\t"), strings.Index(out, "repo\t"); p >= src || src >= r {
		t.Errorf("provider and source are not above repo:\n%s", out)
	}
	out = joinRows(issueSyncRows(apiclient.IssueSyncStatus{Provider: "none", Reason: "source_not_github"}, false))
	for _, want := range []string{"provider\t-", "source\t-"} {
		if !strings.Contains(out, want) {
			t.Errorf("none status does not show %q:\n%s", want, out)
		}
	}
	if short := newIssueSyncCmd().Short; strings.Contains(short, "GitHub") {
		t.Errorf("issue sync's help still names GitHub: %q", short)
	}
}

// TestIssueSyncDoctorRows: doctor's GitHub row lists each project's sync
// health beside the credential check.
func TestIssueSyncDoctorRows(t *testing.T) {
	row := apiclient.DoctorGitHub{Enabled: true, Usable: true, Sync: []apiclient.DoctorProjectIssueSync{
		{ProjectID: 1, Project: "web", OK: true, ImportComplete: true},
		{ProjectID: 2, Reason: "rate_limited"},
	}}
	row.Via = "gh"
	out := joinRows(doctorGitHubRows(row))
	for _, want := range []string{"sync web\tok  last synced never", "sync project 2\tnot ok (rate_limited)", "import incomplete"} {
		if !strings.Contains(out, want) {
			t.Errorf("doctor sync rows do not show %q:\n%s", want, out)
		}
	}
}
