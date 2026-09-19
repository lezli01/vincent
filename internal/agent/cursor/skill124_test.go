package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

// skill_slash_2026.09.18.jsonl was captured from cursor-agent
// 2026.09.18-9a7762b on 2026-09-19 (task 124.2), with vincent's own argv
// (buildArgs: `-p --output-format stream-json --trust --force --model auto`)
// and `/echo-probe zebra` on stdin, in a throwaway repository whose
// .cursor/skills/echo-probe/SKILL.md says to reply `ECHO-PROBE-OK
// $ARGUMENTS`. It is scrubbed as the task 108 captures are: `session_id`
// to SESSION, `request_id` to REQ, the repository to /tmp/wt.
//
// The model answered as the skill says, and the stream reports nothing but
// the echo of the turn: cursor has no skill line to read a load from (§9.7).
const skillSlashFixture = "skill_slash_2026.09.18.jsonl"

// TestUserLinesAreInputEchoes: every captured `user` line is cursor's echo
// of the prompt vincent wrote to its stdin, and normalizes as one (task 124).
func TestUserLinesAreInputEchoes(t *testing.T) {
	names, err := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
	if err != nil || len(names) == 0 {
		t.Fatalf("fixtures: %v, %v", names, err)
	}
	var sawSlash bool
	for _, path := range names {
		name := filepath.Base(path)
		sawSlash = sawSlash || name == skillSlashFixture
		lines := fixtureLineTypes(t, name)
		events := parseFixture(t, name)
		var echoes int
		for i, ev := range events {
			if lines[i] == "user" {
				echoes++
				if ev.Type != agent.EventInputEcho {
					t.Errorf("%s line %d: user line = %s, want input_echo", name, i, ev.Type)
				}
				if len(ev.Raw) == 0 {
					t.Errorf("%s line %d: the echo lost its raw line", name, i)
				}
			}
		}
		if echoes != 1 {
			t.Errorf("%s: %d user lines, want the one echo every turn has", name, echoes)
		}
	}
	if !sawSlash {
		t.Errorf("%s is missing from testdata", skillSlashFixture)
	}
}

// TestNoSkill is cursor's half of §9.1's "only claude reports a skill load":
// no fixture — the one that named a skill included — yields one.
func TestNoSkill(t *testing.T) {
	names, err := filepath.Glob(filepath.Join("testdata", "*.jsonl"))
	if err != nil || len(names) == 0 {
		t.Fatalf("fixtures: %v, %v", names, err)
	}
	for _, path := range names {
		name := filepath.Base(path)
		for i, ev := range parseFixture(t, name) {
			if ev.Type == agent.EventSkill || ev.Skill != nil {
				t.Errorf("%s line %d: produced a skill load", name, i)
			}
		}
	}
}

// TestOldFixturesParseAsBefore is 124.2's promise to every earlier capture:
// each line classifies as it did before, except the echoed `user` line,
// which is the one line this item claims.
func TestOldFixturesParseAsBefore(t *testing.T) {
	for name, before := range oldFixtureTypes {
		want := strings.Fields(before)
		lines := fixtureLineTypes(t, name)
		events := parseFixture(t, name)
		if len(events) != len(want) {
			t.Fatalf("%s: %d events, want %d", name, len(events), len(want))
		}
		for i, ev := range events {
			got := string(ev.Type)
			if lines[i] == "user" && want[i] == string(agent.EventUnknown) {
				if ev.Type != agent.EventInputEcho {
					t.Errorf("%s line %d: user line = %s, want input_echo", name, i, got)
				}
				continue
			}
			if got != want[i] {
				t.Errorf("%s line %d: %s, want %s as before", name, i, got, want[i])
			}
		}
	}
}

// fixtureLineTypes reads each non-blank fixture line's `type`, in order.
func fixtureLineTypes(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) == "" {
			continue
		}
		var line struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(l), &line); err != nil {
			t.Fatalf("decode fixture line: %v", err)
		}
		out = append(out, line.Type)
	}
	return out
}

// oldFixtureTypes is every pre-existing cursor capture's classification, line
// by line, as the parser at 95783734 — the commit before task 124.2 —
// produced it.
var oldFixtureTypes = map[string]string{
	"resume_2026.08.11.jsonl":         "run_header unknown unknown unknown unknown unknown thinking unknown unknown unknown unknown unknown thinking result",
	"resume_unknown_2026.08.11.jsonl": "run_header unknown unknown unknown unknown unknown thinking output result",
	"success_2026.08.04.jsonl":        "run_header unknown unknown unknown unknown thinking output result",
	"success_2026.08.25.jsonl":        "run_header unknown unknown unknown unknown thinking output result",
	"tools_2026.08.11.jsonl":          "run_header unknown unknown unknown unknown thinking output tool_use tool_use tool_result tool_result unknown unknown unknown thinking output result",
	"tools_2026.08.25.jsonl":          "run_header unknown unknown unknown unknown unknown unknown thinking output tool_use tool_use tool_result tool_result unknown unknown thinking output result",
}
