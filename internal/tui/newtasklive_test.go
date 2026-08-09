package tui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
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
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// liveWorkflow is one agent step on the default fake-agent scenario, which
// succeeds. The second step names codex, which the harness does not install,
// so the picker has a genuinely unavailable agent to flag.
const liveWorkflow = `name: implement
description: One agent step, then a shell command.
defaults:
  agent: claude
steps:
  - id: implement
    type: agent
    max_retries: 0
    prompt: |
      Do {{.Task.Title}}
  - id: note
    type: command
    run: git --version
`

// newTaskLiveHarness is the shell against a real daemon: real store, real
// registry, real scheduler and runner, the real fake-agent binary, a real
// git repo. The form's whole point is that what it posts is runnable, and
// only an end-to-end run proves the body it builds is one the engine
// accepts.
type newTaskLiveHarness struct {
	st        *store.Store
	m         *root
	p         *pump
	projectID int64
}

func newNewTaskLiveHarness(t *testing.T) *newTaskLiveHarness {
	t.Helper()
	const token = "newtask-token"
	fake := agenttest.BuildFakeAgent(t)

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)

	globalDir := filepath.Join(t.TempDir(), "workflows")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	if err := os.WriteFile(filepath.Join(globalDir, "implement.yaml"), []byte(liveWorkflow), 0o600); err != nil {
		t.Fatalf("write workflow: %v", err)
	}

	git := gitx.New()
	dataDir := t.TempDir()
	wt := worktree.NewManager(git, dataDir)
	agents := agent.NewRegistry(
		claude.New(func() string { return fake }),
		codex.New(func() string { return filepath.Join(dataDir, "no-codex-here") }),
	)
	cache := agent.NewCatalogCache(agents)
	registry := workflow.NewRegistry(globalDir, workflow.Options{
		KnownAgents: agents.Names(),
		Catalogs:    cache.Catalogs,
	}, nil)
	registry.Reload()

	runner := taskrun.New(taskrun.Deps{
		Store:     st,
		Config:    config.Default,
		Worktrees: wt,
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
		Catalog:     cache,
		Workflows:   registry,
		Worktrees:   wt,
		Runner:      runner,
		WakeRunner:  sched.Wake,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

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

	m := newRoot(ctx, cn)
	m.Update(tea.WindowSizeMsg{Width: 160, Height: 60})
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
	return &newTaskLiveHarness{st: st, m: m, p: p, projectID: proj.ID}
}

// form is the new-task view as the shell holds it.
func (h *newTaskLiveHarness) form(t *testing.T) *newTask {
	t.Helper()
	n, ok := h.m.views[viewNewTask].(*newTask)
	if !ok {
		t.Fatalf("view = %T, want *newTask", h.m.views[viewNewTask])
	}
	return n
}

// sendKey drives one keystroke through the shell, exactly as the runtime
// would, so global routing and capture are exercised too.
func (h *newTaskLiveHarness) sendKey(msg tea.KeyPressMsg) {
	_, cmd := h.m.Update(msg)
	h.p.push(cmd)
}

func (h *newTaskLiveHarness) typeText(s string) {
	for _, k := range keys(s) {
		h.sendKey(k)
	}
}

// TestNewTaskFlowCreatesRunnableTask is T3.5's done-when: a task created
// through the flow runs end-to-end. Everything under the form is real — the
// handlers, the registry, the scheduler, the worktree and the agent process
// — so what this proves is that the body the form builds is one the engine
// can actually run, which is the only claim a stubbed endpoint cannot make.
func TestNewTaskFlowCreatesRunnableTask(t *testing.T) {
	h := newNewTaskLiveHarness(t)

	// n from the board opens the form and loads its catalogs.
	h.sendKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	n := h.form(t)
	h.p.until(10*time.Second, "the form to load its catalogs", func() bool { return n.loaded })

	if h.m.active != viewNewTask {
		t.Fatalf("active view = %v, want the new-task form", h.m.active)
	}
	// One project, so the form picks it and prefills the branch from it.
	if n.projectID != h.projectID {
		t.Fatalf("projectID = %d, want the only project %d", n.projectID, h.projectID)
	}
	if got := strings.TrimSpace(n.branch.Value()); got != "main" {
		t.Fatalf("branch = %q, want the project's default", got)
	}

	// The codex step of the registry's workflow is flagged, because the
	// harness never installed codex. This is the done-when's "unavailable
	// agent visibly flagged", read off a live probe rather than a fixture.
	if !n.agents.Unavailable("codex") {
		t.Fatal("codex reported available; the probe found a binary that does not exist")
	}
	opts := n.agentOptions()
	var codexNote string
	for _, o := range opts {
		if o.value == "codex" {
			codexNote = o.note
		}
	}
	if !strings.Contains(codexNote, "unavailable") {
		t.Errorf("codex option note = %q, want it flagged", codexNote)
	}
	// And the pickers are populated from that same probe.
	if len(n.agents) == 0 {
		t.Fatal("no adapters in the catalog")
	}
	claudeAgent, ok := n.agents.Find("claude")
	if !ok || !claudeAgent.Available {
		t.Fatalf("claude = %+v, want it available", claudeAgent)
	}

	// Pick the registry workflow, then title the task.
	moveTo(n, ntWorkflow)
	h.sendKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	for i, o := range n.pick.options {
		if o.value == "implement" {
			n.pick.cursor = i
		}
	}
	h.sendKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if n.workflow != "implement" {
		t.Fatalf("workflow = %q, want the one just picked", n.workflow)
	}

	moveTo(n, ntTitle)
	h.sendKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	h.typeText("wire the form")
	// While the title field has the keyboard, q must type, not quit.
	if !n.capturesInput() {
		t.Fatal("the title field is not capturing input")
	}
	h.sendKey(tea.KeyPressMsg{Code: tea.KeyEnter})

	// A custom field, so the template context carries something the form put
	// there.
	moveTo(n, ntFields)
	h.sendKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	addField(n, "ticket", "OPS-123")
	h.sendKey(tea.KeyPressMsg{Code: tea.KeyEscape})

	h.sendKey(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})

	// The shell lands on the new task, and the daemon runs it.
	h.p.until(20*time.Second, "the form to create the task", func() bool {
		return h.m.selectedTask != 0
	})
	if h.m.active != viewDetail {
		t.Errorf("active view = %v, want the detail view of the new task", h.m.active)
	}
	id := h.m.selectedTask

	h.p.until(60*time.Second, "the created task to finish", func() bool {
		task, err := h.st.GetTask(context.Background(), id)
		return err == nil && task.State == store.TaskDone
	})

	task, err := h.st.GetTask(context.Background(), id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if task.Title != "wire the form" {
		t.Errorf("Title = %q", task.Title)
	}
	if task.WorkflowName != "implement" {
		t.Errorf("WorkflowName = %q, want the picked workflow", task.WorkflowName)
	}
	if task.Fields["ticket"] != "OPS-123" {
		t.Errorf("Fields = %v, want the pair typed into the editor", task.Fields)
	}
	if task.BaseBranch != "main" {
		t.Errorf("BaseBranch = %q, want the prefilled default", task.BaseBranch)
	}
	// The draft is gone: a second n starts clean rather than resurrecting it.
	h.sendKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if h.m.active != viewNewTask {
		t.Fatalf("active view = %v, want n to reopen the form", h.m.active)
	}
	if n.titleText() != "" {
		t.Errorf("title = %q after reopening; the draft was not discarded", n.titleText())
	}
	// And n from inside the form does not restart it: there it is "no" to the
	// discard prompt.
	h.p.until(10*time.Second, "the reopened form to load", func() bool { return n.loaded })
	moveTo(n, ntTitle)
	h.sendKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	h.typeText("second thoughts")
	h.sendKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	h.sendKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	if n.titleText() != "second thoughts" {
		t.Errorf("title = %q; n inside the form reset the draft", n.titleText())
	}
}
