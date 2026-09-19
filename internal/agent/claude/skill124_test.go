package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// The skill captures (task 124.2) were recorded on 2026-09-19 against claude
// 2.1.277 with vincent's own input-mode argv, the prompt written to stdin as
// one stream-json user line, in a repository holding three probe skills under
// .claude/skills. Paths are rewritten under C:\work\repo and C:\Users\user,
// session ids replaced, and the SessionStart hook lines removed; the line
// structure and the field names are verbatim.
const (
	// skillModelFixture is the model loading a skill it was asked for:
	//
	//	claude -p --output-format stream-json --verbose
	//	  --dangerously-skip-permissions
	//	  --input-format stream-json --permission-prompt-tool stdio
	//
	// prompted `Use the echo-probe skill with the argument "zebra". Do nothing
	// else.`, with echo-probe/SKILL.md:
	//
	//	---
	//	name: echo-probe
	//	description: Echo probe for vincent fixture capture. Use when asked to run the echo probe.
	//	---
	//	Reply with exactly the word ECHO-PROBE followed by the arguments you were given: $ARGUMENTS
	skillModelFixture = "stream_skill_model_2.1.277.jsonl"
	// skillPermissionFixture is the same in restricted mode — `--allowedTools
	// Read,Glob,Grep,Edit,Write,MultiEdit,Bash(git:*),mcp__vincent__*` in place
	// of --dangerously-skip-permissions — prompted `Use the bash-probe skill.
	// Do nothing else.`, and every can_use_tool request allowed. The `Skill`
	// call raises one. bash-probe/SKILL.md:
	//
	//	---
	//	name: bash-probe
	//	description: Bash probe for vincent fixture capture. Use when asked to run the bash probe.
	//	allowed-tools: Bash(echo:*)
	//	---
	//	Run `echo bash-probe` with the Bash tool and reply with its output.
	skillPermissionFixture = "stream_skill_permission_2.1.277.jsonl"
	// skillForkFixture is a `context: fork` skill the human's message invoked:
	// skillModelFixture's argv, prompted `/fork-probe zebra`. fork-probe/SKILL.md:
	//
	//	---
	//	name: fork-probe
	//	description: Fork probe for vincent fixture capture.
	//	context: fork
	//	---
	//	Reply with exactly the word FORK-PROBE followed by the arguments you were given: $ARGUMENTS
	skillForkFixture = "stream_skill_fork_2.1.277.jsonl"
)

// skillModelCall is the `Skill` call in skillModelFixture.
const skillModelCall = "toolu_01DiBcVgHar46Gv7ikcAQUkW"

// skillBodyMarker opens every rendered SKILL.md claude writes into the
// conversation. No normalized field may hold it (T4.16).
const skillBodyMarker = "Base directory for this skill"

// The lines of skillModelFixture, by index.
const (
	modelCallLine   = 1
	modelResultLine = 3
	modelBodyLine   = 4
)

// fixtureLines returns a fixture's non-blank lines verbatim.
func fixtureLines(t *testing.T, name string) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = f.Close() }()
	var lines [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxLineBytes)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			lines = append(lines, append([]byte(nil), sc.Bytes()...))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	return lines
}

// parseAll runs lines through one fresh transcript parser, in order.
func parseAll(lines ...[]byte) []agent.Event {
	parse := new(Adapter).NewLineParser()
	out := make([]agent.Event, 0, len(lines))
	for _, l := range lines {
		out = append(out, parse(l))
	}
	return out
}

// skillSubagent is the spawning call inSubagent attributes a line to.
const skillSubagent = "toolu_01SubagentSpawnCall000"

// inSubagent rewrites a line's parent_tool_use_id to skillSubagent, which is
// how a subagent's copy of a main-loop line is made.
func inSubagent(t *testing.T, line []byte) []byte {
	t.Helper()
	var obj map[string]any
	if err := json.Unmarshal(line, &obj); err != nil {
		t.Fatal(err)
	}
	obj["parent_tool_use_id"] = skillSubagent
	out, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestModelLoadedSkill is the whole of an agent's skill load: the call reads
// as `Skill echo-probe`, its result stays a result, and the rendered body
// becomes the load — carrying the name, the call and the arguments, and none
// of the body.
func TestModelLoadedSkill(t *testing.T) {
	events, _ := fixtureEvents(t, skillModelFixture)

	call := events[modelCallLine]
	if call.Type != agent.EventToolUse || len(call.Tools) != 1 ||
		call.Tools[0].Name != "Skill" || call.Tools[0].Summary != "echo-probe" {
		t.Errorf("call = %+v, want tool_use Skill with summary echo-probe", call)
	}
	if res := events[modelResultLine]; res.Type != agent.EventToolResult || len(res.Results) != 1 ||
		res.Results[0].CallID != skillModelCall {
		t.Errorf("result = %+v, want the Launching skill tool_result", res)
	}

	body := events[modelBodyLine]
	want := agent.SkillInvocation{Name: "echo-probe", Args: "zebra", By: "agent", CallID: skillModelCall}
	if body.Type != agent.EventSkill || body.Skill == nil || *body.Skill != want {
		t.Fatalf("body = %q %+v, want skill %+v", body.Type, body.Skill, want)
	}
	if body.ParentCallID != "" {
		t.Errorf("parent = %q, want the main loop", body.ParentCallID)
	}

	var skills int
	for i, ev := range events {
		if ev.Type == agent.EventSkill {
			skills++
		}
		ev.Raw = nil
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), skillBodyMarker) {
			t.Errorf("line %d: a normalized field carries the SKILL.md body: %s", i, b)
		}
	}
	if skills != 1 {
		t.Errorf("skill events = %d, want the one load", skills)
	}
}

// TestSkillBodyNeedsItsResult: the body line on its own says nothing about
// which skill it is, so without the result directly before it in its scope it
// stays raw, as any other isSynthetic line does.
func TestSkillBodyNeedsItsResult(t *testing.T) {
	lines := fixtureLines(t, skillModelFixture)
	call, result, body := lines[modelCallLine], lines[modelResultLine], lines[modelBodyLine]
	unrelated := lines[modelBodyLine+1] // the assistant's reply

	for _, tc := range []struct {
		name  string
		lines [][]byte
	}{
		{"result removed", [][]byte{call, body}},
		{"a line between", [][]byte{call, result, unrelated, body}},
		{"body alone", [][]byte{body}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := parseAll(tc.lines...)
			if last := events[len(events)-1]; last.Type != agent.EventUnknown {
				t.Errorf("body = %q %+v, want unknown", last.Type, last.Skill)
			}
		})
	}
}

// TestSkillPairingIsScopedByParent is task 124 decision 22: an async
// subagent's lines interleave with the main loop's, so a line from another
// scope neither claims a pending load nor clears it.
func TestSkillPairingIsScopedByParent(t *testing.T) {
	lines := fixtureLines(t, skillModelFixture)
	call, result, body := lines[modelCallLine], lines[modelResultLine], lines[modelBodyLine]
	const sub = skillSubagent

	t.Run("main loop armed, subagent body", func(t *testing.T) {
		events := parseAll(call, result, inSubagent(t, body), body)
		if ev := events[2]; ev.Type != agent.EventUnknown {
			t.Errorf("subagent's body = %q, want unknown: the load is the main loop's", ev.Type)
		}
		if ev := events[3]; ev.Type != agent.EventSkill || ev.Skill.CallID != skillModelCall ||
			ev.Skill.Args != "zebra" || ev.ParentCallID != "" {
			t.Errorf("main loop's body = %q %+v, want the load, untouched by the other scope", ev.Type, ev.Skill)
		}
	})

	t.Run("subagent armed, main loop body", func(t *testing.T) {
		events := parseAll(inSubagent(t, call), inSubagent(t, result), body, inSubagent(t, body))
		if ev := events[2]; ev.Type != agent.EventUnknown {
			t.Errorf("main loop's body = %q, want unknown: the load is the subagent's", ev.Type)
		}
		if ev := events[3]; ev.Type != agent.EventSkill || ev.Skill.Name != "echo-probe" ||
			ev.Skill.Args != "zebra" || ev.ParentCallID != sub {
			t.Errorf("subagent's body = %q %+v parent %q, want its load", ev.Type, ev.Skill, ev.ParentCallID)
		}
	})
}

// TestSkillArgsNeedTheCall is decision 21's stated cost: a transcript range
// that opens after the `Skill` call still yields the load, with no Args.
func TestSkillArgsNeedTheCall(t *testing.T) {
	lines := fixtureLines(t, skillModelFixture)
	events := parseAll(lines[modelResultLine], lines[modelBodyLine])
	ev := events[1]
	if ev.Type != agent.EventSkill || ev.Skill.Name != "echo-probe" || ev.Skill.CallID != skillModelCall {
		t.Fatalf("body = %q %+v, want the load", ev.Type, ev.Skill)
	}
	if ev.Skill.Args != "" {
		t.Errorf("args = %q, want none: the call was before the range", ev.Skill.Args)
	}
}

// TestRefusedSkillCallIsNoLoad is decision 23: a result claude flagged as an
// error arms nothing, even with a commandName beside it, because the refusal
// is already that call's agent.tool_result.
func TestRefusedSkillCallIsNoLoad(t *testing.T) {
	lines := fixtureLines(t, skillModelFixture)
	var obj map[string]any
	if err := json.Unmarshal(lines[modelResultLine], &obj); err != nil {
		t.Fatal(err)
	}
	obj["message"].(map[string]any)["content"].([]any)[0].(map[string]any)["is_error"] = true
	refused, err := json.Marshal(obj)
	if err != nil {
		t.Fatal(err)
	}
	events := parseAll(lines[modelCallLine], refused, lines[modelBodyLine])
	if ev := events[1]; ev.Type != agent.EventToolResult || !ev.Results[0].IsError {
		t.Errorf("refusal = %q %+v, want an error tool_result", ev.Type, ev.Results)
	}
	if ev := events[2]; ev.Type != agent.EventUnknown {
		t.Errorf("body after a refusal = %q, want unknown", ev.Type)
	}
}

// TestSkillPermissionNamesTheSkill is decision 26: claude's description on a
// `Skill` request is the skill's blurb, so the §7.4 summary is the name.
func TestSkillPermissionNamesTheSkill(t *testing.T) {
	events, _ := fixtureEvents(t, skillPermissionFixture)
	reqs := inputRequests(events)
	if len(reqs) != 1 {
		t.Fatalf("input requests = %d, want the one Skill request", len(reqs))
	}
	p := reqs[0].Request.Permission
	if p == nil || p.Tool != "Skill" || p.Summary != "bash-probe" {
		t.Errorf("permission = %+v, want Skill / bash-probe", p)
	}
	// The load still normalizes behind the request on the live path.
	var loads []agent.SkillInvocation
	for _, ev := range events {
		if ev.Type == agent.EventSkill {
			loads = append(loads, *ev.Skill)
		}
	}
	if len(loads) != 1 || loads[0].Name != "bash-probe" || loads[0].By != "agent" || loads[0].Args != "" {
		t.Errorf("loads = %+v, want bash-probe by the agent, with no args", loads)
	}

	// A Skill request whose input names no skill keeps the description.
	ev, _ := parseControlRequest([]byte(`{"type":"control_request","request_id":"r1",` +
		`"request":{"subtype":"can_use_tool","tool_name":"Skill","input":{},"description":"Some blurb"}}`))
	if p := ev.Request.Permission; p == nil || p.Summary != "Some blurb" {
		t.Errorf("nameless Skill request = %+v, want the description", p)
	}
}

// TestSkillLoadsTheSameOnBothPaths: the live run answers the §7.4 control
// lines before the parser sees them, and the transcript route hands them to
// it, and the load must normalize identically either way (task 071).
func TestSkillLoadsTheSameOnBothPaths(t *testing.T) {
	live, _ := fixtureEvents(t, skillPermissionFixture)
	stored := parseAll(fixtureLines(t, skillPermissionFixture)...)
	for i := range live {
		if (live[i].Type == agent.EventSkill) != (stored[i].Type == agent.EventSkill) {
			t.Errorf("line %d: live %q, transcript %q", i, live[i].Type, stored[i].Type)
		}
	}
}

// TestForkedSkillKickoff is decision 20: a `context: fork` skill's kickoff is
// a `local_agent` start no call spawned, titled `/name`, before the init line.
// 2.1.277 puts no arguments in the title, so Args is empty.
func TestForkedSkillKickoff(t *testing.T) {
	events := parseAll(fixtureLines(t, skillForkFixture)...)
	want := agent.SkillInvocation{Name: "fork-probe", By: "human", Forked: true}
	if ev := events[0]; ev.Type != agent.EventSkill || ev.Skill == nil || *ev.Skill != want {
		t.Fatalf("kickoff = %q %+v, want %+v", ev.Type, ev.Skill, want)
	}
	for i, ev := range events {
		ev.Raw = nil
		b, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), skillBodyMarker) {
			t.Errorf("line %d: a normalized field carries the rendered body", i)
		}
	}
	// The header is not the stream's first line here.
	var header int
	for i, ev := range events {
		if ev.Type == agent.EventRunHeader {
			header = i
		}
	}
	if header == 0 {
		t.Error("the run header opens the fork capture; the kickoff should precede it")
	}

	for _, desc := range []string{"fork-probe", "Explore the repo", "/"} {
		line, err := json.Marshal(map[string]any{
			"type": "system", "subtype": "task_started", "task_type": "local_agent",
			"description": desc, "task_id": "a1",
		})
		if err != nil {
			t.Fatal(err)
		}
		if ev := parseAll(line)[0]; ev.Type != agent.EventUnknown {
			t.Errorf("uncalled start titled %q = %q, want unknown", desc, ev.Type)
		}
	}
}

// TestFixtureEventTypesArePinned holds every capture that predates task 124.2
// to the per-line event types it parsed to before — the skill mapping claims
// only the lines it names — and pins the new captures beside them, so a later
// change to either shows up here.
func TestFixtureEventTypesArePinned(t *testing.T) {
	want := map[string]string{
		"stream_edit_2.1.268.jsonl":             "tool_use tool_result tool_use tool_result tool_use tool_result tool_use tool_result tool_use tool_result tool_use tool_result tool_use tool_result",
		"stream_permission_allow_2.1.226.jsonl": "run_header thinking tool_use unknown tool_result thinking output result",
		"stream_permission_deny_2.1.226.jsonl":  "run_header thinking tool_use unknown tool_result thinking output result",
		"stream_question_2.1.226.jsonl":         "run_header thinking tool_use unknown tool_result thinking output result",
		"stream_subagent_async_2.1.268.jsonl": "run_header tool_use unknown subagent_started tool_result thinking tool_use subagent_progress " +
			"unknown tool_result tool_use unknown subagent_started tool_result tool_result tool_use subagent_progress thinking " +
			"tool_use subagent_progress tool_result tool_use tool_result unknown unknown output subagent_finished output " +
			"subagent_finished output result",
		"stream_subagent_failed_2.1.268.jsonl": "tool_use subagent_started tool_result thinking tool_use subagent_finished",
		"stream_subagent_sync_2.1.263.jsonl": "run_header thinking output tool_use subagent_started unknown subagent_progress tool_use " +
			"subagent_progress tool_use tool_result tool_result unknown unknown unknown tool_result unknown subagent_finished " +
			"tool_result output result",
		skillForkFixture:       "skill unknown unknown run_header output result",
		skillModelFixture:      "run_header tool_use unknown tool_result skill output unknown result",
		skillPermissionFixture: "run_header tool_use unknown unknown tool_result skill tool_use tool_result output unknown result",
		contextBlocksFixture:   "run_header unknown unknown unknown output unknown result",
	}
	names, err := filepath.Glob(filepath.Join("testdata", "stream_*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range names {
		name := filepath.Base(path)
		exp, ok := want[name]
		if !ok {
			t.Errorf("%s: no pinned event types; add the capture here", name)
			continue
		}
		var got []string
		for _, ev := range parseAll(fixtureLines(t, name)...) {
			got = append(got, string(ev.Type))
		}
		if g := strings.Join(got, " "); g != exp {
			t.Errorf("%s:\n got %s\nwant %s", name, g, exp)
		}
	}
}
