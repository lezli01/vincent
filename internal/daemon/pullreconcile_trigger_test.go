package daemon

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/trigger"
)

// GitHub triggers ride the reconciler's tick (task 096 decision 31D). Three
// claims: a trigger that cannot be judged says why in its poll status rather
// than going quiet, whether GitHub is switched off or the project is not on
// GitHub; and on a GitHub project the first tick seeds the trigger.

func (f *reconcileFixture) withPRTrigger(t *testing.T) *PullReconciler {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "triggers")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	src := fmt.Sprintf(`id: prs
enabled: true
source:
  type: github_prs
  project: %d
match:
  action: merged
action:
  type: cancel
  target: branch
  branch: '{{ .Event.Pull.HeadRef }}'
on_fire: create
`, f.project.ID)
	if err := os.WriteFile(filepath.Join(dir, "prs.yaml"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	reg := trigger.NewRegistry(dir, nil)
	reg.Reload()
	if e, ok := reg.Get("prs"); !ok || !e.Valid() {
		t.Fatalf("trigger fixture does not load: %+v", e.Errors)
	}
	m := trigger.NewManager(trigger.Deps{Store: f.store, Registry: reg, Enabled: func() bool { return true }})
	return f.reconciler().WithTriggers(m)
}

func cursorOf(t *testing.T, st *store.Store, id string) *store.TriggerCursor {
	t.Helper()
	c, err := st.GetTriggerCursor(t.Context(), id)
	if err != nil {
		t.Fatalf("trigger %s has no cursor row: %v", id, err)
	}
	return c
}

func TestReconcilerFailsGitHubTriggersWhenDisabled(t *testing.T) {
	f := newFixture(t, "https://github.com/lezli01/vincent.git")
	f.cfg.GitHub.Enabled = false
	r := f.withPRTrigger(t)
	r.Tick(t.Context())
	c := cursorOf(t, f.store, "prs")
	if c.LastPollOK || !strings.Contains(c.LastPollError, "github.enabled") || c.Cursor != nil {
		t.Errorf("cursor = %+v; want failing with the github.enabled reason and no snapshot", c)
	}
	if calls := f.ghCalls(t); calls != "" {
		t.Errorf("a disabled integration called gh for a trigger: %s", calls)
	}
}

func TestReconcilerFailsGitHubTriggersOnNonGitHubProject(t *testing.T) {
	f := newFixture(t, "https://gitlab.com/lezli01/vincent.git")
	r := f.withPRTrigger(t)
	r.Tick(t.Context())
	c := cursorOf(t, f.store, "prs")
	if c.LastPollOK || !strings.Contains(c.LastPollError, "not a github.com repository") {
		t.Errorf("cursor = %+v; want failing because origin is not GitHub", c)
	}
}

func TestReconcilerSeedsGitHubTrigger(t *testing.T) {
	f := newFixture(t, "https://github.com/lezli01/vincent.git")
	r := f.withPRTrigger(t)
	r.Tick(t.Context())
	c := cursorOf(t, f.store, "prs")
	if !c.LastPollOK || c.Cursor == nil || !strings.Contains(*c.Cursor, `"kind":"github_prs"`) {
		t.Fatalf("cursor = %+v; want a seeded snapshot and an ok poll", c)
	}
	rows, err := f.store.ListTriggerDeliveries(t.Context(), "prs", 10)
	if err != nil || len(rows) != 0 {
		t.Errorf("a seed tick recorded deliveries: %+v, %v", rows, err)
	}
}

// TestReconcilerImportsIssuesBeforeJudgingTriggers is task 142.7's ordering
// invariant: for a project resolving to github, the tick's GitHub issue pass
// runs before the tick lists for — and so judges — the project's GitHub
// triggers, so a trigger reading issue state sees the same tick's import.
// The fake gh's argv log is the order the calls were made in.
func TestReconcilerImportsIssuesBeforeJudgingTriggers(t *testing.T) {
	f := newFixture(t, "https://github.com/octo/repo.git")
	newCorpus(t, issueRow(1, "open"))
	r := f.withPRTrigger(t)
	r.Tick(t.Context())
	lines := strings.Split(strings.ReplaceAll(f.ghCalls(t), "\r", ""), "\n")
	firstIssue, firstPulls := -1, -1
	for i, line := range lines {
		if firstIssue < 0 && strings.HasPrefix(line, "api ") && strings.Contains(line, "/issues") {
			firstIssue = i
		}
		if firstPulls < 0 && strings.HasPrefix(line, "pr list") {
			firstPulls = i
		}
	}
	if firstIssue < 0 || firstPulls < 0 || firstIssue > firstPulls {
		t.Fatalf("issue pass at line %d, trigger listing at line %d; want the import first:\n%s",
			firstIssue, firstPulls, strings.Join(lines, "\n"))
	}
	if c, err := f.store.GetTriggerCursor(t.Context(), "prs"); err != nil || !c.LastPollOK {
		t.Errorf("trigger not judged on the tick: %+v, %v", c, err)
	}
	if len(f.issues(t)) != 1 {
		t.Errorf("the tick imported %d issues, want 1", len(f.issues(t)))
	}
}
