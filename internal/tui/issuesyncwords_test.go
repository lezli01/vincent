package tui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// #1069: the board's summary edge says where issues come from, when they
// last did, and what write-backs failed — and a failing import says so.
func TestIssueBoardShowsSyncHealth(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	synced := now.Add(-5 * time.Minute)
	healthy := apiclient.IssueSyncStatus{
		Enabled: true, Provider: "github", Source: "octo/web", OK: true, LastSyncedAt: &synced,
		WritesFailed: 1, WritesPending: 2,
	}
	text, bad := syncSummary(healthy, now)
	if text != "octo/web · synced 5m ago · 1 write-back failed · 2 write-back pending" || !bad {
		t.Errorf("healthy summary = %q (bad %v)", text, bad)
	}
	failing := apiclient.IssueSyncStatus{Enabled: true, Provider: "github", Source: "octo/web", Reason: "unauthorized"}
	if text, bad := syncSummary(failing, now); text != "octo/web · the credential was rejected" || !bad {
		t.Errorf("failing summary = %q (bad %v)", text, bad)
	}
	if text, _ := syncSummary(apiclient.IssueSyncStatus{Provider: "none", Reason: "source_not_github"}, now); text != "" {
		t.Errorf("a project with no source has a summary: %q", text)
	}

	v := issuesFixture()
	v.now = func() time.Time { return now }
	v.sync, v.syncFor = &failing, v.project.id
	if got := ansi.Strip(strings.Join(v.filterBox(160), "\n")); !strings.Contains(got, "the credential was rejected") {
		t.Errorf("the board's edge hides the failing import:\n%s", got)
	}
}

// #1069: `S` reports how the pass it asked for ended.
func TestSyncOutcomeIsReported(t *testing.T) {
	asked := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	after := asked.Add(time.Second)
	if _, _, done := syncOutcome(apiclient.IssueSyncStatus{Source: "o/r", Reason: "pending"}, asked); done {
		t.Fatal("a pending pass counted as ended")
	}
	if note, bad, done := syncOutcome(apiclient.IssueSyncStatus{Source: "o/r", OK: true, LastSyncedAt: &after}, asked); !done || bad || note != "synced from o/r" {
		t.Errorf("a finished pass said %q (bad %v, done %v)", note, bad, done)
	}
	if note, bad, done := syncOutcome(apiclient.IssueSyncStatus{Source: "o/r", Reason: "rate_limited"}, asked); !done || !bad ||
		note != "the sync from o/r did not finish: the provider's rate limit is spent" {
		t.Errorf("a failed pass said %q (bad %v, done %v)", note, bad, done)
	}
}

// Review finding 7: an unhealthy source's line is the longest the edge
// carries, and the edge drops a label whole that does not fit. At every
// width the failure stays on screen and the issue count with it.
func TestIssueSyncHealthSurvivesNarrowWidths(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	for name, s := range map[string]apiclient.IssueSyncStatus{
		"origin_changed": {Enabled: true, Provider: "github", Source: "octo/web", Reason: "origin_changed"},
		"jira unauthorized": {
			Enabled: true, Provider: "jira", Source: "example.atlassian.net/PLAT", Reason: "unauthorized",
			WritesFailed: 3, WritesConflict: 1, WritesPending: 2,
		},
	} {
		v := issuesFixture()
		v.now = func() time.Time { return now }
		v.lastLoad = now
		v.sync, v.syncFor = &s, v.project.id
		want, _ := syncSummary(s, now)
		for _, w := range []int{80, 100, 120} {
			got := ansi.Strip(strings.Join(v.filterBox(w), "\n"))
			if !strings.Contains(got, syncReason(s.Reason)) {
				t.Errorf("%s at %d: the failing source is not on screen (%q):\n%s", name, w, want, got)
			}
			if !strings.Contains(got, plural(len(v.issues), "issue", "issues")) {
				t.Errorf("%s at %d: the issue count is gone:\n%s", name, w, got)
			}
			for _, line := range strings.Split(got, "\n") {
				if lw := ansi.StringWidth(line); lw > w {
					t.Errorf("%s at %d: a %d-cell line", name, w, lw)
				}
			}
		}
	}
}
