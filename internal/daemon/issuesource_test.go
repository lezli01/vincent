package daemon

import (
	"testing"

	"github.com/lezli01/vincent/internal/store"
)

// TestIssueSyncHonoursTheBinding is task 142.6 decision 3: a github.com
// project bound to none or jira makes no GitHub issue or comment call and
// records source_not_github, and its pull requests are still linked.
func TestIssueSyncHonoursTheBinding(t *testing.T) {
	for _, src := range []store.IssueSource{
		{Provider: store.IssueProviderNone},
		{Provider: store.IssueProviderJira, JiraSite: "https://acme.atlassian.net", JiraProjectKey: "ENG"},
	} {
		t.Run(src.Provider, func(t *testing.T) {
			f := newFixture(t, "https://github.com/octo/repo.git")
			newCorpus(t, issueRow(1, "open"))
			f.project.IssueSource = src
			if err := f.store.UpdateProject(t.Context(), &f.project); err != nil {
				t.Fatal(err)
			}
			f.reconciler().Tick(t.Context())
			if calls := f.apiCalls(t); len(calls) != 0 {
				t.Errorf("a project bound to %s made issue calls: %v", src.Provider, calls)
			}
			if st := f.syncState(t); st.OK || st.Reason != syncReasonSourceNotGitHub {
				t.Errorf("sync state = %+v, want %s", st, syncReasonSourceNotGitHub)
			}
			if got := len(f.issues(t)); got != 0 {
				t.Errorf("imported %d issues, want none", got)
			}
			if l := f.link(t); l == nil || !l.Linked() || l.Number != 412 {
				t.Errorf("pull request link = %+v, want #412 still linked", l)
			}
		})
	}
}

// A github.com project left to derive still imports: the migrated default
// changes nothing.
func TestIssueSyncDerivedStillImports(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	newCorpus(t, issueRow(1, "open"))
	f.reconciler().Tick(t.Context())
	if st := f.syncState(t); !st.OK {
		t.Errorf("derived github project: %+v", st)
	}
	if got := len(f.issues(t)); got != 1 {
		t.Errorf("imported %d issues, want 1", got)
	}
}
