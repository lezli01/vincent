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

// The task 124.2 captures: a skill the model loaded, and a cursor turn that
// named one.
const (
	skillModelFixture = "stream_skill_model_2.1.277.jsonl"
	skillSlashFixture = "skill_slash_2026.09.18.jsonl"
)

// TestSkillRecordsMatchTheirLiveChunks is task 066 decision 5 for task 124's
// record: over the recorded load, the agent.skill the transcript route writes
// is exactly what the live tail publishes.
func TestSkillRecordsMatchTheirLiveChunks(t *testing.T) {
	lines := claudeFixture(t, skillModelFixture)
	_, records := normalizeClaude(t, lines)

	live := claude.New(func() string { return "" }).NewLineParser()
	var want []map[string]any
	for _, line := range lines {
		for _, c := range agent.LiveChunks(live([]byte(line))) {
			if c.Type == "agent.skill" {
				want = append(want, roundTrip(t, map[string]any{"type": c.Type}, c.Payload))
			}
		}
	}
	var got []map[string]any
	for _, rec := range records {
		if rec["type"] == "agent.skill" {
			got = append(got, rec)
		}
	}
	if len(got) != 1 {
		t.Fatalf("agent.skill records = %d, want the one load", len(got))
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("records =\n%v\nlive chunks =\n%v", got, want)
	}
}

// TestNormalizeSkillRecord pins the wire shape: the load names the skill as
// the CLI resolved it, its arguments and its call, omits every key it leaves
// unset, and never carries the body — which is on no record at all, the raw
// line in the file being where it stays (T4.16).
func TestNormalizeSkillRecord(t *testing.T) {
	out, records := normalizeClaude(t, claudeFixture(t, skillModelFixture))
	var skill map[string]any
	for _, rec := range records {
		if rec["type"] == "agent.skill" {
			skill = rec
		}
	}
	want := map[string]any{
		"type": "agent.skill", "name": "echo-probe", "args": "zebra", "by": "agent",
		"call_id": "toolu_01VLDUuW8jNbLzu8PtaJ6WqS",
	}
	if !reflect.DeepEqual(skill, want) {
		t.Errorf("agent.skill = %v, want %v", skill, want)
	}
	if strings.Contains(out, "Base directory for this skill") {
		t.Errorf("the skill's body reached a normalized record:\n%s", out)
	}
	// The call reads as the skill it loads (T4.14).
	if !strings.Contains(out, `"tools":[{"name":"Skill","summary":"echo-probe","call_id":"toolu_01VLDUuW8jNbLzu8PtaJ6WqS"}]`) {
		t.Errorf("the Skill call has no summary:\n%s", out)
	}

	// Forked and Error are defined before anything sets them; unset, they
	// are absent rather than false and empty.
	for _, key := range []string{"forked", "error", "parent_call_id"} {
		if _, ok := skill[key]; ok {
			t.Errorf("agent.skill carries %q unset", key)
		}
	}
	full := normalizedEvent(agent.Event{
		Type: agent.EventSkill, ParentCallID: "toolu_sub",
		Skill: &agent.SkillInvocation{Name: "fork-probe", By: "human", Forked: true, Error: "refused"},
	}, nil)
	b, err := json.Marshal(full[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := string(b),
		`{"type":"agent.skill","parent_call_id":"toolu_sub","name":"fork-probe","by":"human","forked":true,"error":"refused"}`; got != want {
		t.Errorf("full record =\n%s\nwant\n%s", got, want)
	}
}

// TestNormalizeInputEchoRecord: cursor's echo of the prompt is a record
// carrying only its type — and parent_call_id, were one set — rather than an
// agent.raw holding the prompt a second time (task 124 decision 24).
func TestNormalizeInputEchoRecord(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "agent", "cursor", "testdata", skillSlashFixture))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var buf bytes.Buffer
	if err := normalizeTranscript(&buf, bytes.NewReader(data), cursor.New(func() string { return "" }).NewLineParser()); err != nil {
		t.Fatalf("normalizeTranscript: %v", err)
	}
	var echoes int
	for _, rec := range decodeRecords(t, buf.Bytes()) {
		if rec["type"] == "agent.raw" && strings.Contains(rec["line"].(string), `"type":"user"`) {
			t.Errorf("the echo is still agent.raw: %v", rec)
		}
		if rec["type"] != "agent.input_echo" {
			continue
		}
		echoes++
		if !reflect.DeepEqual(rec, map[string]any{"type": "agent.input_echo"}) {
			t.Errorf("agent.input_echo = %v, want only its type", rec)
		}
	}
	if echoes != 1 {
		t.Errorf("agent.input_echo records = %d, want 1:\n%s", echoes, buf.String())
	}
	nested := normalizedEvent(agent.Event{Type: agent.EventInputEcho, ParentCallID: "toolu_sub"}, nil)
	b, err := json.Marshal(nested[0])
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got, want := string(b), `{"type":"agent.input_echo","parent_call_id":"toolu_sub"}`; got != want {
		t.Errorf("nested echo = %s, want %s", got, want)
	}
}
