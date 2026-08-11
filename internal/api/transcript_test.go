package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/store"
)

// TestLineBoundaryAsEnd covers the rule that keeps a ranged read off the
// middle of a record: a transcript is appended to while it is read, so its
// size regularly lands inside a line being written (§13.2).
func TestLineBoundaryAsEnd(t *testing.T) {
	cases := []struct {
		name string
		data string
		want int64
	}{
		{"empty", "", 0},
		{"complete lines", "a\nbb\n", 5},
		{"trailing partial line", "a\nbb\n{\"type\":\"par", 5},
		{"no newline at all", "{\"type\":\"par", 0},
		{"only a newline", "\n", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := strings.NewReader(tc.data)
			got, err := lineBoundary(r, int64(len(tc.data)))
			if err != nil {
				t.Fatalf("lineBoundary: %v", err)
			}
			if got != tc.want {
				t.Errorf("lineBoundary(%q) = %d, want %d", tc.data, got, tc.want)
			}
		})
	}
}

// TestLineBoundaryAsTailStart covers the other use: a tail window opens at
// the start of the record its byte count lands in, so it never begins
// mid-record and never comes back empty because the last record was larger
// than the window.
func TestLineBoundaryAsTailStart(t *testing.T) {
	const data = "aaaa\nbbbb\ncccc\n"
	cases := []struct {
		name string
		pos  int64
		want int64
	}{
		{"zero stays at zero", 0, 0},
		{"before the file is zero", -10, 0},
		{"mid-record opens that record", 7, 5},
		{"exact record start stays put", 5, 5},
		{"inside the last record opens it", 14, 10},
		{"end of file is the end", 15, 15},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := lineBoundary(strings.NewReader(data), tc.pos)
			if err != nil {
				t.Fatalf("lineBoundary: %v", err)
			}
			if got != tc.want {
				t.Errorf("lineBoundary(%d) = %d, want %d", tc.pos, got, tc.want)
			}
		})
	}
}

// TestNormalizeTranscript proves the §13.2 mapping is lossless: agent lines
// become the §13.3 live-output shapes, vincent's own annotations pass through
// untouched, and a line the dialect does not recognize is surfaced as
// agent.raw rather than dropped.
func TestNormalizeTranscript(t *testing.T) {
	raw := strings.Join([]string{
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hello"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","id":"toolu_01",` +
			`"input":{"file_path":"internal/auth/token.go"}}]}}`,
		`{"type":"vincent.output","phase":"run","stream":"stdout","text":"building"}`,
		`{"type":"system","subtype":"init"}`,
		`{"type":"result","subtype":"success","result":"done","total_cost_usd":0.5,` +
			`"usage":{"input_tokens":10,"output_tokens":3}}`,
	}, "\n") + "\n"

	var out bytes.Buffer
	parser := claude.New(func() string { return "" }).NewLineParser()
	if err := normalizeTranscript(&out, strings.NewReader(raw), parser); err != nil {
		t.Fatalf("normalizeTranscript: %v", err)
	}

	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d normalized lines, want 5:\n%s", len(lines), out.String())
	}
	types := make([]string, len(lines))
	for i, line := range lines {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(line), &probe); err != nil {
			t.Fatalf("line %d is not JSON: %v", i, err)
		}
		types[i] = probe.Type
	}
	want := []string{"agent.output", "agent.tool_use", "vincent.output", "agent.raw", "agent.result"}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("line %d type = %q, want %q", i, types[i], want[i])
		}
	}
	if !strings.Contains(lines[0], `"text":"hello"`) {
		t.Errorf("output text lost: %s", lines[0])
	}
	// T4.14: the record carries the call's subject and its id, not just a
	// name — the wire shape a client renders "▸ Edit token.go" from.
	if !strings.Contains(lines[1],
		`"tools":[{"name":"Edit","summary":"internal/auth/token.go","call_id":"toolu_01"}]`) {
		t.Errorf("tool call not normalized whole: %s", lines[1])
	}
	// The vincent line passes through byte-for-byte, phase and stream intact.
	if !strings.Contains(lines[2], `"phase":"run"`) || !strings.Contains(lines[2], `"text":"building"`) {
		t.Errorf("vincent annotation not passed through: %s", lines[2])
	}
	// The unrecognized line survives verbatim inside agent.raw.
	if !strings.Contains(lines[3], `\"subtype\":\"init\"`) {
		t.Errorf("unknown line not preserved: %s", lines[3])
	}
	if !strings.Contains(lines[4], `"result_text":"done"`) ||
		!strings.Contains(lines[4], `"cost_usd":0.5`) ||
		!strings.Contains(lines[4], `"input_tokens":10`) {
		t.Errorf("result fields lost: %s", lines[4])
	}
}

// TestNormalizeTranscriptWithoutAgent covers a command or gate step: no
// adapter owns the file, so anything that is not vincent's own annotation is
// surfaced verbatim instead of guessed at.
func TestNormalizeTranscriptWithoutAgent(t *testing.T) {
	raw := `{"type":"vincent.command_started","phase":"run","command":"go test"}` + "\n" +
		`{"type":"assistant","message":{"content":[{"type":"text","text":"hi"}]}}` + "\n"
	var out bytes.Buffer
	if err := normalizeTranscript(&out, strings.NewReader(raw), nil); err != nil {
		t.Fatalf("normalizeTranscript: %v", err)
	}
	lines := strings.Split(strings.TrimSuffix(out.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2: %s", len(lines), out.String())
	}
	if !strings.Contains(lines[0], "vincent.command_started") {
		t.Errorf("vincent line not passed through: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"type":"agent.raw"`) {
		t.Errorf("agent line without a parser should be raw: %s", lines[1])
	}
}

// TestNormalizeTranscriptSkipsBlankLines guards the trailing-newline case: a
// range that ends on a line boundary must not emit an empty record.
func TestNormalizeTranscriptSkipsBlankLines(t *testing.T) {
	var out bytes.Buffer
	if err := normalizeTranscript(&out, strings.NewReader("\n\n"), nil); err != nil {
		t.Fatalf("normalizeTranscript: %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("blank lines produced records: %q", out.String())
	}
}

// TestStepRunResponseOverrides covers the two columns PR C added so a
// timeline could flag a hand-edited retry (§6) and nothing had read since.
func TestStepRunResponseOverrides(t *testing.T) {
	summary := snapshotSummary{stepTotal: 2, stepNames: []string{"plan", "implement"}}
	run := &store.StepRun{ID: 7, StepIndex: 1, StepID: "impl", StepType: "agent"}

	plain := toStepRunResponse(run, summary)
	if plain.PromptOverride || plain.RunOverride {
		t.Errorf("unedited attempt flagged as overridden: %+v", plain)
	}
	if plain.StepName != "implement" {
		t.Errorf("step_name = %q, want %q", plain.StepName, "implement")
	}

	run.PromptOverride = "a hand-written prompt"
	edited := toStepRunResponse(run, summary)
	if !edited.PromptOverride || edited.RunOverride {
		t.Errorf("prompt edit not flagged: %+v", edited)
	}

	// An index the snapshot does not cover (a snapshot that failed to parse)
	// renders an empty name rather than panicking.
	if got := toStepRunResponse(&store.StepRun{StepIndex: 9}, summary); got.StepName != "" {
		t.Errorf("out-of-range step name = %q, want empty", got.StepName)
	}
}

// TestTranscriptRangesAndFormats drives the endpoint over a real transcript
// written by a real run: the tail window, the normalized format, the
// line-aligned resume cursor, and the two parameter rejections.
func TestTranscriptRangesAndFormats(t *testing.T) {
	t.Setenv("FAKEAGENT_SCENARIO", "success")
	h := newTaskHarness(t, 0, true)
	created := h.createTask(t, map[string]any{"title": "Transcript ranges"})
	done := h.waitForState(t, created.ID, "done")
	if len(done.Steps) != 1 {
		t.Fatalf("steps = %d, want 1", len(done.Steps))
	}
	step := done.Steps[0]
	base := fmt.Sprintf("/v1/tasks/%d/steps/%d/transcript", created.ID, step.ID)

	// The detail response carries what a timeline renders without re-parsing
	// the snapshot: the step's own name and the task's step count.
	if done.StepTotal != 1 {
		t.Errorf("step_total = %d, want 1", done.StepTotal)
	}
	if step.StepName == "" {
		t.Error("step_name is empty; the timeline would show a step id")
	}

	resp, full := h.doJSON(t, http.MethodGet, base, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("transcript: %d %s", resp.StatusCode, full)
	}
	// The writer always ends a record with a newline, so the whole file is
	// line-aligned and the cursor is its size.
	if next := resp.Header.Get("X-Next-Offset"); next != fmt.Sprint(len(full)) {
		t.Errorf("X-Next-Offset = %s, want %d", next, len(full))
	}

	// A tail window returns a suffix that starts on a record boundary.
	resp, tail := h.doJSON(t, http.MethodGet, base+"?tail=120", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("tail: %d %s", resp.StatusCode, tail)
	}
	if len(tail) == 0 || len(tail) >= len(full) {
		t.Fatalf("tail returned %d bytes of %d", len(tail), len(full))
	}
	if !bytes.HasSuffix(full, tail) {
		t.Error("tail is not a suffix of the full transcript")
	}
	if tail[0] != '{' {
		t.Errorf("tail starts mid-record: %.40q", tail)
	}
	if next := resp.Header.Get("X-Next-Offset"); next != fmt.Sprint(len(full)) {
		t.Errorf("tail X-Next-Offset = %s, want %d", next, len(full))
	}

	// Normalized mode: one record per line, all of them recognized shapes.
	resp, norm := h.doJSON(t, http.MethodGet, base+"?format=normalized", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("normalized: %d %s", resp.StatusCode, norm)
	}
	if bytes.Count(norm, []byte("\n")) != bytes.Count(full, []byte("\n")) {
		t.Errorf("normalized has %d records, raw has %d lines",
			bytes.Count(norm, []byte("\n")), bytes.Count(full, []byte("\n")))
	}
	if !bytes.Contains(norm, []byte("vincent.step_started")) {
		t.Error("normalized output lost vincent's own annotations")
	}
	if !bytes.Contains(norm, []byte(`"type":"agent.`)) {
		t.Error("normalized output has no agent.* records")
	}
	if next := resp.Header.Get("X-Next-Offset"); next != fmt.Sprint(len(full)) {
		t.Errorf("normalized X-Next-Offset = %s, want %d (raw file bytes)", next, len(full))
	}

	// Both range parameters at once is a client bug worth naming, and an
	// unknown format must not silently serve raw bytes.
	if resp, body := h.doJSON(t, http.MethodGet, base+"?offset=0&tail=10", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("offset+tail: %d %s, want 400", resp.StatusCode, body)
	}
	if resp, body := h.doJSON(t, http.MethodGet, base+"?format=pretty", nil); resp.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown format: %d %s, want 400", resp.StatusCode, body)
	}
}

// unknownParser stands in for an adapter whose dialect recognizes nothing.
func unknownParser() agent.LineParser {
	return func(raw []byte) agent.Event { return agent.Event{Type: agent.EventUnknown, Raw: raw} }
}

// TestNormalizeUnknownEventKeepsLine guards the lossless rule directly: a
// parser that recognizes nothing still yields one record per input line.
func TestNormalizeUnknownEventKeepsLine(t *testing.T) {
	var out bytes.Buffer
	if err := normalizeTranscript(&out, strings.NewReader("{\"a\":1}\n{\"b\":2}\n"), unknownParser()); err != nil {
		t.Fatalf("normalizeTranscript: %v", err)
	}
	if n := strings.Count(out.String(), "\n"); n != 2 {
		t.Errorf("got %d records, want 2: %s", n, out.String())
	}
	if !strings.Contains(out.String(), `\"a\":1`) || !strings.Contains(out.String(), `\"b\":2`) {
		t.Errorf("input lines not preserved: %s", out.String())
	}
}
