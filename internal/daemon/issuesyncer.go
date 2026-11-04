package daemon

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/issuesource"
	"github.com/lezli01/vincent/internal/store"
)

// The issue sync loop (task 142.7, spec §12.3): issue sync runs per bound
// provider, on its own goroutine, apart from the pull request reconciler, so
// a slow or hanging issue provider never delays pull request linking.
//
// There is one timer per provider, and the two are owned in different
// places on purpose:
//
//   - GitHub's issue import is paced by `github.poll_interval` and is run by
//     the reconciler's own tick, inline, for each project resolving to
//     github, **before** that tick judges the project's GitHub triggers. A
//     trigger reading `type: issues` state therefore sees the import of the
//     same tick (the ordering invariant of task 142.7). Running it on this
//     loop instead would make the reconciler wait on it, which is the
//     coupling the split exists to remove; running it inline keeps the
//     invariant with no handshake at all, and the GitHub calls were already
//     the reconciler's to pace.
//   - The Jira pass is paced by `jira.poll_interval` and is this loop's
//     timer, gated on `jira.enabled` and the interval exactly as the
//     reconciler is gated on GitHub's. Off, it idles on a heartbeat and
//     re-reads the config, so a hot reload reaches the next tick.
//
// A "sync now" (store.RequestIssueSync) is this loop's for both
// providers: the API stamps the requested row with the project's resolved
// provider, and the request is served by that provider's pass under that
// provider's switches alone — a Jira request is served with GitHub off.
//
// Both passes share one implementation (issuesync.go) and one frame: the
// project's issue-source gate, the stale-project re-read, the sync row, the
// once-per-transition log. Both loops kick the write-back drain, which stays
// the one serialized writer for every provider.
//
// A project resolving to none — explicit or derived — gets no sync row at
// all; neither does an explicit github binding whose origin is not
// github.com. The API derives what to say about those from the binding and
// origin, so the row would only repeat it.

// syncReasonNoClient is the importer's reason for a provider with no client
// (or no source) wired.
const syncReasonNoClient = "no_client"

// IssueSyncer runs the issue import passes: GitHub's on behalf of the
// reconciler's tick, Jira's on its own timer, and every "sync now".
type IssueSyncer struct {
	store  *store.Store
	cfg    func() config.Config
	git    *gitx.Git
	client *github.Client
	logger *slog.Logger
	// issueSrc is the GitHub pass's source (task 142.4): GitHub over client
	// and git. It is never nil — its Bind is how a project is judged
	// GitHub-based even with no client — and calls are gated on client.
	issueSrc issuesource.Source
	// jiraSrc is the Jira pass's source; nil until the Jira client is wired
	// (#908), which makes every Jira-bound project's row say no_client
	// once its credentials are present. Supplying it is all #908 needs to
	// do for the pass to run through the shared importer.
	jiraSrc issuesource.Source
	// outbox is kicked after every Jira tick (task 130.10); nil is a syncer
	// with no write-back drain to wake.
	outbox *IssueOutbox
	// now is the clock, seamed for tests.
	now func() time.Time
	// environ is the daemon's environment before config's policy is
	// applied, read per use; seamed for tests.
	environ func() []string
	// jiraTick and syncNow are what Run calls, seamed so the loop's timing
	// is testable inside a synctest bubble without a store.
	jiraTick, syncNow func(context.Context)
	// wake is a "sync now": one slot, so requests arriving while one is
	// pending coalesce into it.
	wake chan struct{}
}

// NewIssueSyncer builds the syncer. It performs no I/O.
func NewIssueSyncer(
	st *store.Store, cfg func() config.Config,
	git *gitx.Git, client *github.Client, logger *slog.Logger,
) *IssueSyncer {
	s := &IssueSyncer{
		store: st, cfg: cfg, git: git, client: client, logger: logger,
		issueSrc: issuesource.NewGitHub(client, git),
		environ:  os.Environ,
		wake:     make(chan struct{}, 1),
	}
	s.jiraTick, s.syncNow = s.JiraTick, s.SyncRequested
	return s
}

// WithJiraSource wires the Jira pass's source (#908).
func (s *IssueSyncer) WithJiraSource(src issuesource.Source) *IssueSyncer {
	s.jiraSrc = src
	return s
}

// RequestSync wakes Run to serve the projects with an outstanding "sync
// now". It is the store's OnIssueSyncRequested callback, so it runs on the
// API's writing goroutine and never blocks: a request arriving while one is
// pending is the same request. Which pass it wakes is the requested row's
// provider, read when the request is served.
func (s *IssueSyncer) RequestSync(int64) {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// Run ticks the Jira pass until ctx is done, serving "sync now" requests
// as they come. The interval is re-read every tick, so a hot reload that
// changes `jira.poll_interval` — including to 0 — or `jira.enabled`
// reaches the next one; a disabled pass still ticks on a slow heartbeat
// and does nothing, which is how it notices being switched back on.
func (s *IssueSyncer) Run(ctx context.Context) {
	const idle = time.Minute
	for {
		wait := idle
		if cfg := s.cfg(); cfg.Jira.Polls() {
			s.jiraTick(ctx)
			wait = cfg.Jira.PollInterval.Std()
		}
		if !s.waitOut(ctx, wait) {
			return
		}
	}
}

// waitOut waits out one interval, serving "sync now" requests as they come;
// a request does not restart the interval. False when ctx is done.
func (s *IssueSyncer) waitOut(ctx context.Context, wait time.Duration) bool {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-timer.C:
			return true
		case <-s.wake:
			s.syncNow(ctx)
		}
	}
}

// SyncRequested runs the pass of every project with an outstanding "sync
// now", chosen by the requested row's provider and gated by that
// provider's switches alone. A request whose provider is switched off
// stays recorded — the API reports disabled / poll_disabled from config —
// and the next enabled pass serves it. Pull request links and triggers
// wait for the reconciler's tick: a request asks for issues.
func (s *IssueSyncer) SyncRequested(ctx context.Context) {
	cfg := s.cfg()
	gh, jira := cfg.GitHub.Polls(), cfg.Jira.Polls()
	if !gh && !jira {
		return
	}
	states, err := s.store.ListIssueSyncStates(ctx)
	if err != nil {
		s.logger.Warn("issue sync: requests not listed", "error", err)
		return
	}
	for _, st := range states {
		if st.RequestedAt == nil || ctx.Err() != nil {
			continue
		}
		switch {
		case st.Provider == issuesource.ProviderGitHub && gh:
		case st.Provider == issuesource.ProviderJira && jira:
		default:
			continue
		}
		project, err := s.store.GetProject(ctx, st.ProjectID)
		if err != nil {
			s.logger.Warn("issue sync: project not read", "project", st.ProjectID, "error", err)
			continue
		}
		if st.Provider == issuesource.ProviderJira {
			s.jiraPass(ctx, *project)
			continue
		}
		repo, ok := s.issueRepoFor(ctx, *project)
		s.gitHubPass(ctx, *project, repo, ok, s.haveClient())
	}
}

// JiraTick runs the Jira pass for every project resolving to jira. It is
// exported so a test can drive one pass without a timer; the gates config
// owns (jira.enabled, poll_interval) are Run's.
func (s *IssueSyncer) JiraTick(ctx context.Context) {
	if s.outbox != nil {
		defer s.outbox.Kick()
	}
	projects, err := s.store.ListProjects(ctx)
	if err != nil {
		s.logger.Warn("issue sync: projects not listed", "error", err)
		return
	}
	for _, project := range projects {
		if ctx.Err() != nil {
			return
		}
		if resolvesToJira(project) {
			s.jiraPass(ctx, project)
		}
	}
}

// resolvesToJira is the resolver's answer for project with origin unread:
// jira is only ever an explicit binding, so origin cannot change it and
// the Jira pass costs no git subprocess.
func resolvesToJira(project store.Project) bool {
	return issuesource.ResolveWith(project, github.Repo{}, false).Provider == issuesource.ProviderJira
}

// gitHubPass runs the GitHub issue pass for project on an origin already
// read as repo, ok — the reconciler's tick reads it once for the issue,
// trigger and pull request halves alike (review F1). Only a project that
// resolves to github with a github.com origin is GitHub's: one resolving
// to none, or explicitly bound to github on another host, gets no pass and
// no sync row.
func (s *IssueSyncer) gitHubPass(ctx context.Context, project store.Project, repo github.Repo, ok, haveClient bool) {
	if !ok || issuesource.ResolveWith(project, repo, ok).Provider != issuesource.ProviderGitHub {
		return
	}
	b, bound := s.issueBinding(ctx, project, repo, ok)
	s.syncIssues(ctx, project, b, bound, haveClient)
}

// jiraPass is one Jira attempt for project, under the same issue-source
// gate and stale-project re-read as GitHub's. Its gates run in order, and
// a project that fails one makes no call: the credentials config names,
// then a wired source. jira.enabled and poll_interval are the caller's.
func (s *IssueSyncer) jiraPass(ctx context.Context, project store.Project) {
	ctx, cur, st, done, ok := s.beginPass(ctx, project, issuesource.ProviderJira)
	if !ok {
		return
	}
	defer done()
	if !resolvesToJira(*cur) {
		return
	}
	now := s.clock()
	src := s.jiraSrc
	if s.backingOff(st, now) && src != nil {
		return
	}
	next := *st
	next.Provider = issuesource.ProviderJira
	next.LastAttemptAt = &now
	next.RequestedAt = nil
	var (
		b   issuesource.Binding
		err error
	)
	switch {
	case !s.jiraCredentialed(s.cfg()):
		next.OK, next.Reason = false, issuesource.ReasonNoCredential
	case src == nil:
		next.OK, next.Reason = false, syncReasonNoClient
	default:
		var bound bool
		if b, bound = src.Bind(ctx, *cur); !bound {
			// Provisional until #908 says what an unbindable Jira project
			// is: the binding names nothing the source can find.
			next.OK, next.Reason = false, issuesource.ReasonNotFound
			break
		}
		err = s.runImport(ctx, src, *cur, b, st, &next, now)
	}
	s.finishPass(ctx, project, b, st, next, err)
}

// jiraCredentialed is whether both variables jira.user_env and
// jira.token_env name are set and non-empty in the daemon's environment as
// config resolves it (`environment.set` reaches a service install that
// way). Read per use and never kept: a credential fixed while the daemon
// runs is seen on the next pass, and the values are never logged or stored.
func (s *IssueSyncer) jiraCredentialed(cfg config.Config) bool {
	env := cfg.Environment.Resolve(s.environ())
	return envValue(env, cfg.Jira.UserEnv) != "" && envValue(env, cfg.Jira.TokenEnv) != ""
}

// envValue is name's value in a resolved "K=V" environment, "" when unset.
// Windows' variable names are case-insensitive.
func envValue(env []string, name string) string {
	if name == "" {
		return ""
	}
	val := ""
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if k == name || (runtime.GOOS == "windows" && strings.EqualFold(k, name)) {
			val = v
		}
	}
	return val
}

// beginPass takes project's issue-source gate and re-reads the project and
// its sync row under it (review F1). ok is false when the pass must stand
// down: the gate was not taken, the project is gone, or a rebind (or a
// re-point) committed while the pass waited, so the binding the caller
// derived is stale and the next tick derives it afresh. A project with no
// row yet gets a fresh ok row for provider. done releases the gate.
func (s *IssueSyncer) beginPass(ctx context.Context, project store.Project, provider string) (
	passCtx context.Context, cur *store.Project, st *store.IssueSyncState, done func(), ok bool,
) {
	passCtx, done, err := s.store.BeginIssueSourcePass(ctx, project.ID)
	if err != nil {
		return nil, nil, nil, nil, false
	}
	cur, err = s.store.GetProject(passCtx, project.ID)
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.logger.Warn("issue sync: project not read", "project", project.ID, "error", err)
		}
		done()
		return nil, nil, nil, nil, false
	}
	if cur.Path != project.Path || cur.IssueSource != project.IssueSource {
		done()
		return nil, nil, nil, nil, false
	}
	st, err = s.store.GetIssueSyncState(passCtx, project.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		st = &store.IssueSyncState{ProjectID: project.ID, Provider: provider, OK: true}
	case err != nil:
		s.logger.Warn("issue sync: state not read", "project", project.ID, "error", err)
		done()
		return nil, nil, nil, nil, false
	}
	return passCtx, cur, st, done, true
}

// backingOff is whether the row is rate limited until after now: no call,
// no attempt, and the row keeps saying rate_limited until the provider's
// reset. A "sync now" waits too.
func (s *IssueSyncer) backingOff(st *store.IssueSyncState, now time.Time) bool {
	return st.RateLimitedUntil != nil && now.Before(*st.RateLimitedUntil)
}

// runImport is the shared import through src — the issue pass, then the
// comment pass — recording the outcome on next. Progress made before a
// failure is already in next and is kept.
func (s *IssueSyncer) runImport(
	ctx context.Context, src issuesource.Source, project store.Project, b issuesource.Binding,
	st *store.IssueSyncState, next *store.IssueSyncState, now time.Time,
) error {
	p := &issuePass{r: s, src: src, project: project, b: b, st: next}
	err := p.importIssues(ctx, now, st.RequestedAt != nil)
	if err == nil {
		// Only after the issue pass: a comment is filed under an imported
		// issue, so the issues it may belong to come first. Its failure is
		// recorded like the issue pass's, and the issue pass's progress,
		// already in next, is kept.
		err = p.syncComments(ctx)
	}
	if err == nil {
		next.OK, next.Reason, next.LastOKAt, next.RateLimitedUntil = true, "", &now, nil
		return nil
	}
	f := src.Classify(err)
	next.OK, next.Reason = false, f.Reason
	if f.Reason == issuesource.ReasonRateLimited && !f.ResetAt.IsZero() {
		reset := f.ResetAt.UTC()
		next.RateLimitedUntil = &reset
	}
	return err
}

// finishPass logs and writes the attempt's row. A failed call is logged
// each time; a gate is a standing condition of the project, not a failed
// call, so the log says it once per transition — and issue.sync_changed,
// the store's, announces only transitions too. A cancelled pass writes
// nothing: a rebind cancelled it (or the daemon is stopping), and the row
// it would write is the one the rebind dropped.
func (s *IssueSyncer) finishPass(
	ctx context.Context, project store.Project, b issuesource.Binding,
	st *store.IssueSyncState, next store.IssueSyncState, err error,
) {
	if ctx.Err() != nil {
		return
	}
	switch {
	case err != nil:
		s.logger.Warn("issue sync failed", "project", project.ID, "provider", next.Provider,
			"binding", b.String(), "reason", next.Reason, "error", err)
	case !next.OK:
		if st.OK || st.Reason != next.Reason {
			s.logger.Info("issue sync stopped", "project", project.ID, "provider", next.Provider,
				"reason", next.Reason)
		}
	}
	if err := s.store.PutIssueSyncState(ctx, next); err != nil {
		s.logger.Warn("issue sync: state not written", "project", project.ID, "error", err)
	}
}

// haveClient is whether the GitHub pass can make calls at all.
func (s *IssueSyncer) haveClient() bool { return s.client != nil && s.git != nil }

// issueRepoFor is the project's GitHub repository from origin, or false;
// without a git runner origin cannot be read, which reads as "not
// GitHub-based".
func (s *IssueSyncer) issueRepoFor(ctx context.Context, project store.Project) (github.Repo, bool) {
	if s.git == nil {
		return github.Repo{}, false
	}
	return issuesource.RepoOf(ctx, s.git, project)
}

// issueBinding is the GitHub pass's binding for project on an origin
// already read as repo, ok. GitHub's binding is that repository
// (GitHub.Bind is BindRepo of the same RepoOf), so it is not read a second
// time: one git subprocess per project per tick, and the importer cannot
// act on a different origin than the pull request and trigger halves if
// origin is re-pointed mid-tick (review F1). Any other source binds itself.
func (s *IssueSyncer) issueBinding(ctx context.Context, project store.Project, repo github.Repo, ok bool) (issuesource.Binding, bool) {
	if s.issueSrc.Provider() != issuesource.ProviderGitHub {
		return s.issueSrc.Bind(ctx, project)
	}
	if !ok {
		return issuesource.Binding{}, false
	}
	return issuesource.BindRepo(repo), true
}

func (s *IssueSyncer) clock() time.Time {
	if s.now != nil {
		return s.now().UTC()
	}
	return time.Now().UTC()
}
