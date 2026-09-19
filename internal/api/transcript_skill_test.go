package api

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/cursor"
)

// TestSkillRecordsMatchTheirLiveChunks is task 066 decision 5 for task
// 124.2's record: over the real captures, every agent.skill the transcript
// route writes is exactly the chunk the live tail publishes for its line.
func TestSkillRecordsMatchTheirLiveChunks(t *testing.T) {
	for _, name := range []string{
		"stream_skill_model_2.1.277.jsonl",
		"stream_skill_permission_2.1.277.jsonl",
		"stream_skill_fork_2.1.277.jsonl",
	} {
		t.Run(name, func(t *testing.T) {
			lines := claudeFixture(t, name)
			adapter := claude.New(func() string { return "" })
			records := normalizedFixture(t, lines, adapter.NewLineParser())

			live := adapter.NewLineParser()
			var chunks []map[string]any
			for _, line := range lines {
				for _, c := range agent.LiveChunks(live([]byte(line))) {
					if c.Type == "agent.skill" {
						chunks = append(chunks, roundTrip(t, map[string]any{"type": c.Type}, c.Payload))
					}
				}
			}
			var skills []map[string]any
			for _, rec := range records {
				if rec["type"] == "agent.skill" {
					skills = append(skills, rec)
				}
			}
			if len(skills) != 1 || !reflect.DeepEqual(skills, chunks) {
				t.Errorf("records %v, chunks %v: want the one load, identical", skills, chunks)
			}
		})
	}
}

// TestNormalizeSkillRecord pins agent.skill's wire names over the captures,
// and that no record the route writes carries a skill's rendered body.
func TestNormalizeSkillRecord(t *testing.T) {
	for _, tc := range []struct {
		name string
		want string
	}{
		{
			"stream_skill_model_2.1.277.jsonl",
			`{"type":"agent.skill","call_id":"toolu_01DiBcVgHar46Gv7ikcAQUkW","name":"echo-probe","args":"zebra","by":"agent"}`,
		},
		{
			"stream_skill_fork_2.1.277.jsonl",
			`{"type":"agent.skill","name":"fork-probe","by":"human","forked":true}`,
		},
	} {
		lines := claudeFixture(t, tc.name)
		var buf bytes.Buffer
		if err := normalizeTranscript(&buf, strings.NewReader(strings.Join(lines, "\n")+"\n"),
			claude.New(func() string { return "" }).NewLineParser()); err != nil {
			t.Fatalf("normalizeTranscript: %v", err)
		}
		out := buf.String()
		if !strings.Contains(out, tc.want+"\n") {
			t.Errorf("%s: normalized transcript is missing\n%s\ngot\n%s", tc.name, tc.want, out)
		}
		// The body line was the one raw record a skill load used to leave;
		// the fork capture's task_notification is still one, and carries no
		// body either.
		if strings.Contains(out, "Base directory for this skill") {
			t.Errorf("%s: a record carries the rendered SKILL.md", tc.name)
		}
	}
}

// TestNormalizeInputEcho: cursor's echo is an agent.input_echo record carrying
// nothing but its type (task 124 decision 25), and a turn's transcript holds
// no agent.raw for it.
func TestNormalizeInputEcho(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "agent", "cursor", "testdata", "skill_slash_2026.09.18.jsonl"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(string(data), "\r\n", "\n")), "\n")
	records := normalizedFixture(t, lines, cursor.New(func() string { return "" }).NewLineParser())
	var echoes int
	for _, rec := range records {
		switch rec["type"] {
		case "agent.input_echo":
			echoes++
			if len(rec) != 1 {
				t.Errorf("echo record = %v, want the type alone", rec)
			}
		case "agent.raw":
			if strings.Contains(rec["line"].(string), `"type":"user"`) {
				t.Errorf("the echo is still raw: %v", rec)
			}
		}
	}
	if echoes != 1 {
		t.Errorf("echo records = %d, want 1", echoes)
	}

	// Inside a subagent, parent_call_id rides on it as on every record.
	out := normalizedEvent(agent.Event{Type: agent.EventInputEcho, ParentCallID: "toolu_0"}, nil)
	b, err := json.Marshal(out[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := string(b); got != `{"type":"agent.input_echo","parent_call_id":"toolu_0"}` {
		t.Errorf("echo record = %s", got)
	}
}

// TestNormalizeSkillRecordFields maps every SkillInvocation field through a
// constructed event (task 124 decision 24): 124.2's parsers fill only some,
// and the wire names must exist before the rest do.
func TestNormalizeSkillRecordFields(t *testing.T) {
	out := normalizedEvent(agent.Event{Type: agent.EventSkill, ParentCallID: "toolu_0", Skill: &agent.SkillInvocation{
		Name: "plugin:skill", Args: "a b", By: "human", CallID: "toolu_1", Forked: true, Error: "refused",
	}}, nil)
	b, err := json.Marshal(out[0])
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"agent.skill","parent_call_id":"toolu_0","call_id":"toolu_1","name":"plugin:skill",` +
		`"args":"a b","by":"human","forked":true,"error":"refused"}`
	if got := string(b); got != want {
		t.Errorf("record =\n%s\nwant\n%s", got, want)
	}
	if chunks := agent.LiveChunks(agent.Event{Type: agent.EventSkill}); chunks != nil {
		t.Errorf("an empty skill event published %v", chunks)
	}
}

// normalizedFixture runs lines through the transcript route's normalizer.
func normalizedFixture(t *testing.T, lines []string, parse agent.LineParser) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	if err := normalizeTranscript(&buf, strings.NewReader(strings.Join(lines, "\n")+"\n"), parse); err != nil {
		t.Fatalf("normalizeTranscript: %v", err)
	}
	return decodeRecords(t, buf.Bytes())
}
