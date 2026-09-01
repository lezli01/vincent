package tui

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// fixedNow keeps duration rendering deterministic.
var fixedNow = time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)

func newTestDetail(t *testing.T) *detail {
	t.Helper()
	d := newDetail(testCtx(t), newLevelHolder(), newRawHolder())
	d.now = func() time.Time { return fixedNow }
	d.width, d.height = 120, 30
	return d
}

// attempt builds one step-run fixture.
func attempt(id int64, stepIndex, n int, name, state string, live bool) apiclient.StepRun {
	r := apiclient.StepRun{
		ID: id, StepIndex: stepIndex, StepName: name, StepID: name, StepType: "agent",
		Attempt: n, State: state, Agent: ptr("claude"),
		TranscriptPath: ptr("/tmp/t.jsonl"),
		StartedAt:      fixedNow.Add(-time.Minute),
	}
	if !live {
		fin := fixedNow.Add(-30 * time.Second)
		r.FinishedAt = &fin
	}
	return r
}

func loadDetail(d *detail, steps []apiclient.StepRun) {
	d.applyLoaded(detailLoadedMsg{
		id: d.taskID,
		task: apiclient.TaskDetail{
			Task:  apiclient.Task{ID: d.taskID, Title: "detail task", State: stateRunning, StepTotal: 2},
			Steps: steps,
		},
	})
}

func TestDetailHeaderWrapsLongIdentityFields(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 78
	d.width = 48
	d.applyLoaded(detailLoadedMsg{
		id: d.taskID,
		task: apiclient.TaskDetail{
			Task: apiclient.Task{
				ID: d.taskID, State: "done", ProjectName: "vincent",
				Title:       "#90 Notify hook: the daemon has no way to signal a human outside the TUI",
				CurrentStep: 24, StepTotal: 24,
				BranchName: "vincent/78-90-notify-hook-the-daemon-has-no-way-to",
				Workflow:   "github-resolver",
				WorkflowOrigin: &apiclient.WorkflowOrigin{
					Scope: "project", File: ".vincent/workflows/github-resolver.yaml",
				},
			},
			Steps: []apiclient.StepRun{attempt(1006, 23, 1, "Confirm the merge", "succeeded", false)},
		},
	})

	got := d.timelinePanel(24)
	for i, line := range strings.Split(got, "\n") {
		if width := ansi.StringWidth(line); width > d.width {
			t.Errorf("timeline line %d is %d cells wide, want <= %d: %q", i, width, d.width, line)
		}
	}
	plain := ansi.Strip(got)
	for _, want := range []string{
		"outside the TUI", "branch", "vincent/78-90", "as-no-way-to",
		"workflow", ".vincent/workflows/github-resolver.y", "aml)", "Confirm the merge",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("wrapped task header is missing %q:\n%s", want, got)
		}
	}
	if d.timelineTop < 6 {
		t.Errorf("task header used only %d rows; want a wrapped identity block", d.timelineTop)
	}
}

// TestDetailTimelineRendersAttempts covers what §15 asks a timeline to show,
// including the two things this PR made visible: the §17 active duration with
// its excluded wait beside it, and the edited-attempt flag.
func TestDetailTimelineRendersAttempts(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 42

	failed := attempt(1, 0, 1, "implement", "failed", false)
	failed.FailureReason = ptr("template_error")
	live := attempt(2, 0, 2, "implement", "running", true)
	live.PromptOverride = true
	live.InputWaitMS = 20_000 // 20s waiting, inside a 60s wall clock
	live.CostUSD = ptr(0.21)
	live.InputTokens = ptr(int64(8100))
	live.OutputTokens = ptr(int64(120))
	loadDetail(d, []apiclient.StepRun{failed, live})

	got := d.timelinePanel(30)
	for _, want := range []string{
		"#42 detail task",        // header names the task
		"Step 1  implement",      // step group header, 1-based
		"Attempt 1", "Attempt 2", // both attempts
		"template_error", // why the first one died
		editedBadge,      // the human edit PR C recorded and nothing read
		"40s",            // 60s wall clock minus 20s waiting (§17)
		"+20s waiting",   // and the wait is stated, not silently dropped
		"8.1k↓/120↑",     // tokens
		"$0.21",          // cost
		"claude",         // the resolved agent
	} {
		if !strings.Contains(got, want) {
			t.Errorf("timeline missing %q:\n%s", want, got)
		}
	}
}

// TestDetailTimelineRendersParallelGroup: a group's sub-steps share one step
// index (task 014), so the timeline needs a tier the old renderer had no room
// for — one header naming the group, then each sub-step with its own
// attempts. Without it the attempts of three sub-steps interleave under one
// sub-step's name.
func TestDetailTimelineRendersParallelGroup(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 9

	// Two sub-steps at index 1, the lint one having taken a second attempt.
	test1 := attempt(1, 1, 1, "test", "succeeded", false)
	lint1 := attempt(2, 1, 1, "lint", "failed", false)
	lint1.FailureReason = ptr("nonzero_exit")
	lint2 := attempt(3, 1, 2, "lint", "succeeded", false)
	build := attempt(4, 0, 1, "build", "succeeded", false)

	d.applyLoaded(detailLoadedMsg{
		id: d.taskID,
		task: apiclient.TaskDetail{
			Task:  apiclient.Task{ID: d.taskID, Title: "grouped", State: stateRunning, StepTotal: 2},
			Steps: []apiclient.StepRun{build, test1, lint1, lint2},
			WorkflowSteps: []apiclient.WorkflowStep{
				{Index: 0, ID: "build", Type: "command"},
				{Index: 1, ID: "verify", Type: "parallel"},
			},
		},
	})

	got := d.timelinePanel(30)
	for _, want := range []string{
		"Step 1  build",             // the ordinary step is unchanged
		"Step 2  verify (parallel)", // the group is named from the snapshot, not from a row
		"· test",                    // each sub-step gets its own tier
		"· lint",
		"nonzero_exit", // and its own failure
	} {
		if !strings.Contains(got, want) {
			t.Errorf("timeline missing %q:\n%s", want, got)
		}
	}
	for i, line := range strings.Split(got, "\n") {
		if strings.Contains(line, "Step 2  verify") && (i == 0 || strings.TrimSpace(strings.Split(got, "\n")[i-1]) != "") {
			t.Error("the second step is not separated from the first by a breathing row")
		}
	}

	// Attempts must sit under their own sub-step rather than interleaving by
	// number: lint's two attempts are adjacent, after test's one.
	var order []string
	for _, line := range strings.Split(got, "\n") {
		switch {
		case strings.Contains(line, "· test"):
			order = append(order, "test")
		case strings.Contains(line, "· lint"):
			order = append(order, "lint")
		case strings.Contains(line, "Attempt 1"), strings.Contains(line, "Attempt 2"):
			order = append(order, "attempt")
		}
	}
	const want = "attempt test attempt lint attempt attempt"
	if got := strings.Join(order, " "); got != want {
		t.Errorf("timeline order = %q, want %q — a sub-step's attempts belong under it", got, want)
	}
}

// TestDetailSelectionFollowsOnlyTheLiveAttempt is the rollover rule: live
// movement never overrides a user who is browsing history.
func TestDetailSelectionFollowsOnlyTheLiveAttempt(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 7
	first := attempt(1, 0, 1, "plan", "running", true)
	loadDetail(d, []apiclient.StepRun{first})
	if d.selectedRun != 1 {
		t.Fatalf("selected = %d, want the only attempt", d.selectedRun)
	}

	// The step finishes and a new one starts: the cursor was on the live
	// attempt, so it follows.
	done := attempt(1, 0, 1, "plan", "succeeded", false)
	next := attempt(2, 1, 1, "implement", "running", true)
	loadDetail(d, []apiclient.StepRun{done, next})
	if d.selectedRun != 2 {
		t.Fatalf("selected = %d, want the new live attempt", d.selectedRun)
	}

	// Now the user browses back to the finished attempt.
	d.moveSelection(-1)
	if d.selectedRun != 1 {
		t.Fatalf("selected = %d, want the earlier attempt after moving up", d.selectedRun)
	}
	third := attempt(3, 2, 1, "publish", "running", true)
	loadDetail(d, []apiclient.StepRun{done, attempt(2, 1, 1, "implement", "succeeded", false), third})
	if d.selectedRun != 1 {
		t.Errorf("selected = %d; a refresh moved the cursor off what the user was reading", d.selectedRun)
	}
}

// TestDetailBufferedChunksDedupedByOffset is the catch-up seam: chunks that
// arrive while the transcript fetch is in flight are held, then admitted only
// if the fetch did not already cover them.
func TestDetailBufferedChunksDedupedByOffset(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 3
	d.displayRun = 9
	d.task.Steps = []apiclient.StepRun{attempt(9, 0, 1, "implement", "running", true)}
	d.streamID = 3 // a live subscription for this task
	d.fetching = true

	// Two chunks land mid-fetch: one the fetch will cover, one past its end.
	d.update(taskNoteMsgFor(3, 9, 100, "covered by the fetch"))
	d.update(taskNoteMsgFor(3, 9, 200, "newer than the fetch"))
	if len(d.buffer) != 2 {
		t.Fatalf("buffered %d chunks, want 2", len(d.buffer))
	}

	d.applyTranscript(detailTranscriptMsg{
		runID: 9,
		records: []apiclient.TranscriptRecord{
			{Type: "agent.output", Text: "covered by the fetch"},
		},
		next: 150,
	})

	got := strings.Join(d.outputLines(), "\n")
	if strings.Count(got, "covered by the fetch") != 1 {
		t.Errorf("line duplicated across the seam:\n%s", got)
	}
	if !strings.Contains(got, "newer than the fetch") {
		t.Errorf("line past the fetch was dropped:\n%s", got)
	}
	if d.nextOffset != 200 {
		t.Errorf("next offset = %d, want the newest chunk's 200", d.nextOffset)
	}
	if len(d.buffer) != 0 {
		t.Errorf("buffer not drained: %d left", len(d.buffer))
	}
}

// TestDetailChunkForUnknownAttemptIsHeld covers the other half: output from a
// step that started since the last fetch is held and forces a refetch, rather
// than being dropped for the ~150ms the debounce would cost.
func TestDetailChunkForUnknownAttemptIsHeld(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 3
	d.client = apiclient.New("http://127.0.0.1:1", "token") // never called in-test
	d.displayRun = 9
	d.task.Steps = []apiclient.StepRun{attempt(9, 0, 1, "implement", "succeeded", false)}
	d.streamID = 3          // a live subscription for this task
	d.refreshPending = true // a debounce is already pending and must be bypassed

	cmd := d.update(taskNoteMsgFor(3, 77, 10, "first line of a new step"))
	if cmd == nil {
		t.Fatal("an unknown attempt did not trigger a refetch")
	}
	if d.refreshPending {
		t.Error("the refetch is still behind the debounce")
	}
	if len(d.buffer) != 1 {
		t.Fatalf("buffered %d chunks, want the held line", len(d.buffer))
	}

	// When the refetch lands and the view moves to that attempt, the held
	// line is admitted by the same offset rule.
	loadDetail(d, []apiclient.StepRun{
		attempt(9, 0, 1, "implement", "succeeded", false),
		attempt(77, 1, 1, "publish", "running", true),
	})
	d.applyTranscript(detailTranscriptMsg{runID: 77, next: 0})
	if got := strings.Join(d.outputLines(), "\n"); !strings.Contains(got, "first line of a new step") {
		t.Errorf("held line never rendered:\n%s", got)
	}
}

// TestDetailFollowDropsAndRearms covers the follow contract, including the
// counter — dropping follow silently while output keeps arriving is how a
// reader concludes the run stalled.
func TestDetailFollowDropsAndRearms(t *testing.T) {
	d := newTestDetail(t)
	d.taskID = 5
	d.displayRun = 1
	d.task.Steps = []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)}
	d.following = true

	d.following = false // as if the user had scrolled up
	d.appendChunk(apiclient.OutputNote{
		Type: "agent.output", RunID: 1, Offset: 10,
		Payload: json.RawMessage(`{"text":"more output"}`),
	})
	if d.newLines != 1 {
		t.Errorf("new-line counter = %d, want 1", d.newLines)
	}
	if !strings.Contains(d.outputTitle(), "1 new") {
		t.Errorf("paused title does not report unread output: %q", d.outputTitle())
	}

	d.focus = focusOutput
	d.updateKey(tea.KeyPressMsg{Code: 'f', Text: "f"})
	if !d.following || d.newLines != 0 {
		t.Errorf("f did not re-arm follow: following=%v new=%d", d.following, d.newLines)
	}
	if !strings.Contains(d.outputTitle(), "following") {
		t.Errorf("following title missing: %q", d.outputTitle())
	}
}

// TestDetailEmptyStates covers the five distinct nothing-to-show cases; two
// of them used to be the same blank pane.
func TestDetailEmptyStates(t *testing.T) {
	cases := []struct {
		name  string
		setup func(d *detail)
		want  string
	}{
		{"no task", func(*detail) {}, "no task selected"},
		{"queued, no attempts", func(d *detail) {
			d.taskID = 1
			loadDetail(d, nil)
		}, "no attempts yet"},
		{"gate step has no transcript", func(d *detail) {
			d.taskID = 1
			gate := attempt(1, 0, 1, "review", "running", true)
			gate.TranscriptPath = nil
			loadDetail(d, []apiclient.StepRun{gate})
		}, "wrote no transcript"},
		{"transcript pruned", func(d *detail) {
			d.taskID = 1
			loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "failed", false)})
			d.applyTranscript(detailTranscriptMsg{runID: 1, err: errTest})
		}, "may have been pruned"},
		{"running, nothing yet", func(d *detail) {
			d.taskID = 1
			loadDetail(d, []apiclient.StepRun{attempt(1, 0, 1, "implement", "running", true)})
			d.applyTranscript(detailTranscriptMsg{runID: 1})
		}, "no output yet"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := newTestDetail(t)
			tc.setup(d)
			got := d.timelinePanel(15) + "\n" + d.outputPanel(120, 15)
			if !strings.Contains(got, tc.want) {
				t.Errorf("panels missing %q:\n%s", tc.want, got)
			}
		})
	}
}

// TestDetailOutputRendering covers the per-type rules: usage is fetched and
// not shown, unparsed lines collapse behind a count, and an overlong history
// says it was cut.
func TestDetailOutputRendering(t *testing.T) {
	d := newTestDetail(t)
	d.records = []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: "reading token.go"},
		{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{
			{Name: "Edit", Summary: "internal/auth/token.go", CallID: "toolu_01"},
		}},
		{Type: "agent.usage", Raw: json.RawMessage(`{"raw":"{}"}`)},
		{Type: "agent.raw", Line: `{"type":"system"}`},
		{Type: "agent.raw", Line: `{"type":"system"}`},
		{Type: "command.output", Text: "boom", Stream: "stderr"},
		{Type: "vincent.input_request", Kind: "question", Summary: "Which colour?"},
		{Type: "agent.result", ResultText: "all done"},
	}
	got := strings.Join(d.outputLines(), "\n")
	for _, want := range []string{
		"reading token.go", "▸ Edit", "internal/auth/token.go",
		"… 2 unrecognized line(s)", "boom",
		"? Which colour?",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
	// T4.16: the result's text repeats an assistant message already on
	// screen — cursor's is the whole turn — so a succeeding run reports its
	// outcome instead of saying the same words twice.
	if strings.Contains(got, "all done") {
		t.Errorf("result text repeated after the assistant message:\n%s", got)
	}
	if !strings.Contains(got, "✓ done") {
		t.Errorf("no outcome line for the finished run:\n%s", got)
	}
	if strings.Contains(got, `"raw"`) {
		t.Errorf("usage payload rendered into the tail:\n%s", got)
	}

	d.truncated = true
	if !strings.Contains(strings.Join(d.outputLines(), "\n"), "earlier output truncated") {
		t.Error("a truncated history does not say so")
	}
}

// TestDetailRecordCapDropsOldest guards the memory bound: §18 allows an agent
// to emit gigabytes, and the transcript on disk stays the full record.
func TestDetailRecordCapDropsOldest(t *testing.T) {
	d := newTestDetail(t)
	d.displayRun = 1
	for i := range maxRecords + 10 {
		d.appendChunk(apiclient.OutputNote{
			Type: "agent.output", RunID: 1, Offset: int64(i + 1),
			Payload: json.RawMessage(`{"text":"line"}`),
		})
	}
	if len(d.records) != maxRecords {
		t.Errorf("records = %d, want the cap %d", len(d.records), maxRecords)
	}
	if !d.truncated {
		t.Error("dropping the oldest records was not flagged")
	}
}

// TestDetailStreamLifecycle proves the subscription exists exactly while the
// sub-model is on screen with a *running* task: a tail nobody is watching
// costs a connection and unbounded memory for output the transcript already
// holds, and a task that is not running has no live output at all (T3.10).
func TestDetailStreamLifecycle(t *testing.T) {
	d := newTestDetail(t)
	d.setClient(apiclient.New("http://127.0.0.1:1", "token"))

	d.open(12, stateRunning)
	if d.streamID != 0 {
		t.Fatal("subscribed before the panels were on screen")
	}
	d.active = true
	d.syncStream()
	if d.streamID != 12 {
		t.Fatalf("stream task = %d, want 12 once on screen", d.streamID)
	}
	d.active = false
	d.syncStream()
	if d.streamID != 0 {
		t.Errorf("stream still open (task %d) after leaving the screen", d.streamID)
	}

	// A parked task has no live output: opening it must not subscribe.
	d.active = true
	d.open(13, stateAwaitingInput)
	if d.streamID != 0 {
		t.Errorf("stream open (task %d) for a task that is not running", d.streamID)
	}

	// The authoritative fetch overrides the hint in both directions.
	loadDetail(d, nil) // loadDetail installs stateRunning
	if d.streamID != 13 {
		t.Errorf("stream task = %d, want 13 once the fetch says it is running", d.streamID)
	}
	d.applyLoaded(detailLoadedMsg{id: 13, task: apiclient.TaskDetail{
		Task: apiclient.Task{ID: 13, State: stateBlocked},
	}})
	if d.streamID != 0 {
		t.Errorf("stream still open (task %d) after the task stopped running", d.streamID)
	}
}

// taskNoteMsgFor builds one live-output note as it arrives from the stream.
func taskNoteMsgFor(taskID, runID, offset int64, text string) taskNoteMsg {
	payload, _ := json.Marshal(map[string]any{
		"run_id": runID, "offset": offset, "text": text,
	})
	return taskNoteMsg{
		taskID: taskID,
		note: apiclient.OutputNote{
			Type: "agent.output", RunID: runID, Offset: offset, Payload: payload,
		},
	}
}

// errTest stands in for any transcript fetch failure.
var errTest = &apiclient.Error{Status: 404, Code: "not_found", Message: "gone"}
