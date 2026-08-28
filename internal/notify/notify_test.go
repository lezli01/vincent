package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskstate"
)

// fakeStore is the two reads a worker makes. It is a fake rather than a real
// SQLite store because these cases are about the notifier's own behaviour;
// the real store, the real broker and the daemon's own wiring are exercised
// in notify_live_test.go.
type fakeStore struct {
	tasks    map[int64]*store.Task
	projects map[int64]*store.Project
	taskErr  error
}

func (f *fakeStore) GetTask(_ context.Context, id int64) (*store.Task, error) {
	if f.taskErr != nil {
		return nil, f.taskErr
	}
	return f.tasks[id], nil
}

func (f *fakeStore) GetProject(_ context.Context, id int64) (*store.Project, error) {
	return f.projects[id], nil
}

// harness wires a notifier over a fake store with a fixed configuration.
type harness struct {
	notifier *Notifier
	store    *fakeStore
	cfg      config.Config
	logs     *logCapture
	mu       sync.Mutex
}

// logCapture keeps the daemon log lines a case wants to assert on.
type logCapture struct {
	mu    sync.Mutex
	lines []string
}

func (c *logCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.lines = append(c.lines, string(p))
	return len(p), nil
}

func (c *logCapture) contains(sub string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.lines {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func newHarness(t *testing.T, on []taskstate.State, argv []string) *harness {
	t.Helper()
	h := &harness{
		store: &fakeStore{
			tasks: map[int64]*store.Task{
				1: {
					ID: 1, ProjectID: 7, Title: "Fix the flaky gate",
					WorkflowName: "review", WorkflowSnapshot: "name: review\n",
					BranchName: "vincent/1-fix", WorktreePath: filepath.Join("/tmp", "wt", "1"),
					CurrentStep: 2, BlockReason: "step_failed",
				},
			},
			projects: map[int64]*store.Project{7: {ID: 7, Name: "vincent"}},
		},
		logs: &logCapture{},
	}
	h.cfg = config.Default()
	h.cfg.Notify = config.Notify{On: on, Command: argv}
	h.notifier = New(Deps{
		Store: h.store,
		Config: func() config.Config {
			h.mu.Lock()
			defer h.mu.Unlock()
			return h.cfg
		},
		Logger:    slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		StepCount: func(string) int { return 5 },
	})
	h.notifier.timeout = 3 * time.Second
	h.notifier.Start(t.Context())
	t.Cleanup(h.notifier.Stop)
	return h
}

// setConfig replaces the configuration the hook reads, the way a hot reload
// does.
func (h *harness) setConfig(f func(c *config.Config)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	f(&h.cfg)
}

// stateEvent builds a task.state_changed event the way store.statePayload
// does.
func stateEvent(id int64, taskID int64, from, to taskstate.State, extra map[string]any) *store.Event {
	payload := map[string]any{"from": string(from), "to": string(to)}
	for k, v := range extra {
		payload[k] = v
	}
	b, _ := json.Marshal(payload)
	tid := taskID
	return &store.Event{
		ID: id, TS: time.Date(2026, 8, 28, 9, 30, 0, 0, time.UTC),
		Type: store.EventTaskStateChanged, TaskID: &tid, Payload: b,
	}
}

// waitFor polls until cond holds or the deadline passes. Spawning a real
// process is asynchronous by construction, so there is nothing to synchronize
// on but the effect.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestFiresAndDeliversEnvelope is the feature: a matching transition spawns
// the configured command with byte-identical argv, and the JSON on its stdin
// carries what a notifier needs to write a message without calling back into
// the API.
func TestFiresAndDeliversEnvelope(t *testing.T) {
	dir := t.TempDir()
	argv := helperArgv(t, "capture", dir, "--tag", "vincent")
	h := newHarness(t, []taskstate.State{taskstate.Blocked}, argv)

	h.notifier.OnEvent(stateEvent(42, 1, taskstate.Running, taskstate.Blocked, nil))
	waitFor(t, "the notifier child to write its capture", func() bool {
		return len(helperFiles(t, dir)) == 2
	})

	var env Envelope
	var gotArgv string
	for _, body := range helperFiles(t, dir) {
		if strings.HasPrefix(strings.TrimSpace(body), "{") {
			if err := json.Unmarshal([]byte(body), &env); err != nil {
				t.Fatalf("stdin is not the documented envelope: %v (%q)", err, body)
			}
			continue
		}
		gotArgv = strings.TrimSpace(body)
	}
	if gotArgv != "[--tag vincent]" {
		t.Errorf("child argv tail = %q, want [--tag vincent]", gotArgv)
	}

	want := Envelope{
		EventID: 42, TS: "2026-08-28T09:30:00Z", Type: "task.state_changed",
		TaskID: 1, Title: "Fix the flaky gate", From: "running", To: "blocked",
		BlockReason: "step_failed", CurrentStep: 2, StepsTotal: 5,
		WorktreePath: filepath.Join("/tmp", "wt", "1"), Branch: "vincent/1-fix",
		ProjectID: 7, Project: "vincent", Workflow: "review",
	}
	if env != want {
		t.Errorf("envelope =\n  %+v\nwant\n  %+v", env, want)
	}
}

// TestAwaitingInputCarriesTheQuestion: the §7.4 transition already puts the
// normalized request's kind and summary in its event payload, and this is the
// only state that gets an `input` object.
func TestAwaitingInputCarriesTheQuestion(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, []taskstate.State{taskstate.AwaitingInput}, helperArgv(t, "capture", dir))

	h.notifier.OnEvent(stateEvent(9, 1, taskstate.Running, taskstate.AwaitingInput,
		map[string]any{"kind": "choice", "summary": "Overwrite the migration?"}))
	waitFor(t, "the notifier child to write its capture", func() bool {
		return len(helperFiles(t, dir)) == 2
	})

	var env Envelope
	for _, body := range helperFiles(t, dir) {
		if strings.HasPrefix(strings.TrimSpace(body), "{") {
			if err := json.Unmarshal([]byte(body), &env); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
		}
	}
	if env.Input == nil {
		t.Fatal("no input object on an awaiting_input transition")
	}
	if env.Input.Kind != "choice" || env.Input.Summary != "Overwrite the migration?" {
		t.Errorf("input = %+v, want the kind and summary from the event", env.Input)
	}
	// block_reason means "set while blocked" (§14) and must not ride along on
	// any other state, even though the task row carries one.
	if env.BlockReason != "" {
		t.Errorf("block_reason = %q on an awaiting_input transition", env.BlockReason)
	}
}

// TestSpawnsNothing covers every way a transition must be ignored.
func TestSpawnsNothing(t *testing.T) {
	cases := []struct {
		name  string
		on    []taskstate.State
		argv  func(dir string) []string
		event func(t *testing.T) *store.Event
	}{
		{
			name:  "unlisted state",
			on:    []taskstate.State{taskstate.Blocked},
			event: func(*testing.T) *store.Event { return stateEvent(1, 1, taskstate.Queued, taskstate.Running, nil) },
		},
		{
			name: "another event type",
			on:   []taskstate.State{taskstate.Blocked},
			event: func(*testing.T) *store.Event {
				e := stateEvent(1, 1, taskstate.Running, taskstate.Blocked, nil)
				e.Type = store.EventTaskCreated
				return e
			},
		},
		{
			name: "no task id",
			on:   []taskstate.State{taskstate.Blocked},
			event: func(*testing.T) *store.Event {
				e := stateEvent(1, 1, taskstate.Running, taskstate.Blocked, nil)
				e.TaskID = nil
				return e
			},
		},
		{
			name:  "states configured with no command",
			on:    []taskstate.State{taskstate.Blocked},
			argv:  func(string) []string { return nil },
			event: func(*testing.T) *store.Event { return stateEvent(1, 1, taskstate.Running, taskstate.Blocked, nil) },
		},
		{
			name:  "no states configured",
			on:    nil,
			event: func(*testing.T) *store.Event { return stateEvent(1, 1, taskstate.Running, taskstate.Blocked, nil) },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			argv := helperArgv(t, "capture", dir)
			if tc.argv != nil {
				argv = tc.argv(dir)
			}
			h := newHarness(t, tc.on, argv)
			h.notifier.OnEvent(tc.event(t))
			// Give a spawn that should not happen time to happen anyway.
			time.Sleep(200 * time.Millisecond)
			if got := helperFiles(t, dir); len(got) != 0 {
				t.Errorf("spawned a notifier: %q", got)
			}
		})
	}
}

// TestFanOutLaneIsSkipped: a lane is an ordinary task row (§7.6), so a
// twenty-lane tree reaching done would otherwise send twenty-one messages.
// The root parent's own transition still fires (task 046 decision 2).
func TestFanOutLaneIsSkipped(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, []taskstate.State{taskstate.Done}, helperArgv(t, "capture", dir))
	parent := int64(1)
	h.store.tasks[2] = &store.Task{
		ID: 2, ProjectID: 7, Title: "lane 0", ParentTaskID: &parent,
		WorkflowName: "lane", BranchName: "vincent/2-lane",
	}

	h.notifier.OnEvent(stateEvent(1, 2, taskstate.Running, taskstate.Done, nil))
	time.Sleep(200 * time.Millisecond)
	if got := helperFiles(t, dir); len(got) != 0 {
		t.Fatalf("a fan-out lane notified: %q", got)
	}

	h.notifier.OnEvent(stateEvent(2, 1, taskstate.Running, taskstate.Done, nil))
	waitFor(t, "the root task's own notification", func() bool {
		return len(helperFiles(t, dir)) == 2
	})
}

// TestHungChildIsKilled: the child never exits, the notifier kills its
// process tree at the timeout and logs it, and the worker is free again.
func TestHungChildIsKilled(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, []taskstate.State{taskstate.Blocked}, helperArgv(t, "hang", dir))
	h.notifier.timeout = 500 * time.Millisecond

	start := time.Now()
	h.notifier.OnEvent(stateEvent(1, 1, taskstate.Running, taskstate.Blocked, nil))
	waitFor(t, "the hung notifier to be killed and logged", func() bool {
		return h.logs.contains("notification command failed") && h.logs.contains("killed after")
	})
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Errorf("kill took %s; the timeout is meant to bound it", elapsed)
	}
	if !h.logs.contains("level=WARN") {
		t.Error("a killed notifier was not logged at warn")
	}
}

// TestPerChildTimeoutIsTenSeconds pins the documented, un-configurable value
// the tests above lower for speed.
func TestPerChildTimeoutIsTenSeconds(t *testing.T) {
	if perChildTimeout != 10*time.Second {
		t.Errorf("perChildTimeout = %s, want 10s (§12.3, task 046 decision 7)", perChildTimeout)
	}
	if New(Deps{Store: &fakeStore{}, Config: config.Default}).timeout != perChildTimeout {
		t.Error("New did not take the documented timeout")
	}
}

// TestNonZeroExitIsLoggedWithStderr: a notifier that ran and failed is
// reported with its exit code and a stderr tail, and is never retried.
func TestNonZeroExitIsLoggedWithStderr(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, []taskstate.State{taskstate.Blocked}, helperArgv(t, "fail", dir))

	h.notifier.OnEvent(stateEvent(1, 1, taskstate.Running, taskstate.Blocked, nil))
	waitFor(t, "the failed notifier to be logged", func() bool {
		return h.logs.contains("notification command failed")
	})
	if !h.logs.contains("exit status 3") {
		t.Error("the log line does not carry the exit code")
	}
	if !h.logs.contains("deliberate failure") {
		t.Error("the log line does not carry the stderr tail")
	}
	// No retry: exactly one run happened.
	time.Sleep(300 * time.Millisecond)
	if got := helperFiles(t, dir); len(got) != 1 {
		t.Errorf("the failed notifier ran %d times; it must not be retried", len(got))
	}
}

// TestStderrTailIsTruncated keeps a chatty notifier from flooding daemon.log.
func TestStderrTailIsTruncated(t *testing.T) {
	var b tailBuffer
	if _, err := b.Write([]byte(strings.Repeat("a", stderrTail*3))); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := b.Write([]byte("the reason it failed")); err != nil {
		t.Fatalf("write: %v", err)
	}
	got := b.String()
	if len(got) > stderrTail {
		t.Errorf("kept %d bytes, want at most %d", len(got), stderrTail)
	}
	if !strings.HasSuffix(got, "the reason it failed") {
		t.Error("the tail was dropped; a script that logs before it fails puts the reason last")
	}
}

// TestConcurrencyCapAndQueueDrop is asserted against the spawn hook rather
// than real processes: what is under test is the pool and the queue, and
// counting four concurrent children is the same assertion either way.
func TestConcurrencyCapAndQueueDrop(t *testing.T) {
	h := newHarness(t, []taskstate.State{taskstate.Blocked}, []string{"unused"})

	var inFlight, peak, ran atomic.Int64
	release := make(chan struct{})
	h.notifier.spawn = func(context.Context, job, []byte) error {
		n := inFlight.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		ran.Add(1)
		<-release
		inFlight.Add(-1)
		return nil
	}

	// Fill the workers and the queue, then push well past both. Publish must
	// stay prompt with every worker busy: this hook runs on the store's
	// writing goroutine.
	const burst = maxWorkers + queueCapacity + 32
	start := time.Now()
	for i := range burst {
		h.notifier.OnEvent(stateEvent(int64(i+1), 1, taskstate.Running, taskstate.Blocked, nil))
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("OnEvent blocked for %s across %d events; it must never block the publisher",
			elapsed, burst)
	}
	waitFor(t, "every worker to be busy", func() bool { return inFlight.Load() == maxWorkers })
	if !h.logs.contains("notifier queue full") {
		t.Error("the excess was not logged as a drop")
	}
	close(release)

	waitFor(t, "the queue to drain", func() bool { return ran.Load() >= maxWorkers+queueCapacity })
	// Nothing past the workers plus the queue may have run: the rest was
	// dropped, not backed up.
	time.Sleep(200 * time.Millisecond)
	if got := ran.Load(); got > maxWorkers+queueCapacity {
		t.Errorf("%d notifications ran; at most %d were admitted", got, maxWorkers+queueCapacity)
	}
	if peak.Load() > maxWorkers {
		t.Errorf("%d children ran at once, cap is %d", peak.Load(), maxWorkers)
	}
}

// TestUnreadableTaskIsSkipped: a task that cannot be read loses its
// notification and nothing else. The daemon keeps serving.
func TestUnreadableTaskIsSkipped(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, []taskstate.State{taskstate.Blocked}, helperArgv(t, "capture", dir))
	h.store.taskErr = errors.New("database is locked")

	h.notifier.OnEvent(stateEvent(1, 1, taskstate.Running, taskstate.Blocked, nil))
	waitFor(t, "the skip to be logged", func() bool {
		return h.logs.contains("task could not be read")
	})
	if got := helperFiles(t, dir); len(got) != 0 {
		t.Errorf("spawned a notifier without a task: %q", got)
	}
}

// TestHotReloadChangesWhatFires: the hook reads the configuration per event,
// so an edit to notify.on takes effect on the next transition with no
// restart and no re-registration.
func TestHotReloadChangesWhatFires(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, []taskstate.State{taskstate.Blocked}, helperArgv(t, "capture", dir))

	h.notifier.OnEvent(stateEvent(1, 1, taskstate.Running, taskstate.Done, nil))
	time.Sleep(200 * time.Millisecond)
	if got := helperFiles(t, dir); len(got) != 0 {
		t.Fatalf("done fired before it was configured: %q", got)
	}

	h.setConfig(func(c *config.Config) { c.Notify.On = []taskstate.State{taskstate.Done} })
	h.notifier.OnEvent(stateEvent(2, 1, taskstate.Running, taskstate.Done, nil))
	waitFor(t, "the reloaded state to fire", func() bool { return len(helperFiles(t, dir)) == 2 })
}

// TestStopIsIdempotentWithoutStart guards the shutdown path: Stop runs before
// broker.Close(), and a daemon that failed early may never have started the
// pool.
func TestStopIsIdempotentWithoutStart(_ *testing.T) {
	n := New(Deps{
		Store:  &fakeStore{},
		Config: config.Default,
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	n.Stop()
	n.Stop()
}
