package store

import (
	"database/sql"
	"errors"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestProjectIssueSourceRoundTrip: a new project derives (the migrated
// default), and the binding round-trips through update and read.
func TestProjectIssueSourceRoundTrip(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	got, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !got.IssueSource.Equal(IssueSource{}) {
		t.Fatalf("new project's binding = %+v, want derive", got.IssueSource)
	}
	want := IssueSource{
		Provider: IssueProviderJira, JiraSite: "https://x.atlassian.net", JiraProjects: []JiraProject{{Key: "ENG", JQL: "labels = a"}}, JiraMappingJSON: `{"overrides":{"reopen":"Back"}}`,
	}
	got.IssueSource = want
	if err := s.UpdateProject(ctx, got); err != nil {
		t.Fatal(err)
	}
	again, err := s.GetProject(ctx, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !again.IssueSource.Equal(want) {
		t.Errorf("binding = %+v, want %+v", again.IssueSource, want)
	}
}

func TestIssueSourceEffective(t *testing.T) {
	for _, c := range []struct {
		provider string
		origin   bool
		want     string
	}{
		{IssueProviderDerive, true, IssueProviderGitHub},
		{IssueProviderDerive, false, IssueProviderNone},
		{IssueProviderNone, true, IssueProviderNone},
		{IssueProviderJira, true, IssueProviderJira},
		{IssueProviderGitHub, false, IssueProviderGitHub},
	} {
		if got := (IssueSource{Provider: c.provider}).Effective(c.origin); got != c.want {
			t.Errorf("Effective(%q, origin github %v) = %q, want %q", c.provider, c.origin, got, c.want)
		}
	}
}

// TestUpdateProjectRebind is task 142.6 decision 4: a rebind marks the old
// provider's live remotes missing, drops the sync row and announces each
// touched issue, in the update's transaction; re-importing resets a remote to
// live; a plain update touches neither.
func TestUpdateProjectRebind(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	a := mustUpsertRemote(t, s, remoteIn(p.ID, "I_a", 1))
	b := mustUpsertRemote(t, s, remoteIn(p.ID, "I_b", 2))
	if err := s.SetIssueRemoteStatus(ctx, p.ID, "github", "I_b", RemoteStatusMoved, "x/y#1"); err != nil {
		t.Fatal(err)
	}
	if err := s.PutIssueSyncState(ctx, IssueSyncState{ProjectID: p.ID, Provider: "github", Repo: "o/r", OK: true}); err != nil {
		t.Fatal(err)
	}
	updatesBefore := len(issueEvents(t, s, EventIssueUpdated))

	// A plain update is not a rebind.
	p.Name = "renamed"
	if err := s.UpdateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetIssueSyncState(ctx, p.ID); err != nil {
		t.Fatalf("plain update dropped the sync row: %v", err)
	}

	p.IssueSource = IssueSource{Provider: IssueProviderNone}
	if err := s.UpdateProjectRebind(ctx, p, &Rebind{OldProvider: IssueProviderGitHub}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetIssueSyncState(ctx, p.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("sync row after rebind = %v, want ErrNotFound", err)
	}
	gotA, err := s.GetIssue(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotA.Remote == nil || gotA.Remote.Status != RemoteStatusMissing {
		t.Errorf("live remote after rebind = %+v, want missing", gotA.Remote)
	}
	gotB, err := s.GetIssue(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if gotB.Remote == nil || gotB.Remote.Status != RemoteStatusMoved {
		t.Errorf("moved remote after rebind = %+v, want moved kept", gotB.Remote)
	}
	evs := issueEvents(t, s, EventIssueUpdated)
	if len(evs) != updatesBefore+1 {
		t.Fatalf("issue.updated events = %d, want %d", len(evs), updatesBefore+1)
	}
	if m := payloadOf(t, evs[len(evs)-1]); m["id"] != float64(a.ID) || m["by"] != "sync" {
		t.Errorf("rebind event payload = %v", m)
	}

	// Binding back: the re-import matches on the key and resets live.
	refreshed := mustUpsertRemote(t, s, remoteIn(p.ID, "I_a", 1))
	if refreshed.ID != a.ID || refreshed.Remote == nil || refreshed.Remote.Status != RemoteStatusLive {
		t.Errorf("re-import = id %d remote %+v, want issue %d live", refreshed.ID, refreshed.Remote, a.ID)
	}
}

// A project registered before 0051 keeps deriving its issue source from
// origin after the upgrade: the migration adds the binding as "".
func TestMigrateKeepsExistingProjectsDerived(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	migrateTo(t, path, 50)
	func() {
		db, err := sql.Open("sqlite", dsn(path))
		if err != nil {
			t.Fatalf("reopen at 0050: %v", err)
		}
		defer func() { _ = db.Close() }()
		if _, err := db.Exec(`INSERT INTO projects (name, path, default_branch, created_at, updated_at)
			VALUES ('old', '/old', 'main', ?, ?)`, formatTime(time.Now()), formatTime(time.Now())); err != nil {
			t.Fatalf("insert pre-0051 project: %v", err)
		}
	}()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open migrated: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ps, err := s.ListProjects(t.Context())
	if err != nil || len(ps) != 1 {
		t.Fatalf("projects = %+v, %v", ps, err)
	}
	if !ps[0].IssueSource.Equal(IssueSource{}) {
		t.Errorf("migrated binding = %+v, want derive", ps[0].IssueSource)
	}
}

// A Jira binding made before 0056 keeps its one project and its JQL as the
// only entry of the list (task 146); a binding without JQL stores none, and
// a project not on Jira gets no list.
func TestMigrateJiraBindingToProjectList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "vincent.db")
	migrateTo(t, path, 55)
	func() {
		db, err := sql.Open("sqlite", dsn(path))
		if err != nil {
			t.Fatalf("reopen at 0055: %v", err)
		}
		defer func() { _ = db.Close() }()
		now := formatTime(time.Now())
		for _, row := range [][]string{
			{"narrowed", "jira", "ENG", "labels = backend"},
			{"whole", "jira", "OPS", ""},
			{"derived", "", "", ""},
		} {
			if _, err := db.Exec(`INSERT INTO projects (name, path, default_branch, created_at, updated_at,
				issue_provider, jira_site, jira_project_key, jira_jql)
				VALUES (?, ?, 'main', ?, ?, ?, 'https://acme.atlassian.net', ?, ?)`,
				row[0], "/"+row[0], now, now, row[1], row[2], row[3]); err != nil {
				t.Fatalf("insert pre-0056 project: %v", err)
			}
		}
	}()
	s, err := Open(path)
	if err != nil {
		t.Fatalf("open migrated: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	ps, err := s.ListProjects(t.Context())
	if err != nil || len(ps) != 3 {
		t.Fatalf("projects = %+v, %v", ps, err)
	}
	want := map[string][]JiraProject{
		"narrowed": {{Key: "ENG", JQL: "labels = backend"}},
		"whole":    {{Key: "OPS"}},
		"derived":  nil,
	}
	for _, p := range ps {
		if got := p.IssueSource.JiraProjects; !slices.Equal(got, want[p.Name]) {
			t.Errorf("%s: jira projects = %+v, want %+v", p.Name, got, want[p.Name])
		}
	}
}

// A Jira binding that drops one of its projects abandons only that
// project's issues (task 146 decision 4): KeepRepos stay live.
func TestUpdateProjectRebindKeepsStillBoundProjects(t *testing.T) {
	s := openTest(t)
	ctx := t.Context()
	p := testProject(t, s, "p1")
	jiraRemote := func(key, repo string, n int) RemoteIssue {
		r := remoteIn(p.ID, key, n)
		r.Provider, r.Repo = IssueProviderJira, repo
		return r
	}
	eng := mustUpsertRemote(t, s, jiraRemote("100", "ENG", 1))
	ops := mustUpsertRemote(t, s, jiraRemote("200", "OPS", 1))
	p.IssueSource = IssueSource{Provider: IssueProviderJira, JiraSite: "https://x", JiraProjects: []JiraProject{{Key: "ENG"}}}
	if err := s.UpdateProjectRebind(ctx, p, &Rebind{OldProvider: IssueProviderJira, KeepRepos: []string{"eng"}}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		id   int64
		want string
	}{{eng.ID, RemoteStatusLive}, {ops.ID, RemoteStatusMissing}} {
		got, err := s.GetIssue(ctx, c.id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Remote == nil || got.Remote.Status != c.want {
			t.Errorf("issue %d remote = %+v, want %q", c.id, got.Remote, c.want)
		}
	}
}
