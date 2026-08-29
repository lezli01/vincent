package daemon

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/github/githubtest"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
)

// The task↔pull-request reconciler (task 052). Two claims: the §13.2 gate
// holds on the poll path too — a disabled integration or a non-GitHub project
// makes **no** `gh` invocation, asserted with a fake `gh` whose argv log must
// be empty — and the link, once written, obeys the human.

type reconcileFixture struct {
	store    *store.Store
	project  store.Project
	task     *store.Task
	argvFile string
	cfg      config.Config
	client   *github.Client
	git      *gitx.Git
}

// taskBranch is the branch the fixture's one task is created on, and the head
// branch the fake `gh`'s first pull request reports — so the match rule under
// test holds by default, and a test that wants a *miss* moves the pull request
// rather than the task.
const taskBranch = "vincent/1-a-task"

// newFixture builds a project whose `origin` is the github.com remote named in
// remote, one unarchived task on taskBranch, and a client pointed at fakegh.
func newFixture(t *testing.T, remote string) *reconcileFixture {
	t.Helper()
	branch := taskBranch
	ghPath := githubtest.BuildFakeGH(t)
	argv := filepath.Join(t.TempDir(), "argv.log")
	t.Setenv("FAKEGH_ARGV_FILE", argv)
	t.Setenv("FAKEGH_PR_BRANCH", branch)

	dir := testrepo.Init(t, "master")
	testrepo.Run(t, dir, "remote", "add", "origin", remote)

	st, err := store.Open(filepath.Join(t.TempDir(), "vincent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	project := store.Project{Name: "repo", Path: dir, DefaultBranch: "master"}
	if err := st.CreateProject(t.Context(), &project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &store.Task{
		ProjectID: project.ID, Title: "a task", WorkflowName: "adhoc",
		BaseBranch: "master", BranchName: branch, State: store.TaskQueued,
	}
	if err := st.CreateTask(t.Context(), task, nil); err != nil {
		t.Fatalf("create task: %v", err)
	}
	cfg := config.Default()
	cfg.GitHub.PollInterval = config.Duration(time.Minute)
	return &reconcileFixture{
		store: st, project: project, task: task, argvFile: argv, cfg: cfg,
		client: github.New(github.Options{GHPath: ghPath}),
		git:    gitx.New(),
	}
}

func (f *reconcileFixture) reconciler() *PullReconciler {
	return NewPullReconciler(f.store, func() config.Config { return f.cfg }, f.git, f.client,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// ghCalls returns everything the fake `gh` was invoked with this test.
func (f *reconcileFixture) ghCalls(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(f.argvFile)
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatalf("read argv log: %v", err)
	}
	return string(b)
}

func (f *reconcileFixture) link(t *testing.T) *github.PullLink {
	t.Helper()
	out, err := f.store.GetTask(t.Context(), f.task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	return out.GitHubPull
}

// TestReconcilerLinksByHeadBranch: a pull request whose head branch equals a
// task's branch links that task on the next tick, marked `auto`.
func TestReconcilerLinksByHeadBranch(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	f.reconciler().Tick(t.Context())
	link := f.link(t)
	if link == nil || !link.Linked() {
		t.Fatalf("no link was written: %+v", link)
	}
	if link.Number != 412 {
		t.Errorf("number = %d, want 412", link.Number)
	}
	if link.Repo != "octo/repo" {
		t.Errorf("repo = %q, want octo/repo — the identity rides on the task", link.Repo)
	}
	if link.Source != github.SourceAuto {
		t.Errorf("source = %q, want %q", link.Source, github.SourceAuto)
	}
}

// TestReconcilerMakesNoCallWhenDisabled is the gate on the poll path, in the
// form 035 asserts it: the argv log must be empty.
func TestReconcilerMakesNoCallWhenDisabled(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	f.cfg.GitHub.Enabled = false
	f.reconciler().Tick(t.Context())
	if calls := f.ghCalls(t); calls != "" {
		t.Fatalf("a disabled integration invoked gh:\n%s", calls)
	}
	if link := f.link(t); link != nil {
		t.Errorf("a disabled integration wrote a link: %+v", link)
	}
}

// TestReconcilerMakesNoCallForNonGitHubProject: the gate stops at the first
// "no", so a project whose origin is not a github.com remote is never probed.
func TestReconcilerMakesNoCallForNonGitHubProject(t *testing.T) {
	f := newFixture(t, "https://gitlab.com/octo/repo.git")
	f.reconciler().Tick(t.Context())
	if calls := f.ghCalls(t); calls != "" {
		t.Fatalf("a non-GitHub project invoked gh:\n%s", calls)
	}
	if link := f.link(t); link != nil {
		t.Errorf("a non-GitHub project got a link: %+v", link)
	}
}

// TestReconcilerRespectsAHumanUnlink is the acceptance criterion the third
// state exists for: a refusal is sticky, and the next tick does not undo it.
func TestReconcilerRespectsAHumanUnlink(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	r := f.reconciler()
	r.Tick(t.Context())
	if _, err := f.store.SetTaskGitHubPull(t.Context(), f.task.ID,
		store.SuppressPull(f.link(t), time.Now().UTC())); err != nil {
		t.Fatalf("unlink: %v", err)
	}
	r.Tick(t.Context())
	link := f.link(t)
	if link == nil || !link.Suppressed {
		t.Fatalf("the next tick re-applied a human unlink: %+v", link)
	}
	if link.Linked() {
		t.Error("a suppressed link reads as linked after a tick")
	}
}

// TestReconcilerNeverOverwritesAHumanLink: the branch is the ground truth, a
// person is a better one.
func TestReconcilerNeverOverwritesAHumanLink(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	if _, err := f.store.SetTaskGitHubPull(t.Context(), f.task.ID,
		store.LinkPull("octo/repo", 999, github.SourceHuman, time.Now().UTC())); err != nil {
		t.Fatalf("link: %v", err)
	}
	f.reconciler().Tick(t.Context())
	link := f.link(t)
	if link.Number != 999 || link.Source != github.SourceHuman {
		t.Fatalf("the reconciler overwrote a human link: %+v", link)
	}
}

// TestReconcilerLeavesUnmatchedBranchesAlone: a task whose branch no open
// pull request is from gets nothing — matching is exact, not fuzzy.
func TestReconcilerLeavesUnmatchedBranchesAlone(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	// The corpus's first pull request is from a branch nothing here uses.
	t.Setenv("FAKEGH_PR_BRANCH", "somebody-elses-branch")
	f.reconciler().Tick(t.Context())
	if link := f.link(t); link != nil {
		t.Fatalf("an unmatched branch was linked: %+v", link)
	}
	if calls := f.ghCalls(t); calls == "" {
		t.Error("a GitHub-based project made no call at all: the listing never ran")
	}
}
