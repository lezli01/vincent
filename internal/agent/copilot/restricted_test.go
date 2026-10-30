package copilot

import (
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// restrictedResults collects a fixture's tool results by tool name, in order.
func restrictedResults(t *testing.T, name string) (map[string][]agent.ToolResult, *agent.RunResult) {
	t.Helper()
	events := parseFixture(t, name)
	got := map[string][]agent.ToolResult{}
	for _, ev := range events {
		switch ev.Type {
		case agent.EventToolResult:
			r := ev.Results[0]
			got[r.Name] = append(got[r.Name], r)
		case agent.EventToolUse:
			if n := ev.Tools[0].Name; n == "bash" || n == "powershell" {
				t.Errorf("%s: shell tool_use %+v; restricted copilot has no shell", name, ev.Tools[0])
			}
		case agent.EventError:
			t.Errorf("%s: error event %s; a refused call is not a run failure", name, ev.Raw)
		}
	}
	return got, resultOf(t, events)
}

// TestRestrictedWriteFixture (task 138.10): under the restricted argv the edit
// inside the worktree ran through apply_patch, the path outside it was a
// `denied` result — Blocked, because the call never ran — and the model had
// no shell to call, so no shell result exists to classify. A call to a tool
// missing from --available-tools (`Tool 'x' does not exist.`, 138.3) was not
// reproduced by any capture, so its classification is left as any other
// failed call's: an error, not Blocked (§9.9).
func TestRestrictedWriteFixture(t *testing.T) {
	got, res := restrictedResults(t, "restricted_write_1.0.105.jsonl")
	if p := got["apply_patch"]; len(p) != 1 || p[0].IsError {
		t.Errorf("apply_patch results = %+v, want one successful edit", p)
	}
	var blocked int
	for _, r := range got["view"] {
		if r.Blocked {
			blocked++
			if !r.IsError {
				t.Errorf("blocked result %+v is not an error", r)
			}
		}
	}
	if blocked != 1 {
		t.Errorf("blocked view results = %d, want the one outside the worktree: %+v", blocked, got["view"])
	}
	if res.IsError {
		t.Errorf("result = %+v, want success", res)
	}
}

// TestRestrictedResumeFixture: a --resume run of a restricted session, with
// the same flags, still denied the write outside the worktree and had no
// shell — which is why SupportsRestrictedResume is true (task 138.10).
func TestRestrictedResumeFixture(t *testing.T) {
	got, res := restrictedResults(t, "restricted_resume_1.0.105.jsonl")
	if v := got["view"]; len(v) == 0 || !v[0].Blocked {
		t.Errorf("view results = %+v, want the outside path blocked first", v)
	}
	if res.IsError || res.SessionID != "11111111-2222-4333-8444-555555555556" {
		t.Errorf("result = %+v, want success in the resumed session", res)
	}
}

// TestRestrictedMCPFixture: with `vincent` in --available-tools and
// --allow-tool=vincent, the server's tools are visible and approved in a
// restricted run (task 057 decision 9).
func TestRestrictedMCPFixture(t *testing.T) {
	got, res := restrictedResults(t, "restricted_mcp_1.0.105.jsonl")
	if h := got["vincent-health"]; len(h) != 1 || h[0].IsError {
		t.Errorf("vincent-health results = %+v, want one success", h)
	}
	if res.IsError {
		t.Errorf("result = %+v, want success", res)
	}
}
