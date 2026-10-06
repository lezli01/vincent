package claude

import (
	"os"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
)

// startBackgroundRun is startRun with a background wait, which is the field
// task 133 is about and the one startRun leaves zero.
func startBackgroundRun(t *testing.T, wait time.Duration, extraEnv ...string) agent.RunHandle {
	t.Helper()
	a := fakeAdapter(t)
	env := append(os.Environ(), "FAKEAGENT_SCENARIO=background")
	env = append(env, extraEnv...)
	h, err := a.Start(t.Context(), agent.RunSpec{
		Prompt:         "run the suite",
		WorkDir:        t.TempDir(),
		PermissionMode: agent.FullAuto,
		Env:            env,
		BackgroundWait: wait,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	return h
}

func countResults(events []agent.Event) int {
	n := 0
	for _, ev := range events {
		if ev.Type == agent.EventResult {
			n++
		}
	}
	return n
}

// The tracker against the real CLI's lines: two background shells, one
// finishing after the first result and one (`sleep 1000`) never finishing,
// until the stdin close kills it.
func TestBackgroundTrackerFixture(t *testing.T) {
	var (
		b         backgroundTasks
		atResults []int
		p         streamParser
	)
	for _, line := range fixtureLines(t, "stream_background_2.1.289.jsonl") {
		b.observe(line)
		if p.parse(line).Type == agent.EventResult {
			atResults = append(atResults, b.outstanding())
		}
	}
	if want := []int{2, 1}; len(atResults) != len(want) || atResults[0] != want[0] || atResults[1] != want[1] {
		t.Fatalf("outstanding at each result = %v, want %v", atResults, want)
	}
	if b.outstanding() != 0 {
		t.Errorf("outstanding after the kill = %d, want 0", b.outstanding())
	}
}

// The bug task 133 fixes: the model ends its turn on work still running, and
// the run must stay open until the CLI wakes it with the work's outcome.
func TestBackgroundWorkWakesTheModel(t *testing.T) {
	h := startBackgroundRun(t, time.Minute)
	events := drain(t, h)
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.IsError || res.ExitCode != 0 {
		t.Fatalf("exit=%d isError=%v (%s), want success", res.ExitCode, res.IsError, res.ErrorMessage)
	}
	if res.ResultText != "the suite passed" {
		t.Errorf("ResultText = %q, want the woken turn's answer", res.ResultText)
	}
	if n := countResults(events); n != 2 {
		t.Errorf("results = %d, want 2", n)
	}
	// Usage is per turn and summed; cost is the process's running total.
	if res.InputTokens != 110 || res.OutputTokens != 47 {
		t.Errorf("tokens = %d/%d, want 110/47 summed over both turns", res.InputTokens, res.OutputTokens)
	}
	if res.CostUSD == nil || *res.CostUSD != 0.02 {
		t.Errorf("cost = %v, want the last result's 0.02", res.CostUSD)
	}
}

// A zero wait is the old behaviour, kept as the opt-out: the run ends at the
// first result and the CLI kills the work.
func TestBackgroundWaitZeroEndsAtFirstResult(t *testing.T) {
	h := startBackgroundRun(t, 0, "FAKEAGENT_BACKGROUND_MS=30000")
	events := drain(t, h)
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ResultText != "waiting on the suite" || countResults(events) != 1 {
		t.Errorf("result %q after %d results, want the first and only one", res.ResultText, countResults(events))
	}
}

// Work that outlives the window is stopped, and the run still ends on the
// agent's answer rather than as a failure.
func TestBackgroundWaitLapses(t *testing.T) {
	h := startBackgroundRun(t, 300*time.Millisecond, "FAKEAGENT_BACKGROUND_MS=30000")
	start := time.Now()
	drain(t, h)
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("run took %s; the window should have ended it", elapsed)
	}
	if res.IsError || res.ExitCode != 0 || res.ResultText != "waiting on the suite" {
		t.Errorf("exit=%d isError=%v result=%q, want a success on the first answer",
			res.ExitCode, res.IsError, res.ResultText)
	}
}

// A task that never finishes — a server the agent left up — holds the run
// only for the window after the turn that outlived it, never for the whole
// of the step's timeout.
func TestBackgroundWaitLingeringTask(t *testing.T) {
	h := startBackgroundRun(t, 500*time.Millisecond,
		"FAKEAGENT_BACKGROUND_MS=50", "FAKEAGENT_BACKGROUND_LINGER=1")
	events := drain(t, h)
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.ResultText != "the suite passed" || countResults(events) != 2 {
		t.Errorf("result %q after %d results, want the woken turn's answer after 2",
			res.ResultText, countResults(events))
	}
}
