package main_test

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// TestDialectDiscrimination pins the argv rule that selects a dialect. It
// matters more since T5.2 than it did with two adapters: cursor's run argv is
// claude's argv plus flags (`-p --output-format stream-json`), so the two are
// distinguishable only by `--trust`, which vincent passes to cursor in both
// permission modes and to nothing else.
func TestDialectDiscrimination(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)

	tests := []struct {
		name string
		args []string
		// marker is a line shape unique to the dialect's stream.
		marker string
	}{
		{
			name:   "exec first is codex",
			args:   []string{"exec", "--json"},
			marker: `"type":"turn.completed"`,
		},
		{
			name:   "trust anywhere is cursor",
			args:   []string{"-p", "--output-format", "stream-json", "--trust", "--force", "--model", "auto"},
			marker: `"type":"thinking"`,
		},
		{
			name:   "restricted cursor is still cursor",
			args:   []string{"-p", "--output-format", "stream-json", "--trust", "--sandbox", "enabled"},
			marker: `"type":"thinking"`,
		},
		{
			name:   "neither is claude",
			args:   []string{"-p", "--output-format", "stream-json", "--verbose"},
			marker: `"total_cost_usd"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out := runAgent(t, bin, tt.args)
			if !strings.Contains(out, tt.marker) {
				t.Errorf("stream lacks %s — wrong dialect for %v:\n%s", tt.marker, tt.args, out)
			}
		})
	}
}

// TestCursorScenarioOverride mirrors the codex knob: one process environment
// must be able to drive cursor and claude differently, which a gate pointing
// both adapters at this binary depends on.
func TestCursorScenarioOverride(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)
	env := []string{"FAKEAGENT_SCENARIO=big-usage", "FAKEAGENT_SCENARIO_CURSOR=success"}
	cursorArgs := []string{"-p", "--output-format", "stream-json", "--trust", "--force"}

	cursor := runAgent(t, bin, cursorArgs, env...)
	if strings.Contains(cursor, "burning tokens") {
		t.Errorf("cursor ran FAKEAGENT_SCENARIO, not its own override:\n%s", cursor)
	}
	if !strings.Contains(cursor, "done: ") {
		t.Errorf("cursor did not run the overriding scenario:\n%s", cursor)
	}

	claude := runAgent(t, bin, []string{"-p", "--output-format", "stream-json"}, env...)
	if !strings.Contains(claude, "burning tokens") {
		t.Errorf("claude dialect lost FAKEAGENT_SCENARIO to the cursor override:\n%s", claude)
	}
}

// TestCursorProbeSubcommands covers the two argv shapes that carry no run
// flags at all: they must answer before any dialect dispatch, or the option
// and login probes fall through to a claude-shaped run.
func TestCursorProbeSubcommands(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)

	models := runAgent(t, bin, []string{"models"})
	if !strings.Contains(models, "auto - Auto") || !strings.Contains(models, "Tip:") {
		t.Errorf("models output missing the real shape:\n%s", models)
	}
	status := runAgent(t, bin, []string{"status"})
	if !strings.Contains(status, "Logged in") {
		t.Errorf("status output = %q, want a logged-in line", status)
	}
}
