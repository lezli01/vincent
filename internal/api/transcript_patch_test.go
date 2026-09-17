package api

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
)

const editFixture = "stream_edit_2.1.268.jsonl"

// normalizeClaude runs claude lines through the transcript route's
// normalizer and returns the records it writes.
func normalizeClaude(t *testing.T, lines []string) (string, []map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	parser := claude.New(func() string { return "" }).NewLineParser()
	if err := normalizeTranscript(&buf, strings.NewReader(strings.Join(lines, "\n")+"\n"), parser); err != nil {
		t.Fatalf("normalizeTranscript: %v", err)
	}
	return buf.String(), decodeRecords(t, buf.Bytes())
}

// TestPatchRecordsMatchTheirLiveChunks is task 066 decision 5 for task 110's
// record: over the recorded edits, every outcome and patch the transcript
// route writes is exactly what the live tail publishes, in the same order.
func TestPatchRecordsMatchTheirLiveChunks(t *testing.T) {
	lines := claudeFixture(t, editFixture)
	_, records := normalizeClaude(t, lines)

	live := claude.New(func() string { return "" }).NewLineParser()
	var chunks []map[string]any
	for _, line := range lines {
		for _, c := range agent.LiveChunks(live([]byte(line))) {
			chunks = append(chunks, roundTrip(t, map[string]any{"type": c.Type}, c.Payload))
		}
	}

	var got []map[string]any
	patches := 0
	for _, rec := range records {
		switch rec["type"] {
		case "agent.tool_result", "agent.patch":
			got = append(got, rec)
			if rec["type"] == "agent.patch" {
				patches++
			}
		}
	}
	var want []map[string]any
	for _, c := range chunks {
		if c["type"] == "agent.tool_result" || c["type"] == "agent.patch" {
			want = append(want, c)
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("records =\n%v\nlive chunks =\n%v", got, want)
	}
	// Two single-file edits, a replace_all and an overwrite; the create, the
	// failure and the subagent's edit carry none.
	if patches != 4 {
		t.Errorf("agent.patch records = %d, want 4", patches)
	}
}

// TestNormalizePatchRecord pins the wire shape: one `user` line yields the
// outcome and then the patch, and a result with no patch writes no
// agent.patch record and no `patch` key.
func TestNormalizePatchRecord(t *testing.T) {
	lines := claudeFixture(t, editFixture)

	out, records := normalizeClaude(t, lines[7:8])
	if len(records) != 2 {
		t.Fatalf("records = %d, want the outcome and its patch:\n%s", len(records), out)
	}
	if records[0]["type"] != "agent.tool_result" || records[1]["type"] != "agent.patch" {
		t.Errorf("types = %v, %v; want agent.tool_result then agent.patch", records[0]["type"], records[1]["type"])
	}
	for _, s := range []string{
		`"results":[{"call_id":"toolu_016f23nV6rrnjD4KUjusuej3","summary":"+1 −1","verb":"updated"}]`,
		`{"type":"agent.patch","patch":"@@ -1,1 +1,1 @@\n-feat(trigger): finish event triggers`,
		`GitHub sources, reactions and HTTP ingress","call_id":"toolu_016f23nV6rrnjD4KUjusuej3"}`,
	} {
		if !strings.Contains(out, s) {
			t.Errorf("normalized transcript is missing %s:\n%s", s, out)
		}
	}

	out, records = normalizeClaude(t, lines[9:10])
	if len(records) != 1 || strings.Contains(out, "agent.patch") || strings.Contains(out, `"patch"`) {
		t.Errorf("a create wrote a patch:\n%s", out)
	}
}
