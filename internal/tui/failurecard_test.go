package tui

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// cardTask is a blocked task at its second step, with an earlier step that
// succeeded and a failing attempt at the current one.
func cardTask(reason string, failing apiclient.StepRun) apiclient.TaskDetail {
	return apiclient.TaskDetail{
		Task: apiclient.Task{
			ID: 4, Title: "card task", State: stateBlocked, CurrentStep: 1, StepTotal: 3,
			StepName: failing.StepName, BlockReason: &reason,
			AvailableActions: []string{
				apiclient.ActionCancel, apiclient.ActionRetry, apiclient.ActionSkip,
				apiclient.ActionRepair, apiclient.ActionChat,
			},
		},
		Steps: []apiclient.StepRun{
			{ID: 1, StepIndex: 0, StepName: "build", Attempt: 1, State: stepSucceeded},
			failing,
		},
	}
}

func failedRun(id int64, attempt int, reason string) apiclient.StepRun {
	return apiclient.StepRun{
		ID: id, StepIndex: 1, StepID: "verify", StepName: "verify", StepType: "command",
		Attempt: attempt, State: "failed", FailureReason: &reason,
		ExitCode: ptr(2), TranscriptPath: ptr("/tmp/t.jsonl"),
	}
}

// cardView is a task view on the Overview over a hand-built task.
func cardView(t *testing.T, task apiclient.TaskDetail) *taskView {
	t.Helper()
	d := newTestDetail(t)
	d.taskID = task.ID
	d.applyLoaded(detailLoadedMsg{id: task.ID, task: task})
	v := newTaskView(d)
	v.tab = taskTabOverview
	v.width, v.height = 100, 40
	v.syncFailureEvidence() // what update does after every message
	return v
}

func TestFailureCardNonzeroExit(t *testing.T) {
	task := cardTask("nonzero_exit", failedRun(5, 2, "nonzero_exit"))
	c := deriveFailureCard(task, laneBlame{}, false)
	if !c.hasRun || c.run.ID != 5 || c.stepless {
		t.Fatalf("card = %+v, want attempt 5 carrying the reason", c)
	}
	line, code := c.headline()
	if line != "✗ Step 2 verify · attempt 2 · command exited non-zero" || code != "nonzero_exit" {
		t.Fatalf("headline = %q, %q", line, code)
	}
	recs := []apiclient.TranscriptRecord{
		{Type: "vincent.output", Phase: "run", Text: "compiling"},
		{Type: "vincent.output", Phase: "run", Text: "main.go:3: undefined: x\n\n"},
		{Type: "agent.tool_use"},
		{Type: "agent.error", Message: "exit status 2"},
	}
	if got := selectEvidence(c.reason, recs, 2); !slices.Equal(got, []string{"main.go:3: undefined: x", "exit status 2"}) {
		t.Fatalf("evidence = %q", got)
	}
}

// An agent step's check_failed is the check's output, never the agent's
// final message.
func TestFailureCardCheckFailedEvidenceIsTheCheck(t *testing.T) {
	recs := []apiclient.TranscriptRecord{
		{Type: "agent.output", Text: "All done, the tests pass."},
		{Type: "vincent.output", Phase: "check", Stream: "stderr", Text: "FAIL TestThing"},
		{Type: "vincent.output", Phase: "check", Stream: "stdout", Text: "exit status 1"},
		{Type: "agent.output", Text: "I am confident."},
	}
	got := selectEvidence(reasonCheckFailed, recs, evidenceLines)
	if !slices.Equal(got, []string{"FAIL TestThing", "exit status 1"}) {
		t.Fatalf("evidence = %q, want the check's lines only", got)
	}
}

func TestFailureCardNamesTheLoopIteration(t *testing.T) {
	run := failedRun(5, 1, "nonzero_exit")
	run.Iteration, run.LoopTotal, run.LoopItem = 4, 10, ptr("x")
	c := deriveFailureCard(cardTask("nonzero_exit", run), laneBlame{}, false)
	line, _ := c.headline()
	if !strings.Contains(line, `· iteration 4 of 10 · item "x" · attempt 1 ·`) {
		t.Fatalf("headline = %q", line)
	}
}

func TestFailureCardBlamesTheLane(t *testing.T) {
	v := fanOutFixture(t, "lane_failed", `lane "api" (task 42) is blocked, not done`)
	v.tab = taskTabOverview
	got := ansi.Strip(v.render(120, 40))
	for _, want := range []string{
		`⚠ Step 1 lanes · lane "api" · attempt 1 · lane failed  lane_failed`,
		"task 42", "the lane is blocked on worktree has uncommitted changes · worktree_dirty", "l open the lane",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("card misses %q:\n%s", want, got)
		}
	}
	lane := false
	for _, b := range v.liveBindings(bindingsFor(ctxTaskOverview)) {
		lane = lane || b.op == keymap.Lane
	}
	if !lane {
		t.Error("the Overview's bindings do not offer l on a blamed lane")
	}
	// No blame, no `l`.
	v = cardView(t, cardTask("nonzero_exit", failedRun(5, 1, "nonzero_exit")))
	for _, b := range v.liveBindings(bindingsFor(ctxTaskOverview)) {
		if b.op == keymap.Lane {
			t.Error("the Overview offers l with no lane to open")
		}
	}
}

func TestFailureCardSteplessBlockUsesBlockDetail(t *testing.T) {
	task := cardTask("worktree_dirty", apiclient.StepRun{ID: 5, StepIndex: 0, StepName: "build", State: stepSucceeded})
	task.Steps = task.Steps[1:]
	task.BlockDetail = ptr("README.md is modified in the worktree")
	c := deriveFailureCard(task, laneBlame{}, false)
	if !c.stepless || c.wantsEvidence() {
		t.Fatalf("card = %+v, want a step-less block that fetches nothing", c)
	}
	v := cardView(t, task)
	if cmd := v.syncFailureEvidence(); cmd != nil {
		t.Fatal("a step-less block issued a transcript fetch")
	}
	got := ansi.Strip(v.render(100, 40))
	for _, want := range []string{"worktree has uncommitted changes  worktree_dirty", "README.md is modified in the worktree"} {
		if !strings.Contains(got, want) {
			t.Errorf("card misses %q:\n%s", want, got)
		}
	}
	// An older daemon serves no block_detail: the card says where to look.
	task.BlockDetail = nil
	got = ansi.Strip(cardView(t, task).render(100, 40))
	if !strings.Contains(got, "details are in the daemon log (: daemon)") {
		t.Errorf("card without block_detail misses the daemon-log line:\n%s", got)
	}
}

func TestFailureCardAbortedIsMuted(t *testing.T) {
	task := cardTask("canceled", failedRun(5, 1, "canceled"))
	task.State = stateAborted
	task.AvailableActions = []string{apiclient.ActionArchive}
	v := cardView(t, task)
	got := ansi.Strip(v.render(100, 40))
	if !strings.Contains(got, "■ Step 2 verify · attempt 1 · canceled") {
		t.Errorf("aborted card misses its muted headline:\n%s", got)
	}
	for _, not := range []string{"✗", "Evidence", "retry"} {
		if strings.Contains(got, not) {
			t.Errorf("aborted card shows %q:\n%s", not, got)
		}
	}
	if cmd := v.syncFailureEvidence(); cmd != nil || v.evidence.runID != 0 {
		t.Error("an aborted card fetched evidence")
	}
}

func TestFailureCardActionOrder(t *testing.T) {
	offered := []string{
		apiclient.ActionCancel, apiclient.ActionChat, apiclient.ActionRepair,
		apiclient.ActionSkip, apiclient.ActionRetry, apiclient.ActionArchive,
	}
	var got []string
	for _, a := range cardActions("check_failed", offered) {
		got = append(got, a.key+" "+a.action)
		if a.does == "" {
			t.Errorf("%s has no explanation", a.action)
		}
	}
	want := []string{"r retry", "E edit_retry", "s skip", "R repair", "T chat", "c cancel", "A archive"}
	if !slices.Equal(got, want) {
		t.Fatalf("actions = %q, want %q", got, want)
	}
	// No retry offered, no E.
	for _, a := range cardActions("check_failed", []string{apiclient.ActionCancel}) {
		if a.action == actionEditRetry {
			t.Fatal("E offered without retry")
		}
	}
}

func TestFailureCardDropsUnboundActions(t *testing.T) {
	// Skip taking `c` leaves cancel with no key at all.
	km, _, err := keymap.BuildLenient(map[string]string{"skip": "c"})
	if err != nil {
		t.Fatalf("keymap: %v", err)
	}
	setKeymap(km)
	t.Cleanup(func() { setKeymap(keymap.Default()) })
	if opKey(keymap.Cancel) != "" {
		t.Fatalf("cancel is still bound to %q", opKey(keymap.Cancel))
	}
	for _, a := range cardActions("check_failed", []string{apiclient.ActionRetry, apiclient.ActionCancel}) {
		if a.action == apiclient.ActionCancel {
			t.Fatal("an unbound cancel is still on the card")
		}
	}
}

// The status message is the step's own words: quoted, apart, and never the
// reason.
func TestFailureCardStatusIsNeverTheReason(t *testing.T) {
	run := failedRun(5, 1, "check_failed")
	run.StatusMessage = ptr("all green here")
	task := cardTask("check_failed", run)
	c := deriveFailureCard(task, laneBlame{}, false)
	if line, _ := c.headline(); strings.Contains(line, "all green") {
		t.Fatalf("the status reached the headline: %q", line)
	}
	got := ansi.Strip(cardView(t, task).render(100, 40))
	if !strings.Contains(got, "status  “all green here”") {
		t.Errorf("card misses the quoted status:\n%s", got)
	}
	// It does not stand in for missing evidence either.
	if !strings.Contains(got, "details are in the daemon log") {
		t.Errorf("status replaced the missing evidence:\n%s", got)
	}
}

func TestFailureCardCollapsesOnShortTerminals(t *testing.T) {
	v := cardView(t, cardTask("check_failed", failedRun(5, 2, "check_failed")))
	got := ansi.Strip(v.render(80, cardCollapseRows-1))
	for _, want := range []string{"✗ Step 2 verify · attempt 2 · check failed", "r retry · E edit retry · s skip", "3 output of attempt 2"} {
		if !strings.Contains(got, want) {
			t.Errorf("collapsed card misses %q:\n%s", want, got)
		}
	}
	for _, not := range []string{"Evidence", "What you can do", "Blocked at"} {
		if strings.Contains(got, not) {
			t.Errorf("collapsed card keeps %q:\n%s", not, got)
		}
	}
}

func TestFailureCardFitsAndReadsWithoutColour(t *testing.T) {
	v := cardView(t, cardTask("check_failed", failedRun(5, 2, "check_failed")))
	frame := v.render(80, 30)
	for _, line := range strings.Split(frame, "\n") {
		if w := ansi.StringWidth(line); w > 80 {
			t.Errorf("line is %d wide at 80 columns: %q", w, ansi.Strip(line))
		}
	}
	var buf bytes.Buffer
	w := &colorprofile.Writer{Forward: &buf, Profile: colorprofile.ASCII}
	if _, err := w.Write([]byte(frame)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "✗ Step 2 verify") {
		t.Errorf("the failure glyph does not survive NO_COLOR:\n%s", buf.String())
	}
}

// One fetch per failing attempt: an event that leaves the attempt where it
// was fetches nothing, and a retry that blocks again fetches the new one.
func TestFailureEvidenceFetchedOncePerRun(t *testing.T) {
	task := cardTask("check_failed", failedRun(5, 1, "check_failed"))
	v := cardView(t, task)
	v.detail.client = apiclient.New("http://127.0.0.1:1", "token")
	v.evidence = failureEvidence{} // cardView's sync ran without a client
	if cmd := v.syncFailureEvidence(); cmd == nil || v.evidence.runID != 5 || !v.evidence.fetching {
		t.Fatalf("no fetch for the failing attempt: %+v", v.evidence)
	}
	if got := ansi.Strip(v.render(100, 40)); !strings.Contains(got, "loading evidence…") {
		t.Errorf("an in-flight fetch does not say so:\n%s", got)
	}
	v.update(failureEvidenceMsg{taskID: 4, runID: 5, records: []apiclient.TranscriptRecord{
		{Type: "vincent.output", Phase: "check", Text: "FAIL TestThing"},
	}})
	if got := ansi.Strip(v.render(100, 40)); !strings.Contains(got, "FAIL TestThing") {
		t.Errorf("the evidence is not on the card:\n%s", got)
	}
	// Events refresh the task; the failing attempt is the same one.
	v.detail.applyLoaded(detailLoadedMsg{id: 4, task: task})
	if cmd := v.syncFailureEvidence(); cmd != nil {
		t.Fatal("a refresh on the same failing attempt refetched")
	}
	// Retry, block again: a new attempt, one new fetch.
	task.Steps = append(task.Steps, failedRun(6, 2, "check_failed"))
	v.detail.applyLoaded(detailLoadedMsg{id: 4, task: task})
	if cmd := v.syncFailureEvidence(); cmd == nil || v.evidence.runID != 6 {
		t.Fatalf("the new failing attempt was not fetched: %+v", v.evidence)
	}
	// Off the Overview, nothing is fetched.
	task.Steps = append(task.Steps, failedRun(7, 3, "check_failed"))
	v.detail.applyLoaded(detailLoadedMsg{id: 4, task: task})
	v.tab = taskTabSteps
	if cmd := v.syncFailureEvidence(); cmd != nil {
		t.Fatal("the card fetched while another tab was open")
	}
}
