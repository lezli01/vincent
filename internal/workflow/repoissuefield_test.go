package workflow

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestRepoWorkflowsNeverHandTheIssueFieldToGitHub (task 130 decision 8):
// the declared `issue` field holds the vincent issue id, so this
// repository's own workflows read the GitHub number from `github_issue`
// and never render `issue` — or `.Issue.Number` — anywhere at all. Every
// one of them still validates.
func TestRepoWorkflowsNeverHandTheIssueFieldToGitHub(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", ".vincent", "workflows", "*.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("no repo workflows found: %v", err)
	}
	forbidden := regexp.MustCompile(`index \.Task\.Fields "issue"|\.Issue\.Number`)
	declaresGitHubIssue := 0
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if loc := forbidden.FindIndex(raw); loc != nil {
			line := 1 + strings.Count(string(raw[:loc[0]]), "\n")
			t.Errorf("%s:%d renders the vincent issue id where a GitHub number belongs", filepath.Base(path), line)
		}
		wf, verrs, err := Parse(raw, Options{})
		if err != nil || len(verrs) > 0 {
			t.Errorf("%s: %v %v", filepath.Base(path), err, verrs)
			continue
		}
		for _, f := range wf.Fields {
			if f.Name == "github_issue" {
				declaresGitHubIssue++
			}
		}
	}
	// github-resolve-issue, -dag and -unit act on the GitHub issue.
	if declaresGitHubIssue < 3 {
		t.Errorf("%d repo workflows declare github_issue, want at least 3", declaresGitHubIssue)
	}
}
