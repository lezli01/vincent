package wait

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// handRolledDeadline matches the shape every per-package wait helper had
// before #731: a fixed wall-clock budget computed inline, which the Windows
// -race leg overruns. A helper that delegates to this package has none.
var handRolledDeadline = regexp.MustCompile(`deadline :?= time\.Now\(\)\.Add\(`)

// helperFiles are the files #731 names as carrying their own wait helper with
// its own deadline. Each must delegate to this package (or drop the helper),
// so that one env factor scales every budget.
var helperFiles = []string{
	"internal/notify/notify_test.go",
	"internal/trigger/manager_test.go",
	"internal/chatrun/runner_test.go",
	"internal/tui/newtaskbranchlive_test.go",
	"internal/apiclient/stream_task_test.go",
	"internal/apiclient/stream_chat_test.go",
	"internal/cli/actions_e2e_test.go",
	"internal/cli/logs_test.go",
	"internal/cli/fields_e2e_test.go",
	"internal/daemon/issuesync_test.go",
	"internal/workflow/watch_test.go",
	"internal/taskrun/status_test.go",
	"internal/taskrun/followup_test.go",
}

// repoRoot walks up from the package directory to go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test's working directory")
		}
		dir = parent
	}
}

// TestNoHandRolledWaitDeadlines is #731's first acceptance criterion: one
// wait helper, and the per-package copies delegate to it or are removed.
func TestNoHandRolledWaitDeadlines(t *testing.T) {
	root := repoRoot(t)
	for _, rel := range helperFiles {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		if err != nil {
			t.Errorf("%s: %v", rel, err)
			continue
		}
		for i, line := range strings.Split(string(b), "\n") {
			if handRolledDeadline.MatchString(line) {
				t.Errorf("%s:%d: hand-rolled wait deadline, not scaled by the shared helper: %s",
					rel, i+1, strings.TrimSpace(line))
			}
		}
	}
}

// TestWindowsCIScalesTestBudgets is #731's second criterion seen from CI: the
// Windows leg must set the factor the shared helper and the test-side client
// and daemon-health budgets multiply by.
func TestWindowsCIScalesTestBudgets(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(repoRoot(t), ".github", "workflows", "ci.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), "VINCENT_TEST_TIMEOUT_SCALE") {
		t.Error("ci.yml never sets VINCENT_TEST_TIMEOUT_SCALE, so no test budget scales on the Windows -race leg")
	}
}
