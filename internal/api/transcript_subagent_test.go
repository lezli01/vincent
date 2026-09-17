package api

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
)

// TestSubagentRecordsMatchTheirLiveChunks is task 066 decision 5 for task
// 109's records: over a real capture, every record the transcript route
// writes for a line is exactly what the live tail publishes for it, so a
// client renders both through one path and a subagent does not change shape
// when the step finishes.
func TestSubagentRecordsMatchTheirLiveChunks(t *testing.T) {
	for _, name := range []string{
		"stream_subagent_async_2.1.268.jsonl",
		"stream_subagent_sync_2.1.263.jsonl",
		"stream_subagent_failed_2.1.268.jsonl",
	} {
		t.Run(name, func(t *testing.T) {
			lines := claudeFixture(t, name)
			var buf bytes.Buffer
			adapter := claude.New(func() string { return "" })
			if err := normalizeTranscript(&buf,
				strings.NewReader(strings.Join(lines, "\n")+"\n"), adapter.NewLineParser()); err != nil {
				t.Fatalf("normalizeTranscript: %v", err)
			}
			records := decodeRecords(t, buf.Bytes())

			live := adapter.NewLineParser()
			var chunks []map[string]any
			for _, line := range lines {
				for _, c := range agent.LiveChunks(live([]byte(line))) {
					chunks = append(chunks, roundTrip(t, map[string]any{"type": c.Type}, c.Payload))
				}
			}

			kinds := map[string]int{}
			var matched int
			for _, rec := range records {
				kind, _ := rec["type"].(string)
				// agent.raw is the one record LiveChunks does not own: a runner
				// publishes it itself, from agent.UnmodeledLine.
				if kind == "agent.raw" || (!strings.HasPrefix(kind, "agent.subagent_") && rec["parent_call_id"] == nil) {
					continue
				}
				kinds[kind]++
				for len(chunks) > 0 && !reflect.DeepEqual(chunks[0], rec) {
					chunks = chunks[1:]
				}
				if len(chunks) == 0 {
					t.Fatalf("record %v has no matching live chunk", rec)
				}
				chunks = chunks[1:]
				matched++
			}
			if kinds["agent.subagent_started"] == 0 || kinds["agent.subagent_finished"] == 0 || matched == 0 {
				t.Errorf("records by type = %v, want starts and finishes", kinds)
			}
		})
	}
}

// TestNormalizeSubagentRecordNames pins the wire names of the three records
// and the background launch outcome.
func TestNormalizeSubagentRecordNames(t *testing.T) {
	lines := claudeFixture(t, "stream_subagent_async_2.1.268.jsonl")
	var buf bytes.Buffer
	if err := normalizeTranscript(&buf, strings.NewReader(strings.Join(lines, "\n")+"\n"),
		claude.New(func() string { return "" }).NewLineParser()); err != nil {
		t.Fatalf("normalizeTranscript: %v", err)
	}
	out := buf.String()
	for _, want := range []string{
		`"type":"agent.subagent_started"`,
		`"description":"Verify 022 gate walkthrough claims","subagent_type":"general-purpose","background":true`,
		`"type":"agent.subagent_progress"`,
		`"last_tool":"Bash"`,
		`"type":"agent.subagent_finished"`,
		`"duration_ms":300797`,
		`"status":"completed","summary":"Done.`,
		`"tool_uses":30,"total_tokens":110215`,
		`"call_id":"toolu_01VxaW1YHEhXY4R1c7fvbXLF"`,
		`"results":[{"call_id":"toolu_01VxaW1YHEhXY4R1c7fvbXLF","verb":"started in background"}]`,
		`"parent_call_id":"toolu_015ULCiXx2FP8kNzrbkiLFjU"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("normalized transcript is missing %s", want)
		}
	}
}

// TestNormalizeOmitsSubagentKeys: an adapter that reports no subagents emits
// none of task 109's keys.
func TestNormalizeOmitsSubagentKeys(t *testing.T) {
	lines := []string{
		`{"type":"thread.started","thread_id":"th_1"}`,
		`{"type":"item.completed","item":{"id":"msg_1","type":"agent_message","text":"done"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10,"output_tokens":2}}`,
	}
	var buf bytes.Buffer
	if err := normalizeTranscript(&buf, strings.NewReader(strings.Join(lines, "\n")+"\n"),
		codex.New(func() string { return "" }).NewLineParser()); err != nil {
		t.Fatalf("normalizeTranscript: %v", err)
	}
	for _, key := range []string{
		`"parent_call_id"`, `"description"`, `"subagent_type"`, `"background"`, `"status"`,
		`"summary"`, `"tool_uses"`, `"total_tokens"`, `"last_tool"`, "agent.subagent_",
	} {
		if strings.Contains(buf.String(), key) {
			t.Errorf("codex transcript carries %s:\n%s", key, buf.String())
		}
	}
}

func claudeFixture(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "agent", "claude", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

func decodeRecords(t *testing.T, data []byte) []map[string]any {
	t.Helper()
	var out []map[string]any
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var rec map[string]any
		if err := json.Unmarshal(sc.Bytes(), &rec); err != nil {
			t.Fatalf("decode record: %v", err)
		}
		out = append(out, rec)
	}
	return out
}

// roundTrip re-decodes a chunk through JSON so its numbers compare equal to
// a decoded record's.
func roundTrip(t *testing.T, head, payload map[string]any) map[string]any {
	t.Helper()
	merged := map[string]any{}
	for k, v := range payload {
		merged[k] = v
	}
	for k, v := range head {
		merged[k] = v
	}
	data, err := json.Marshal(merged)
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode chunk: %v", err)
	}
	return out
}
