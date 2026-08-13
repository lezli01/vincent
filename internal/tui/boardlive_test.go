package tui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/worktree"
)

const threeStepWorkflow = `name: three
steps:
  - id: one
    type: command
    run: echo 1
  - id: two
    type: command
    run: echo 2
  - id: three
    type: command
    run: echo 3
`

// boardLiveHarness wires the shell to the real handlers over a real store,
// exactly as the daemon runs them.
type boardLiveHarness struct {
	st        *store.Store
	broker    *events.Broker
	m         *root
	p         *pump
	projectID int64
}

func newBoardLiveHarness(t *testing.T) *boardLiveHarness {
	t.Helper()
	const token = "board-token"
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	s := api.New(api.Deps{
		Token:       token,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Broker:      broker,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("port: %v", err)
	}
	cn := connector{
		resolveDataDir: func() (string, error) { return t.TempDir(), nil },
		readRuntime: func(string) (daemon.RuntimeInfo, error) {
			return daemon.RuntimeInfo{PID: 1, Port: port, StartedAt: time.Now()}, nil
		},
		checkHealth: daemon.CheckHealth,
		newClient: func(string) (*apiclient.Client, error) {
			return apiclient.New(ts.URL, token), nil
		},
		startDetached: func() (int, error) { t.Fatal("auto-start must not trigger"); return 0, nil },
		startTimeout:  time.Second,
		pollInterval:  time.Millisecond,
	}

	m := newRoot(testCtx(t), cn, ackedDir(t))
	// A width the full column set fits, so assertions read real values
	// rather than truncation.
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})

	msg := runCmd(t, m.Init(), 10*time.Second)
	if _, ok := msg.(connectedMsg); !ok {
		t.Fatalf("probe = %T, want connectedMsg", msg)
	}
	_, cmd := m.Update(msg)
	p := newPump(t, m, cmd)
	p.until(10*time.Second, "the event stream to go live", func() bool { return m.streamLive })

	proj := &store.Project{Name: "board", Path: "/nowhere", DefaultBranch: "main"}
	if err := st.CreateProject(context.Background(), proj); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return &boardLiveHarness{st: st, broker: broker, m: m, p: p, projectID: proj.ID}
}

func (h *boardLiveHarness) createTask(t *testing.T, title string) *store.Task {
	t.Helper()
	ctx := context.Background()
	task := &store.Task{
		ProjectID: h.projectID, Title: title, WorkflowName: "three",
		WorkflowSnapshot: threeStepWorkflow,
		BaseBranch:       "main", State: store.TaskQueued,
	}
	resolve := func(id int64) (string, error) { return worktree.BranchName(id, task.Title), nil }
	if err := h.st.CreateTask(ctx, task, resolve); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

// TestBoardRendersLiveFromSSE is T3.2's headline done-when: the board tracks
// state the daemon commits, driven by the event stream rather than polling.
// The step advance is the part that did not work before this PR — the engine
// moves the cursor without a state change, so k/n used to freeze.
func TestBoardRendersLiveFromSSE(t *testing.T) {
	h := newBoardLiveHarness(t)
	task := h.createTask(t, "live board task")
	ctx := context.Background()

	h.p.until(20*time.Second, "the new task to appear on the board", func() bool {
		return strings.Contains(content(h.m), "live board task")
	})
	h.p.until(10*time.Second, "the first step to render", func() bool {
		return strings.Contains(content(h.m), "1/3")
	})

	// Admit it, then advance a step — the write that emits no state change.
	if _, _, err := h.st.TransitionTask(
		ctx, task.ID, store.TaskQueued, store.TaskRunning, store.TaskChange{},
	); err != nil {
		t.Fatalf("TransitionTask: %v", err)
	}
	h.p.until(20*time.Second, "the running state to render", func() bool {
		return strings.Contains(content(h.m), string(store.TaskRunning))
	})

	step := 1
	if err := h.st.SetTaskProgress(ctx, task.ID, &step, nil); err != nil {
		t.Fatalf("SetTaskProgress: %v", err)
	}
	// The step name travels with the counter, so the board says what is
	// running. Waiting for the name, not the count: since the panels fused,
	// "2/3" can render on the detail header from its own fetch before the
	// board's debounced refresh lands, so a bare count is not proof the
	// *board* updated (macOS CI caught the gap).
	h.p.until(20*time.Second, "the advanced step to render on the board", func() bool {
		return strings.Contains(content(h.m), "2/3 two")
	})
}

// TestBoardPinsAndBadgesAwaitingInput is the rest of the done-when: a task
// waiting on a human is visibly flagged, above everything else.
func TestBoardPinsAndBadgesAwaitingInput(t *testing.T) {
	h := newBoardLiveHarness(t)
	quiet := h.createTask(t, "quiet task")
	noisy := h.createTask(t, "noisy task")
	ctx := context.Background()

	h.p.until(20*time.Second, "both tasks to appear", func() bool {
		got := content(h.m)
		return strings.Contains(got, "quiet task") && strings.Contains(got, "noisy task")
	})

	// The quiet one runs; the noisy one ends up waiting on a human.
	if _, _, err := h.st.TransitionTask(
		ctx, quiet.ID, store.TaskQueued, store.TaskRunning, store.TaskChange{},
	); err != nil {
		t.Fatalf("TransitionTask(quiet): %v", err)
	}
	if _, _, err := h.st.TransitionTask(
		ctx, noisy.ID, store.TaskQueued, store.TaskRunning, store.TaskChange{},
	); err != nil {
		t.Fatalf("TransitionTask(noisy): %v", err)
	}
	if _, _, err := h.st.TransitionTask(
		ctx, noisy.ID, store.TaskRunning, store.TaskAwaitingInput, store.TaskChange{},
	); err != nil {
		t.Fatalf("TransitionTask(awaiting): %v", err)
	}

	h.p.until(20*time.Second, "the awaiting-input badge", func() bool {
		return strings.Contains(content(h.m), string(store.TaskAwaitingInput))
	})

	got := content(h.m)
	noisyAt := strings.Index(got, "noisy task")
	quietAt := strings.Index(got, "quiet task")
	if noisyAt < 0 || quietAt < 0 {
		t.Fatalf("a task vanished from the board: %q", got)
	}
	if noisyAt > quietAt {
		t.Errorf("the awaiting-input task is not pinned above the running one:\n%s", got)
	}
	if !strings.Contains(got, attentionBadge) {
		t.Errorf("no attention badge rendered: %q", got)
	}
	if !strings.Contains(got, "1 need attention") {
		t.Errorf("header does not count the waiting task: %q", got)
	}
}

// TestBoardEnterOpensDetail proves the hand-off PR J inherits.
func TestBoardEnterOpensDetail(t *testing.T) {
	h := newBoardLiveHarness(t)
	task := h.createTask(t, "openable task")

	h.p.until(20*time.Second, "the task to appear", func() bool {
		return strings.Contains(content(h.m), "openable task")
	})

	_, cmd := h.m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on the board produced no command")
	}
	h.p.push(cmd)
	s := h.m.views[viewHome].(*shell)
	if s.focus != panelTimeline {
		t.Fatalf("focus = %v, want the timeline panel after enter", s.focus)
	}
	if s.detail.taskID != task.ID {
		t.Fatalf("detail task = %d, want %d", s.detail.taskID, task.ID)
	}
	// The open fetches immediately, so the header naming the task is also
	// proof the hand-off reached a sub-model with a working client.
	want := "#" + strconv.FormatInt(task.ID, 10) + " openable task"
	h.p.until(10*time.Second, "the detail header to render", func() bool {
		return strings.Contains(content(h.m), want)
	})
}
