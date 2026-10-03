package tui

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lezli01/vincent/internal/github"
	"github.com/lezli01/vincent/internal/github/githubtest"
)

// The live harness's GitHub wiring: the real handlers and a real daemon-side
// GitHub client pointed at cmd/fakegh, shared by the new-task form's source
// tests, the pull-request takeover, outcome-card and pull-request write tests. The new-task form's GitHub issue
// row these helpers were written for (task 035) is gone: it submitted
// `github_issue`, which task 130 decision 7 removed (task 130.11).

const ghLiveOrigin = "https://github.com/octo/repo.git"

// githubLiveHarness wires the fake `gh` into the live harness and hands back
// the argv log, so a test can assert on the calls the daemon made.
func newGitHubLiveHarness(t *testing.T, opts liveOptions) (*newTaskLiveHarness, string) {
	t.Helper()
	fake := githubtest.BuildFakeGH(t)
	argvLog := filepath.Join(t.TempDir(), "gh-argv.txt")
	t.Setenv("FAKEGH_ARGV_FILE", argvLog)
	if os.Getenv("FAKEGH_SCENARIO") == "" {
		t.Setenv("FAKEGH_SCENARIO", "success")
	}
	opts.github = github.New(github.Options{
		GHPath: fake,
		Getenv: func(string) string { return "" },
	})
	return newNewTaskLiveHarnessWith(t, opts), argvLog
}

func ghLiveCalls(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}
