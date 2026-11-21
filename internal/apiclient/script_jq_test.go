package apiclient

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The shipped workflows read vincent's own `--json` output with jq, inside
// step bodies no validator looks into. These tests pull each such expression
// out of the file and run it through jq over the wire type the command
// emits, so a script and the shape it reads cannot drift apart unnoticed
// (review F1 and F2 on #872: `train-pr` read `.url` off a GitHubPullCreated
// and merge-train's `deliver` read `.merged` off a GitHubTaskPull, both of
// which nest those under `.pull`).

// scriptJQ returns the one capture of pattern in the file at rel, from the
// repository root. A missing match fails: the pattern is stale.
func scriptJQ(t *testing.T, rel, pattern string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(pattern).FindStringSubmatch(strings.ReplaceAll(string(raw), "\r\n", "\n"))
	if m == nil {
		t.Fatalf("%s: no match for %q; the pattern is stale", rel, pattern)
	}
	return m[1]
}

// runJQ runs jq -r with args over v marshalled, and returns its output with
// the trailing newline removed.
func runJQ(t *testing.T, jq string, v any, args ...string) string {
	t.Helper()
	in, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(jq, append([]string{"-r"}, args...)...)
	cmd.Stdin = bytes.NewReader(in)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("jq %q over %s: %v", args, in, err)
	}
	return strings.TrimRight(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n")
}

func lookJQ(t *testing.T) string {
	t.Helper()
	jq, err := exec.LookPath("jq")
	if err != nil {
		t.Skip("jq is not on PATH")
	}
	return jq
}

func TestHandoffTrainReadsThePullRequestURL(t *testing.T) {
	jq := lookJQ(t)
	const file = ".vincent/workflows/github-handoff-train.yaml"

	created := GitHubPullCreated{Created: true, Pushed: true, Pull: &GitHubPullRequest{Number: 7, URL: "https://github.com/o/r/pull/7"}}
	urlExpr := scriptJQ(t, file, `jq -r '([^']*)' "\$s/pr\.json" > "\$s/url\.txt"`)
	if got := runJQ(t, jq, created, urlExpr); got != created.Pull.URL {
		t.Errorf("url.txt from a created pull request = %q, want %q", got, created.Pull.URL)
	}

	createdExpr := scriptJQ(t, file, `if \[ "\$\(jq -r '([^']*)' "\$s/pr\.json"\)" != true \]`)
	if got := runJQ(t, jq, created, createdExpr); got != "true" {
		t.Errorf("a created pull request reads as created = %q", got)
	}
	fallback := GitHubPullCreated{Pushed: true, CompareURL: "https://github.com/o/r/compare/b", Reason: "forbidden"}
	if got := runJQ(t, jq, fallback, createdExpr); got == "true" {
		t.Error("the compare-URL fallback reads as created")
	}

	// The reuse path is gh's shape, not vincent's, but it fills the same file.
	listExpr := scriptJQ(t, file, `--json number,url --jq '([^']*)'`)
	list := []map[string]any{{"number": 7, "url": "https://github.com/o/r/pull/7"}}
	if got := runJQ(t, jq, list, listExpr); got != "7\thttps://github.com/o/r/pull/7" {
		t.Errorf("an open pull request from gh pr list = %q, want number<TAB>url", got)
	}
	if got := runJQ(t, jq, []any{}, listExpr); got != "" {
		t.Errorf("no open pull request = %q, want nothing", got)
	}
}

func TestMergeTrainExampleReadsTheMerge(t *testing.T) {
	jq := lookJQ(t)
	const file = "examples/merge-train.yaml"

	expr := scriptJQ(t, file, `jq -r --arg head "[^"]*" '([^']*)'`)
	pull := func(merged bool, head string) GitHubTaskPull {
		return GitHubTaskPull{Linked: true, Repo: "o/r", Number: 7, Pull: &GitHubPullRequest{Number: 7, Merged: merged, HeadSHA: head}}
	}
	for _, tc := range []struct {
		name string
		pull GitHubTaskPull
		want string
	}{
		{"merged at the verified commit", pull(true, "abc"), "true"},
		{"not merged", pull(false, "abc"), "false"},
		{"merged at another commit", pull(true, "def"), "false"},
	} {
		if got := runJQ(t, jq, tc.pull, "--arg", "head", "abc", expr); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestMergeTrainExampleStacksOnlyChanges is review F3 on #872: a handoff
// started by hand can claim a `reply` or `none` delivery, which has no commit
// to stack; `deliver --all` would close its issue as completed regardless.
// The example releases every such item and stacks only the changes.
func TestMergeTrainExampleStacksOnlyChanges(t *testing.T) {
	jq := lookJQ(t)
	const file = "examples/merge-train.yaml"

	// $VINCENT_HANDOFF_FILE's shape (internal/taskrun/handoff.go).
	handoff := map[string]any{"id": 3, "items": []map[string]any{
		{"issue": map[string]any{"id": 11}, "delivery": map[string]any{"kind": "change"}, "head_sha": "aaa"},
		{"issue": map[string]any{"id": 12}, "delivery": map[string]any{"kind": "reply"}, "head_sha": ""},
		{"issue": map[string]any{"id": 13}, "delivery": map[string]any{"kind": "none"}, "head_sha": ""},
		{"issue": map[string]any{"id": 14}, "delivery": map[string]any{"kind": "change"}, "head_sha": "bbb"},
	}}

	released := scriptJQ(t, file, `for issue in \$\(jq -r '([^']*)' "\$VINCENT_HANDOFF_FILE"\)`)
	if got := runJQ(t, jq, handoff, released); got != "12\n13" {
		t.Errorf("released = %q, want the reply and the none", got)
	}
	stacked := scriptJQ(t, file, `for head in \$\(jq -r '([^']*)' "\$VINCENT_HANDOFF_FILE"\)`)
	if got := runJQ(t, jq, handoff, stacked); got != "aaa\nbbb" {
		t.Errorf("stacked = %q, want the two changes in order", got)
	}
}

// TestResolveSweepReadmitsAPartlyDeliveredIssue is #1042's survey half: the
// sweep's reshape reads the handoff marker off `vincent issue ls --json`, so
// an issue a train delivered and left open is surveyed again rather than
// excluded for the done task it still sits on.
func TestResolveSweepReadmitsAPartlyDeliveredIssue(t *testing.T) {
	jq := lookJQ(t)
	const file = ".vincent/workflows/github-resolve-issue-multiple.yaml"
	reshape := scriptJQ(t, file, `jq '(\[\.\[\] \| select\(\.source\.number != null\)[^']*)' \\\n\s*"\$scratch/vincent-issues\.json"`)
	src := &IssueSource{Provider: "github", Repo: "o/r", Number: 7}
	issues := []Issue{
		{ID: 1, Title: "part", Lane: "open", Source: src, Handoff: &IssueHandoff{State: "delivered"}},
		{ID: 2, Title: "pending", Lane: "hand_off", Source: src, Handoff: &IssueHandoff{State: "pending"}},
		{ID: 3, Title: "none", Lane: "open", Source: src},
	}
	got := runJQ(t, jq, issues, "-c", reshape+" | map(.partly_delivered)")
	if got != "[true,false,false]" {
		t.Errorf("partly_delivered over delivered, pending and no marker = %s, want [true,false,false]", got)
	}
}

// TestHandoffTrainFollowsEachDeliverysClose is #1042's train half: one
// explicit --close would override every item's suggested close_reason, so a
// delivery that asked to leave its issue open would be closed anyway.
func TestHandoffTrainFollowsEachDeliverysClose(t *testing.T) {
	const file = ".vincent/workflows/github-handoff-train.yaml"
	deliver := scriptJQ(t, file, `\n\s*(vincent handoff deliver [^\n]*)`)
	if strings.Contains(deliver, "--close") {
		t.Errorf("the train's deliver passes --close, overriding each delivery's suggestion: %s", deliver)
	}
}
