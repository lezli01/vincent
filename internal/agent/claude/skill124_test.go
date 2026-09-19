package claude

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// The skill captures (task 124.2) were taken with claude 2.1.277 on
// 2026-09-19, with vincent's own input-mode argv (buildArgs: `-p
// --output-format stream-json --verbose --input-format stream-json
// --permission-prompt-tool stdio --model haiku`) in a throwaway repository
// holding two probe skills under .claude/skills:
//
//   - echo-probe, whose body is "Reply with exactly one line:
//     `ECHO-PROBE-OK $ARGUMENTS`", loaded by the model under
//     `--dangerously-skip-permissions` when asked to load it with args
//     "zebra" (stream_skill_model_2.1.277.jsonl);
//   - bash-probe, carrying `allowed-tools: Bash(echo:*)`, loaded by the model
//     under `restricted`'s `--allowedTools`, which raises a `Skill`
//     permission request that was allowed
//     (stream_skill_permission_2.1.277.jsonl).
//
// Each is scrubbed: the hook and rate-limit lines are dropped, the init line
// is cut to its built-in tools and the two probe skills, the session id is a
// placeholder, and the repository path is /work/repo — including inside the
// synthetic body's "Base directory for this skill:". Everything else is
// verbatim.
const (
	skillModelFixture      = "stream_skill_model_2.1.277.jsonl"
	skillPermissionFixture = "stream_skill_permission_2.1.277.jsonl"
)

// The model capture's lines, 0-based: the Skill call, its result, the body.
const (
	skillCallLine   = 4
	skillResultLine = 5
	skillBodyLine   = 6
	skillReplyLine  = 8
	skillCallID     = "toolu_01VLDUuW8jNbLzu8PtaJ6WqS"
)

// fixtureLines reads a capture's non-blank lines.
func fixtureLines(t *testing.T, name string) [][]byte {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer func() { _ = f.Close() }()
	var out [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxLineBytes)
	for sc.Scan() {
		if strings.TrimSpace(sc.Text()) != "" {
			out = append(out, append([]byte(nil), sc.Bytes()...))
		}
	}
	if err := sc.Err(); err != nil {
		t.Fatalf("scan fixture: %v", err)
	}
	return out
}

// editLine decodes a stream line, lets edit change it, and re-encodes it.
func editLine(t *testing.T, raw []byte, edit func(map[string]any)) []byte {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("decode line: %v", err)
	}
	edit(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("encode line: %v", err)
	}
	return out
}

// parseAll runs lines through one parser, in order, as a transcript read does.
func parseAll(lines [][]byte) []agent.Event {
	p := new(streamParser)
	out := make([]agent.Event, 0, len(lines))
	for _, l := range lines {
		out = append(out, p.parse(l))
	}
	return out
}

// TestModelLoadedSkill is decision 25's mapping, table-driven over the model
// capture: which lines between a `Skill` result and a synthetic body still
// let the body be attributed to the load, and which do not.
func TestModelLoadedSkill(t *testing.T) {
	lines := fixtureLines(t, skillModelFixture)
	call, result, body, reply := lines[skillCallLine], lines[skillResultLine], lines[skillBodyLine], lines[skillReplyLine]
	asSubagent := func(raw []byte) []byte {
		return editLine(t, raw, func(m map[string]any) { m["parent_tool_use_id"] = "toolu_sub" })
	}
	failed := editLine(t, result, func(m map[string]any) {
		m["message"].(map[string]any)["content"].([]any)[0].(map[string]any)["is_error"] = true
	})
	twoResults := editLine(t, result, func(m map[string]any) {
		content := m["message"].(map[string]any)["content"].([]any)
		m["message"].(map[string]any)["content"] = append(content, map[string]any{
			"type": "tool_result", "tool_use_id": "toolu_other", "content": "ok",
		})
	})
	// A line with text beside the call normalizes as output, and its call
	// still counts.
	withText := editLine(t, call, func(m map[string]any) {
		msg := m["message"].(map[string]any)
		msg["content"] = append([]any{map[string]any{"type": "text", "text": "Loading it."}}, msg["content"].([]any)...)
	})
	want := &agent.SkillInvocation{Name: "echo-probe", Args: "zebra", By: "agent", CallID: skillCallID}
	for _, tc := range []struct {
		name  string
		lines [][]byte
		want  *agent.SkillInvocation // nil: the body stays unknown
	}{
		{"the capture as recorded", lines[:skillBodyLine+1], want},
		{"a body with no Skill result before it", [][]byte{body}, nil},
		{"a body with only the call before it", [][]byte{call, body}, nil},
		{"a line of the same parent in between", [][]byte{call, result, reply, body}, nil},
		{"a subagent's line in between", [][]byte{call, result, asSubagent(reply), body}, want},
		{"a subagent's own load", [][]byte{asSubagent(call), asSubagent(result), asSubagent(body)}, want},
		{"a call beside text", [][]byte{withText, result, body}, want},
		{"a failed Skill result", [][]byte{call, failed, body}, nil},
		{"a result line with two results", [][]byte{call, twoResults, body}, nil},
		{"a range opened after the call", [][]byte{result, body}, &agent.SkillInvocation{
			Name: "echo-probe", By: "agent", CallID: skillCallID,
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			events := parseAll(tc.lines)
			last := events[len(events)-1]
			if tc.want == nil {
				if last.Type != agent.EventUnknown || last.Skill != nil {
					t.Fatalf("body = %s %+v, want unknown", last.Type, last.Skill)
				}
				return
			}
			if last.Type != agent.EventSkill || last.Skill == nil {
				t.Fatalf("body = %s, want skill", last.Type)
			}
			if *last.Skill != *tc.want {
				t.Errorf("skill = %+v, want %+v", *last.Skill, *tc.want)
			}
			// The Skill result itself stays the tool result it always was.
			for _, ev := range events[:len(events)-1] {
				if ev.Type == agent.EventSkill {
					t.Errorf("a line before the body produced a skill: %+v", ev)
				}
			}
		})
	}
}

// TestSkillLoadedOnceAndStillAToolCall pins the rest of the capture: the
// `Skill` call reads as the skill it loads (T4.14), its result is an ordinary
// tool result, the load is reported exactly once, and nothing after it — the
// reply included — is taken for a second one.
func TestSkillLoadedOnceAndStillAToolCall(t *testing.T) {
	events := parseAll(fixtureLines(t, skillModelFixture))
	callEv := events[skillCallLine]
	if callEv.Type != agent.EventToolUse || len(callEv.Tools) != 1 {
		t.Fatalf("call = %+v, want one tool use", callEv)
	}
	if tool := callEv.Tools[0]; tool.Name != "Skill" || tool.Summary != "echo-probe" || tool.CallID != skillCallID {
		t.Errorf("tool = %+v, want Skill echo-probe", tool)
	}
	resultEv := events[skillResultLine]
	if resultEv.Type != agent.EventToolResult || len(resultEv.Results) != 1 ||
		resultEv.Results[0].CallID != skillCallID || resultEv.Results[0].IsError {
		t.Errorf("result = %+v, want the call's successful tool result", resultEv)
	}
	var loads int
	for _, ev := range events {
		if ev.Type == agent.EventSkill {
			loads++
		}
	}
	if loads != 1 {
		t.Errorf("skill events = %d, want exactly one per load", loads)
	}
}

// TestSkillBodyIsNeverNormalized is T4.16 for a skill: the rendered SKILL.md
// reaches no normalized field, only the raw line the transcript writes.
func TestSkillBodyIsNeverNormalized(t *testing.T) {
	for _, name := range []string{skillModelFixture, skillPermissionFixture} {
		for i, ev := range parseAll(fixtureLines(t, name)) {
			ev.Raw = nil
			b, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("encode event: %v", err)
			}
			for _, body := range []string{"Base directory for this skill", "# Echo probe", "# Bash probe"} {
				if strings.Contains(string(b), body) {
					t.Errorf("%s line %d: %q reached a normalized field: %s", name, i, body, b)
				}
			}
		}
	}
}

// TestSkillArgsAreOneCappedLine: Args is a line, capped where a tool summary
// is, so a pathological argument cannot inflate the record.
func TestSkillArgsAreOneCappedLine(t *testing.T) {
	lines := fixtureLines(t, skillModelFixture)
	long := "first line\nsecond " + strings.Repeat("x", 2*agent.ToolSummaryMax)
	call := editLine(t, lines[skillCallLine], func(m map[string]any) {
		m["message"].(map[string]any)["content"].([]any)[0].(map[string]any)["input"] = map[string]any{
			"skill": "echo-probe", "args": long,
		}
	})
	events := parseAll([][]byte{call, lines[skillResultLine], lines[skillBodyLine]})
	got := events[2].Skill
	if got == nil {
		t.Fatalf("body = %s, want skill", events[2].Type)
	}
	if want := agent.OneLine(long, agent.ToolSummaryMax); got.Args != want {
		t.Errorf("args = %q, want %q", got.Args, want)
	}
}

// TestSkillPermissionFixture is the §7.4 half (task 124): a `Skill`
// permission request names the skill rather than quoting its frontmatter
// description, and the load it allowed still normalizes as one, through the
// live path that answers the control line itself.
func TestSkillPermissionFixture(t *testing.T) {
	events, _ := fixtureEvents(t, skillPermissionFixture)
	reqs := inputRequests(events)
	if len(reqs) != 1 || reqs[0].Request == nil || reqs[0].Request.Permission == nil {
		t.Fatalf("input requests = %+v, want one permission request", reqs)
	}
	if perm := reqs[0].Request.Permission; perm.Tool != "Skill" || perm.Summary != "bash-probe" {
		t.Errorf("permission = %+v, want Skill/bash-probe", perm)
	}
	var loads []agent.SkillInvocation
	for _, ev := range events {
		if ev.Type == agent.EventSkill {
			loads = append(loads, *ev.Skill)
		}
	}
	want := []agent.SkillInvocation{{Name: "bash-probe", By: "agent", CallID: "toolu_012G3Vpyt1D1t3kBSFpvD9Ld"}}
	if !slices.Equal(loads, want) {
		t.Errorf("loads = %+v, want %+v", loads, want)
	}
	// The transcript route re-reads the control line through the parser; it
	// must agree with the live path about the load (task 071 decision 1).
	var refetched []agent.SkillInvocation
	for _, ev := range parseAll(fixtureLines(t, skillPermissionFixture)) {
		if ev.Type == agent.EventSkill {
			refetched = append(refetched, *ev.Skill)
		}
	}
	if !slices.Equal(refetched, want) {
		t.Errorf("refetched loads = %+v, want %+v", refetched, want)
	}
}

// TestSkillPermissionSummaryFallback: a `Skill` request that names no skill
// falls back as every other request does.
func TestSkillPermissionSummaryFallback(t *testing.T) {
	for _, tc := range []struct {
		name, input, description, want string
	}{
		{"named", `{"skill":"bash-probe"}`, "a probe", "bash-probe"},
		{"no skill, a description", `{}`, "a probe", "a probe"},
		{"nothing", `{}`, "", "Skill"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := permissionSummary(&controlPayload{
				ToolName: "Skill", Input: json.RawMessage(tc.input), Description: tc.description,
			})
			if got != tc.want {
				t.Errorf("summary = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestOldFixturesParseAsBefore is 124.2's promise to every earlier capture:
// none of them holds a skill load, so every line classifies exactly as it did
// before the skill memory existed.
func TestOldFixturesParseAsBefore(t *testing.T) {
	for name, want := range oldFixtureTypes {
		var got []string
		for _, ev := range parseAll(fixtureLines(t, name)) {
			got = append(got, string(ev.Type))
		}
		if strings.Join(got, " ") != want {
			t.Errorf("%s:\n got %s\nwant %s", name, strings.Join(got, " "), want)
		}
	}
}

// oldFixtureTypes is every pre-existing claude capture's classification, line by
// line, as the parser at 95783734 — the commit before task 124.2 — produced it.
var oldFixtureTypes = map[string]string{
	"stream_edit_2.1.268.jsonl":             "tool_use tool_result tool_use tool_result tool_use tool_result tool_use tool_result tool_use tool_result tool_use tool_result tool_use tool_result",
	"stream_permission_allow_2.1.226.jsonl": "run_header thinking tool_use unknown tool_result thinking output result",
	"stream_permission_deny_2.1.226.jsonl":  "run_header thinking tool_use unknown tool_result thinking output result",
	"stream_question_2.1.226.jsonl":         "run_header thinking tool_use unknown tool_result thinking output result",
	"stream_subagent_async_2.1.268.jsonl":   "run_header tool_use unknown subagent_started tool_result thinking tool_use subagent_progress unknown tool_result tool_use unknown subagent_started tool_result tool_result tool_use subagent_progress thinking tool_use subagent_progress tool_result tool_use tool_result unknown unknown output subagent_finished output subagent_finished output result",
	"stream_subagent_failed_2.1.268.jsonl":  "tool_use subagent_started tool_result thinking tool_use subagent_finished",
	"stream_subagent_sync_2.1.263.jsonl":    "run_header thinking output tool_use subagent_started unknown subagent_progress tool_use subagent_progress tool_use tool_result tool_result unknown unknown unknown tool_result unknown subagent_finished tool_result output result",
}
