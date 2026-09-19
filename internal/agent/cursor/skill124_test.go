package cursor

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// skillSlashFixture was recorded on 2026-09-19 against cursor-agent
// 2026.09.18-9a7762b (task 124.2) with vincent's full-auto argv,
//
//	cursor-agent -p --output-format stream-json --trust --force --model auto
//
// and `/echo-probe zebra` piped to stdin, in a repository whose
// .claude/skills/echo-probe/SKILL.md reads:
//
//	---
//	name: echo-probe
//	description: Echo probe for vincent fixture capture. Use when asked to run the echo probe.
//	---
//	Reply with exactly the word ECHO-PROBE followed by the arguments you were given: $ARGUMENTS
//
// The skill ran — the reply is `ECHO-PROBE zebra` — and nothing in the stream
// says so. The repository path is rewritten to C:\work\repo and the session id
// to SESSION; every other byte is as captured.
const skillSlashFixture = "skill_slash_2026.09.18.jsonl"

// TestUserLineIsTheInputEcho: every capture's one `user` line is cursor's echo
// of the piped prompt, and nothing else is.
func TestUserLineIsTheInputEcho(t *testing.T) {
	names, err := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range names {
		name := filepath.Base(path)
		var echoes int
		for i, ev := range parseFixture(t, name) {
			isUser := strings.HasPrefix(string(ev.Raw), `{"type":"user"`)
			if isUser != (ev.Type == agent.EventInputEcho) {
				t.Errorf("%s line %d: %q from %.40s", name, i, ev.Type, ev.Raw)
			}
			if ev.Type == agent.EventInputEcho {
				echoes++
			}
		}
		if echoes != 1 {
			t.Errorf("%s: %d echoes, want the one prompt", name, echoes)
		}
	}
	events := parseFixture(t, skillSlashFixture)
	var text struct {
		Message struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal(events[1].Raw, &text); err != nil || len(text.Message.Content) != 1 ||
		text.Message.Content[0].Text != "/echo-probe zebra" {
		t.Errorf("echo line = %s, want the piped prompt verbatim", events[1].Raw)
	}
}

// TestNoSkillEvents states cursor's side of task 124.2 positively: a turn that
// ran a skill produces no EventSkill, because the stream never says one ran.
func TestNoSkillEvents(t *testing.T) {
	names, err := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range names {
		for i, ev := range parseFixture(t, filepath.Base(path)) {
			if ev.Type == agent.EventSkill || ev.Skill != nil {
				t.Errorf("%s line %d: produced a skill event", filepath.Base(path), i)
			}
		}
	}
	events := parseFixture(t, skillSlashFixture)
	if last := events[len(events)-2]; last.Type != agent.EventOutput || last.Text != "ECHO-PROBE zebra" {
		t.Errorf("reply = %q %q, want the skill's output", last.Type, last.Text)
	}
}

// TestFixtureEventTypesArePinned holds every capture to its per-line event
// types. The captures that predate task 124.2 differ from what they parsed to
// before only in their `user` line, which was unknown and is now the echo.
func TestFixtureEventTypesArePinned(t *testing.T) {
	want := map[string]string{
		"resume_2026.08.11.jsonl": "run_header input_echo unknown unknown unknown unknown thinking " +
			"unknown unknown unknown unknown unknown thinking result",
		"resume_unknown_2026.08.11.jsonl": "run_header input_echo unknown unknown unknown unknown thinking output result",
		skillSlashFixture: "run_header input_echo unknown unknown unknown unknown unknown unknown thinking " +
			"output result",
		"success_2026.08.04.jsonl": "run_header input_echo unknown unknown unknown thinking output result",
		"success_2026.08.25.jsonl": "run_header input_echo unknown unknown unknown thinking output result",
		"tools_2026.08.11.jsonl": "run_header input_echo unknown unknown unknown thinking output tool_use tool_use " +
			"tool_result tool_result unknown unknown unknown thinking output result",
		"tools_2026.08.25.jsonl": "run_header input_echo unknown unknown unknown unknown unknown thinking output " +
			"tool_use tool_use tool_result tool_result unknown unknown thinking output result",
	}
	names, err := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
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
		for _, ev := range parseFixture(t, name) {
			got = append(got, string(ev.Type))
		}
		if g := strings.Join(got, " "); g != exp {
			t.Errorf("%s:\n got %s\nwant %s", name, g, exp)
		}
	}
}
