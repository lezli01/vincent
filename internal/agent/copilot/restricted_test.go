package copilot

import (
	"bytes"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// restrictedResults collects a fixture's tool results by tool name, in order,
// and the CallIDs of the calls whose arguments name a path under the capture's
// out-of-worktree directory.
func restrictedResults(t *testing.T, name string) (map[string][]agent.ToolResult, map[string]bool, *agent.RunResult) {
	t.Helper()
	events := parseFixture(t, name)
	got := map[string][]agent.ToolResult{}
	outside := map[string]bool{}
	for _, ev := range events {
		switch ev.Type {
		case agent.EventToolResult:
			r := ev.Results[0]
			got[r.Name] = append(got[r.Name], r)
		case agent.EventToolUse:
			if n := ev.Tools[0].Name; n == "bash" || n == "powershell" {
				t.Errorf("%s: shell tool_use %+v; restricted copilot has no shell", name, ev.Tools[0])
			}
			if bytes.Contains(ev.Raw, []byte("/tmp/copilot-fixture/outside/")) {
				outside[ev.Tools[0].CallID] = true
			}
		case agent.EventError:
			t.Errorf("%s: error event %s; a refused call is not a run failure", name, ev.Raw)
		}
	}
	return got, outside, resultOf(t, events)
}

// outsideWritesBlocked asserts that every write tool call naming a path
// outside the worktree came back Blocked, and returns how many there were.
func outsideWritesBlocked(t *testing.T, name string, got map[string][]agent.ToolResult, outside map[string]bool) int {
	t.Helper()
	var n int
	for _, tool := range []string{"apply_patch", "edit", "create"} {
		for _, r := range got[tool] {
			if !outside[r.CallID] {
				continue
			}
			n++
			if !r.Blocked || !r.IsError {
				t.Errorf("%s: %s outside the worktree = %+v, want a Blocked error", name, tool, r)
			}
		}
	}
	return n
}

// TestRestrictedWriteFixture (task 138.10, review F1): under the restricted
// argv the edit inside the worktree ran through apply_patch, and both writes
// outside it — an apply_patch deleting and re-adding an existing file there,
// and one adding a new file, both via `*** Add File:` paths inside the patch
// text — were `denied` results: Blocked, because the call never ran. The
// model had no shell to call, so no shell result exists to classify. A call to
// a tool missing from --available-tools (`Tool 'x' does not exist.`, 138.3)
// was not reproduced by any capture, so its classification is left as any
// other failed call's: an error, not Blocked (§9.9).
func TestRestrictedWriteFixture(t *testing.T) {
	name := "restricted_write_1.0.105.jsonl"
	got, outside, res := restrictedResults(t, name)
	var inside int
	for _, r := range got["apply_patch"] {
		if !outside[r.CallID] {
			inside++
			if r.IsError {
				t.Errorf("apply_patch inside the worktree = %+v, want success", r)
			}
		}
	}
	if inside != 1 {
		t.Errorf("apply_patch inside the worktree ran %d times, want 1: %+v", inside, got["apply_patch"])
	}
	if n := outsideWritesBlocked(t, name, got, outside); n != 2 {
		t.Errorf("writes outside the worktree = %d, want 2", n)
	}
	if res.IsError {
		t.Errorf("result = %+v, want success", res)
	}
}

// TestRestrictedResumeFixture: a --resume run of a restricted session, with
// the same flags, still denied both writes outside the worktree (a new file
// and an existing one, each through apply_patch) and had no shell — which is
// why SupportsRestrictedResume is true (task 138.10, review F1).
func TestRestrictedResumeFixture(t *testing.T) {
	name := "restricted_resume_1.0.105.jsonl"
	got, outside, res := restrictedResults(t, name)
	if n := outsideWritesBlocked(t, name, got, outside); n != 2 || len(got["apply_patch"]) != 2 {
		t.Errorf("writes outside the worktree = %d of %+v, want both apply_patch calls", n, got["apply_patch"])
	}
	if res.IsError || res.SessionID != "11111111-2222-4333-8444-555555555556" {
		t.Errorf("result = %+v, want success in the resumed session", res)
	}
}

// TestRestrictedMCPFixture: with `vincent` in --available-tools and
// --allow-tool=vincent, the server's tools are visible and approved in a
// restricted run (task 057 decision 9).
func TestRestrictedMCPFixture(t *testing.T) {
	got, _, res := restrictedResults(t, "restricted_mcp_1.0.105.jsonl")
	if h := got["vincent-health"]; len(h) != 1 || h[0].IsError {
		t.Errorf("vincent-health results = %+v, want one success", h)
	}
	if res.IsError {
		t.Errorf("result = %+v, want success", res)
	}
}
