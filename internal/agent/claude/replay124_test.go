package claude

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// The replay captures (task 124.10) were recorded on 2026-09-20 against
// claude 2.1.277 with vincent's own chat-turn argv — the input-mode one plus
// --replay-user-messages — the prompt written to stdin as one stream-json
// user line, in a throwaway directory outside the repository holding two
// probe skills under .claude/skills. Paths are rewritten under /work/repo and
// /home/user, session ids replaced, the SessionStart hook lines removed, and
// the init line's personal inventory (the machine's plugins, MCP servers,
// agents and skills) replaced with the probe's own; the line structure and
// the field names are verbatim.
const (
	// replayHumanFixture is a skill the human invoked, prompted
	// `/echo-probe hello <there> & co` — arguments chosen to prove they
	// arrive unescaped — against skill124_test.go's echo-probe/SKILL.md.
	replayHumanFixture = "stream_skill_human_2.1.277.jsonl"
	// replayDeniedFixture is a command a skill injected and that the
	// permission check refused, in restricted mode — `--allowedTools
	// Read,Glob,Grep,Edit,Write,MultiEdit,Bash(git:*),mcp__vincent__*` in
	// place of --dangerously-skip-permissions — prompted `/inject-probe`,
	// with inject-probe/SKILL.md:
	//
	//	---
	//	name: inject-probe
	//	description: Inject probe for vincent fixture capture.
	//	allowed-tools: Bash(echo:*)
	//	---
	//	!`mkdir -p probe-made-dir && echo made`
	//
	//	Reply with exactly the word INJECT-PROBE.
	//
	// claude replays *only* the refusal here — no command-tag replay
	// precedes it — and then ends the run with an empty success reporting
	// `num_turns: 0`. It is therefore the capture that exercises decision
	// 61's nameless fallback.
	replayDeniedFixture = "stream_skill_inject_denied_2.1.277.jsonl"
	// replayControlFixture is an ordinary message and the §7.4 traffic it
	// caused: prompted `Use the AskUserQuestion tool to ask me whether I
	// prefer red or blue, then reply with the answer and nothing else.`, the
	// one can_use_tool request answered with the allow vincent writes. Both
	// the message echo and vincent's own control_response are on stdout.
	replayControlFixture = "stream_replay_control_2.1.277.jsonl"
	// replayUnknownFixture is a `/name` claude could not resolve, prompted
	// `/no-such-skill hi`: echoed as a bare string with no command elements
	// at all, and answered in prose.
	replayUnknownFixture = "stream_replay_unknown_2.1.277.jsonl"
)

// The lines of the replay captures, by index.
const (
	humanReplayLine   = 1
	deniedReplayLine  = 1
	controlEchoLine   = 1
	controlRespLine   = 4
	unknownReplayLine = 1
)

// TestHumanInvokedSkill is the whole of a human's invocation: the replayed
// command elements become the one agent.skill, carrying the name and the
// arguments exactly as they were typed.
func TestHumanInvokedSkill(t *testing.T) {
	events := parseAll(fixtureLines(t, replayHumanFixture)...)

	want := agent.SkillInvocation{Name: "echo-probe", Args: "hello <there> & co", By: "human"}
	if ev := events[humanReplayLine]; ev.Type != agent.EventSkill || ev.Skill == nil || *ev.Skill != want {
		t.Fatalf("replay = %q %+v, want skill %+v", ev.Type, ev.Skill, want)
	}
	var skills, unknowns int
	for _, ev := range events {
		switch ev.Type { //nolint:exhaustive // only the two counts matter here
		case agent.EventSkill:
			skills++
		case agent.EventUnknown:
			unknowns++
		}
	}
	if skills != 1 {
		t.Errorf("skill events = %d, want the one invocation", skills)
	}
	// The one unknown is the rate_limit_event, not a replayed line.
	if unknowns != 1 {
		t.Errorf("unknown events = %d, want only the rate_limit_event", unknowns)
	}
}

// TestHumanInvocationArgumentsAreVerbatim is decision 67: `<command-args>` is
// not escaped on the wire, so unescaping it would corrupt a message that
// typed a literal entity. Only the refusal body is unescaped.
func TestHumanInvocationArgumentsAreVerbatim(t *testing.T) {
	line := replayLine(t, `<command-message>p</command-message>`+"\n"+
		`<command-name>/p</command-name>`+"\n"+
		`<command-args>literal &amp; and &lt;tags&gt;</command-args>`)
	ev := parseAll(line)[0]
	if ev.Type != agent.EventSkill || ev.Skill == nil {
		t.Fatalf("replay = %q, want a skill", ev.Type)
	}
	if ev.Skill.Args != "literal &amp; and &lt;tags&gt;" {
		t.Errorf("args = %q, want them exactly as typed", ev.Skill.Args)
	}
}

// TestRefusedInjectedCommand is decision 65: the `<local-command-stderr>`
// replay is the invocation's failure, with the body unescaped and capped.
// This capture has no command-tag replay before it, so the record is the
// nameless one the fallback yields.
func TestRefusedInjectedCommand(t *testing.T) {
	ev := parseAll(fixtureLines(t, replayDeniedFixture)...)[deniedReplayLine]
	if ev.Type != agent.EventSkill || ev.Skill == nil {
		t.Fatalf("refusal = %q %+v, want a skill", ev.Type, ev.Skill)
	}
	if ev.Skill.By != "human" || ev.Skill.Name != "" {
		t.Errorf("record = %+v, want a nameless invocation by the human", *ev.Skill)
	}
	if !strings.Contains(ev.Skill.Error, "mkdir -p probe-made-dir && echo made") {
		t.Errorf("error = %q, want the refused command with its entities decoded", ev.Skill.Error)
	}
	if strings.Contains(ev.Skill.Error, "&amp;") {
		t.Errorf("error = %q, still carries an escape", ev.Skill.Error)
	}
	// The real refusal runs to several hundred characters, so it is also
	// what proves the cap: capped to agent.ToolSummaryMax runes plus the
	// ellipsis, flattened to one line.
	if n := len([]rune(ev.Skill.Error)); n > agent.ToolSummaryMax+1 || !strings.HasSuffix(ev.Skill.Error, "…") {
		t.Errorf("error is %d runes (%q), want it capped at %d", n, ev.Skill.Error, agent.ToolSummaryMax)
	}
	if strings.ContainsAny(ev.Skill.Error, "\r\n") {
		t.Errorf("error = %q, want one line", ev.Skill.Error)
	}
}

// TestRefusalTakesTheLastReplayedName is the rest of decision 65: when an
// invocation *was* replayed earlier in the turn, its refusal carries that
// name rather than rendering every invocation's failure identically.
func TestRefusalTakesTheLastReplayedName(t *testing.T) {
	invocation := fixtureLines(t, replayHumanFixture)[humanReplayLine]
	refusal := fixtureLines(t, replayDeniedFixture)[deniedReplayLine]

	events := parseAll(invocation, refusal)
	if ev := events[1]; ev.Skill == nil || ev.Skill.Name != "echo-probe" || ev.Skill.Error == "" {
		t.Errorf("refusal = %q %+v, want echo-probe's failure", ev.Type, ev.Skill)
	}
	// A parser that never saw an invocation still reports the failure.
	if ev := parseAll(refusal)[0]; ev.Skill == nil || ev.Skill.Name != "" || ev.Skill.Error == "" {
		t.Errorf("lone refusal = %q %+v, want the nameless failure", ev.Type, ev.Skill)
	}
}

// TestEchoesAreNotSkills is decision 64 and the rest of the mapping: an
// ordinary message, a `/name` claude could not resolve, and vincent's own
// answer handed back to it are all agent.input_echo — and none of them is
// agent.unknown, which is what the transcript would render as agent.raw.
func TestEchoesAreNotSkills(t *testing.T) {
	control := parseAll(fixtureLines(t, replayControlFixture)...)
	for _, i := range []int{controlEchoLine, controlRespLine} {
		if ev := control[i]; ev.Type != agent.EventInputEcho {
			t.Errorf("line %d = %q, want input_echo", i, ev.Type)
		}
	}
	unknown := parseAll(fixtureLines(t, replayUnknownFixture)...)
	if ev := unknown[unknownReplayLine]; ev.Type != agent.EventInputEcho || ev.Skill != nil {
		t.Errorf("unresolved /name = %q %+v, want input_echo and no skill", ev.Type, ev.Skill)
	}
	for name, events := range map[string][]agent.Event{
		replayControlFixture: control,
		replayUnknownFixture: unknown,
	} {
		for i, ev := range events {
			if ev.Type == agent.EventUnknown && strings.Contains(string(ev.Raw), `"isReplay"`) {
				t.Errorf("%s line %d stayed unknown: %s", name, i, ev.Raw)
			}
			if ev.Type == agent.EventSkill {
				t.Errorf("%s line %d reported a skill nobody invoked: %+v", name, i, ev.Skill)
			}
		}
	}
}

// TestReplaysAreInNoSkillScope is decision 68: a replayed line and an echoed
// control_response interleaving between a `Skill` result and its body must
// neither claim the load nor disarm the scope.
func TestReplaysAreInNoSkillScope(t *testing.T) {
	model := fixtureLines(t, skillModelFixture)
	replay := fixtureLines(t, replayControlFixture)[controlEchoLine]
	response := fixtureLines(t, replayControlFixture)[controlRespLine]

	events := parseAll(model[modelCallLine], model[modelResultLine], replay, response, model[modelBodyLine])
	for i, want := range map[int]agent.EventType{2: agent.EventInputEcho, 3: agent.EventInputEcho} {
		if ev := events[i]; ev.Type != want {
			t.Errorf("interleaved line %d = %q, want %q", i, ev.Type, want)
		}
	}
	want := agent.SkillInvocation{Name: "echo-probe", Args: "zebra", By: "agent", CallID: skillModelCall}
	if ev := events[4]; ev.Type != agent.EventSkill || ev.Skill == nil || *ev.Skill != want {
		t.Fatalf("body = %q %+v, want the model's load, untouched by the replays", ev.Type, ev.Skill)
	}
}

// TestReplaysCarryNoBody: a replay is a message, and a message is already on
// screen as the human's own. Nothing normalized may hold its text.
func TestReplaysCarryNoBody(t *testing.T) {
	for _, name := range []string{
		replayHumanFixture, replayDeniedFixture, replayControlFixture, replayUnknownFixture,
	} {
		for i, ev := range parseAll(fixtureLines(t, name)...) {
			if ev.Type != agent.EventSkill {
				continue
			}
			ev.Raw = nil
			b, err := json.Marshal(ev)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), "command-message") {
				t.Errorf("%s line %d: a normalized field carries the replayed markup: %s", name, i, b)
			}
		}
	}
}

// replayLine renders one `isReplay` user line whose content is text, which is
// the string shape claude uses for an expanded command.
func replayLine(t *testing.T, text string) []byte {
	t.Helper()
	line, err := json.Marshal(map[string]any{
		"type":       "user",
		"isReplay":   true,
		"message":    map[string]any{"role": "user", "content": text},
		"session_id": "s1",
	})
	if err != nil {
		t.Fatal(err)
	}
	return line
}

// TestMalformedReplayIsAnEcho: a replay whose markup vincent cannot read is
// the echo it always was, never a half-filled invocation — a wrong guess
// fails silently (T4.17).
func TestMalformedReplayIsAnEcho(t *testing.T) {
	for name, text := range map[string]string{
		"no closing name": `<command-name>/p`,
		"empty name":      `<command-name>/</command-name><command-args>x</command-args>`,
		"stderr unclosed": `<local-command-stderr>boom`,
		"plain text":      `just a message`,
	} {
		t.Run(name, func(t *testing.T) {
			if ev := parseAll(replayLine(t, text))[0]; ev.Type != agent.EventInputEcho {
				t.Errorf("replay = %q %+v, want input_echo", ev.Type, ev.Skill)
			}
		})
	}
}
