package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/cursor"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
	"github.com/lezli01/vincent/internal/worktree"
)

// newRestrictedHarness registers claude and cursor, which is the pair the
// §9.4 gate discriminates between: cursor cannot restrict where its CLI
// sandbox is unavailable, claude always can.
//
// Neither binary needs to exist for the gate to answer — the verdict is a fact
// about the adapter and the OS — which is exactly what this harness proves by
// pointing cursor at nothing.
func newRestrictedHarness(t *testing.T, claudePath string) *workflowHarness {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	git := gitx.New()
	wt := worktree.NewManager(git, t.TempDir())
	globalDir := filepath.Join(t.TempDir(), "workflows")
	reg := agent.NewRegistry(
		claude.New(func() string { return claudePath }),
		cursor.New(func() string { return "/nonexistent/cursor-agent-not-here" }),
	)
	cache := agent.NewCatalogCache(reg)
	wfreg := workflow.NewRegistry(globalDir, workflow.Options{
		KnownAgents: reg.Names(),
		Catalogs:    cache.Catalogs,
	}, nil)
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
		Workflows:   wfreg,
		Agents:      reg,
		Catalog:     cache,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &workflowHarness{
		projectHarness: &projectHarness{ts: ts, store: st, wt: wt},
		reg:            wfreg,
		globalDir:      globalDir,
	}
}

// restrictedWorkflow leaves its restricted step's agent to the task, which is
// what makes the task-level choice the thing being gated.
const restrictedWorkflow = "name: careful\n" +
	"steps:\n" +
	"  - {id: locked, type: agent, permission_mode: restricted, prompt: read only}\n"

// TestTaskCreateRefusesAgentThatCannotRestrict is the §9.4 gate moving from
// the engine to task creation (task 040): the same run used to reach an
// adapter, start nothing, and fail the step `restricted_unsupported`.
func TestTaskCreateRefusesAgentThatCannotRestrict(t *testing.T) {
	t.Cleanup(cursor.SetSandboxAvailable(false))
	h := newRestrictedHarness(t, agenttest.BuildFakeAgent(t))
	writeWorkflowFile(t, h.globalDir, "careful", restrictedWorkflow)
	h.reg.ReloadGlobal()
	p := h.mustCreate(t, map[string]any{"path": testrepo.Init(t, "main")})
	id := int64(p["id"].(float64))

	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": id, "title": "no", "workflow": "careful", "agent": "cursor",
	})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	for _, want := range []string{"locked", "cursor", "restrict"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("message %s missing %q", body, want)
		}
	}

	// The same workflow on the capable agent creates, so the gate refuses one
	// selection rather than the workflow.
	resp, body = h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": id, "title": "yes", "workflow": "careful", "agent": "claude",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("claude task: %d %s", resp.StatusCode, body)
	}
}

// TestTaskCreateAllowsRestrictionWhereTheSandboxRuns is the other platform
// leg, forced on every OS CI runs: where cursor can restrict, nothing is
// refused.
func TestTaskCreateAllowsRestrictionWhereTheSandboxRuns(t *testing.T) {
	t.Cleanup(cursor.SetSandboxAvailable(true))
	h := newRestrictedHarness(t, agenttest.BuildFakeAgent(t))
	writeWorkflowFile(t, h.globalDir, "careful", restrictedWorkflow)
	h.reg.ReloadGlobal()
	p := h.mustCreate(t, map[string]any{"path": testrepo.Init(t, "main")})
	id := int64(p["id"].(float64))

	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": id, "title": "yes", "workflow": "careful", "agent": "cursor",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("restricted task refused where the sandbox runs: %d %s", resp.StatusCode, body)
	}
}

// TestRestrictedGateOrdering pins the order the three creation-time checks
// report in: the catalog first, because a model is the field the human just
// typed, and the platform fact last, because it is the least surprising thing
// to be told about.
func TestRestrictedGateOrdering(t *testing.T) {
	t.Cleanup(cursor.SetSandboxAvailable(false))
	h := newRestrictedHarness(t, agenttest.BuildFakeAgent(t))
	writeWorkflowFile(t, h.globalDir, "careful", restrictedWorkflow)
	h.reg.ReloadGlobal()
	p := h.mustCreate(t, map[string]any{"path": testrepo.Init(t, "main")})
	id := int64(p["id"].(float64))

	// A claude-only effort on a cursor step is a §8.2 catalog error; the task
	// also cannot restrict on cursor. The catalog message is the one returned.
	resp, body := h.doJSON(t, http.MethodPost, "/v1/tasks", map[string]any{
		"project_id": id, "title": "both", "workflow": "careful",
		"agent": "cursor", "effort": "xhigh",
	})
	wantError(t, resp, body, http.StatusBadRequest, CodeValidationFailed)
	if !strings.Contains(string(body), "effort") {
		t.Errorf("message %s does not lead with the catalog problem", body)
	}
}
