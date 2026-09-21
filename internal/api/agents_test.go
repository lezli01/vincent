package api

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
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
// distinction is the point of adding it: null means the probe could not tell,
// while false means installed-and-doomed — a state that was previously
// indistinguishable from healthy. claude answers through `auth status` since
// task 107, and both endpoints serve it from the same cache, so both are read.
func TestAgentsReportLoggedIn(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	probe := func(t *testing.T) (agents []agentResponse, info []AgentStatus) {
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
		resp, body = doRequest(t, ts, http.MethodGet, "/v1/info", testToken)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("info: %d %s", resp.StatusCode, body)
		}
		var inf infoResponse
		if err := json.Unmarshal(body, &inf); err != nil {
			t.Fatalf("info body: %v", err)
		}
		if len(inf.Agents) != 2 {
			t.Fatalf("info agents = %d, want claude and cursor", len(inf.Agents))
		}
		return out.Agents, inf.Agents
	}
	definite := func(t *testing.T, where string, got *bool, want bool) {
		t.Helper()
		if got == nil || *got != want {
			t.Errorf("%s logged_in = %v, want a definite %v", where, got, want)
		}
	}

	t.Run("logged in", func(t *testing.T) {
		agents, info := probe(t)
		definite(t, "claude /v1/agents", agents[0].LoggedIn, true)
		definite(t, "claude /v1/info", info[0].LoggedIn, true)
		definite(t, "cursor /v1/agents", agents[1].LoggedIn, true)
	})

	t.Run("logged out", func(t *testing.T) {
		t.Setenv("FAKEAGENT_CURSOR_LOGGED_OUT", "1")
		t.Setenv("FAKEAGENT_CLAUDE_LOGGED_OUT", "1")
		agents, info := probe(t)
		for i, name := range []string{"claude", "cursor"} {
			if !agents[i].Available {
				t.Errorf("%s available = false; a logged-out CLI is still installed", name)
			}
			definite(t, name+" /v1/agents", agents[i].LoggedIn, false)
			definite(t, name+" /v1/info", info[i].LoggedIn, false)
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
// refusal cannot disagree (decision row 29). All three shipped adapters
// answer yes since task 070, and agenttest.StubNonResuming carries the no —
// the answer comes from the adapter, whether or not a binary is installed.
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
			agenttest.StubNonResuming{},
		)
	}
	probe := func(t *testing.T, withRegistry bool) map[string]*bool {
		t.Helper()
		got := map[string]*bool{}
		for _, a := range servedAgents(t, newReg(), withRegistry) {
			got[a.Name] = a.SupportsResume
		}
		return got
	}

	got := probe(t, true)
	// Every shipped adapter resumes since task 070: claude and codex reload a
	// session, cursor a chat, each pinned by a capture against a named build.
	// The stub is what states the false case now that no shipped adapter does.
	for name, want := range map[string]bool{
		"claude": true, "codex": true, "cursor": true,
		agenttest.NonResumingName: false,
	} {
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

// servedAgents is GET /v1/agents' body from a server whose catalog probes reg,
// and whose adapter registry is reg too when withRegistry is set — the
// capability fields' only source.
func servedAgents(t *testing.T, reg *agent.Registry, withRegistry bool) []agentResponse {
	t.Helper()
	agents := reg
	if !withRegistry {
		agents = nil
	}
	return servedAgentsFrom(t, reg, agents)
}

// servedAgentsFrom is servedAgents with the two registries split: catalog
// probes the adapters that become rows, agents is the one the capability
// fields are looked up in — nil for a daemon with none. An adapter in the
// first and not the second is the "the registry does not know the name"
// case, which must answer null rather than a false.
func servedAgentsFrom(t *testing.T, catalog, agents *agent.Registry) []agentResponse {
	t.Helper()
	deps := Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Catalog:     agent.NewCatalogCache(catalog),
		Agents:      agents,
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
	return out.Agents
}

// TestAgentsReportSkillCapabilities pins §9.6's three skill fields (task
// 124): `supports_skill_listing` from `agent.CanListSkills`, and
// `skill_sigil` and `skill_position` from the adapter's `SkillSyntax`.
//
// The sigils and positions are pinned literally, since a client inserts
// them verbatim. The listing bit is proven both ways against the agenttest
// stubs; of the shipped adapters only positive answers are pinned — codex's
// (task 124.8) and claude's (task 124.7). A refusal is never pinned to
// cursor: a test asserting that of it would invert itself the day #514
// lands. A registered adapter that cannot invoke answers "" for both syntax
// fields; no registry at all answers null for all three.
func TestAgentsReportSkillCapabilities(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	newReg := func() *agent.Registry {
		return agent.NewRegistry(
			claude.New(func() string { return fake }),
			codex.New(func() string { return "/nonexistent/codex-not-here" }),
			cursor.New(func() string { return "/nonexistent/cursor-agent-not-here" }),
			agenttest.StubNoSkills{},
			&agenttest.StubSkills{Syntax: agent.SkillSyntax{Sigil: "$", Position: agent.SkillLeading}},
		)
	}
	str := func(p *string) string {
		if p == nil {
			return "<null>"
		}
		return *p
	}

	served := servedAgents(t, newReg(), true)
	got := map[string]agentResponse{}
	for _, a := range served {
		got[a.Name] = a
	}
	for name, want := range map[string][2]string{
		"claude":               {"/", "leading"},
		"codex":                {"$", "anywhere"},
		"cursor":               {"/", "anywhere"},
		agenttest.NoSkillsName: {"", ""},
		agenttest.SkillsName:   {"$", "leading"},
	} {
		a, ok := got[name]
		if !ok {
			t.Errorf("%s is not served", name)
			continue
		}
		if s, p := str(a.SkillSigil), str(a.SkillPosition); s != want[0] || p != want[1] {
			t.Errorf("%s skill_sigil, skill_position = %q, %q, want %q, %q (task 124 decision 18)",
				name, s, p, want[0], want[1])
		}
	}
	for name, want := range map[string]bool{
		"claude":               true,
		"codex":                true,
		agenttest.NoSkillsName: false,
		agenttest.SkillsName:   true,
	} {
		switch v := got[name].SupportsSkillListing; {
		case v == nil:
			t.Errorf("%s supports_skill_listing = null, want %v — the registry was asked", name, want)
		case *v != want:
			t.Errorf("%s supports_skill_listing = %v, want %v", name, *v, want)
		}
	}
	for _, name := range []string{"claude", "codex", "cursor"} {
		if got[name].SupportsSkillListing == nil {
			t.Errorf("%s supports_skill_listing = null — the registry was asked", name)
		}
	}

	for _, a := range servedAgents(t, newReg(), false) {
		if a.SupportsSkillListing != nil || a.SkillSigil != nil || a.SkillPosition != nil {
			t.Errorf("%s answered listing set=%v, sigil %s, position %s with no registry to ask, want null for all three",
				a.Name, a.SupportsSkillListing != nil, str(a.SkillSigil), str(a.SkillPosition))
		}
	}
}

// TestAgentsReportFileMentionCapabilities pins §9.6's four file-mention
// fields (task 126.4): `supports_file_mentions` from
// `agent.CanMentionFiles`, and `file_mention_sigil`,
// `file_mention_position` and `file_mention_expands` from the adapter's
// `FileMentionSyntax`.
//
// The point of the table is that the boolean does **not** separate the
// shipped adapters and `expands` does: all three implement `FileMentioner`
// since task 126.3, and §9.1 states that expansion, not the interface, is
// the honest capability statement. The sigil and position are pinned
// literally, since a client inserts the sigil verbatim. The false legs are
// `agenttest.StubNoMentions`, never a shipped adapter — a refusal pinned to
// a real CLI inverts itself the day that CLI changes.
//
// An adapter the registry does not know answers null for all four, as does a
// server with no registry at all: "nobody can say" is not "no".
func TestAgentsReportFileMentionCapabilities(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	newReg := func() *agent.Registry {
		return agent.NewRegistry(
			claude.New(func() string { return fake }),
			codex.New(func() string { return "/nonexistent/codex-not-here" }),
			cursor.New(func() string { return "/nonexistent/cursor-agent-not-here" }),
			agenttest.StubNoMentions{},
		)
	}
	str := func(p *string) string {
		if p == nil {
			return "<null>"
		}
		return *p
	}
	boolean := func(p *bool) string {
		if p == nil {
			return "<null>"
		}
		return strconv.FormatBool(*p)
	}

	got := map[string]agentResponse{}
	for _, a := range servedAgents(t, newReg(), true) {
		got[a.Name] = a
	}
	type mention struct {
		mentions bool
		sigil    string
		position string
		expands  bool
	}
	// claude expands an `@path` in the argv vincent builds; codex and cursor
	// leave the model to read the path with a tool (§5.5, §9.2, §9.3, §9.7).
	for name, want := range map[string]mention{
		"claude":                 {true, "@", "anywhere", true},
		"codex":                  {true, "@", "anywhere", false},
		"cursor":                 {true, "@", "anywhere", false},
		agenttest.NoMentionsName: {false, "", "", false},
	} {
		a, ok := got[name]
		if !ok {
			t.Errorf("%s is not served", name)
			continue
		}
		if a.SupportsFileMentions == nil || a.FileMentionSigil == nil ||
			a.FileMentionPosition == nil || a.FileMentionExpands == nil {
			t.Errorf("%s: a mention field arrived null with a registry to ask: mentions %s, sigil %s, position %s, expands %s",
				name, boolean(a.SupportsFileMentions), str(a.FileMentionSigil),
				str(a.FileMentionPosition), boolean(a.FileMentionExpands))
			continue
		}
		have := mention{
			*a.SupportsFileMentions, *a.FileMentionSigil,
			*a.FileMentionPosition, *a.FileMentionExpands,
		}
		if have != want {
			t.Errorf("%s supports_file_mentions, file_mention_sigil, file_mention_position, file_mention_expands = %+v, want %+v (§9.1, task 126 decision 22)",
				name, have, want)
		}
	}

	// A row the registry cannot answer for: the catalog probes all four
	// adapters, the registry knows only claude.
	known := agent.NewRegistry(claude.New(func() string { return fake }))
	for _, a := range servedAgentsFrom(t, newReg(), known) {
		if a.Name == "claude" {
			if a.SupportsFileMentions == nil || a.FileMentionExpands == nil {
				t.Errorf("claude answered null though the registry knows it: %+v", a)
			}
			continue
		}
		assertNoMentionJudgement(t, a, "the registry does not know the name")
	}

	for _, a := range servedAgentsFrom(t, newReg(), nil) {
		assertNoMentionJudgement(t, a, "no registry to ask")
	}
}

// assertNoMentionJudgement fails unless every mention field is null, which is
// the one answer a client may not filter on.
func assertNoMentionJudgement(t *testing.T, a agentResponse, why string) {
	t.Helper()
	if a.SupportsFileMentions != nil || a.FileMentionSigil != nil ||
		a.FileMentionPosition != nil || a.FileMentionExpands != nil {
		t.Errorf("%s answered mentions set=%v, sigil set=%v, position set=%v, expands set=%v with %s, want null for all four",
			a.Name, a.SupportsFileMentions != nil, a.FileMentionSigil != nil,
			a.FileMentionPosition != nil, a.FileMentionExpands != nil, why)
	}
}
