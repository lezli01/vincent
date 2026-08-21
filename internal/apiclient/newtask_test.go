package apiclient_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/taskrun"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// validWorkflow names claude on one step and codex on the other, so the
// unavailable-agent flag has something real to catch: the harness points
// claude at the fakeagent binary and codex at a path that does not exist.
const validWorkflow = `name: two-step
description: Implement then publish.
fields:
  - name: ticket
    label: Ticket
    type: string
    pattern: '^OPS-[0-9]+$'
defaults:
  agent: claude
steps:
  - id: implement
    type: agent
    prompt: do the thing
  - id: review
    type: agent
    agent: codex
    prompt: review the thing
`

// brokenWorkflow fails strict decode: "steps" is required.
const brokenWorkflow = `name: busted
description: Missing its steps.
`

// createHarness is the real store, registry, catalog and handlers behind a
// real git repository — everything POST /v1/tasks validates against.
type createHarness struct {
	client    *apiclient.Client
	store     *store.Store
	projectID int64
	repo      string
}

func newCreateHarness(t *testing.T) *createHarness {
	t.Helper()
	fake := agenttest.BuildFakeAgent(t)
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	globalDir := filepath.Join(t.TempDir(), "workflows")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	writeWorkflow(t, globalDir, "two-step.yaml", validWorkflow)
	writeWorkflow(t, globalDir, "busted.yaml", brokenWorkflow)

	git := gitx.New()
	dataDir := t.TempDir()
	wt := worktree.NewManager(git, dataDir)
	reg := agent.NewRegistry(
		claude.New(func() string { return fake }),
		codex.New(func() string { return filepath.Join(dataDir, "no-codex-here") }),
	)
	cache := agent.NewCatalogCache(reg)
	workflows := workflow.NewRegistry(globalDir, workflow.Options{
		KnownAgents: reg.Names(),
		Catalogs:    cache.Catalogs,
	}, nil)
	workflows.Reload()

	runner := taskrun.New(taskrun.Deps{
		Store:     st,
		Config:    config.Default,
		Worktrees: wt,
		Agents:    reg,
		DataDir:   dataDir,
		Logger:    slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	s := api.New(api.Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Broker:      broker,
		Git:         git,
		Worktrees:   wt,
		Agents:      reg,
		Catalog:     cache,
		Workflows:   workflows,
		Runner:      runner,
		WakeRunner:  func() {},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	repo := testrepo.Init(t, "main")
	p := &store.Project{Name: "vincent", Path: repo, DefaultBranch: "main"}
	if err := st.CreateProject(context.Background(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return &createHarness{
		client:    apiclient.New(ts.URL, testToken),
		store:     st,
		projectID: p.ID,
		repo:      repo,
	}
}

func writeWorkflow(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func TestListProjectsReturnsCreationDefaults(t *testing.T) {
	h := newCreateHarness(t)
	projects, err := h.client.ListProjects(t.Context())
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(projects) != 1 {
		t.Fatalf("projects = %d, want 1", len(projects))
	}
	p := projects[0]
	if p.ID != h.projectID || p.Path != h.repo {
		t.Errorf("project = %+v, want id %d path %s", p, h.projectID, h.repo)
	}
	// The two values the form prefills from.
	if p.DefaultBranch != "main" {
		t.Errorf("DefaultBranch = %q, want main", p.DefaultBranch)
	}
	if got := p.Workflow(); got != apiclient.AdhocWorkflow {
		t.Errorf("Workflow() = %q, want %q for a project with no default", got, apiclient.AdhocWorkflow)
	}
}

func TestListWorkflowsKeepsBrokenEntriesVisible(t *testing.T) {
	h := newCreateHarness(t)
	entries, err := h.client.ListWorkflows(t.Context(), h.projectID)
	if err != nil {
		t.Fatalf("ListWorkflows: %v", err)
	}
	byName := map[string]apiclient.WorkflowEntry{}
	for _, e := range entries {
		byName[e.Name] = e
	}
	good, ok := byName["two-step"]
	if !ok {
		t.Fatalf("two-step missing from %v", byName)
	}
	if !good.Valid() {
		t.Errorf("two-step invalid: %v", good.Errors)
	}
	if good.Description != "Implement then publish." {
		t.Errorf("Description = %q", good.Description)
	}
	if len(good.Fields) != 1 || good.Fields[0].Name != "ticket" ||
		good.Fields[0].DisplayLabel() != "Ticket" || good.Fields[0].Pattern != `^OPS-[0-9]+$` {
		t.Errorf("Fields = %+v, want the declared ticket contract", good.Fields)
	}
	if len(good.Steps) != 2 {
		t.Fatalf("steps = %d, want 2", len(good.Steps))
	}
	// §8.6 levels 1 and 3: the first step inherits defaults.agent, the
	// second pins its own. This is what the unavailable flag reads.
	if good.Steps[0].Agent != "claude" || good.Steps[1].Agent != "codex" {
		t.Errorf("step agents = %q, %q; want claude, codex", good.Steps[0].Agent, good.Steps[1].Agent)
	}
	bad, ok := byName["busted"]
	if !ok {
		t.Fatalf("busted missing — a broken workflow must stay listed, not vanish")
	}
	if bad.Valid() {
		t.Error("busted reported valid")
	}
	if bad.FirstError() == "" {
		t.Error("busted has no error message to show")
	}
}

func TestListAgentsCarriesAvailabilityAndProvenance(t *testing.T) {
	h := newCreateHarness(t)
	agents, err := h.client.ListAgents(t.Context(), false)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	cl, ok := agents.Find("claude")
	if !ok {
		t.Fatalf("claude missing from %+v", agents)
	}
	if !cl.Available {
		t.Fatalf("claude unavailable: %s", cl.Error)
	}
	if len(cl.Models) == 0 {
		t.Error("claude has no model options for the picker to render")
	}
	for _, m := range cl.Models {
		if m.Source != apiclient.OptionSourceCLI && m.Source != apiclient.OptionSourceCurated {
			t.Errorf("model %q has source %q, want cli or curated", m.Value, m.Source)
		}
	}
	if agents.Unavailable("claude") {
		t.Error("Unavailable(claude) = true for a found adapter")
	}
	if !agents.Unavailable("codex") {
		t.Error("Unavailable(codex) = false; the binary does not exist")
	}
	// An empty agent is §8.6's "adapter default", which the registry cannot
	// resolve — flagging it would accuse a step that may be fine.
	if agents.Unavailable("") {
		t.Error(`Unavailable("") = true; an unset agent must never be flagged`)
	}
	if agents.Unavailable("no-such-adapter") {
		t.Error("Unavailable reported on an adapter the catalog does not know")
	}
}

func TestCreateTaskSendsTheWholeForm(t *testing.T) {
	h := newCreateHarness(t)
	workflowName := "two-step"
	desc := "the description as typed"
	branch := "main"
	priority := 3
	agentName := "claude"
	task, err := h.client.CreateTask(t.Context(), apiclient.CreateTaskRequest{
		ProjectID:   h.projectID,
		Workflow:    &workflowName,
		Title:       "  wire the form  ",
		Description: &desc,
		Fields:      map[string]string{"ticket": "OPS-123"},
		BaseBranch:  &branch,
		Priority:    &priority,
		Agent:       &agentName,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if task.ID == 0 {
		t.Fatal("created task has no id")
	}
	if task.Title != "wire the form" {
		t.Errorf("Title = %q, want the server-trimmed form", task.Title)
	}
	if task.Workflow != workflowName || task.Priority != 3 {
		t.Errorf("workflow/priority = %q/%d, want %q/3", task.Workflow, task.Priority, workflowName)
	}
	if task.Description != desc {
		t.Errorf("Description = %q, want %q", task.Description, desc)
	}
	stored, err := h.store.GetTask(t.Context(), task.ID)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if stored.Fields["ticket"] != "OPS-123" {
		t.Errorf("stored fields = %v, want ticket OPS-123", stored.Fields)
	}
	if stored.AgentOverride != "claude" {
		t.Errorf("AgentOverride = %q, want claude", stored.AgentOverride)
	}
}

func TestCreateTaskRejectsUnknownBaseBranch(t *testing.T) {
	h := newCreateHarness(t)
	branch := "no-such-branch"
	_, err := h.client.CreateTask(t.Context(), apiclient.CreateTaskRequest{
		ProjectID:  h.projectID,
		Title:      "t",
		BaseBranch: &branch,
	})
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *apiclient.Error", err)
	}
	if apiErr.Status != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", apiErr.Status)
	}
	// The form routes the message back onto the row it names, so the name
	// has to be in it.
	if !strings.Contains(apiErr.Message, "base_branch") {
		t.Errorf("message = %q, want it to name base_branch", apiErr.Message)
	}
}

func TestCreateTaskWarnsOnCatalogUnknownModel(t *testing.T) {
	h := newCreateHarness(t)
	agentName, model := "claude", "definitely-not-a-real-model"
	task, err := h.client.CreateTask(t.Context(), apiclient.CreateTaskRequest{
		ProjectID: h.projectID,
		Title:     "unknown model",
		Agent:     &agentName,
		Model:     &model,
	})
	if err != nil {
		t.Fatalf("CreateTask: %v — an unknown model is advisory, not fatal", err)
	}
	if len(task.Warnings) == 0 {
		t.Fatal("no warnings on the 201; the form would report a clean create")
	}
}
