package main_test

import (
	"encoding/json"
	"os/exec"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
)

// claudeChatArgv is the argv a chat turn gives claude (task 124.10): the
// input-mode one, plus the replay flag when the run asks for its invocations
// to be reported.
func claudeChatArgv(replay bool) []string {
	args := []string{
		"-p", "--output-format", "stream-json", "--verbose",
		"--dangerously-skip-permissions",
		"--input-format", "stream-json", "--permission-prompt-tool", "stdio",
	}
	if replay {
		args = append(args, "--replay-user-messages")
	}
	return args
}

// runClaudeTurn runs fakeagent with one stream-json user message on stdin and
// returns the events its stdout normalizes to through the real claude parser.
func runClaudeTurn(t *testing.T, bin string, replay bool, message string, env ...string) []agent.Event {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type": "user",
		"message": map[string]any{
			"role":    "user",
			"content": []any{map[string]any{"type": "text", "text": message}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, claudeChatArgv(replay)...)
	cmd.Stdin = strings.NewReader(string(line) + "\n")
	cmd.Env = append(cmd.Environ(), env...)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("run fakeagent: %v", err)
	}
	parse := claude.New(func() string { return "" }).NewLineParser()
	var events []agent.Event
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l = strings.TrimRight(l, "\r"); l != "" {
			events = append(events, parse([]byte(l)))
		}
	}
	return events
}

// skills reports every skill invocation among the events, and whether any
// replayed line stayed unrecognized.
func skills(events []agent.Event) (loads []agent.SkillInvocation, replays, unknownReplays int) {
	for _, ev := range events {
		if strings.Contains(string(ev.Raw), `"isReplay"`) {
			replays++
			if ev.Type == agent.EventUnknown {
				unknownReplays++
			}
		}
		if ev.Type == agent.EventSkill {
			loads = append(loads, *ev.Skill)
		}
	}
	return loads, replays, unknownReplays
}

// TestSkillHumanScenario pins the fake's human invocation to what the real
// claude parser makes of it, and to the flag rather than to the scenario: the
// same scenario without --replay-user-messages on the argv replays nothing at
// all, which is how a vincent that stopped passing the flag fails here.
func TestSkillHumanScenario(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)
	const env = "FAKEAGENT_SCENARIO=skill-human"

	loads, replays, unknown := skills(runClaudeTurn(t, bin, true, "/probe x y", env))
	want := agent.SkillInvocation{Name: "probe", Args: "x y", By: "human"}
	if len(loads) != 1 || loads[0] != want {
		t.Errorf("loads = %+v, want [%+v]", loads, want)
	}
	if replays != 1 || unknown != 0 {
		t.Errorf("replayed lines = %d, of them unrecognized = %d, want 1 and 0", replays, unknown)
	}

	// A plain message is echoed, and an echo is no invocation.
	loads, replays, unknown = skills(runClaudeTurn(t, bin, true, "just a message", env))
	if len(loads) != 0 {
		t.Errorf("plain message reported %+v, want no invocation", loads)
	}
	if replays != 1 || unknown != 0 {
		t.Errorf("plain echo: replayed %d, unrecognized %d, want 1 and 0", replays, unknown)
	}

	// Without the flag the CLI says nothing about the invocation at all.
	loads, replays, _ = skills(runClaudeTurn(t, bin, false, "/probe x y", env))
	if len(loads) != 0 || replays != 0 {
		t.Errorf("without the flag: loads %+v, replays %d, want none of either", loads, replays)
	}
}

// TestSkillInjectDeniedScenario: the refusal replay is the invocation's
// failure, nameless because the capture it is modelled on carries no
// command-tag replay before it, and it too rides on the flag.
func TestSkillInjectDeniedScenario(t *testing.T) {
	t.Parallel()
	bin := agenttest.BuildFakeAgent(t)
	const env = "FAKEAGENT_SCENARIO=skill-inject-denied"

	loads, replays, unknown := skills(runClaudeTurn(t, bin, true, "/inject-probe", env))
	if len(loads) != 1 || loads[0].By != "human" || loads[0].Name != "" || loads[0].Error == "" {
		t.Fatalf("loads = %+v, want the one nameless failure by the human", loads)
	}
	if !strings.Contains(loads[0].Error, "mkdir -p probe-made-dir && echo made") {
		t.Errorf("error = %q, want the refused command with its entities decoded", loads[0].Error)
	}
	if replays != 1 || unknown != 0 {
		t.Errorf("replayed lines = %d, of them unrecognized = %d, want 1 and 0", replays, unknown)
	}

	if loads, replays, _ = skills(runClaudeTurn(t, bin, false, "/inject-probe", env)); len(loads) != 0 || replays != 0 {
		t.Errorf("without the flag: loads %+v, replays %d, want none of either", loads, replays)
	}
}
