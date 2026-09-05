package tui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// recordedAttempt is a fully populated migration-0027 record: every rendered
// input, every resolution field, the control-flow record and the outcome. It
// is built as an apiclient.StepRun directly rather than run through a daemon —
// the daemon half lands separately, and this tab is a reader of the wire type.
func recordedAttempt() apiclient.StepRun {
	prompt := "Implement the change described in OPS-42.\n\n" +
		"<previous-attempt-failure attempt=\"1\">\nthe build did not compile\n</previous-attempt-failure>"
	check := "go test ./..."
	guard := "true"
	forEach := `["api","web"]`
	fin := fixedNow.Add(-30 * time.Second)
	return apiclient.StepRun{
		ID: 501, StepIndex: 1, StepID: "implement", StepName: "Implement", StepType: "agent",
		Attempt: 2, Iteration: 3, LoopTotal: 5, LoopItem: ptr("api"), State: "failed",
		Agent: ptr("claude"), Model: ptr("opus"), Effort: ptr("high"),
		ExitCode: ptr(1), CheckExitCode: ptr(2),
		FailureReason: ptr("check_failed"), ResultSummary: "two packages still fail",
		TranscriptPath: ptr("/tmp/1-2.jsonl"),
		InputTokens:    ptr(int64(1200)), OutputTokens: ptr(int64(340)), CostUSD: ptr(0.42),
		InputWaitMS: 45_000, PromptOverride: true,

		RenderedPrompt: &prompt, RenderedCheck: &check,
		RenderedIf: &guard, RenderedForEach: &forEach,

		AgentSource: ptr("task"), ModelSource: ptr("workflow"), EffortSource: ptr("adapter"),
		PermissionMode: ptr("restricted"),
		TimeoutMS:      600_000, CheckTimeoutMS: 120_000,
		Shell: ptr("/bin/sh"), WorkDir: ptr("/tmp/wt/7"),

		StartedAt: fixedNow.Add(-4 * time.Minute), FinishedAt: &fin,
	}
}

// unrecordedAttempt is the same step, in the same loop iteration, carrying no
// record at all: no rendered input and no resolution. Every nil here means
// "the record does not exist" — a row written before migration 0027, or a
// field the step type never had — which the tab has to say in words.
//
// Iteration and LoopTotal are set because they are not part of 0027: a row
// with no input record still knows which pass of the loop it was. They also
// put it in its sibling's tier, so detail.attempts orders the two by attempt
// (2 then 3) rather than floating this one above by iteration 0.
func unrecordedAttempt() apiclient.StepRun {
	fin := fixedNow.Add(-30 * time.Second)
	return apiclient.StepRun{
		ID: 502, StepIndex: 1, StepID: "implement", StepName: "Implement", StepType: "agent",
		Attempt: 3, Iteration: 3, LoopTotal: 5, State: "succeeded",
		StartedAt: fixedNow.Add(-time.Minute), FinishedAt: &fin,
	}
}

// stepDetailsFixture is a workspace standing on Step Details with both
// attempts loaded and the recorded one selected.
func stepDetailsFixture(t *testing.T) *taskView {
	t.Helper()
	d := newTestDetail(t)
	d.taskID = 7
	d.applyLoaded(detailLoadedMsg{
		id: d.taskID,
		task: apiclient.TaskDetail{
			Task: apiclient.Task{
				ID: 7, Title: "step details task", State: stateRunning, StepTotal: 2,
				LaneID: ptr("api"),
			},
			Steps: []apiclient.StepRun{recordedAttempt(), unrecordedAttempt()},
			WorkflowSteps: []apiclient.WorkflowStep{{
				Index: 1, ID: "implement", Type: "agent", Prompt: "Implement {{ .Fields.ticket }}.",
				ResolvedFrom: []string{"release", "shared-implement"},
			}},
		},
	})
	d.selectedRun = 501
	v := newTaskView(d)
	v.tab = taskTabStepDetails
	v.width, v.height = 140, 40
	return v
}

// `6` is unconditional, which is the property that lets the pull-request tab
// move to `7` without costing anything: the digit means Step Details whether
// or not a pull request is linked, so no tab's number depends on the shape of
// the strip (issue #323, superseding 068.3's placement).
func TestStepDetailsTabIsReachedBySixWithOrWithoutAPullRequest(t *testing.T) {
	for _, linked := range []bool{false, true} {
		v := tabbedTaskFixture(t, taskTabSteps)
		if linked {
			v.applyPull(taskPullMsg{taskID: v.detail.taskID, pull: linkedPull()})
		}
		v.updateKey(registryKey(t, "6"))
		if v.tab != taskTabStepDetails {
			t.Fatalf("linked=%v: 6 moved to %v, want Step Details", linked, v.tab)
		}
	}
}

// The reciprocal: `7` is the conditional one, and absent still means absent.
func TestPullRequestTabIsReachedBySevenOnlyWhenLinked(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabSteps)
	if cmd := v.updateKey(registryKey(t, "7")); cmd != nil || v.tab != taskTabSteps {
		t.Fatalf("7 moved to %v with nothing linked, want no move", v.tab)
	}
	v.applyPull(taskPullMsg{taskID: v.detail.taskID, pull: linkedPull()})
	v.updateKey(registryKey(t, "7"))
	if v.tab != taskTabPull {
		t.Fatalf("7 moved to %v, want Pull Request", v.tab)
	}
}

// tab/⇧tab and [/] walk the strip as it stands, in both shapes. Step Details
// sits between Workflow and the conditional tab, so the cycle changes length
// but not order when the pull request comes and goes.
func TestStepDetailsSitsInTheCycleInBothStripShapes(t *testing.T) {
	forward := []taskViewTab{
		taskTabDetails, taskTabOutput, taskTabDiff, taskTabWorkflow, taskTabStepDetails,
		taskTabSteps,
	}
	walk := func(t *testing.T, v *taskView, key string, want []taskViewTab) {
		t.Helper()
		for i, tab := range want {
			v.updateKey(registryKey(t, key))
			if v.tab != tab {
				t.Fatalf("%q press %d landed on %v, want %v", key, i+1, v.tab, tab)
			}
		}
	}

	v := tabbedTaskFixture(t, taskTabSteps)
	walk(t, v, "tab", forward)
	v = tabbedTaskFixture(t, taskTabSteps)
	walk(t, v, "]", forward)

	linked := append(append([]taskViewTab(nil), forward[:5]...), taskTabPull, taskTabSteps)
	v = tabbedTaskFixture(t, taskTabSteps)
	v.applyPull(taskPullMsg{taskID: v.detail.taskID, pull: linkedPull()})
	walk(t, v, "tab", linked)

	// Backwards, from Steps, is the strip in reverse — which is where the
	// conditional tab's absence used to land the cycle on a tab that is not
	// on the strip at all.
	v = tabbedTaskFixture(t, taskTabSteps)
	walk(t, v, "shift+tab", []taskViewTab{taskTabStepDetails, taskTabWorkflow})
	v = tabbedTaskFixture(t, taskTabSteps)
	v.applyPull(taskPullMsg{taskID: v.detail.taskID, pull: linkedPull()})
	walk(t, v, "[", []taskViewTab{taskTabPull, taskTabStepDetails})
}

// The strip's hit-testing is rebuilt on every render, so a new label between
// two old ones moves every hit box after it. A click has to land on the label
// under the pointer, not on the one that used to be at that column.
func TestStepDetailsLabelIsClickableOnTheStrip(t *testing.T) {
	v := tabbedTaskFixture(t, taskTabSteps)
	v.applyPull(taskPullMsg{taskID: v.detail.taskID, pull: linkedPull()})
	v.bodyY = 3
	v.render(160, 40)

	hits := map[taskViewTab]taskTabHit{}
	for _, hit := range v.tabHits {
		hits[hit.tab] = hit
	}
	for _, tab := range []taskViewTab{taskTabStepDetails, taskTabPull} {
		hit, ok := hits[tab]
		if !ok {
			t.Fatalf("tab %v has no hit box on the strip", tab)
		}
		if got := hit.x1 - hit.x0; got != len(taskTabNames[tab]) {
			t.Fatalf("tab %v hit box is %d wide, want %d", tab, got, len(taskTabNames[tab]))
		}
		v.tab = taskTabSteps
		v.updateClick(tea.MouseClickMsg{X: hit.x0, Y: 1})
		if v.tab != tab {
			t.Fatalf("a click at column %d selected %v, want %v", hit.x0, v.tab, tab)
		}
		v.tab = taskTabSteps
		v.updateClick(tea.MouseClickMsg{X: hit.x1 - 1, Y: 1})
		if v.tab != tab {
			t.Fatalf("a click at column %d selected %v, want %v", hit.x1-1, v.tab, tab)
		}
	}
}

// Task 049 decision 4: one attempt cursor for the whole workspace. Arriving
// from Output or Diff lands on the attempt already being read, and moving it
// here is still moved when the reader goes back.
func TestStepDetailsSharesTheWorkspaceAttemptCursor(t *testing.T) {
	for _, from := range []taskViewTab{taskTabOutput, taskTabDiff} {
		v := stepDetailsFixture(t)
		v.tab = from
		v.detail.selectedRun = 502

		v.updateKey(registryKey(t, "6"))
		if v.detail.selectedRun != 502 {
			t.Fatalf("arriving from %v moved the cursor to %d, want 502", from, v.detail.selectedRun)
		}
		got := ansi.Strip(v.renderStepDetails(140, 40))
		if !strings.Contains(got, "attempt 3") {
			t.Fatalf("arriving from %v did not render the selected attempt:\n%s", from, got)
		}

		v.updateKey(registryKey(t, "left"))
		if v.detail.selectedRun != 501 {
			t.Fatalf("← selected %d, want 501", v.detail.selectedRun)
		}
		v.updateKey(registryKey(t, "right"))
		if v.detail.selectedRun != 502 {
			t.Fatalf("→ selected %d, want 502", v.detail.selectedRun)
		}
		// And the move is the workspace's: going back to Output shows it.
		v.updateKey(registryKey(t, "up"))
		v.tab = taskTabOutput
		if v.detail.selectedRun != 501 {
			t.Fatalf("↑ did not carry back to %v: cursor is %d", from, v.detail.selectedRun)
		}
	}
}

// Every group renders its own facts for a fully populated attempt.
func TestStepDetailsRendersEveryGroupForARecordedAttempt(t *testing.T) {
	v := stepDetailsFixture(t)
	got := ansi.Strip(v.renderStepDetails(140, 200))

	for _, want := range []string{
		// The identity, and the groups themselves.
		"step 2", "Implement", "iteration 3", "attempt 2", "failed",
		"Input", "Resolution", "Control flow", "Outcome",
		// Input: the render, the daemon's trailer marked as its own, the
		// check command and the summary.
		"Implement the change described in OPS-42.",
		"appended by vincent: the previous attempt's failure",
		"<previous-attempt-failure", "the build did not compile",
		"go test ./...", "two packages still fail",
		// Resolution: each value with the level that supplied it, plus the
		// §7.9 include chain read off the task's own snapshot.
		"claude (from the task)", "opus (from the workflow)", "high (from the adapter)",
		"restricted", "10m00s", "2m00s", "/bin/sh", "/tmp/wt/7",
		"release → shared-implement",
		// Control flow.
		"if: rendered to", "true", "3 of 5", "for_each item", "api",
		"for_each list", "1. api", "2. web", "fan-out lane",
		// Outcome.
		"1.2k↓/340↑", "$0.42", "45s", "check_failed", "/tmp/1-2.jsonl",
		"yes — the prompt",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("step details misses %q:\n%s", want, got)
		}
	}
	// A command step's script is not a missing record on an agent step.
	if strings.Contains(got, "rendered run") {
		t.Errorf("an agent step was asked about a run script:\n%s", got)
	}
}

// A row written before the record existed must not read as an attempt that was
// handed nothing. Every field says so in words instead.
func TestStepDetailsSaysNotRecordedForAPreMigrationAttempt(t *testing.T) {
	v := stepDetailsFixture(t)
	v.detail.selectedRun = 502
	got := ansi.Strip(v.renderStepDetails(140, 200))

	// The full wording has to appear, but the field column wraps it, so the
	// per-field tally counts the marker's unwrappable head instead — at this
	// width the sentence is split across two lines and a whole-phrase count
	// would report one field when nine said it.
	if !strings.Contains(got, stepInputNotRecorded) {
		t.Fatalf("nothing said %q:\n%s", stepInputNotRecorded, got)
	}
	const marker = "not recorded"
	if n := strings.Count(got, marker); n < 6 {
		t.Fatalf("only %d fields said %q; the prompt and the resolution must all say it:\n%s",
			n, marker, got)
	}
	// The prompt block is the one that would otherwise draw as an empty body.
	promptAt := strings.Index(got, "rendered prompt")
	if promptAt < 0 {
		t.Fatalf("an agent step rendered no prompt block:\n%s", got)
	}
	if !strings.Contains(got[promptAt:], marker) {
		t.Errorf("the prompt block did not say the record is absent:\n%s", got[promptAt:])
	}
	// An optional field the step never had is not reported as a lost record.
	if strings.Contains(got, "rendered check") {
		t.Errorf("an absent `check:` was reported as a missing record:\n%s", got)
	}
}

// A recorded render that came out empty is not the same as no record, and the
// two must not print the same words.
func TestStepDetailsDistinguishesAnEmptyRenderFromNoRecord(t *testing.T) {
	empty := ""
	run := recordedAttempt()
	run.RenderedPrompt = &empty
	if got := stepRenderedBlock(run.RenderedPrompt, 80, true); !strings.Contains(
		ansi.Strip(strings.Join(got, "\n")), stepInputRenderedEmpty) {
		t.Fatalf("an empty render printed %q, want the empty-render wording", got)
	}
	if got := stepRenderedBlock(nil, 80, true); !strings.Contains(
		ansi.Strip(strings.Join(got, "\n")), stepInputNotRecorded) {
		t.Fatalf("an absent record printed %q, want the not-recorded wording", got)
	}
}

// The record has a size ceiling, and a prefix shown as though it were whole is
// the thing this design refused. The marker is on screen, not in a log.
func TestStepDetailsMarksATruncatedRecord(t *testing.T) {
	v := stepDetailsFixture(t)
	run := recordedAttempt()
	run.InputTruncated = true
	v.detail.task.Steps[0] = run

	got := ansi.Strip(v.renderStepDetails(140, 200))
	if !strings.Contains(got, "64 KiB") || !strings.Contains(got, "prefix of what the step got") {
		t.Fatalf("a truncated record rendered no marker:\n%s", got)
	}
}

// A command step is asked about its script and not about a prompt, and its
// check command is shown when it recorded one.
func TestStepDetailsAsksACommandStepAboutItsScript(t *testing.T) {
	v := stepDetailsFixture(t)
	run := recordedAttempt()
	run.StepType = "command"
	run.RenderedPrompt, run.RenderedRun = nil, ptr("go build ./...")
	v.detail.task.Steps[0] = run

	got := ansi.Strip(v.renderStepDetails(140, 200))
	if !strings.Contains(got, "rendered run") || !strings.Contains(got, "go build ./...") {
		t.Fatalf("a command step's script is missing:\n%s", got)
	}
	if strings.Contains(got, "rendered prompt") {
		t.Errorf("a command step was asked about a prompt:\n%s", got)
	}
}

// The sidebar lists attempts, and a click on one selects it — the same shape
// the Task Details sidebar takes, against the shared cursor.
func TestStepDetailsSidebarSelectsAnAttemptByMouse(t *testing.T) {
	v := stepDetailsFixture(t)
	v.bodyY = 3
	v.render(140, 40)

	// The sidebar's order is the workspace's own (detail.attempts): step
	// index, then iteration, then attempt. The row a click lands on is what
	// this asserts, so the expectation is read off that order rather than
	// hard-coded to an id the sort could legitimately move.
	second := v.detail.attempts()[1]
	v.updateClick(tea.MouseClickMsg{X: 3, Y: v.bodyY + v.stepDetails.sidebarY + 1})
	if v.detail.selectedRun != second.ID {
		t.Fatalf("a click on the second sidebar row selected %d, want %d", v.detail.selectedRun, second.ID)
	}
	got := ansi.Strip(v.render(140, 40))
	want := fmt.Sprintf("attempt %d", second.Attempt)
	if !strings.Contains(got, "Step Details") || !strings.Contains(got, want) {
		t.Fatalf("the clicked attempt (%q) is not on screen:\n%s", want, got)
	}
}

// A task with no attempts has nothing to put in a sidebar, and says so rather
// than drawing an empty inspector.
func TestStepDetailsWithoutAttemptsSaysSo(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 9
	loadDetail(d, nil)
	v := newTaskView(d)
	v.tab = taskTabStepDetails

	if got := ansi.Strip(v.renderStepDetails(120, 20)); !strings.Contains(got, "no attempts yet") {
		t.Fatalf("an attemptless task rendered %q, want the placeholder", got)
	}
}
