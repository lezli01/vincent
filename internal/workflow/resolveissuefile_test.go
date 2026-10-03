package workflow

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"
)

// TestResolveWorkflowsReadTheIssueFile (130.14): the repository's resolve
// workflows read the task's issue from $VINCENT_ISSUE_FILE, never from
// `gh issue view`, and `-multiple` creates its tasks from vincent issues
// rather than through the legacy `--github-issue` path.
//
// Only what a shell runs is scanned — every `run:` and `check:` body, with
// its comment lines dropped — so a comment explaining the file's gh shape is
// not mistaken for a call.
func TestResolveWorkflowsReadTheIssueFile(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("..", "..", ".vincent", "workflows", "github-resolve-issue*.yaml"))
	if err != nil || len(files) != 4 {
		t.Fatalf("resolve workflows = %v (%v), want the four of them", files, err)
	}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		var doc any
		if err := yaml.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		name := filepath.Base(path)
		for _, body := range shellBodies(doc) {
			if strings.Contains(body, "gh issue view") {
				t.Errorf("%s: a step runs `gh issue view`; read $VINCENT_ISSUE_FILE instead", name)
			}
			if strings.Contains(body, "--github-issue") {
				t.Errorf("%s: a step passes --github-issue; create the task with --issue <id>", name)
			}
		}
		switch name {
		case "github-resolve-issue.yaml", "github-resolve-issue-dag.yaml", "github-resolve-issue-unit.yaml":
			if !strings.Contains(strings.Join(shellBodies(doc), "\n"), `cp "$VINCENT_ISSUE_FILE"`) {
				t.Errorf("%s: no step copies $VINCENT_ISSUE_FILE", name)
			}
		}
	}
}

// shellBodies collects every `run:` and `check:` string in a parsed
// workflow, at any depth, without its shell comment lines.
func shellBodies(node any) []string {
	var out []string
	switch n := node.(type) {
	case map[string]any:
		for k, v := range n {
			if s, ok := v.(string); ok && (k == "run" || k == "check") {
				var kept []string
				for _, line := range strings.Split(s, "\n") {
					if !strings.HasPrefix(strings.TrimSpace(line), "#") {
						kept = append(kept, line)
					}
				}
				out = append(out, strings.Join(kept, "\n"))
				continue
			}
			out = append(out, shellBodies(v)...)
		}
	case []any:
		for _, v := range n {
			out = append(out, shellBodies(v)...)
		}
	}
	return out
}
