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

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/scheduler"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
)

// askWorkflow is one agent step whose fake agent asks a question mid-run.
const askWorkflow = `name: ask
steps:
  - id: implement
    type: agent
    max_retries: 0
    prompt: |
      Do {{.Task.Title}}
`

// actionLiveHarness is the board harness plus a real engine: a real runner
// spawning the real fake-agent binary against a real worktree, behind the
// real handlers. The answer round trip is the one behavior in this PR that
// no unit test can honestly prove — the request comes out of a live process
// and the answer has to reach it.
type actionLiveHarness struct {
	st        *store.Store
	broker    *events.Broker
	m         *root
	p         *pump
	projectID int64
}

func newActionLiveHarness(t *testing.T) *actionLiveHarness {
	t.Helper()
	const token = "action-token"
	fake := agenttest.BuildFakeAgent(t)

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	git := gitx.New()
	dataDir := t.TempDir()
	agents := agent.NewRegistry(claude.New(func() string { return fake }))
	runner := taskrun.New(taskrun.Deps{
		Store:     st,
		Config:    config.Default,
		Worktrees: worktree.NewManager(git, dataDir),
		Agents:    agents,
		DataDir:   dataDir,
		Events:    broker,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	sched := scheduler.New(scheduler.Deps{
		Store:    st,
		Config:   config.Default,
		Admitter: runner,
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	st.SetEventHook(func(e *store.Event) {
		broker.Publish(e)
		if scheduler.WakeOn(e) {
			sched.Wake()
		}
	})

	s := api.New(api.Deps{
		Token:       token,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Broker:      broker,
		Git:         git,
		Agents:      agents,
		Catalog:     agent.NewCatalogCache(agents),
		Worktrees:   worktree.NewManager(git, dataDir),
		Runner:      runner,
		WakeRunner:  sched.Wake,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	// The context is taken *after* the server: cleanups run last-registered
	// first, so cancelling it (which drops the shell's SSE connections) has to
	// happen before httptest waits for those connections to close.
	ctx := testCtx(t)
	runner.Start(ctx)
	sched.Start(ctx)
	t.Cleanup(sched.Stop)
	t.Cleanup(runner.Stop)

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

	m := newRoot(ctx, cn, ackedDir(t))
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 40})
	msg := runCmd(t, m.Init(), 10*time.Second)
	if _, ok := msg.(connectedMsg); !ok {
		t.Fatalf("probe = %T, want connectedMsg", msg)
	}
	_, cmd := m.Update(msg)
	p := newPump(t, m, cmd)
	p.until(10*time.Second, "the event stream to go live", func() bool { return m.streamLive })

	repo := testrepo.Init(t, "main")
	proj := &store.Project{Name: "live", Path: repo, DefaultBranch: "main"}
	if err := st.CreateProject(context.Background(), proj); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return &actionLiveHarness{st: st, broker: broker, m: m, p: p, projectID: proj.ID}
}

func (h *actionLiveHarness) createTask(t *testing.T, title string) *store.Task {
	t.Helper()
	task := &store.Task{
		ProjectID: h.projectID, Title: title, WorkflowName: "ask",
		WorkflowSnapshot: askWorkflow,
		BaseBranch:       "main", BranchName: "vincent/live-" + title,
		State: store.TaskQueued,
	}
	if err := h.st.CreateTask(context.Background(), task, nil); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	return task
}

func (h *actionLiveHarness) state(t *testing.T, id int64) store.TaskState {
	t.Helper()
	task, err := h.st.GetTask(context.Background(), id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	return task.State
}

// TestDetailAnswersLiveAgentQuestion is T3.4's done-when: a fake-agent
// question is answerable from the detail view and the run resumes live. The
// agent process is real and blocked on stdin, so the answer this form submits
// is the thing that unblocks it — a stubbed endpoint would prove only that a
// button posts.
func TestDetailAnswersLiveAgentQuestion(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "ask-question")
	h := newActionLiveHarness(t)
	task := h.createTask(t, "answerable")

	_, cmd := h.m.Update(selectTaskMsg{id: task.ID})
	h.p.push(cmd)

	h.p.until(60*time.Second, "the agent to ask its question", func() bool {
		return h.state(t, task.ID) == store.TaskAwaitingInput
	})
	h.p.until(30*time.Second, "the form to render the question", func() bool {
		return strings.Contains(content(h.m), "?") && detailOf(h.m).form != nil
	})

	form := detailOf(h.m).form
	if form.req.Kind != apiclient.InputKindQuestion || len(form.req.Questions) == 0 {
		t.Fatalf("form request = %+v, want the agent's question", form.req)
	}
	if len(form.req.Questions[0].Options) == 0 {
		t.Fatalf("question has no options: %+v", form.req.Questions[0])
	}
	// The action bar offers answer, and nothing that would be invalid here.
	if actions := detailOf(h.m).target(); !actions.has(apiclient.ActionAnswer) ||
		actions.has(apiclient.ActionApprove) {
		t.Errorf("available actions = %v, want answer without approve", actions.actions)
	}

	// Open the popup, pick the first option and submit, exactly as a human
	// would: the form never opens itself (§15 — it announces itself and the
	// human presses the key).
	h.press(t, "enter")
	if v := h.m.views[viewTask].(*taskView); !v.popup {
		t.Fatal("enter did not open the answer popup")
	}
	h.press(t, " ")
	h.press(t, "enter")

	h.p.until(60*time.Second, "the run to resume and finish", func() bool {
		s := h.state(t, task.ID)
		return s == store.TaskDone || s == store.TaskBlocked
	})
	if got := h.state(t, task.ID); got != store.TaskDone {
		t.Fatalf("task ended %s, want done — the answer did not reach the agent", got)
	}
	// The form exists exactly while a request is pending, so the refetch that
	// follows the answer must take it away again.
	h.p.until(30*time.Second, "the answered form to disappear", func() bool {
		return detailOf(h.m).form == nil
	})
}

// TestBoardApprovesGateFromTheRow drives an action from the board, where
// triage happens: the key acts on the row under the cursor.
func TestBoardApprovesGateFromTheRow(t *testing.T) {
	h := newActionLiveHarness(t)
	task := h.createTask(t, "gated")
	ctx := context.Background()

	// Park the task at a gate with its open manual row, the way the engine
	// leaves one (§6).
	if _, _, err := h.st.TransitionTask(ctx, task.ID,
		store.TaskQueued, store.TaskAwaitingGate, store.TaskChange{}); err != nil {
		t.Fatalf("park at gate: %v", err)
	}
	run := &store.StepRun{
		TaskID: task.ID, StepIndex: 0, StepID: "implement", StepType: "manual",
		Attempt: 1, State: store.StepRunning, StartedAt: time.Now(),
		ResultSummary: "check the diff before publishing",
	}
	if err := h.st.CreateStepRun(ctx, run); err != nil {
		t.Fatalf("CreateStepRun: %v", err)
	}

	h.p.until(30*time.Second, "the board to show the gated task", func() bool {
		return strings.Contains(content(h.m), "gated")
	})
	h.press(t, "a")

	h.p.until(30*time.Second, "the gate to be approved", func() bool {
		return h.state(t, task.ID) != store.TaskAwaitingGate
	})
	if got := h.state(t, task.ID); got != store.TaskQueued && got != store.TaskRunning && got != store.TaskDone {
		t.Fatalf("task is %s after approve, want it advanced", got)
	}
}

// press sends one key through the shell, the way a terminal would.
func (h *actionLiveHarness) press(t *testing.T, key string) {
	t.Helper()
	_, cmd := h.m.Update(keyPress(key))
	h.p.push(cmd)
}

// keyPress builds the key message a terminal would deliver for one rune, or
// for enter.
func keyPress(key string) tea.KeyPressMsg {
	if key == "enter" {
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	}
	return tea.KeyPressMsg{Code: rune(key[0]), Text: key}
}

// detailOf reaches the detail sub-model for assertions about its state.
func detailOf(m *root) *detail {
	return m.views[viewHome].(*shell).detail
}
