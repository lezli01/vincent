package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// newResolveHarness serves a registry that knows both adapters, so a workflow
// may legally switch agents mid-list — the §8.6 agent-scoped inheritance case
// the endpoint exists to get right.
func newResolveHarness(t *testing.T) *workflowHarness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	git := gitx.New()
	wt := worktree.NewManager(git, t.TempDir())
	globalDir := filepath.Join(t.TempDir(), "workflows")
	reg := workflow.NewRegistry(globalDir,
		workflow.Options{KnownAgents: []string{"claude", "codex"}}, nil)
	s := New(Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Git:         git,
		Worktrees:   wt,
		Workflows:   reg,
		OnProjectsChanged: func() {
			projects, err := st.ListProjects(t.Context())
			if err != nil {
				return
			}
			roots := make(map[int64]string, len(projects))
			for i := range projects {
				roots[projects[i].ID] = projects[i].Path
			}
			reg.SetProjects(roots)
		},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &workflowHarness{
		projectHarness: &projectHarness{ts: ts, store: st, wt: wt},
		reg:            reg,
		globalDir:      globalDir,
	}
}

func postResolve(t *testing.T, h *workflowHarness, body map[string]any) (*http.Response, resolveResponse) {
	t.Helper()
	resp, raw := h.doJSON(t, http.MethodPost, "/v1/resolve", body)
	var out resolveResponse
	if resp.StatusCode == http.StatusOK {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("resolve body not JSON: %v (%s)", err, raw)
		}
	}
	return resp, out
}

// stepByID finds a resolved step, failing the test when the endpoint dropped
// it — indexes lining up with the registry listing is part of the contract.
func stepByID(t *testing.T, res resolveResponse, id string) resolvedStepResponse {
	t.Helper()
	for _, s := range res.Steps {
		if s.ID == id {
			return s
		}
	}
	t.Fatalf("step %q missing from resolution: %+v", id, res.Steps)
	return resolvedStepResponse{}
}

func assertField(t *testing.T, got *resolvedField, wantValue, wantSource, what string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s: field is null, want %q from %q", what, wantValue, wantSource)
	}
	if got.Value != wantValue || got.Source != wantSource {
		t.Errorf("%s = %q (from %q), want %q (from %q)",
			what, got.Value, got.Source, wantValue, wantSource)
	}
}

// resolveYAML has one step riding the defaults, one command step, and one
// step pinning its own agent and model — the three shapes a form must render
// differently.
const resolveYAML = `name: mixed
defaults:
  agent: claude
  model: sonnet
  effort: high
steps:
  - {id: plan, type: agent, prompt: hi}
  - {id: build, type: command, run: "echo hi"}
  - {id: ship, type: agent, prompt: hi, agent: codex, model: gpt-5-codex}
`

func TestResolveWorkflowDefaultsAndStepPins(t *testing.T) {
	h := newResolveHarness(t)
	writeWorkflowFile(t, h.globalDir, "mixed", resolveYAML)
	h.reg.ReloadGlobal()

	resp, res := postResolve(t, h, map[string]any{"workflow": "mixed"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if res.Workflow != "mixed" || len(res.Steps) != 3 {
		t.Fatalf("resolution = %+v, want three steps of mixed", res)
	}

	plan := stepByID(t, res, "plan")
	assertField(t, plan.Agent, "claude", "workflow", "plan agent")
	assertField(t, plan.Model, "sonnet", "workflow", "plan model")
	assertField(t, plan.Effort, "high", "workflow", "plan effort")

	// A step pinning its own agent takes level 1 for the fields it names and
	// resets the rest: the workflow's claude-scoped effort must not reach codex.
	ship := stepByID(t, res, "ship")
	assertField(t, ship.Agent, "codex", "step", "ship agent")
	assertField(t, ship.Model, "gpt-5-codex", "step", "ship model")
	assertField(t, ship.Effort, "", "adapter", "ship effort")

	// A command step keeps its place with nothing resolved.
	build := stepByID(t, res, "build")
	if build.Agent != nil || build.Model != nil || build.Effort != nil {
		t.Errorf("command step carries a resolution: %+v", build)
	}
	if res.Steps[1].ID != "build" {
		t.Errorf("step order = %v, want the registry's own order", res.Steps)
	}
}

func TestResolveTaskOverrideReplacesWorkflowDefaults(t *testing.T) {
	h := newResolveHarness(t)
	writeWorkflowFile(t, h.globalDir, "mixed", resolveYAML)
	h.reg.ReloadGlobal()

	_, res := postResolve(t, h, map[string]any{"workflow": "mixed", "model": "opus"})
	plan := stepByID(t, res, "plan")
	assertField(t, plan.Agent, "claude", "workflow", "plan agent")
	assertField(t, plan.Model, "opus", "task", "plan model")
	// The step's own pin still wins over the task override (§8.6 level 1).
	assertField(t, stepByID(t, res, "ship").Model, "gpt-5-codex", "step", "ship model")
}

// TestResolveOverrideSwitchingAgentResets is §8.6's agent-scoped inheritance:
// an override that moves a step to codex must not carry claude's model along.
func TestResolveOverrideSwitchingAgentResets(t *testing.T) {
	h := newResolveHarness(t)
	writeWorkflowFile(t, h.globalDir, "mixed", resolveYAML)
	h.reg.ReloadGlobal()

	_, res := postResolve(t, h, map[string]any{"workflow": "mixed", "agent": "codex"})
	plan := stepByID(t, res, "plan")
	assertField(t, plan.Agent, "codex", "task", "plan agent")
	assertField(t, plan.Model, "", "adapter", "plan model")
	assertField(t, plan.Effort, "", "adapter", "plan effort")
}

// TestResolveNamesTheAdapterDefault is the T4.7 finding itself: a workflow
// naming no agent anywhere still runs *something*, and the registry listing
// cannot say what. Resolution names it.
func TestResolveNamesTheAdapterDefault(t *testing.T) {
	h := newResolveHarness(t)
	writeWorkflowFile(t, h.globalDir, "bare",
		"name: bare\nsteps:\n  - {id: work, type: agent, prompt: hi}\n")
	h.reg.ReloadGlobal()

	_, res := postResolve(t, h, map[string]any{"workflow": "bare"})
	work := stepByID(t, res, "work")
	assertField(t, work.Agent, "claude", "adapter", "bare agent")
	// No adapter reports a default model today, so level 4 is honestly empty:
	// the CLI decides at run time, and the form says exactly that.
	assertField(t, work.Model, "", "adapter", "bare model")
}

func TestResolveProjectScopeShadowing(t *testing.T) {
	h := newResolveHarness(t)
	repo := testrepo.Init(t, "main")
	writeWorkflowFile(t, h.globalDir, "feature",
		"name: feature\ndefaults:\n  agent: claude\nsteps:\n  - {id: a, type: agent, prompt: hi}\n")
	writeWorkflowFile(t, filepath.Join(repo, workflow.ProjectDirName), "feature",
		"name: feature\ndefaults:\n  agent: codex\nsteps:\n  - {id: a, type: agent, prompt: hi}\n")
	h.reg.ReloadGlobal()
	p := h.mustCreate(t, map[string]any{"path": repo})
	id := int64(p["id"].(float64))

	_, global := postResolve(t, h, map[string]any{"workflow": "feature"})
	assertField(t, stepByID(t, global, "a").Agent, "claude", "workflow", "global scope agent")

	_, scoped := postResolve(t, h, map[string]any{"workflow": "feature", "project_id": id})
	assertField(t, stepByID(t, scoped, "a").Agent, "codex", "workflow", "project scope agent")
}

func TestResolveErrors(t *testing.T) {
	h := newResolveHarness(t)
	writeWorkflowFile(t, h.globalDir, "mixed", resolveYAML)
	h.reg.ReloadGlobal()

	for _, tc := range []struct {
		name string
		body map[string]any
		want int
	}{
		{"no workflow", map[string]any{}, http.StatusBadRequest},
		{"blank workflow", map[string]any{"workflow": "   "}, http.StatusBadRequest},
		{"unknown workflow", map[string]any{"workflow": "nope"}, http.StatusNotFound},
		{"bad project id", map[string]any{"workflow": "mixed", "project_id": 0}, http.StatusBadRequest},
		{"missing project", map[string]any{"workflow": "mixed", "project_id": 4242}, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp, _ := postResolve(t, h, tc.body)
			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d", resp.StatusCode, tc.want)
			}
		})
	}
}

// TestResolveRejectsGet keeps the §13.1 envelope on the wrong method: the
// endpoint takes a body, so GET has to fail like every other POST route.
func TestResolveRejectsGet(t *testing.T) {
	h := newResolveHarness(t)
	resp, _ := h.doJSON(t, http.MethodGet, "/v1/resolve", nil)
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
}
