package main_test

import (
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/cursor"
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

// TestSkillModelScenario pins the fake's skill load to what the real claude
// parser makes of it (task 124.2): the call reads as `Skill echo-probe`, and
// the isSynthetic body becomes the one agent.skill, carrying the name, the
// call and the arguments and none of the body. The fake cursor's prompt echo
// is its input echo, as a real cursor's is.
func TestSkillModelScenario(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)

	out := runAgent(t, bin, []string{"-p", "--output-format", "stream-json", "--verbose"},
		"FAKEAGENT_SCENARIO=skill-model")
	parse := claude.New(func() string { return "" }).NewLineParser()
	var loads []agent.SkillInvocation
	var summary string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		ev := parse([]byte(strings.TrimRight(line, "\r")))
		switch ev.Type { //nolint:exhaustive // only the skill's two lines matter here
		case agent.EventToolUse:
			summary = ev.Tools[0].Summary
		case agent.EventSkill:
			loads = append(loads, *ev.Skill)
		case agent.EventUnknown:
			if strings.Contains(line, "isSynthetic") {
				t.Errorf("the skill body stayed raw: %s", line)
			}
		}
	}
	want := agent.SkillInvocation{Name: "echo-probe", Args: "zebra", By: "agent", CallID: "toolu_fake_skill_1"}
	if len(loads) != 1 || loads[0] != want {
		t.Errorf("loads = %+v, want [%+v]", loads, want)
	}
	if summary != "echo-probe" {
		t.Errorf("Skill call summary = %q, want echo-probe", summary)
	}

	cur := runAgent(t, bin, []string{"-p", "--output-format", "stream-json", "--trust", "--force"})
	parseCursor := cursor.New(func() string { return "" }).NewLineParser()
	var echoes int
	for _, line := range strings.Split(strings.TrimSpace(cur), "\n") {
		if parseCursor([]byte(strings.TrimRight(line, "\r"))).Type == agent.EventInputEcho {
			echoes++
		}
	}
	if echoes != 1 {
		t.Errorf("fake cursor echoes = %d, want its one user line", echoes)
	}
}
