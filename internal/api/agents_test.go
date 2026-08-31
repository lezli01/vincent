package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/agent/cursor"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/workflow"
)

// newAgentsServer serves /v1/agents and /v1/info over a registry where
// claude resolves to the fake binary and codex is deliberately missing. The
// workflow registry rides the same catalog cache, exactly as the daemon
// wires it, so validate-endpoint tests see the curated catalogs.
func newAgentsServer(t *testing.T) *httptest.Server {
	t.Helper()
	fake := agenttest.BuildFakeAgent(t)
	reg := agent.NewRegistry(
		claude.New(func() string { return fake }),
		codex.New(func() string { return "/nonexistent/codex-not-here" }),
		cursor.New(func() string { return "/nonexistent/cursor-agent-not-here" }),
	)
	cache := agent.NewCatalogCache(reg)
	s := New(Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Catalog:     cache,
		Workflows: workflow.NewRegistry("", workflow.Options{
			KnownAgents: reg.Names(),
			Catalogs:    cache.Catalogs,
		}, nil),
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// TestAgentsReportLoggedIn pins the three states of the §9.5 field. The
// distinction is the point of adding it: null means the adapter cannot tell,
// which is normal, while false means installed-and-doomed — a state that was
// previously indistinguishable from healthy.
func TestAgentsReportLoggedIn(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	probe := func(t *testing.T) []agentResponse {
		t.Helper()
		reg := agent.NewRegistry(
			claude.New(func() string { return fake }),
			cursor.New(func() string { return fake }),
		)
		s := New(Deps{
			Token: testToken, Config: config.Default, StartedAt: time.Now(),
			ListenAddr: "127.0.0.1:0", RequestStop: func() {},
			Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
			Catalog: agent.NewCatalogCache(reg),
		})
		ts := httptest.NewServer(s.Handler())
		t.Cleanup(ts.Close)
		resp, body := doRequest(t, ts, http.MethodGet, "/v1/agents", testToken)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("agents: %d %s", resp.StatusCode, body)
		}
		var out struct {
			Agents []agentResponse `json:"agents"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("agents body: %v", err)
		}
		if len(out.Agents) != 2 {
			t.Fatalf("agents = %d, want claude and cursor", len(out.Agents))
		}
		return out.Agents
	}

	t.Run("logged in", func(t *testing.T) {
		agents := probe(t)
		if agents[0].LoggedIn != nil {
			t.Errorf("claude logged_in = %v, want null — it has no cheap probe (§9.5)", *agents[0].LoggedIn)
		}
		if agents[1].LoggedIn == nil || !*agents[1].LoggedIn {
			t.Errorf("cursor logged_in = %v, want a definite true", agents[1].LoggedIn)
		}
	})

	t.Run("logged out", func(t *testing.T) {
		t.Setenv("FAKEAGENT_CURSOR_LOGGED_OUT", "1")
		agents := probe(t)
		cu := agents[1]
		if !cu.Available {
			t.Error("cursor available = false; a logged-out CLI is still installed")
		}
		if cu.LoggedIn == nil || *cu.LoggedIn {
			t.Errorf("cursor logged_in = %v, want a definite false", cu.LoggedIn)
		}
	})
}

// postValidate POSTs a workflow to /v1/workflows/validate.
func postValidate(t *testing.T, ts *httptest.Server, yaml string) validateResponse {
	t.Helper()
	payload, err := json.Marshal(map[string]string{"yaml": yaml})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, ts.URL+"/v1/workflows/validate", bytes.NewReader(payload))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("validate: %d %s (%v)", resp.StatusCode, body, err)
	}
	var out validateResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("validate body: %v", err)
	}
	return out
}

// TestWorkflowValidateCatalog proves the §8.2 catalog findings reach the
// validate endpoint: cross-catalog values invalidate, unknown values warn.
func TestWorkflowValidateCatalog(t *testing.T) {
	ts := newAgentsServer(t)

	out := postValidate(t, ts, `
name: bad
steps:
  - id: one
    type: agent
    agent: codex
    model: sonnet
    prompt: p
`)
	if out.Valid || len(out.Errors) != 1 {
		t.Errorf("got valid=%v errors=%v, want the claude-model-on-codex error", out.Valid, out.Errors)
	}

	out = postValidate(t, ts, `
name: warned
steps:
  - id: one
    type: agent
    model: made-up-model-x
    prompt: p
`)
	if !out.Valid || len(out.Warnings) != 1 {
		t.Errorf("got valid=%v warnings=%v, want valid with one catalog warning", out.Valid, out.Warnings)
	}
	if len(out.Warnings) == 1 && out.Warnings[0].Path != "steps[0].model" {
		t.Errorf("warning path = %q, want steps[0].model", out.Warnings[0].Path)
	}
}

func TestAgentsEndpoint(t *testing.T) {
	ts := newAgentsServer(t)
	resp, body := doRequest(t, ts, http.MethodGet, "/v1/agents", testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agents: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Agents []agentResponse `json:"agents"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("agents body: %v", err)
	}
	// Three adapters, in registration order — the daemon's own order (T5.3).
	if len(out.Agents) != 3 {
		t.Fatalf("agents = %d entries, want claude, codex and cursor", len(out.Agents))
	}
	cl, cx, cu := out.Agents[0], out.Agents[1], out.Agents[2]
	if cl.Name != "claude" || !cl.Available || cl.Version != "2.1.224" {
		t.Errorf("claude = %+v, want available 2.1.224", cl)
	}
	if len(cl.Models) == 0 || len(cl.Efforts) == 0 {
		t.Error("claude catalog empty; want probed+curated options")
	}
	if cl.ProbeError != nil {
		t.Errorf("claude probe_error = %q, want null", *cl.ProbeError)
	}
	if cx.Name != "codex" || cx.Available {
		t.Errorf("codex = %+v, want unavailable", cx)
	}
	if len(cx.Efforts) != 5 || len(cx.Models) != 0 {
		t.Errorf("codex catalog = %d efforts %d models, want curated 5/0 despite the missing binary (§9.6)",
			len(cx.Efforts), len(cx.Models))
	}
	if cx.ProbedAt == "" {
		t.Error("codex probed_at empty")
	}
	// Cursor's catalog is the mirror image of codex's: models but no efforts,
	// because effort is encoded in the model id (§9.7). Its curated floor
	// survives the missing binary, and it is the one adapter that reports a
	// non-empty default model.
	if cu.Name != "cursor" || cu.Available {
		t.Errorf("cursor = %+v, want unavailable", cu)
	}
	if len(cu.Efforts) != 0 || len(cu.Models) != 1 {
		t.Errorf("cursor catalog = %d efforts %d models, want curated 0/1 (auto) despite the missing binary (§9.6, §9.7)",
			len(cu.Efforts), len(cu.Models))
	}
	if cu.DefaultModel != "auto" {
		t.Errorf("cursor default_model = %q, want auto — the only non-empty adapter default (§9.7)", cu.DefaultModel)
	}

	// /v1/info serves availability from the same cache (T2.11): same truth.
	resp, body = doRequest(t, ts, http.MethodGet, "/v1/info", testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("info: %d %s", resp.StatusCode, body)
	}
	var info struct {
		Agents []AgentStatus `json:"agents"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatalf("info body: %v", err)
	}
	if len(info.Agents) != 3 || !info.Agents[0].Available ||
		info.Agents[1].Available || info.Agents[2].Available {
		t.Errorf("info agents = %+v, want claude available + codex and cursor not", info.Agents)
	}

	// ?refresh=true still answers 200 with the same shape.
	resp, body = doRequest(t, ts, http.MethodGet, "/v1/agents?refresh=true", testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agents refresh: %d %s", resp.StatusCode, body)
	}
}

func TestAgentsRequiresAuth(t *testing.T) {
	ts := newAgentsServer(t)
	resp, body := doRequest(t, ts, http.MethodGet, "/v1/agents", "")
	wantError(t, resp, body, http.StatusUnauthorized, CodeUnauthorized)
}

func TestAgentsWithoutCatalog(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	resp, body := doRequest(t, ts, http.MethodGet, "/v1/agents", testToken)
	wantError(t, resp, body, http.StatusInternalServerError, CodeInternal)
}

// TestAgentsReportSupportsResume pins §9.6's `supports_resume` (added
// 2026-08-31, issue #279): the field the TUI's chat picker filters on. It
// comes from the same `agent.CanResume` the creation gate in
// POST /v1/chats consults, so the picker and the `agent_cannot_resume`
// refusal cannot disagree (decision row 29) — claude yes, codex and cursor
// no, whether or not a binary is installed.
//
// Null is a fourth answer rather than a false: a daemon with no registry to
// ask says nothing, and no client may filter an adapter out on that.
func TestAgentsReportSupportsResume(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	newReg := func() *agent.Registry {
		return agent.NewRegistry(
			claude.New(func() string { return fake }),
			codex.New(func() string { return "/nonexistent/codex-not-here" }),
			cursor.New(func() string { return "/nonexistent/cursor-agent-not-here" }),
		)
	}
	probe := func(t *testing.T, withRegistry bool) map[string]*bool {
		t.Helper()
		reg := newReg()
		deps := Deps{
			Token:       testToken,
			Config:      config.Default,
			StartedAt:   time.Now(),
			ListenAddr:  "127.0.0.1:0",
			RequestStop: func() {},
			Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
			Catalog:     agent.NewCatalogCache(reg),
		}
		if withRegistry {
			deps.Agents = reg
		}
		ts := httptest.NewServer(New(deps).Handler())
		t.Cleanup(ts.Close)
		resp, body := doRequest(t, ts, http.MethodGet, "/v1/agents", testToken)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("agents: %d %s", resp.StatusCode, body)
		}
		var out struct {
			Agents []agentResponse `json:"agents"`
		}
		if err := json.Unmarshal(body, &out); err != nil {
			t.Fatalf("agents body: %v", err)
		}
		got := map[string]*bool{}
		for _, a := range out.Agents {
			got[a.Name] = a.SupportsResume
		}
		return got
	}

	got := probe(t, true)
	// codex joined claude with task 070: `exec resume <thread_id>`, pinned by
	// a capture. cursor is the one that still cannot (§9.7).
	for name, want := range map[string]bool{"claude": true, "codex": true, "cursor": false} {
		switch v := got[name]; {
		case v == nil:
			t.Errorf("%s supports_resume = null, want %v — the registry was asked", name, want)
		case *v != want:
			t.Errorf("%s supports_resume = %v, want %v (§9.2, §9.3, §9.7)", name, *v, want)
		}
	}

	for name, v := range probe(t, false) {
		if v != nil {
			t.Errorf("%s supports_resume = %v with no registry to ask, want null", name, *v)
		}
	}
}
