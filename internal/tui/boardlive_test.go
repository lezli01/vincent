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
	return newBoardLiveHarnessConfig(t, config.Default)
}

// newBoardLiveHarnessConfig is the same harness against a chosen
// configuration — the daemon serves `tui:` on /v1/config, so this is how a
// configured board is exercised over the real handler rather than by poking
// the model.
func newBoardLiveHarnessConfig(t *testing.T, cfg func() config.Config) *boardLiveHarness {
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
		Config:      cfg,
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
	if err := h.st.SetTaskProgress(ctx, task.ID, &step, nil, nil); err != nil {
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
	h.p.until(10*time.Second, "the task workspace to open", func() bool {
		return h.m.active == viewTask
	})
	if h.m.active != viewTask {
		t.Fatalf("active view = %v, want the task workspace after enter", h.m.active)
	}
	taskView := h.m.views[viewTask].(*taskView)
	if taskView.tab != taskTabSteps {
		t.Fatalf("task tab = %v, want Steps & Attempts", taskView.tab)
	}
	if taskView.detail.taskID != task.ID {
		t.Fatalf("detail task = %d, want %d", taskView.detail.taskID, task.ID)
	}
	// The open fetches immediately, so the header naming the task is also
	// proof the hand-off reached a sub-model with a working client.
	want := "#" + strconv.FormatInt(task.ID, 10) + " openable task"
	h.p.until(10*time.Second, "the detail header to render", func() bool {
		return strings.Contains(content(h.m), want)
	})
}

// TestBoardGroupsFromTheDaemonConfig walks the whole path the grouping takes:
// config.yaml → GET /v1/config → apiclient → the task table. It is a live
// test for the reason the others are — the board renders what the daemon
// serves, and a wire type that drifted would show up here and nowhere else.
func TestBoardGroupsFromTheDaemonConfig(t *testing.T) {
	h := newBoardLiveHarness(t)
	h.createTask(t, "grouped task")

	h.p.until(20*time.Second, "the task to appear", func() bool {
		return strings.Contains(content(h.m), "grouped task")
	})
	// The project header, then the workflow header under it — the default
	// grouping, fetched rather than assumed.
	h.p.until(10*time.Second, "the group headers to render", func() bool {
		got := content(h.m)
		return strings.Contains(got, "▾ board") && strings.Contains(got, "▾ three")
	})
	b := h.m.views[viewHome].(*shell).board
	if !b.group.equal(defaultGrouping()) {
		t.Errorf("board grouping = %s, want the configured %s",
			b.group.label(), defaultGrouping().label())
	}
	// A grouped level costs no column: the header names it.
	if strings.Contains(content(h.m), "WORKFLOW") {
		t.Error("the WORKFLOW column is still on a board grouped by workflow")
	}
}

// TestBoardHonoursAConfiguredFlatTable is the other end of the setting: a
// config that asks for no grouping gets the table every version before this
// one rendered, columns included.
func TestBoardHonoursAConfiguredFlatTable(t *testing.T) {
	flat := func() config.Config {
		cfg := config.Default()
		cfg.TUI.Board.GroupBy = nil
		return cfg
	}
	h := newBoardLiveHarnessConfig(t, flat)
	h.createTask(t, "flat task")

	h.p.until(20*time.Second, "the task to appear", func() bool {
		return strings.Contains(content(h.m), "flat task")
	})
	h.p.until(10*time.Second, "the configured flat table to apply", func() bool {
		b := h.m.views[viewHome].(*shell).board
		return len(b.group) == 0
	})
	got := content(h.m)
	// The open glyph only: ▸ is the diff pane's and the pickers' marker too,
	// so scanning a whole frame for it proves nothing. A flat board renders no
	// headers at all, folded or otherwise.
	if strings.Contains(got, groupGlyphOpen) {
		t.Errorf("a flat board rendered a group header:\n%s", got)
	}
	if !strings.Contains(got, "WORKFLOW") {
		t.Errorf("a flat board dropped the WORKFLOW column:\n%s", got)
	}
}

// TestCollapsedGroupOpensForAwaitingInput drives task 054 decision 3's third
// safeguard through the real event path: the daemon commits a transition into
// awaiting_input, the shell's live SSE stream carries it, and the fold hiding
// that task opens by itself. The unit test pokes the model; this proves the
// event the daemon actually publishes is the one the board reads.
func TestCollapsedGroupOpensForAwaitingInput(t *testing.T) {
	h := newBoardLiveHarness(t)
	quiet := h.createTask(t, "quiet task")
	noisy := h.createTask(t, "noisy task")
	ctx := context.Background()

	h.p.until(20*time.Second, "both tasks to appear", func() bool {
		got := content(h.m)
		return strings.Contains(got, "quiet task") && strings.Contains(got, "noisy task")
	})

	b := h.m.views[viewHome].(*shell).board
	b.updateKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	if got := content(h.m); strings.Contains(got, "noisy task") {
		t.Fatalf("← did not fold the group away:\n%s", got)
	}
	if len(b.folds) == 0 {
		t.Fatal("← folded nothing")
	}

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

	h.p.until(20*time.Second, "the fold to open over the waiting task", func() bool {
		return len(b.folds) == 0 && strings.Contains(content(h.m), "noisy task")
	})
}
