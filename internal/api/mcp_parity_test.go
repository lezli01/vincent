package api

import (
	"log/slog"
	"net/http"
	"slices"
	"testing"

	"github.com/lezli01/vincent/internal/mcp"
)

// TestMCPToolSurfaceMatchesRouteTable is the guard task 057 decision 4 is
// worth having: the tool surface is the route table minus five destructive
// admin routes and the two SSE routes, so a route added later cannot be
// silently unexposed *or* silently exposed. Both directions fail here.
func TestMCPToolSurfaceMatchesRouteTable(t *testing.T) {
	t.Parallel()
	srv := New(Deps{Logger: slog.New(slog.DiscardHandler)})

	type key struct{ method, path string }
	registered := map[key]bool{}
	for _, r := range srv.Routes() {
		registered[key{r.Method, r.Path}] = true
	}

	exempt := map[key]bool{}
	for _, r := range append(slices.Clone(mcp.Excluded), mcp.Streaming...) {
		k := key{r.Method, r.Path}
		if !registered[k] {
			t.Errorf("%s %s is listed as not-a-tool but is not a route", r.Method, r.Path)
		}
		exempt[k] = true
	}

	tools := map[key]string{}
	for _, r := range mcp.Routes() {
		k := key{r.Method, r.Path}
		if !registered[k] {
			t.Errorf("tool %q claims route %s %s, which the server does not register",
				r.Tool, r.Method, r.Path)
		}
		if exempt[k] {
			t.Errorf("tool %q exposes %s %s, which is deliberately not a tool", r.Tool, r.Method, r.Path)
		}
		if prev, dup := tools[k]; dup {
			t.Errorf("routes %s %s mapped to both %q and %q", r.Method, r.Path, prev, r.Tool)
		}
		tools[k] = r.Tool
	}

	for k := range registered {
		if exempt[k] || tools[k] != "" {
			continue
		}
		t.Errorf("route %s %s is neither a tool nor listed as not-a-tool; "+
			"add it to internal/mcp's table or to Excluded/Streaming with a reason",
			k.method, k.path)
	}
}

// TestMCPExcludesDestructiveAdminByName asserts the exclusions by name, so
// removing one from Excluded is a test failure rather than a quiet widening of
// what an agent may do to the daemon supervising it.
//
// It covers four families: the six destructive-admin routes (task 057's five,
// plus `PATCH /v1/config` from task 060), task 063's chat routes, task 065's
// workflow writes, and task 069's one write to a forge. The chat family is
// excluded for a different reason —
// starting an agent process that no §11 cap admitted and no scheduler ordered
// — and the list is asserted whole so a new chat route added without a
// decision about it fails here rather than appearing as a tool.
func TestMCPExcludesDestructiveAdminByName(t *testing.T) {
	t.Parallel()
	want := []struct{ method, path string }{
		{http.MethodPost, "/v1/daemon/stop"},
		{http.MethodPost, "/v1/daemon/backup"},
		// Task 060 decision 4: a step must not rewrite the rules it runs
		// under — the argv the daemon spawns, what its children inherit, or
		// whether steps get MCP at all.
		{http.MethodPatch, "/v1/config"},
		{http.MethodDelete, "/v1/projects/{id}"},
		{http.MethodPost, "/v1/maintenance/gc"},
		{http.MethodPost, "/v1/doctor/fix"},
		{http.MethodGet, "/v1/chats"},
		{http.MethodPost, "/v1/chats"},
		{http.MethodGet, "/v1/chats/{id}"},
		{http.MethodPost, "/v1/chats/{id}/send"},
		{http.MethodPost, "/v1/chats/{id}/answer"},
		{http.MethodPost, "/v1/chats/{id}/cancel"},
		{http.MethodPost, "/v1/chats/{id}/archive"},
		// The two read routes are excluded by the same §13.4 reasoning, not
		// by the Streaming carve-out: a chat's stream and its per-turn
		// transcript are the surface a *human* drives a chat through, and an
		// agent calling either already has a session of its own. Listing them
		// here rather than in Streaming keeps one rule for the whole family.
		{http.MethodGet, "/v1/chats/{id}/events"},
		{http.MethodGet, "/v1/chats/{id}/turns/{seq}/transcript"},
		// Task 065 decision 5, under the same wording: a workflow file is
		// what the daemon runs, so an agent editing one is an agent
		// rewriting the rules it runs under.
		{http.MethodPost, "/v1/workflows"},
		{http.MethodPatch, "/v1/workflows"},
		// Task 069 decision 3: the one route that writes to a forge. Row 27
		// was amended to let a *human* push a task's branch and open its pull
		// request, and "the keypress is the consent" is only true while a
		// human is the one pressing it. An agent's path to the same outcome —
		// `git push` and `gh pr create` in its own worktree — is untouched.
		{http.MethodPost, "/v1/tasks/{id}/github/pull/create"},
	}
	if len(mcp.Excluded) != len(want) {
		t.Fatalf("mcp.Excluded has %d entries, want %d", len(mcp.Excluded), len(want))
	}
	for _, w := range want {
		if !slices.ContainsFunc(mcp.Excluded, func(r mcp.Route) bool {
			return r.Method == w.method && r.Path == w.path
		}) {
			t.Errorf("%s %s is no longer excluded from the MCP tool surface", w.method, w.path)
		}
	}
	for _, r := range mcp.Excluded {
		if slices.Contains(mcp.Names(), r.Path) {
			t.Errorf("%s is exposed as a tool", r.Path)
		}
	}
}

// TestMCPEndpointsAreRegistered proves the two §13.4 endpoints are mounted and
// behind the §13.1 chain: `/mcp` refuses an unauthenticated request, and the
// per-step endpoint refuses an unknown run rather than 404ing into the "no
// such endpoint" fallback.
func TestMCPEndpointsAreRegistered(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, nil)
	for _, tc := range []struct {
		name, path string
		want       int
	}{
		{"shared endpoint needs the daemon token", "/mcp", http.StatusUnauthorized},
		{"per-step endpoint needs its own secret", mcp.StepPathPrefix + "7", http.StatusUnauthorized},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			resp, body := doRequest(t, ts, http.MethodPost, tc.path, "")
			if resp.StatusCode != tc.want {
				t.Errorf("status = %d, want %d (body %s)", resp.StatusCode, tc.want, body)
			}
		})
	}
}
