package daemon

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/issuesource"
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

// stallingSource is the GitHub source with one listing held open: once
// armed, its next ListIssues reads the page and then waits for its context
// to end or for release, the shape of a rebind landing while a pass is
// between its call and its writes.
type stallingSource struct {
	*issuesource.GitHub
	armed   atomic.Bool
	listing chan struct{}
	release chan struct{}
}

func (s *stallingSource) ListIssues(ctx context.Context, b issuesource.Binding, opts issuesource.ListOptions) (issuesource.Page, error) {
	page, err := s.GitHub.ListIssues(ctx, b, opts)
	if s.armed.CompareAndSwap(true, false) {
		close(s.listing)
		select {
		case <-ctx.Done():
		case <-s.release:
		}
	}
	return page, err
}

// assertRebound checks what a rebind to none leaves behind: no sync row and
// every imported remote missing.
func assertRebound(t *testing.T, f *reconcileFixture) {
	t.Helper()
	if _, err := f.store.GetIssueSyncState(t.Context(), f.project.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("sync row after the rebind = %v, want ErrNotFound", err)
	}
	for n, is := range f.issues(t) {
		if is.Remote.Status != store.RemoteStatusMissing {
			t.Errorf("issue #%d remote = %q after the rebind, want missing", n, is.Remote.Status)
		}
	}
}

// TestIssueSyncRebindMidPassSticks is review F1: a rebind that lands while
// a pass is between its listing and its writes is not undone by them — the
// pass neither resets the abandoned remotes to live nor writes back the
// sync row the rebind dropped.
func TestIssueSyncRebindMidPassSticks(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	c := newCorpus(t, issueRow(1, "open"), issueRow(2, "open"))
	r := f.reconciler()
	src := &stallingSource{
		GitHub:  issuesource.NewGitHub(f.client, f.git),
		listing: make(chan struct{}),
		release: make(chan struct{}),
	}
	r.issueSrc = src
	r.Tick(t.Context())
	if got := len(f.issues(t)); got != 2 {
		t.Fatalf("imported %d issues, want 2", got)
	}
	c.edit(1, issueBase.Add(time.Hour), func(row map[string]any) { row["title"] = "edited" })

	src.armed.Store(true)
	ticked := make(chan struct{})
	go func() {
		defer close(ticked)
		r.Tick(context.WithoutCancel(t.Context()))
	}()
	<-src.listing
	f.project.IssueSource = store.IssueSource{Provider: store.IssueProviderNone}
	if err := f.store.UpdateProjectRebind(t.Context(), &f.project, &store.Rebind{OldProvider: store.IssueProviderGitHub}); err != nil {
		t.Fatal(err)
	}
	close(src.release)
	<-ticked
	assertRebound(t, f)
}

// TestIssueSyncStaleProjectStandsDown is review F1's other order: a pass
// handed a project read before a rebind committed acts on nothing.
func TestIssueSyncStaleProjectStandsDown(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	newCorpus(t, issueRow(1, "open"))
	r := f.reconciler()
	r.Tick(t.Context())
	stale := f.project
	calls := len(f.apiCalls(t))
	f.project.IssueSource = store.IssueSource{Provider: store.IssueProviderNone}
	if err := f.store.UpdateProjectRebind(t.Context(), &f.project, &store.Rebind{OldProvider: store.IssueProviderGitHub}); err != nil {
		t.Fatal(err)
	}
	b, bound := r.issueSrc.Bind(t.Context(), stale)
	r.syncIssues(t.Context(), stale, b, bound, true)
	if got := f.apiCalls(t)[calls:]; len(got) != 0 {
		t.Errorf("a pass on the stale project made calls: %v", got)
	}
	assertRebound(t, f)
}
