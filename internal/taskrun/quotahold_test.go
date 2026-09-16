package taskrun

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// This file is task 106: an adapter whose usage window vincent watched close
// holds every task that is about to spawn on it, not only the task that hit
// the wall. The observations are seeded rather than produced by a real stop
// wherever the stop is not what is being asserted, so each test states the
// window it runs against.

// quotaClock is the one clock the runner and the scheduler share in these
// tests, so "the window reopened" is a step of the test rather than a sleep.
type quotaClock struct{ offset atomic.Int64 }

func (c *quotaClock) now() time.Time { return time.Now().Add(time.Duration(c.offset.Load())) }

// advance moves both clocks past a seeded reset in one step.
func (c *quotaClock) advance(d time.Duration) { c.offset.Add(int64(d)) }

// syncBuffer is a log sink the engine's goroutines and the test can share.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// newQuotaHarness is an engine harness whose runner and scheduler both read
// clock, and whose daemon log is kept for the lines a test asserts on.
func newQuotaHarness(t *testing.T, clock *quotaClock, mutate func(*config.Config)) (*engineHarness, *syncBuffer) {
	t.Helper()
	logs := &syncBuffer{}
	h := newEngineHarnessWith(t, mutate, func(d *Deps) {
		d.Now = clock.now
		d.Logger = slog.New(slog.NewTextHandler(logs, nil))
	})
	h.schedNow = clock.now
	return h, logs
}

// seedWall files an observed, still-shut window for agentName, reset an hour
// from the harness clock's now.
func seedWall(t *testing.T, h *engineHarness, clock *quotaClock, agentName string, reported bool) *store.AgentQuota {
	t.Helper()
	now := clock.now()
	q := &store.AgentQuota{
		Agent:            agentName,
		ObservedAt:       now.Add(-time.Minute).UTC(),
		ResetsAt:         now.Add(time.Hour).UTC(),
		ResetsAtReported: reported,
		Source:           store.QuotaSourceObserved,
	}
	if _, err := h.store.UpsertAgentQuota(t.Context(), q); err != nil {
		t.Fatalf("seed %s observation: %v", agentName, err)
	}
	stored, err := h.store.GetAgentQuota(t.Context(), agentName)
	if err != nil {
		t.Fatalf("read back %s observation: %v", agentName, err)
	}
	return stored
}

// quotaEvents counts the daemon-level `agent.quota_changed` events so far.
func quotaEvents(t *testing.T, h *engineHarness) int {
	t.Helper()
	events, err := h.store.ListEvents(t.Context(), store.EventFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	n := 0
	for _, e := range events {
		if e.Type == store.EventAgentQuotaChanged {
			n++
		}
	}
	return n
}

// holdPayloads is the payload of every transition that put the task on a
// usage-limit hold.
func holdPayloads(t *testing.T, h *engineHarness, id int64) []map[string]any {
	t.Helper()
	events, err := h.store.ListEvents(t.Context(), store.EventFilter{})
	if err != nil {
		t.Fatalf("ListEvents: %v", err)
	}
	var out []map[string]any
	for _, e := range events {
		if e.TaskID == nil || *e.TaskID != id || e.Type != store.EventTaskStateChanged {
			continue
		}
		var p map[string]any
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatalf("decode event payload: %v", err)
		}
		if p["queued_reason"] == ReasonUsageLimit {
			out = append(out, p)
		}
	}
	return out
}

// assertEarlyHold is the shape every avoided wall takes: queued on the
// ordinary usage-limit hold, until the observation's own reset.
func assertEarlyHold(t *testing.T, held *store.Task, wall *store.AgentQuota) {
	t.Helper()
	if held.QueuedReason != ReasonUsageLimit {
		t.Errorf("queued_reason = %q, want %s", held.QueuedReason, ReasonUsageLimit)
	}
	if held.AdmitNotBefore == nil || !held.AdmitNotBefore.Equal(wall.ResetsAt) {
		t.Errorf("admit_not_before = %v, want the observation's resets_at %s",
			held.AdmitNotBefore, wall.ResetsAt)
	}
}

// resume advances the shared clock past every seeded reset and wakes the
// scheduler, which is what the tick would do once the window reopened.
func resume(h *engineHarness, clock *quotaClock) {
	clock.advance(2 * time.Hour)
	h.sched.Wake()
}

// TestWalledAdapterHoldsWithoutSpawning is the feature: an agent step whose
// adapter's observed window is still shut never starts. No row, no
// transcript, no retry — and the observation it read is left exactly as it
// was, because nothing new was learned.
func TestWalledAdapterHoldsWithoutSpawning(t *testing.T) {
	clock := &quotaClock{}
	h, logs := newQuotaHarness(t, clock, nil)
	wall := seedWall(t, h, clock, "claude", false)
	eventsBefore := quotaEvents(t, h)

	task := h.createTask(t, claudeStepSnapshot)
	h.start(t)
	held := h.waitForHold(t, task.ID)
	assertEarlyHold(t, held, wall)

	if runs := h.stepRuns(t, task.ID); len(runs) != 0 {
		t.Errorf("step runs = %+v, want none — the process was never started", runs)
	}
	if _, err := os.Stat(filepath.Join(h.dataDir, "transcripts", strconv.FormatInt(task.ID, 10))); !os.IsNotExist(err) {
		t.Errorf("transcript directory exists (stat err %v); nothing ran to write one", err)
	}
	attempts, err := h.store.CountStepAttempts(t.Context(),
		store.StepRef{TaskID: task.ID, StepID: "implement"}, time.Time{})
	if err != nil {
		t.Fatalf("CountStepAttempts: %v", err)
	}
	if attempts.Failed != 0 || attempts.Last != 0 {
		t.Errorf("attempts = %+v, want none counted", attempts)
	}

	after := claudeQuota(t, h)
	if !after.ObservedAt.Equal(wall.ObservedAt) || after.ResetsAtReported != wall.ResetsAtReported ||
		!after.ResetsAt.Equal(wall.ResetsAt) {
		t.Errorf("observation changed from %+v to %+v; an avoided wall records nothing", wall, after)
	}
	if got := quotaEvents(t, h); got != eventsBefore {
		t.Errorf("agent.quota_changed events = %d, want %d — nothing about the window changed",
			got, eventsBefore)
	}

	payloads := holdPayloads(t, h, task.ID)
	if len(payloads) != 1 || payloads[0]["agent"] != "claude" {
		t.Errorf("hold payloads = %+v, want one naming claude", payloads)
	}
	if !strings.Contains(logs.String(), "adapter's usage window is still shut; not spawning") {
		t.Error("no log line telling an avoided wall from one that was hit")
	}
}

// TestExpiredObservationSpawns: an observation whose reset has passed, by the
// engine's clock, is history rather than a wall.
func TestExpiredObservationSpawns(t *testing.T) {
	clock := &quotaClock{}
	h, _ := newQuotaHarness(t, clock, nil)
	seedWall(t, h, clock, "claude", true)
	clock.advance(2 * time.Hour)

	task := h.createTask(t, claudeStepSnapshot)
	h.start(t)
	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%q), want done — the window had reopened", done.State, done.BlockReason)
	}
	if runs := h.stepRuns(t, task.ID); len(runs) != 1 || runs[0].State != store.StepSucceeded {
		t.Errorf("step runs = %+v, want one succeeded attempt", runs)
	}
}

// TestEarlyHoldFollowsTheMode is decision 2's table: the check holds only
// where the stop itself would wait, and reads the mode when it runs.
func TestEarlyHoldFollowsTheMode(t *testing.T) {
	cases := []struct {
		mode     string
		reported bool
		hold     bool
	}{
		{config.UsageLimitAlways, false, true},
		{config.UsageLimitReportedOnly, false, false},
		{config.UsageLimitReportedOnly, true, true},
		{config.UsageLimitNever, true, false},
		{config.UsageLimitNever, false, false},
	}
	for _, tc := range cases {
		leg := "estimated"
		if tc.reported {
			leg = "reported"
		}
		t.Run(tc.mode+"/"+leg, func(t *testing.T) {
			clock := &quotaClock{}
			// Built on `always` and switched before the task is created, so
			// the mode under test is one the check can only have read live.
			h, _ := newQuotaHarness(t, clock, func(c *config.Config) {
				c.UsageLimitAutoContinue = config.UsageLimitAlways
			})
			h.reload(func(c *config.Config) { c.UsageLimitAutoContinue = tc.mode })
			wall := seedWall(t, h, clock, "claude", tc.reported)

			task := h.createTask(t, claudeStepSnapshot)
			h.start(t)
			if tc.hold {
				assertEarlyHold(t, h.waitForHold(t, task.ID), wall)
				if runs := h.stepRuns(t, task.ID); len(runs) != 0 {
					t.Errorf("step runs = %+v, want none", runs)
				}
				return
			}
			done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
			if done.State != store.TaskDone {
				t.Fatalf("task = %s (%q), want done — this mode lets each task find the wall itself",
					done.State, done.BlockReason)
			}
			if runs := h.stepRuns(t, task.ID); len(runs) != 1 {
				t.Errorf("step runs = %d, want the one attempt that spawned", len(runs))
			}
		})
	}
}

// TestWallOnAnotherAdapterDoesNotHold: the observation is per adapter, so a
// codex step runs straight past claude's wall.
func TestWallOnAnotherAdapterDoesNotHold(t *testing.T) {
	clock := &quotaClock{}
	h, _ := newQuotaHarness(t, clock, nil)
	seedWall(t, h, clock, "claude", true)

	task := h.createTask(t, codexStepSnapshot)
	h.start(t)
	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("codex task = %s (%q), want done", done.State, done.BlockReason)
	}
}

// TestCommandStepBeforeTheWallRunsOnce: the check is at the spawn, so the
// command step ahead of the agent step runs and the cursor moves past it. The
// re-admission after the reset starts at the agent step.
func TestCommandStepBeforeTheWallRunsOnce(t *testing.T) {
	clock := &quotaClock{}
	h, _ := newQuotaHarness(t, clock, nil)
	wall := seedWall(t, h, clock, "claude", false)

	snapshot := "name: prep-then-agent\ndefaults:\n  agent: claude\nsteps:\n" +
		commandStep("prepare", "git --version") +
		"  - id: implement\n    type: agent\n    prompt: \"Do the work\"\n"
	task := h.createTask(t, snapshot)
	h.start(t)

	held := h.waitForHold(t, task.ID)
	assertEarlyHold(t, held, wall)
	if held.CurrentStep != 1 {
		t.Errorf("current_step = %d, want 1 — the command step before the wall is done", held.CurrentStep)
	}
	runs := h.stepRuns(t, task.ID)
	if len(stepRunsFor(runs, "prepare")) != 1 || len(stepRunsFor(runs, "implement")) != 0 {
		t.Fatalf("step runs = %+v, want the command step alone", runs)
	}

	resume(h, clock)
	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%q), want done after the reset", done.State, done.BlockReason)
	}
	runs = h.stepRuns(t, task.ID)
	if got := stepRunsFor(runs, "prepare"); len(got) != 1 {
		t.Errorf("prepare ran %d times, want once — re-admission starts at the agent step", len(got))
	}
	if got := stepRunsFor(runs, "implement"); len(got) != 1 || got[0].State != store.StepSucceeded {
		t.Errorf("implement rows = %+v, want one succeeded attempt", got)
	}
}

// TestParallelGroupHoldsOnlyTheWalledLane: the open lane runs, the walled lane
// writes no row, and the collected outcome holds the task. After the reset
// only the unfinished lane runs (§7.5).
func TestParallelGroupHoldsOnlyTheWalledLane(t *testing.T) {
	clock := &quotaClock{}
	h, _ := newQuotaHarness(t, clock, nil)
	wall := seedWall(t, h, clock, "claude", false)

	agentLane := func(id, adapter string) string {
		return "  - id: " + id + "\n    type: agent\n    agent: " + adapter +
			"\n    prompt: \"Do the work\"\n"
	}
	task := h.createTask(t, groupSnapshot("", agentLane("walled", "claude"), agentLane("open", "codex")))
	h.start(t)

	assertEarlyHold(t, h.waitForHold(t, task.ID), wall)
	lanes := subRuns(h.stepRuns(t, task.ID))
	if len(lanes["walled"]) != 0 {
		t.Errorf("walled lane rows = %+v, want none", lanes["walled"])
	}
	if got := lanes["open"]; len(got) != 1 || got[0].State != store.StepSucceeded {
		t.Errorf("open lane rows = %+v, want one succeeded attempt", got)
	}

	resume(h, clock)
	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%q), want done after the reset", done.State, done.BlockReason)
	}
	lanes = subRuns(h.stepRuns(t, task.ID))
	if got := lanes["walled"]; len(got) != 1 || got[0].State != store.StepSucceeded {
		t.Errorf("walled lane rows = %+v, want one succeeded attempt", got)
	}
	if got := lanes["open"]; len(got) != 1 {
		t.Errorf("open lane ran %d times, want once — only the unfinished lane re-runs", len(got))
	}
}

// TestRepairOnAWalledAdapterHolds: a repair is an agent spawn like any other.
// It holds with its request undrained, so the next admission runs the repair
// rather than a plain retry.
func TestRepairOnAWalledAdapterHolds(t *testing.T) {
	clock := &quotaClock{}
	h, _ := newQuotaHarness(t, clock, nil)
	blocked := blockedOnCheck(t, h)
	wall := seedWall(t, h, clock, "claude", false)

	if _, err := h.runner.Repair(t.Context(), blocked.ID,
		store.RepairRequest{Prompt: "try something", Agent: "claude"}); err != nil {
		t.Fatalf("Repair: %v", err)
	}
	held := h.waitForHold(t, blocked.ID)
	assertEarlyHold(t, held, wall)
	if held.PendingRepair == nil {
		t.Error("the repair request was drained by a hold; the next admission would not run it")
	}
	if got := repairRuns(h.stepRuns(t, blocked.ID)); len(got) != 0 {
		t.Errorf("repair rows = %+v, want none", got)
	}

	resume(h, clock)
	after := h.waitForDrainedRepair(t, blocked.ID)
	if after.State != store.TaskBlocked || after.BlockReason != ReasonCheckFailed {
		t.Errorf("task = %s/%q, want blocked/%s once the repair ran",
			after.State, after.BlockReason, ReasonCheckFailed)
	}
	if got := repairRuns(h.stepRuns(t, blocked.ID)); len(got) != 1 || got[0].State == store.StepInterrupted {
		t.Errorf("repair rows = %+v, want the one attempt that ran", got)
	}
}

// TestFollowUpOnAWalledAdapterHolds: a follow-up round's agent step goes
// through the same spawn, and waits the same way.
func TestFollowUpOnAWalledAdapterHolds(t *testing.T) {
	clock := &quotaClock{}
	h, _ := newQuotaHarness(t, clock, nil)
	done := doneTask(t, h)
	wall := seedWall(t, h, clock, "claude", false)

	if _, err := h.runner.FollowUp(t.Context(), done.ID, agentFollowUp(t, "one more thing")); err != nil {
		t.Fatalf("FollowUp: %v", err)
	}
	assertEarlyHold(t, h.waitForHold(t, done.ID), wall)
	if got := followUpRuns(h.stepRuns(t, done.ID), 1); len(got) != 0 {
		t.Errorf("follow-up rows = %+v, want none", got)
	}

	resume(h, clock)
	after := h.settle(t, done.ID)
	if after.State != store.TaskDone {
		t.Fatalf("task = %s (%q), want done after the round ran", after.State, after.BlockReason)
	}
	if got := followUpRuns(h.stepRuns(t, done.ID), 1); len(got) != 1 || got[0].State != store.StepSucceeded {
		t.Errorf("follow-up rows = %+v, want one succeeded attempt", got)
	}
}

// TestEditRetryOverrideSurvivesAnEarlyHold: the check is ahead of the insert
// that drains an `edit + retry`, so the edit waits on the task for the attempt
// that really runs. The block it starts from is a real stop under `never`,
// retried under `always`.
func TestEditRetryOverrideSurvivesAnEarlyHold(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "usage-limit")
	// Walled exactly once: the attempt after the reset succeeds.
	t.Setenv("FAKEAGENT_USAGE_LIMIT_MARKER", filepath.Join(t.TempDir(), "window-spent"))
	clock := &quotaClock{}
	h, _ := newQuotaHarness(t, clock, func(c *config.Config) {
		c.UsageLimitRecheckInterval = config.Duration(time.Hour)
		c.UsageLimitAutoContinue = config.UsageLimitNever
	})
	task := h.createTask(t, quotaModeSnapshot)
	h.start(t)
	blocked := h.waitForBlock(t, task.ID)
	if blocked.BlockReason != ReasonUsageLimit {
		t.Fatalf("block_reason = %q, want %s", blocked.BlockReason, ReasonUsageLimit)
	}
	wall := claudeQuota(t, h)

	h.reload(func(c *config.Config) { c.UsageLimitAutoContinue = config.UsageLimitAlways })
	if _, _, err := h.runner.Retry(t.Context(), task.ID, store.Override{Prompt: "edited"}); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	held := h.waitForHold(t, task.ID)
	assertEarlyHold(t, held, wall)
	if held.PendingOverride == nil || held.PendingOverride.Prompt != "edited" {
		t.Errorf("pending override = %+v, want the edit still waiting", held.PendingOverride)
	}
	if runs := h.stepRuns(t, task.ID); len(runs) != 1 {
		t.Errorf("step runs = %d, want only the attempt that hit the wall", len(runs))
	}

	resume(h, clock)
	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%q), want done after the reset", done.State, done.BlockReason)
	}
	if done.PendingOverride != nil {
		t.Errorf("pending override = %+v, want it drained onto the attempt", done.PendingOverride)
	}
	runs := h.stepRuns(t, task.ID)
	if len(runs) != 2 || runs[1].PromptOverride != "edited" {
		t.Errorf("step runs = %+v, want the edited attempt second", runs)
	}
}

// TestPauseResumeWhileWalledDoesNotSpawn: pausing a held task and resuming it
// clears its hold (task 003 decision 1). While the observation is live the
// re-admission meets the check, not the wall.
func TestPauseResumeWhileWalledDoesNotSpawn(t *testing.T) {
	clock := &quotaClock{}
	h, _ := newQuotaHarness(t, clock, nil)
	wall := seedWall(t, h, clock, "claude", false)

	task := h.createTask(t, claudeStepSnapshot)
	h.start(t)
	h.waitForHold(t, task.ID)

	if _, err := h.runner.Pause(t.Context(), task.ID); err != nil {
		t.Fatalf("Pause: %v", err)
	}
	h.waitForState(t, task.ID, store.TaskPaused)
	if _, err := h.runner.Resume(t.Context(), task.ID); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	assertEarlyHold(t, h.waitForHold(t, task.ID), wall)

	if runs := h.stepRuns(t, task.ID); len(runs) != 0 {
		t.Errorf("step runs = %+v, want none across both admissions", runs)
	}
	if got := holdPayloads(t, h, task.ID); len(got) != 2 {
		t.Errorf("usage-limit holds = %d, want 2 — one per admission", len(got))
	}
}

// TestUnreadableObservationSpawns: a read failure on the display table is
// logged and the step runs. The row is made unreadable over a second
// connection, the way recover_test injects its storage failure.
func TestUnreadableObservationSpawns(t *testing.T) {
	clock := &quotaClock{}
	h, logs := newQuotaHarness(t, clock, nil)
	db, err := sql.Open("sqlite", h.store.Path())
	if err != nil {
		t.Fatalf("open second connection: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if _, err := db.Exec(`INSERT INTO agent_quota
		(agent, observed_at, resets_at, resets_at_reported, source)
		VALUES ('claude', 'not a time', 'not a time', 1, 'observed')`); err != nil {
		t.Fatalf("seed unreadable observation: %v", err)
	}

	task := h.createTask(t, claudeStepSnapshot)
	h.start(t)
	done := h.waitForState(t, task.ID, store.TaskDone, store.TaskBlocked)
	if done.State != store.TaskDone {
		t.Fatalf("task = %s (%q), want done — a failed read must not stall work",
			done.State, done.BlockReason)
	}
	if !strings.Contains(logs.String(), "read agent quota; spawning anyway") {
		t.Errorf("the read failure was not logged:\n%s", logs.String())
	}
}
