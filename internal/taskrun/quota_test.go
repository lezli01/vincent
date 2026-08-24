package taskrun

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// claudeStepSnapshot and codexStepSnapshot name their adapter rather than
// leaning on §8.6 level 4, because which adapter the observation is filed
// under is the whole point of task 026.
const claudeStepSnapshot = `name: quota-claude
defaults:
  agent: claude
steps:
  - id: implement
    type: agent
    max_retries: 0
    prompt: "Do the work"
`

const codexStepSnapshot = `name: quota-codex
defaults:
  agent: codex
steps:
  - id: implement
    type: agent
    max_retries: 0
    prompt: "Do the work"
`

// claudeQuota reads the observation filed under claude, which is the adapter
// every walled workflow in this file names.
func claudeQuota(t *testing.T, h *engineHarness) *store.AgentQuota {
	t.Helper()
	q, err := h.store.GetAgentQuota(t.Context(), "claude")
	if err != nil {
		t.Fatalf("GetAgentQuota(claude): %v", err)
	}
	return q
}

// TestUsageLimitRecordsAnEstimatedWindow is the half that matters most: the
// CLI named no reset, so the effective one is
// `now + usage_limit_recheck_interval`, and the row must say so. Rendering a
// computed guess as something the CLI stated is exactly what
// `resets_at_reported` exists to prevent.
func TestUsageLimitRecordsAnEstimatedWindow(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "usage-limit")
	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.UsageLimitRecheckInterval = config.Duration(time.Hour)
	})
	task := h.createTask(t, claudeStepSnapshot)
	before := time.Now()
	h.start(t)

	held := h.waitForHold(t, task.ID)

	q := claudeQuota(t, h)
	if q.ResetsAtReported {
		t.Error("resets_at_reported = true; the fake CLI named no reset time")
	}
	if q.Source != store.QuotaSourceObserved {
		t.Errorf("source = %q, want %q", q.Source, store.QuotaSourceObserved)
	}
	// The row and the hold must agree: they are the same decision, recorded
	// twice because `admit_not_before` is cleared by the next transition out
	// of queued (task 003 decision 1) and this is not.
	if !q.ResetsAt.Equal(held.AdmitNotBefore.UTC()) {
		t.Errorf("resets_at = %s, want the hold's %s", q.ResetsAt, held.AdmitNotBefore.UTC())
	}
	if got := q.ResetsAt.Sub(before); got < 55*time.Minute || got > 65*time.Minute {
		t.Errorf("resets_at is %s away, want about the 1h recheck interval", got)
	}
	if !q.Spent(before) {
		t.Error("the observation reads as not spent at the moment it was made")
	}

	// Nothing else picked up an observation it never earned.
	rows, err := h.store.ListAgentQuota(t.Context())
	if err != nil {
		t.Fatalf("ListAgentQuota: %v", err)
	}
	if len(rows) != 1 {
		t.Errorf("rows = %+v, want claude's alone", rows)
	}
}

// TestUsageLimitRecordsAReportedWindow is the other half: a reset the CLI
// stated is recorded as a fact, and beats the configured interval in the row
// exactly as it does in the hold.
func TestUsageLimitRecordsAReportedWindow(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "usage-limit")
	t.Setenv("FAKEAGENT_USAGE_LIMIT_RESET", "1800") // 30 minutes from now
	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.UsageLimitRecheckInterval = config.Duration(4 * time.Hour)
	})
	task := h.createTask(t, claudeStepSnapshot)
	before := time.Now()
	h.start(t)

	h.waitForHold(t, task.ID)

	q := claudeQuota(t, h)
	if !q.ResetsAtReported {
		t.Error("resets_at_reported = false; the CLI named this reset")
	}
	if got := q.ResetsAt.Sub(before); got < 25*time.Minute || got > 35*time.Minute {
		t.Errorf("resets_at is %s away, want ~30m from the CLI rather than the 4h interval", got)
	}
}

// TestSuccessfulAgentStepClearsTheObservedWindow is why a lapsed reset alone
// does not delete the row and a success does. The hold here is an estimate;
// the re-run completing is first-hand evidence the window reopened, and the
// board must not go on claiming "spent until …" about an adapter it can watch
// doing work.
func TestSuccessfulAgentStepClearsTheObservedWindow(t *testing.T) {
	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.UsageLimitRecheckInterval = config.Duration(time.Millisecond)
	})
	t.Setenv("FAKEAGENT_SCENARIO", "usage-limit")
	t.Setenv("FAKEAGENT_USAGE_LIMIT_MARKER", filepath.Join(t.TempDir(), "window-spent"))

	task := h.createTask(t, claudeStepSnapshot)
	h.start(t)

	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%s), want done", done.State, done.BlockReason)
	}
	if _, err := h.store.GetAgentQuota(t.Context(), "claude"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetAgentQuota after a successful run = %v, want ErrNotFound", err)
	}
}

// TestSuccessOnAnotherAdapterKeepsTheWindow: the observation is per adapter,
// so codex finishing a step says nothing about claude's window. Without this
// the whole table would collapse into a single global flag the first time two
// adapters were in play.
func TestSuccessOnAnotherAdapterKeepsTheWindow(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "usage-limit")
	// The marker makes the fake CLI report the wall exactly once, so the
	// second task's run succeeds. With one slot and the claude task created
	// first, §11 admits it first and it is the one that hits the wall.
	t.Setenv("FAKEAGENT_USAGE_LIMIT_MARKER", filepath.Join(t.TempDir(), "window-spent"))
	h := newEngineHarnessWith(t, func(c *config.Config) {
		c.MaxParallelTasks = 1
		// Long enough that the claude task cannot be re-admitted during the
		// test: what is asserted is the surviving row, not a race with it.
		c.UsageLimitRecheckInterval = config.Duration(time.Hour)
	})
	walled := h.createTask(t, claudeStepSnapshot)
	other := h.createTask(t, codexStepSnapshot)
	h.start(t)

	h.waitForHold(t, walled.ID)
	before := claudeQuota(t, h)

	done := h.waitForState(t, other.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("codex task = %s (%s), want done", done.State, done.BlockReason)
	}

	after := claudeQuota(t, h)
	if !after.ResetsAt.Equal(before.ResetsAt) || !after.ObservedAt.Equal(before.ObservedAt) {
		t.Errorf("claude's observation changed from %+v to %+v when codex succeeded", before, after)
	}
	if _, err := h.store.GetAgentQuota(t.Context(), "codex"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("GetAgentQuota(codex) = %v, want ErrNotFound — it never ran out", err)
	}
}
