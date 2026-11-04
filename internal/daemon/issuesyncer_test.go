package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/issuesource"
	"github.com/lezli01/vincent/internal/store"
)

// The issue sync loop (task 142.7): issue sync runs per bound provider, the
// Jira pass on its own timer and gates, the two switches independent, and
// "sync now" served by the requested row's provider.

// bindJira binds the fixture's project to Jira.
func (f *reconcileFixture) bindJira(t *testing.T) {
	t.Helper()
	f.project.IssueSource = store.IssueSource{
		Provider: store.IssueProviderJira, JiraSite: "https://acme.atlassian.net", JiraProjectKey: "ENG",
	}
	if err := f.store.UpdateProject(t.Context(), &f.project); err != nil {
		t.Fatal(err)
	}
}

// jiraEnv is a daemon environment carrying the default credential names.
func jiraEnv(user, token string) func() []string {
	return func() []string {
		env := []string{"PATH=/bin"}
		if user != "" {
			env = append(env, "JIRA_USER="+user)
		}
		if token != "" {
			env = append(env, "JIRA_API_TOKEN="+token)
		}
		return env
	}
}

func noSyncRow(t *testing.T, f *reconcileFixture) {
	t.Helper()
	if _, err := f.store.GetIssueSyncState(t.Context(), f.project.ID); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("sync row = %v, want none", err)
	}
}

// TestIssueSyncWritesNoRowForNone: a project resolving to none — derived on
// a non-GitHub origin, or bound to none explicitly — and an explicit github
// binding on a non-github.com origin get no sync row from either loop.
func TestIssueSyncWritesNoRowForNone(t *testing.T) {
	for _, tc := range []struct {
		name string
		src  store.IssueSource
	}{
		{"derived", store.IssueSource{}},
		{"explicit none", store.IssueSource{Provider: store.IssueProviderNone}},
		{"github off github.com", store.IssueSource{Provider: store.IssueProviderGitHub}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, "https://gitlab.com/octo/repo.git")
			f.project.IssueSource = tc.src
			if err := f.store.UpdateProject(t.Context(), &f.project); err != nil {
				t.Fatal(err)
			}
			r := f.reconciler()
			r.Tick(t.Context())
			r.JiraTick(t.Context())
			noSyncRow(t, f)
			if calls := f.apiCalls(t); len(calls) != 0 {
				t.Errorf("issue calls for a project resolving to none: %v", calls)
			}
		})
	}
}

// TestJiraPassGates: the Jira pass's gates in order — a credential variable
// missing or empty is no_credential, both present with no wired source is
// no_client — on a row stamped provider jira. A wired source is not reached
// while a credential is missing.
func TestJiraPassGates(t *testing.T) {
	for _, tc := range []struct {
		name        string
		user, token string
		set         map[string]string // config's environment.set
		src         bool
		want        string
	}{
		{"no user", "", "tok", nil, false, issuesource.ReasonNoCredential},
		{"no token", "me", "", nil, false, issuesource.ReasonNoCredential},
		{"no token, source wired", "me", "", nil, true, issuesource.ReasonNoCredential},
		{"both set, no source", "me", "tok", nil, false, syncReasonNoClient},
		{"token from environment.set", "me", "", map[string]string{"JIRA_API_TOKEN": "tok"}, false, syncReasonNoClient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, "https://github.com/octo/repo.git")
			f.bindJira(t)
			f.cfg.Environment.Set = tc.set
			r := f.reconciler()
			r.environ = jiraEnv(tc.user, tc.token)
			var stub *stubSource
			if tc.src {
				stub = newStubSource(&stubIssue{key: "ENG-1", open: true, updated: issueBase})
				r.WithJiraSource(stub)
			}
			r.JiraTick(t.Context())
			st := f.syncState(t)
			if st.OK || st.Reason != tc.want || st.Provider != issuesource.ProviderJira {
				t.Errorf("sync state = %+v, want provider jira, reason %s", st, tc.want)
			}
			if stub != nil && len(stub.calls) != 0 {
				t.Errorf("the source was called past a failing credential gate: %v", stub.calls)
			}
			if calls := f.ghCalls(t); calls != "" {
				t.Errorf("the Jira pass invoked gh:\n%s", calls)
			}
		})
	}
}

// TestJiraPassRunsTheWiredSource: with credentials and a source, the Jira
// pass imports through the shared importer — the seam #908 fills.
func TestJiraPassRunsTheWiredSource(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	f.bindJira(t)
	r := f.reconciler()
	r.environ = jiraEnv("me", "tok")
	r.WithJiraSource(newStubSource(&stubIssue{key: "ENG-1", open: true, updated: issueBase}))
	r.JiraTick(t.Context())
	if st := f.syncState(t); !st.OK || st.Provider != issuesource.ProviderJira {
		t.Errorf("sync state = %+v, want an ok jira row", st)
	}
	if got := f.stubIssues(t); len(got) != 1 {
		t.Errorf("imported %d issues, want 1", len(got))
	}
}

// runLoops runs both loops until the test ends.
func runLoops(t *testing.T, r *PullReconciler) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{}, 2)
	go func() { r.Run(ctx); done <- struct{}{} }()
	go func() { r.IssueSyncer.Run(ctx); done <- struct{}{} }()
	t.Cleanup(func() { cancel(); <-done; <-done })
}

// TestIssueSyncSwitchesAreIndependent: `github.enabled: false` stops GitHub
// issue import and pull request linking but not the Jira pass, and
// `jira.enabled: false` stops the Jira pass but not pull request linking.
func TestIssueSyncSwitchesAreIndependent(t *testing.T) {
	t.Run("github off", func(t *testing.T) {
		f := newFixture(t, "https://github.com/octo/repo.git")
		newCorpus(t, issueRow(1, "open"))
		f.bindJira(t)
		f.cfg.GitHub.Enabled = false
		r := f.reconciler()
		r.environ = jiraEnv("me", "tok")
		runLoops(t, r)
		waitFor(t, func() bool {
			st, err := f.store.GetIssueSyncState(t.Context(), f.project.ID)
			return err == nil && st.Reason == syncReasonNoClient
		})
		if st := f.syncState(t); st.Provider != issuesource.ProviderJira {
			t.Errorf("sync state = %+v, want the Jira pass's", st)
		}
		if calls := f.ghCalls(t); calls != "" {
			t.Errorf("github.enabled false invoked gh:\n%s", calls)
		}
		if l := f.link(t); l.Linked() {
			t.Errorf("github.enabled false linked a pull request: %+v", l)
		}
	})
	t.Run("jira off", func(t *testing.T) {
		f := newFixture(t, "https://github.com/octo/repo.git")
		newCorpus(t, issueRow(1, "open"))
		f.bindJira(t)
		f.cfg.Jira.Enabled = false
		r := f.reconciler()
		r.environ = jiraEnv("me", "tok")
		runLoops(t, r)
		waitFor(t, func() bool { return f.link(t).Linked() })
		noSyncRow(t, f)
		if calls := f.apiCalls(t); len(calls) != 0 {
			t.Errorf("a jira-bound project made GitHub issue calls: %v", calls)
		}
	})
	t.Run("jira off writes nothing", func(t *testing.T) {
		f := newFixture(t, "https://gitlab.com/octo/repo.git")
		f.bindJira(t)
		f.cfg.Jira.Enabled = false
		r := f.reconciler()
		r.environ = jiraEnv("me", "tok")
		var ticks atomic.Int32
		r.jiraTick = func(ctx context.Context) { ticks.Add(1); r.JiraTick(ctx) }
		runLoops(t, r)
		if err := f.store.RequestIssueSyncAs(t.Context(), f.project.ID, issuesource.ProviderJira); err != nil {
			t.Fatal(err)
		}
		r.RequestSync(f.project.ID)
		time.Sleep(300 * time.Millisecond)
		if n := ticks.Load(); n != 0 {
			t.Errorf("jira.enabled false ran %d Jira ticks", n)
		}
		if st := f.syncState(t); st.RequestedAt == nil || st.LastAttemptAt != nil {
			t.Errorf("jira.enabled false served a request: %+v", st)
		}
	})
}

// TestIssueSyncNowWakesTheResolvedProvider: "sync now" is served by the
// requested row's provider, gated by that provider's switches alone — a
// Jira request is served with GitHub off, and a GitHub request with Jira
// off.
func TestIssueSyncNowWakesTheResolvedProvider(t *testing.T) {
	t.Run("jira", func(t *testing.T) {
		f := newFixture(t, "https://github.com/octo/repo.git")
		f.bindJira(t)
		f.cfg.GitHub.Enabled = false
		f.cfg.Jira.PollInterval = config.Duration(time.Hour) // only a wake runs it again
		r := f.reconciler()
		// The loop reads the environment on its own goroutine.
		var env atomic.Pointer[func() []string]
		noToken := jiraEnv("me", "")
		env.Store(&noToken)
		r.environ = func() []string { return (*env.Load())() }
		f.store.OnIssueSyncRequested(r.RequestSync)
		runLoops(t, r)
		waitFor(t, func() bool {
			st, err := f.store.GetIssueSyncState(t.Context(), f.project.ID)
			return err == nil && st.Reason == issuesource.ReasonNoCredential
		})
		both := jiraEnv("me", "tok")
		env.Store(&both)
		if err := f.store.RequestIssueSyncAs(t.Context(), f.project.ID, issuesource.ProviderJira); err != nil {
			t.Fatal(err)
		}
		waitFor(t, func() bool {
			st := f.syncState(t)
			return st.RequestedAt == nil && st.Reason == syncReasonNoClient
		})
		if calls := f.ghCalls(t); calls != "" {
			t.Errorf("a Jira sync now invoked gh:\n%s", calls)
		}
	})
	t.Run("github", func(t *testing.T) {
		f := newFixture(t, "https://github.com/octo/repo.git")
		newCorpus(t, issueRow(1, "open"))
		f.cfg.Jira.Enabled = false
		r := f.reconciler()
		if err := f.store.RequestIssueSyncAs(t.Context(), f.project.ID, issuesource.ProviderGitHub); err != nil {
			t.Fatal(err)
		}
		r.SyncRequested(t.Context())
		if st := f.syncState(t); !st.OK || st.RequestedAt != nil || len(f.issues(t)) != 1 {
			t.Errorf("a GitHub sync now with Jira off: %+v, %d issues", st, len(f.issues(t)))
		}
	})
	t.Run("github request, github off", func(t *testing.T) {
		f := newFixture(t, "https://github.com/octo/repo.git")
		f.cfg.GitHub.PollInterval = 0
		r := f.reconciler()
		if err := f.store.RequestIssueSyncAs(t.Context(), f.project.ID, issuesource.ProviderGitHub); err != nil {
			t.Fatal(err)
		}
		r.SyncRequested(t.Context())
		if st := f.syncState(t); st.RequestedAt == nil {
			t.Errorf("a GitHub request was served with GitHub polling off: %+v", st)
		}
		if calls := f.ghCalls(t); calls != "" {
			t.Errorf("a GitHub request with polling off invoked gh:\n%s", calls)
		}
	})
}

// TestIssueSyncLoopTiming: the issue loop runs the Jira pass every
// jira.poll_interval while Jira polls, idles on the heartbeat while it does
// not and notices a reload on the next one, and serves a "sync now" without
// restarting the interval. Timers and channels only, so virtual time.
func TestIssueSyncLoopTiming(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var cfgVal atomic.Value
		cfg := config.Default()
		cfg.Jira.Enabled = false
		cfgVal.Store(cfg)
		s := NewIssueSyncer(nil, func() config.Config { return cfgVal.Load().(config.Config) }, nil, nil,
			slog.New(slog.NewTextHandler(io.Discard, nil)))
		var ticks, nows atomic.Int32
		s.jiraTick = func(context.Context) { ticks.Add(1) }
		s.syncNow = func(context.Context) { nows.Add(1) }
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan struct{})
		go func() { s.Run(ctx); close(done) }()

		time.Sleep(5 * time.Minute)
		synctest.Wait()
		if ticks.Load() != 0 {
			t.Fatalf("a disabled Jira pass ticked %d times", ticks.Load())
		}
		s.RequestSync(0)
		synctest.Wait()
		if nows.Load() != 1 {
			t.Fatalf("a wake served %d requests, want 1", nows.Load())
		}

		cfg.Jira.Enabled = true
		cfg.Jira.PollInterval = config.Duration(5 * time.Minute)
		cfgVal.Store(cfg)
		time.Sleep(time.Minute) // the heartbeat notices the reload
		synctest.Wait()
		if ticks.Load() != 1 {
			t.Fatalf("after the reload: %d ticks, want 1", ticks.Load())
		}
		time.Sleep(4 * time.Minute)
		s.RequestSync(0) // mid-interval: served, the interval not restarted
		synctest.Wait()
		if ticks.Load() != 1 || nows.Load() != 2 {
			t.Fatalf("mid-interval: %d ticks, %d requests", ticks.Load(), nows.Load())
		}
		time.Sleep(time.Minute)
		synctest.Wait()
		if ticks.Load() != 2 {
			t.Fatalf("one interval on: %d ticks, want 2", ticks.Load())
		}
		cancel()
		<-done
	})
}

// TestOutboxRegistryKeyedByProvider: a Jira row never drains under GitHub's
// gate — no_client with Jira on and no source, disabled with Jira off — and
// a GitHub row is never held by `jira.enabled: false`.
func TestOutboxRegistryKeyedByProvider(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	o := NewIssueOutbox(f.store, func() config.Config { return f.cfg }, f.client,
		slog.New(slog.NewTextHandler(io.Discard, nil)))

	if src, hold := o.byProvider(issuesource.ProviderJira); src != nil || hold != outboxReasonNoClient {
		t.Errorf("jira row, GitHub and Jira on, no Jira source: %v, %q; want no_client", src, hold)
	}
	if src, hold := o.byProvider(issuesource.ProviderGitHub); src == nil || hold != "" {
		t.Errorf("github row, both on: %v, %q", src, hold)
	}

	f.cfg.Jira.Enabled = false
	if src, hold := o.byProvider(issuesource.ProviderGitHub); src == nil || hold != "" {
		t.Errorf("github row held by jira.enabled false: %q", hold)
	}
	if src, hold := o.byProvider(issuesource.ProviderJira); src != nil || hold != outboxReasonDisabled {
		t.Errorf("jira row, Jira off: %v, %q; want disabled", src, hold)
	}

	f.cfg.Jira.Enabled = true
	f.cfg.GitHub.Enabled = false
	stub := newStubSource()
	o.WithJiraSource(stub)
	if src, hold := o.byProvider(issuesource.ProviderJira); src != stub || hold != "" {
		t.Errorf("jira row, GitHub off, Jira source wired: %v, %q", src, hold)
	}
	if src, hold := o.byProvider(issuesource.ProviderGitHub); src != nil || hold != outboxReasonDisabled {
		t.Errorf("github row, GitHub off: %v, %q; want disabled", src, hold)
	}
}

// TestEnvValue: the credential lookup reads the last entry for a name and
// treats an empty value as unset.
func TestEnvValue(t *testing.T) {
	env := []string{"A=1", "JIRA_USER=", "B=x=y", "A=2"}
	for name, want := range map[string]string{"A": "2", "JIRA_USER": "", "B": "x=y", "C": "", "": ""} {
		if got := envValue(env, name); got != want {
			t.Errorf("envValue(%q) = %q, want %q", name, got, want)
		}
	}
}
