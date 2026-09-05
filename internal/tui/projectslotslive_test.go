package tui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
)

// The projects view's slot figures, against the real handlers (§11, issue
// #324). The `running / cap` column, the rail summary and the "running now"
// fact are rendered beside the per-project cap the scheduler applies with
// store.CountSlotHoldersByProject, which counts every task in a slot-holding
// state — `awaiting_input` as well as `running`, fan-out lanes as well as
// roots. `GET /v1/tasks` omits lanes by default (task 014 decision 13,
// §13.2), so the view cannot arrive at that number by walking the task list
// it holds; it has to render the served `slots_used`. Only a live round-trip
// proves it does, because a view that decoded zero would read as a project
// with nothing running — a confident wrong answer, which is the bug.

// projectSlotsHarness wires a projects view to the real API handlers over a
// real store, which is the only place the lane rows exist.
type projectSlotsHarness struct {
	st        *store.Store
	view      *projectsView
	projectID int64
	url       string
	token     string
}

// newProjectSlotsHarness seeds one project, capped at limit (nil = no cap of
// its own, so the daemon-wide 3 of config.Default is what it competes for).
func newProjectSlotsHarness(t *testing.T, limit *int) *projectSlotsHarness {
	t.Helper()
	const token = "projects-token"
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	pr := &store.Project{
		Name: "slots", Path: "/nowhere", DefaultBranch: "main",
		MaxParallelTasks: limit,
	}
	if err := st.CreateProject(context.Background(), pr); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

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

	return &projectSlotsHarness{
		st: st, view: newProjectsView(), projectID: pr.ID,
		url: ts.URL, token: token,
	}
}

// seed creates one task, as a lane of parent when parent is non-nil (§7.6).
func (h *projectSlotsHarness) seed(t *testing.T, name string, state store.TaskState, parent *int64) *store.Task {
	t.Helper()
	task := &store.Task{
		ProjectID:        h.projectID,
		Title:            name,
		WorkflowName:     "adhoc",
		WorkflowSnapshot: "steps: []",
		BaseBranch:       "main",
		BranchName:       "vincent/" + name,
		State:            state,
		ParentTaskID:     parent,
	}
	if parent != nil {
		idx := 0
		task.ParentStepIndex = &idx
		task.LaneID = name
	}
	if err := h.st.CreateTask(context.Background(), task, nil); err != nil {
		t.Fatalf("CreateTask(%s): %v", name, err)
	}
	return task
}

// load runs the view's own load command against the real server and feeds it
// the message that command produced, so nothing here bypasses the wire.
func (h *projectSlotsHarness) load(t *testing.T) apiclient.Project {
	t.Helper()
	h.view.update(tea.WindowSizeMsg{Width: 120, Height: 40})
	msg := runCmd(t, h.view.setClient(apiclient.New(h.url, h.token)), 10*time.Second)
	loaded, ok := msg.(projectsLoadedMsg)
	if !ok {
		t.Fatalf("load = %T, want projectsLoadedMsg", msg)
	}
	if loaded.err != nil {
		t.Fatalf("load: %v", loaded.err)
	}
	h.view.update(loaded)
	for _, pr := range h.view.projects {
		if pr.ID == h.projectID {
			return pr
		}
	}
	t.Fatalf("projects = %v, want the seeded one", h.view.projects)
	return apiclient.Project{}
}

func TestProjectSlotColumnCountsEverySlotHolder(t *testing.T) {
	h := newProjectSlotsHarness(t, nil)

	// A parked fan-out parent has released its slot before its lanes need
	// one (§7.6), so of these four rows exactly two hold one — and neither
	// of the two is a `running` root, which is all the old walk looked for.
	parent := h.seed(t, "parent", store.TaskAwaitingChildren, nil)
	h.seed(t, "lane", store.TaskRunning, &parent.ID)
	h.seed(t, "asking", store.TaskAwaitingInput, nil)
	h.seed(t, "waiting", store.TaskQueued, nil)

	pr := h.load(t)
	if pr.SlotsUsed != 2 {
		t.Fatalf("SlotsUsed = %d, want 2 (a running lane and a task awaiting input)", pr.SlotsUsed)
	}

	// The task list the view also holds is the root-only one, which is why
	// the figure cannot be derived here: the lane is absent from it, so a
	// walk for `running` rows would have said 0.
	for _, task := range h.view.tasks {
		if task.Title == "lane" {
			t.Fatalf("task list carries the lane; /v1/tasks omits it by default (§13.2)")
		}
	}

	if got, want := h.view.capCell(pr), "2 / — (global 3)"; got != want {
		t.Errorf("cap cell = %q, want %q", got, want)
	}
	if got := h.view.projectRailSummary(pr); !strings.Contains(got, "2 running") {
		t.Errorf("rail summary = %q, want both slot holders counted", got)
	}
	fact := runningNowLine(t, ansi.Strip(h.view.renderProjectOverview(pr, 40)))
	if !strings.Contains(fact, "2") {
		t.Errorf("`running now` fact = %q, want 2", fact)
	}
}

// runningNowLine picks the overview's `running now` fact out of the panel, so
// the assertion is on that number rather than on any 2 rendered anywhere.
func runningNowLine(t *testing.T, overview string) string {
	t.Helper()
	for line := range strings.SplitSeq(overview, "\n") {
		if strings.Contains(line, "running now") {
			return line
		}
	}
	t.Fatalf("overview = %q, want a `running now` fact", overview)
	return ""
}

// The same count reads against a cap the project owns.
func TestProjectSlotColumnCountsAgainstTheProjectCap(t *testing.T) {
	two := 2
	h := newProjectSlotsHarness(t, &two)
	parent := h.seed(t, "parent", store.TaskAwaitingChildren, nil)
	h.seed(t, "lane", store.TaskRunning, &parent.ID)

	pr := h.load(t)
	if got, want := h.view.capCell(pr), "1 / 2"; got != want {
		t.Errorf("cap cell = %q, want %q", got, want)
	}
	if got := h.view.projectRailSummary(pr); !strings.Contains(got, "1 running · cap 2") {
		t.Errorf("rail summary = %q, want the lane counted against the project cap", got)
	}
}
