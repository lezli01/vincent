package main_test

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent/agenttest"
)

// TestCodexScenarioOverride pins the dialect-scoped scenario knob. The M3
// gate (T3.8) points both adapters at this binary, which means one process
// environment drives two dialects — the rehearsal needs claude asking a
// question while codex succeeds, and FAKEAGENT_SCENARIO alone cannot say
// that.
func TestCodexScenarioOverride(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)

	env := []string{"FAKEAGENT_SCENARIO=big-usage", "FAKEAGENT_SCENARIO_CODEX=success"}

	codex := runAgent(t, bin, []string{"exec", "--json"}, env...)
	if strings.Contains(codex, "burning tokens") {
		t.Errorf("codex ran FAKEAGENT_SCENARIO, not its own override:\n%s", codex)
	}
	if !strings.Contains(codex, "done: ") {
		t.Errorf("codex did not run the overriding scenario:\n%s", codex)
	}

	// The override must be invisible to the claude dialect, or one gate's
	// convenience becomes another dialect's silent behavior change.
	claude := runAgent(t, bin, []string{"-p", "--output-format", "stream-json"}, env...)
	if !strings.Contains(claude, "burning tokens") {
		t.Errorf("claude dialect lost FAKEAGENT_SCENARIO to the codex override:\n%s", claude)
	}

	// Unset, the codex dialect still follows the shared scenario.
	shared := runAgent(t, bin, []string{"exec", "--json"}, "FAKEAGENT_SCENARIO=big-usage")
	if !strings.Contains(shared, "burning tokens") {
		t.Errorf("codex ignored FAKEAGENT_SCENARIO with no override set:\n%s", shared)
	}
}
