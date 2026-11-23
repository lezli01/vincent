package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// feedDetail is a task of three attempts with the middle one open.
func feedDetail(t *testing.T) *detail {
	t.Helper()
	d := newTestDetail(t)
	d.taskID = 5
	d.loaded = true
	plan := attempt(1, 0, 1, "plan", "succeeded", false)
	plan.ResultSummary = "Plan: read the wiring, add a JSON body."
	plan.CostUSD = ptr(0.04)
	exit := 1
	verify := attempt(2, 1, 1, "verify", "failed", false)
	verify.StepType, verify.Agent, verify.ExitCode = "command", nil, &exit
	publish := attempt(3, 2, 1, "publish", "succeeded", false)
	d.task.Steps = []apiclient.StepRun{plan, verify, publish}
	d.selectedRun, d.displayRun = 2, 2
	d.records = []apiclient.TranscriptRecord{
		{Type: "vincent.step_started"},
		{Type: "vincent.command_started", Raw: []byte(`{"command":"set -e\nsha256sum -c SHA256SUMS"}`)},
		{Type: "command.output", Text: "vincent_linux_amd64.tar.gz: FAILED", Stream: "stderr"},
		{Type: "vincent.step_finished"},
	}
	d.seqs = d.stamp.take(len(d.records))
	d.width = 100
	return d
}

func feedText(d *detail) string {
	lines, _ := d.feedLinesAt()
	return ansi.Strip(strings.Join(lines, "\n"))
}

// The feed is the whole task (task 168.4): every other attempt folds to one
// dashed line from its row, and the open one sits between a divider naming
// it and a line saying how it ended.
func TestFeedFoldsEveryOtherAttemptAroundTheOpenOne(t *testing.T) {
	got := feedText(feedDetail(t))
	lines := strings.Split(got, "\n")
	if !strings.HasPrefix(lines[0], "┄┄ ") || !strings.Contains(lines[0], "plan") ||
		!strings.Contains(lines[0], "$0.04") || !strings.Contains(lines[0], "“Plan: read the wiring") {
		t.Errorf("the first attempt is not folded to its row:\n%s", got)
	}
	if !strings.HasPrefix(lines[1], "── verify") || !strings.Contains(lines[1], "command") || !strings.Contains(lines[1], "attempt 1") {
		t.Errorf("the open attempt has no divider:\n%s", got)
	}
	for _, want := range []string{"$ set -e", "(+1 line · v shows them)", "┃ vincent_linux_amd64.tar.gz: FAILED", "✗ exited 1 after 30s"} {
		if !strings.Contains(got, want) {
			t.Errorf("the feed misses %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "step_started") || strings.Contains(got, "step_finished") {
		t.Errorf("the step markers still draw rows:\n%s", got)
	}
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "┄┄ ") || !strings.Contains(last, "publish") {
		t.Errorf("the later attempt is not folded after the open one:\n%s", got)
	}
}

// The pinned line says, on a live attempt, how long, how many tokens, what it
// cost and whether the pane follows; on a finished one, which attempt is open.
func TestFeedStatusLine(t *testing.T) {
	d := feedDetail(t)
	if got := ansi.Strip(d.feedStatus(100)); !strings.Contains(got, "verify · failed") || !strings.Contains(got, "run 2 of 3") {
		t.Errorf("finished status = %q", got)
	}
	live := attempt(4, 3, 1, "implement", "running", true)
	live.InputTokens, live.OutputTokens, live.CostUSD = ptr(int64(41000)), ptr(int64(6000)), ptr(0.31)
	d.task.Steps = append(d.task.Steps, live)
	d.displayRun, d.following = 4, true
	got := ansi.Strip(d.feedStatus(120))
	for _, want := range []string{"working…", "41.0k↑/6.0k↓", "$0.31", "▼ following", "run 4 of 4"} {
		if !strings.Contains(got, want) {
			t.Errorf("live status misses %q: %q", want, got)
		}
	}
	d.following, d.newLines = false, 3
	if got := ansi.Strip(d.feedStatus(120)); !strings.Contains(got, "▲ 3 new below · f") {
		t.Errorf("paused status = %q", got)
	}
}

// enter unfolds the newest tool call in view, and again folds it.
func TestEnterUnfoldsTheNewestCallInView(t *testing.T) {
	d := outputStripDetail(t, attempt(1, 0, 1, "implement", "running", true))
	d.records = []apiclient.TranscriptRecord{
		{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{{Name: "Read", Summary: "a.go", CallID: "c1"}}},
		{Type: "agent.tool_use", Tools: []apiclient.TranscriptTool{{Name: "Bash", Summary: "go vet ./...", CallID: "c2"}}},
		{Type: "agent.command_output", CallID: "c2", Output: "limits.go:42: unreachable code\n"},
	}
	d.seqs = d.stamp.take(len(d.records))
	v := newTaskView(d)
	v.tab = taskTabOutput
	v.renderOutput(100, 20)
	if strings.Contains(ansi.Strip(v.renderOutput(100, 20)), "unreachable code") {
		t.Fatal("a folded call's output is on screen")
	}
	v.updateKey(registryKey(t, "enter"))
	if !d.expandedCalls["c2"] || d.expandedCalls["c1"] {
		t.Fatalf("enter unfolded %v, want the newest call c2", d.expandedCalls)
	}
	if got := ansi.Strip(v.renderOutput(100, 20)); !strings.Contains(got, "unreachable code") {
		t.Errorf("the unfolded call's output is not on screen:\n%s", got)
	}
	v.updateKey(registryKey(t, "enter"))
	if d.expandedCalls["c2"] {
		t.Error("a second enter did not fold the call")
	}
}

// The plan is one row where it first appeared, carrying the newest list.
func TestPlanUpdatesInPlace(t *testing.T) {
	recs := []apiclient.TranscriptRecord{
		{Type: "agent.plan", Items: []apiclient.TranscriptPlanItem{{Text: "read"}, {Text: "fix"}}},
		{Type: "agent.output", Text: "reading"},
		{Type: "agent.plan", Items: []apiclient.TranscriptPlanItem{{Text: "read", Completed: true}, {Text: "fix"}}},
	}
	got := plainLines(outputLines(recs, levelNormal, 80, lineOpts{latestPlan: true}))
	plans := 0
	for _, l := range got {
		if strings.HasPrefix(l, gutterPlan) {
			plans++
			if !strings.Contains(l, "✓ read") {
				t.Errorf("the plan row is not the newest list: %q", l)
			}
		}
	}
	if plans != 1 || !strings.HasPrefix(got[0], gutterPlan) {
		t.Errorf("want one plan row, where it first appeared: %q", got)
	}
}
