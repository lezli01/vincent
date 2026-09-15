package apiclient_test

import (
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/github/githubtest"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// The pull-request write methods (task 068.4) against the **real** handlers,
// with the daemon's GitHub client pointed at cmd/fakegh — which is what keeps
// the client's wire types and the server's from drifting.

const liveFakeHead = "d3adb33fd3adb33fd3adb33fd3adb33fd3adb33f"

// newGitHubWriteClient serves a project whose origin is github.com with one
// task linked to the fake's #412, and returns the task id and the gh argv log.
func newGitHubWriteClient(t *testing.T) (*apiclient.Client, int64, string) {
	t.Helper()
	fake := githubtest.BuildFakeGH(t)
	argvLog := filepath.Join(t.TempDir(), "gh-argv.txt")
	t.Setenv("FAKEGH_ARGV_FILE", argvLog)
	t.Setenv("FAKEGH_SCENARIO", "success")
	t.Setenv("FAKEGH_STATE_FILE", filepath.Join(t.TempDir(), "state.json"))

	st, err := store.Open(filepath.Join(t.TempDir(), "vincent.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	repo := testrepo.Init(t, "main")
	cmd := exec.Command("git", "remote", "add", "origin", "https://github.com/octo/repo.git")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git remote add: %v\n%s", err, out)
	}
	ctx := t.Context()
	project := &store.Project{Name: "repo", Path: repo, DefaultBranch: "main"}
	if err := st.CreateProject(ctx, project); err != nil {
		t.Fatalf("create project: %v", err)
	}
	task := &store.Task{
		ProjectID: project.ID, Title: "a task", WorkflowName: "adhoc",
		BaseBranch: "main", BranchName: "vincent/1-a-task", State: store.TaskQueued,
	}
	if err := st.CreateTask(ctx, task, nil); err != nil {
		t.Fatalf("create task: %v", err)
	}
	if _, err := st.SetTaskGitHubPull(ctx, task.ID,
		store.LinkPull("octo/repo", 412, github.SourceHuman, time.Now().UTC())); err != nil {
		t.Fatalf("link: %v", err)
	}

	git := gitx.New()
	s := api.New(api.Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Git:         git,
		Worktrees:   worktree.NewManager(git, t.TempDir()),
		GitHub: github.New(github.Options{
			GHPath: fake,
			Getenv: func(string) string { return "" },
		}),
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return apiclient.New(ts.URL, testToken), task.ID, argvLog
}

func TestGitHubPullWritesRoundTrip(t *testing.T) {
	c, taskID, argvLog := newGitHubWriteClient(t)
	ctx := t.Context()

	comment, err := c.CommentGitHubPull(ctx, taskID, "Ship it.")
	if err != nil || comment.URL != "https://github.com/octo/repo/pull/412#issuecomment-1" {
		t.Fatalf("CommentGitHubPull = %+v, %v", comment, err)
	}
	rerun, err := c.RerunGitHubPullChecks(ctx, taskID, 5150)
	if err != nil || rerun.RunID != 5150 {
		t.Fatalf("RerunGitHubPullChecks = %+v, %v", rerun, err)
	}
	closed, err := c.CloseGitHubPull(ctx, taskID)
	if err != nil || closed.Status() != "closed" {
		t.Fatalf("CloseGitHubPull = %+v, %v", closed, err)
	}
	reopened, err := c.ReopenGitHubPull(ctx, taskID)
	if err != nil || reopened.Status() != "open" {
		t.Fatalf("ReopenGitHubPull = %+v, %v", reopened, err)
	}
	merged, err := c.MergeGitHubPull(ctx, taskID, apiclient.GitHubPullMergeRequest{Method: "rebase", HeadSHA: liveFakeHead})
	if err != nil || merged.Status() != "merged" || merged.HeadSHA != liveFakeHead {
		t.Fatalf("MergeGitHubPull = %+v, %v", merged, err)
	}

	b, _ := os.ReadFile(argvLog)
	for _, want := range []string{
		"pr comment 412 -R octo/repo --body-file -",
		"run rerun 5150 --failed -R octo/repo",
		"pr close 412 -R octo/repo",
		"pr reopen 412 -R octo/repo",
		"pr merge 412 -R octo/repo --rebase --match-head-commit " + liveFakeHead,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("gh was not asked for %q:\n%s", want, b)
		}
	}
}

// A refusal reaches the client as *apiclient.Error carrying the named reason.
func TestGitHubPullWriteRefusalCarriesTheReason(t *testing.T) {
	c, taskID, _ := newGitHubWriteClient(t)
	_, err := c.MergeGitHubPull(t.Context(), taskID,
		apiclient.GitHubPullMergeRequest{Method: "merge", HeadSHA: "0000000000000000000000000000000000000000"})
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Details["reason"] != github.ReasonHeadChanged {
		t.Fatalf("a moved head answered %v", err)
	}
	_, err = c.RerunGitHubPullChecks(t.Context(), taskID, 9999)
	if !errors.As(err, &apiErr) || apiErr.Details["reason"] != github.ReasonBadRequest {
		t.Fatalf("an unknown run answered %v", err)
	}
}
