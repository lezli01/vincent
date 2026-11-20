package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

func railFixture(t *testing.T, runs []apiclient.StepRun, steps ...apiclient.WorkflowStep) *detail {
	t.Helper()
	d := newTestDetail(t)
	d.loaded = true
	d.task.State = stateRunning
	d.task.StepTotal = len(steps)
	d.task.WorkflowSteps = steps
	d.task.Steps = runs
	if len(runs) > 0 {
		d.selectedRun = runs[len(runs)-1].ID
	}
	return d
}

func railText(d *detail, width int) []string {
	lines, _, _, _ := d.railRows(width)
	out := make([]string, len(lines))
	for i, l := range lines {
		out[i] = strings.TrimRight(ansi.Strip(l), " ")
	}
	return out
}

// One row per step on a connecting line, the whole way to done, and nothing
// after the last step (task 164.1).
func TestRailIsOneRowPerStepToTheEnd(t *testing.T) {
	d := railFixture(t,
		[]apiclient.StepRun{
			{ID: 1, StepIndex: 0, StepID: "plan", StepName: "plan", Attempt: 1, State: "succeeded"},
		},
		apiclient.WorkflowStep{Index: 0, ID: "plan", Type: "agent"},
		apiclient.WorkflowStep{Index: 1, ID: "review", Type: "manual"},
		apiclient.WorkflowStep{Index: 2, ID: "publish", Type: "command"},
	)
	got := railText(d, 30)
	want := []string{ic("✓") + "  plan", "│", ic("○") + "  review  gate", "│", ic("○") + "  publish"}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rail:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A retried step that is selected lists its attempts under it, the selected
// one marked, and says how many there were.
func TestRailOpensASelectedRetriedStepsAttempts(t *testing.T) {
	reason := "nonzero_exit"
	d := railFixture(t,
		[]apiclient.StepRun{
			{ID: 1, StepIndex: 0, StepID: "test", StepName: "test", Attempt: 1, State: "failed", FailureReason: &reason},
			{ID: 2, StepIndex: 0, StepID: "test", StepName: "test", Attempt: 2, State: "succeeded"},
		},
		apiclient.WorkflowStep{Index: 0, ID: "test", Type: "command"},
	)
	got := strings.Join(railText(d, 40), "\n")
	for _, want := range []string{"↻ 2 attempts", ic("✗") + " 1  failed", "▸ " + ic("✓") + " 2  succeeded"} {
		if !strings.Contains(got, want) {
			t.Errorf("rail misses %q:\n%s", want, got)
		}
	}
	d.selectedRun = 0
	if got := strings.Join(railText(d, 40), "\n"); strings.Contains(got, "failed") {
		t.Errorf("an unselected step opened its attempts:\n%s", got)
	}
}

// A condition step says what it tested, and a step a false guard skipped
// says so (task 164.1).
func TestRailSaysWhatAGuardTested(t *testing.T) {
	skip := "condition"
	d := railFixture(t,
		[]apiclient.StepRun{
			{ID: 1, StepIndex: 0, StepID: "check", StepName: "check", Attempt: 1, State: "succeeded", RenderedIf: ptr("true")},
			{ID: 2, StepIndex: 1, StepID: "docs", StepName: "docs", Attempt: 1, State: "skipped", SkipReason: &skip, RenderedIf: ptr("false")},
		},
		apiclient.WorkflowStep{Index: 0, ID: "check", Type: "condition"},
		apiclient.WorkflowStep{Index: 1, ID: "docs", Type: "agent"},
	)
	got := strings.Join(railText(d, 50), "\n")
	for _, want := range []string{"◇  check  condition", "│  if: true", "skipped · if: false was false"} {
		if !strings.Contains(got, want) {
			t.Errorf("rail misses %q:\n%s", want, got)
		}
	}
}
