package daemon

import (
	"context"
	"log/slog"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
)

// The task↔pull-request reconciler (task 052, spec §12.3).
//
// It is a daemon subsystem rather than a side effect of the listing endpoint,
// for two reasons. A link written only when someone opens a screen exists
// only for the projects somebody happened to open, which is not a durable
// link; and a GET that mutates rows is a shape no other write in this API
// takes. So the endpoint stays pure and this runs on a timer.
//
// It is modelled on internal/notify's posture: it reads the config per tick,
// so a hot reload governs the next one; it is bounded — one listing per
// GitHub-based project; and its failure policy is **quiet**. A rate-limited
// or unreachable GitHub degrades to "no new links this tick" and logs at
// debug, never a per-tick error storm and never a task state change. This is
// the daemon's first standing outbound network traffic, and `github.
// poll_interval: 0` switches it off without switching the integration off.

// PullReconciler links tasks to the pull requests opened from their branches.
type PullReconciler struct {
	store  *store.Store
	cfg    func() config.Config
	git    *gitx.Git
	client *github.Client
	logger *slog.Logger
	// now is the clock, seamed for tests.
	now func() time.Time
}

// NewPullReconciler builds the reconciler. It performs no I/O.
func NewPullReconciler(
	st *store.Store, cfg func() config.Config,
	git *gitx.Git, client *github.Client, logger *slog.Logger,
) *PullReconciler {
	return &PullReconciler{store: st, cfg: cfg, git: git, client: client, logger: logger}
}

// Run ticks until ctx is done. The interval is re-read every tick, so a hot
// reload that changes `github.poll_interval` — including to 0 — reaches the
// next one. A disabled reconciler still ticks on a slow heartbeat and does
// nothing, which is how it notices being switched back on without a restart.
func (r *PullReconciler) Run(ctx context.Context) {
	const idle = time.Minute
	for {
		wait := idle
		if cfg := r.cfg(); cfg.GitHub.Polls() {
			r.Tick(ctx)
			wait = cfg.GitHub.PollInterval.Std()
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Tick reconciles every project once. It is exported so a test can drive one
// pass without a timer, which is also what keeps the "no call at all" property
// assertable on this path.
func (r *PullReconciler) Tick(ctx context.Context) {
	// The gate first, and it stops at the first "no" — exactly as the API's
	// does. A disabled integration makes no call, and neither does a project
	// whose origin is not a github.com remote.
	if !r.cfg().GitHub.Enabled || r.client == nil || r.git == nil {
		return
	}
	projects, err := r.store.ListProjects(ctx)
	if err != nil {
		r.logf("pull request reconcile: projects not listed", "error", err)
		return
	}
	for _, project := range projects {
		if ctx.Err() != nil {
			return
		}
		r.reconcileProject(ctx, project)
	}
}

func (r *PullReconciler) reconcileProject(ctx context.Context, project store.Project) {
	repo, ok := r.repoFor(ctx, project)
	if !ok {
		return
	}
	candidates, err := r.store.LinkCandidates(ctx, project.ID)
	if err != nil {
		r.logf("pull request reconcile: tasks not listed",
			"project", project.ID, "error", err)
		return
	}
	// Nothing to match against is not worth a network call. A project with no
	// unarchived task cannot gain a link this tick however many pull requests
	// it has open.
	wanted := map[string]store.LinkCandidate{}
	for _, c := range candidates {
		// A human link is never overwritten and a human unlink is never
		// un-suppressed, so neither is a candidate. The branch is the ground
		// truth; a person is a better one.
		if c.Pull != nil && (c.Pull.Source == github.SourceHuman || c.Pull.Suppressed) {
			continue
		}
		if c.Pull.Linked() {
			continue
		}
		wanted[c.BranchName] = c
	}
	if len(wanted) == 0 {
		return
	}
	pulls, err := r.client.ListPulls(ctx, repo, github.ListOptions{})
	if err != nil {
		// Quiet by design: this is the rate-limited and unreachable path, and
		// it must not become a per-tick error storm in the daemon log.
		r.logf("pull request reconcile: listing unavailable",
			"project", project.ID, "repo", repo.String(), "reason", github.ReasonOf(err))
		return
	}
	for _, pull := range pulls {
		candidate, ok := wanted[pull.HeadBranch]
		if !ok || pull.HeadBranch == "" {
			continue
		}
		link := &github.PullLink{
			Repo: repo.String(), Number: pull.Number,
			Source: github.SourceAuto, LinkedAt: r.clock(),
		}
		if _, err := r.store.SetTaskGitHubPull(ctx, candidate.TaskID, link); err != nil {
			r.logf("pull request reconcile: link not written",
				"task", candidate.TaskID, "error", err)
			continue
		}
		// One line per link written, not per tick: a link is news, and a tick
		// that changed nothing is not.
		r.logger.Info("linked task to pull request",
			"task", candidate.TaskID, "repo", repo.String(), "pull", pull.Number,
			"branch", pull.HeadBranch)
		// A branch names one task (§10 claims it), so the entry is spent.
		delete(wanted, pull.HeadBranch)
	}
}

// repoFor derives the project's GitHub identity from its `origin` remote at
// the point of use — the same derivation the API does, and the same known
// narrowness: an SSH alias, or a GitHub remote not named `origin`, is simply
// not GitHub-based (task 035 decision 5, unreversed by task 052).
func (r *PullReconciler) repoFor(ctx context.Context, project store.Project) (github.Repo, bool) {
	ctx, cancel := context.WithTimeout(ctx, gitx.QueryTimeout)
	defer cancel()
	remote, err := r.git.Run(ctx, project.Path, "remote", "get-url", "origin")
	if err != nil {
		return github.Repo{}, false
	}
	return github.ParseRemote(remote)
}

func (r *PullReconciler) clock() time.Time {
	if r.now != nil {
		return r.now().UTC()
	}
	return time.Now().UTC()
}

// logf keeps the recurring failures at debug. An unreachable GitHub every
// five minutes is a condition, not an incident, and the row `vincent doctor`
// exists to explain already says so on demand.
func (r *PullReconciler) logf(msg string, args ...any) {
	if r.logger != nil {
		r.logger.Debug(msg, args...)
	}
}
