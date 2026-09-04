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
	"github.com/lezli01/vincent/internal/agent/codex"
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
		`{"type":"system","subtype":"compact_boundary"}`,
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
	// The unrecognized line survives verbatim inside agent.raw. `init` is
	// normalized since task 066, so the unmodelled subtype beside it is what
	// stands in for "a line this dialect does not model".
	if !strings.Contains(lines[3], `\"subtype\":\"compact_boundary\"`) {
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

// TestStepRunResponseInputRecord covers the migration-0027 record (issue
// #323): the rendered input an attempt was handed and the resolution behind
// it, including the nil-versus-empty distinction the pointers exist for.
func TestStepRunResponseInputRecord(t *testing.T) {
	summary := snapshotSummary{stepTotal: 2, stepNames: []string{"plan", "implement"}}

	// An attempt from before the record existed: every field says "nothing was
	// recorded", which is not "given an empty prompt".
	bare := toStepRunResponse(&store.StepRun{ID: 7, StepIndex: 1, StepID: "impl"}, summary)
	if bare.RenderedPrompt != nil || bare.RenderedRun != nil || bare.RenderedCheck != nil ||
		bare.RenderedIf != nil || bare.RenderedForEach != nil {
		t.Errorf("unrecorded attempt claims an input: %+v", bare)
	}
	if bare.InputTruncated || bare.TimeoutMS != 0 || bare.CheckTimeoutMS != 0 {
		t.Errorf("unrecorded attempt claims limits: %+v", bare)
	}
	if bare.AgentSource != nil || bare.ModelSource != nil || bare.EffortSource != nil ||
		bare.PermissionMode != nil || bare.Shell != nil || bare.WorkDir != nil {
		t.Errorf("unrecorded attempt claims a resolution: %+v", bare)
	}

	empty := ""
	run := &store.StepRun{
		ID: 8, StepIndex: 1, StepID: "impl", StepType: "command",
		RenderedRun: strptr("go test ./..."), RenderedCheck: &empty,
		RenderedIf: strptr("true"), RenderedForEach: strptr(`["a","b"]`),
		InputTruncated: true,
		AgentSource:    "task", ModelSource: "workflow", EffortSource: "adapter",
		PermissionMode: "restricted", TimeoutMS: 600_000, CheckTimeoutMS: 120_000,
		Shell: "/bin/sh", WorkDir: "/tmp/wt",
	}
	got := toStepRunResponse(run, summary)
	if got.RenderedRun == nil || *got.RenderedRun != "go test ./..." {
		t.Errorf("rendered_run = %v, want the recorded bytes", got.RenderedRun)
	}
	// The empty render passes through as a non-null empty string: nilIfEmpty
	// here would collapse it into "nothing was recorded".
	if got.RenderedCheck == nil || *got.RenderedCheck != "" {
		t.Errorf("rendered_check = %v, want a non-null empty render", got.RenderedCheck)
	}
	if got.RenderedPrompt != nil {
		t.Errorf("rendered_prompt = %v, want null on a command step", got.RenderedPrompt)
	}
	if got.RenderedIf == nil || *got.RenderedIf != "true" {
		t.Errorf("rendered_if = %v, want the guard's render", got.RenderedIf)
	}
	if got.RenderedForEach == nil || *got.RenderedForEach != `["a","b"]` {
		t.Errorf("rendered_for_each = %v, want the resolved list", got.RenderedForEach)
	}
	if !got.InputTruncated {
		t.Error("input_truncated dropped")
	}
	if got.AgentSource == nil || *got.AgentSource != "task" ||
		got.ModelSource == nil || *got.ModelSource != "workflow" ||
		got.EffortSource == nil || *got.EffortSource != "adapter" {
		t.Errorf("sources = %v/%v/%v, want task/workflow/adapter",
			got.AgentSource, got.ModelSource, got.EffortSource)
	}
	if got.PermissionMode == nil || *got.PermissionMode != "restricted" {
		t.Errorf("permission_mode = %v, want restricted", got.PermissionMode)
	}
	if got.TimeoutMS != 600_000 || got.CheckTimeoutMS != 120_000 {
		t.Errorf("timeouts = %d/%d, want 600000/120000", got.TimeoutMS, got.CheckTimeoutMS)
	}
	if got.Shell == nil || *got.Shell != "/bin/sh" || got.WorkDir == nil || *got.WorkDir != "/tmp/wt" {
		t.Errorf("shell/work_dir = %v/%v, want /bin/sh and /tmp/wt", got.Shell, got.WorkDir)
	}
}

// strptr is the test's way of writing a recorded field that is present.
func strptr(s string) *string { return &s }

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

// TestNormalizeThinkingAndToolResults covers the two record types T4.16 added
// (§13.2). Both come out of a captured claude run: the reasoning blocks and
// the tool_result lines were in the stream all along and normalized to
// agent.raw.
func TestNormalizeThinkingAndToolResults(t *testing.T) {
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"thinking",` +
			`"thinking":"The user wants hello.txt.","signature":"EucCCpMBCBAY"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"toolu_01",` +
			`"name":"Write","input":{"file_path":"hello.txt","content":"hi"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_01",` +
			`"content":"File created successfully at: hello.txt"}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_02",` +
			`"content":"permission denied","is_error":true}]}}`,
	}
	var buf bytes.Buffer
	parser := claude.New(func() string { return "" }).NewLineParser()
	if err := normalizeTranscript(&buf, strings.NewReader(strings.Join(lines, "\n")+"\n"), parser); err != nil {
		t.Fatalf("normalizeTranscript: %v", err)
	}
	out := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(out) != len(lines) {
		t.Fatalf("got %d normalized lines, want %d:\n%s", len(out), len(lines), buf.String())
	}

	types := make([]string, len(out))
	for i, l := range out {
		var probe struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal([]byte(l), &probe); err != nil {
			t.Fatalf("line %d is not JSON: %v", i, err)
		}
		types[i] = probe.Type
	}
	want := []string{"agent.thinking", "agent.tool_use", "agent.tool_result", "agent.tool_result"}
	for i := range want {
		if types[i] != want[i] {
			t.Errorf("line %d type = %q, want %q", i, types[i], want[i])
		}
	}

	// The reasoning text rides on `text`, like assistant output — and the
	// signature blob, which is an attestation rather than prose, does not.
	if !strings.Contains(out[0], `"text":"The user wants hello.txt."`) {
		t.Errorf("thinking text lost: %s", out[0])
	}
	if strings.Contains(out[0], "EucCCpMBCBAY") {
		t.Errorf("signature blob reached the wire: %s", out[0])
	}

	// A result correlates to its call and says what happened; the failed one
	// is flagged rather than reading as a quiet success.
	if !strings.Contains(out[2], `"call_id":"toolu_01"`) ||
		!strings.Contains(out[2], "File created successfully") {
		t.Errorf("tool result not normalized: %s", out[2])
	}
	if !strings.Contains(out[3], `"is_error":true`) {
		t.Errorf("failed tool result not flagged: %s", out[3])
	}
}

// TestNormalizeRunHeaderAndResultMetadata covers the wire names task 066
// added (§13.2): the new agent.run_header record, and the result line's own
// account of the run. Both come off the shapes a captured claude run
// actually sends.
//
// The key names here are the *contract* with internal/taskrun's live chunks:
// a client renders the live tail and the fetched scrollback through one path,
// so a name that differs by one character shows up as output that changes the
// moment a step finishes. taskrun/chunks_test.go pins the other side.
func TestNormalizeRunHeaderAndResultMetadata(t *testing.T) {
	lines := []string{
		`{"type":"system","subtype":"init","session_id":"s1","cwd":"C:\\work\\repo",` +
			`"tools":["Task","Bash","Write"]}`,
		`{"type":"user","parent_tool_use_id":"toolu_parent","message":{"content":[` +
			`{"type":"tool_result","tool_use_id":"toolu_01","content":"File created"}]},` +
			`"tool_use_result":{"type":"create","filePath":"hello.txt"}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"toolu_02",` +
			`"content":"no user is available; permission denied","is_error":true}]},` +
			`"tool_use_result":"Error: no user is available; permission denied",` +
			`"tool_result_meta":[{"id":"toolu_02","non_execution_kind":"permission-rule"}]}`,
		`{"type":"result","subtype":"success","is_error":false,"result":"done",` +
			`"duration_ms":7324,"duration_api_ms":5706,"num_turns":2,` +
			`"stop_reason":"end_turn","terminal_reason":"completed",` +
			`"total_cost_usd":0.02206225,` +
			`"usage":{"input_tokens":18,"output_tokens":536,` +
			`"cache_read_input_tokens":60280,"cache_creation_input_tokens":6835},` +
			`"modelUsage":{"claude-haiku-4-5-20251001":{"inputTokens":18,"outputTokens":536,` +
			`"cacheReadInputTokens":60280,"cacheCreationInputTokens":6835,"costUSD":0.02206225}},` +
			`"permission_denials":[{"tool_name":"Write","tool_use_id":"toolu_02"}]}`,
	}
	var buf bytes.Buffer
	parser := claude.New(func() string { return "" }).NewLineParser()
	if err := normalizeTranscript(&buf, strings.NewReader(strings.Join(lines, "\n")+"\n"), parser); err != nil {
		t.Fatalf("normalizeTranscript: %v", err)
	}
	out := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(out) != len(lines) {
		t.Fatalf("got %d normalized lines, want %d:\n%s", len(out), len(lines), buf.String())
	}

	for _, want := range []string{
		`"type":"agent.run_header"`,
		`"work_dir":"C:\\work\\repo"`,
		`"available_tools":["Task","Bash","Write"]`,
	} {
		if !strings.Contains(out[0], want) {
			t.Errorf("run header missing %s: %s", want, out[0])
		}
	}

	// The verb rides beside the summary, and the subagent attribution rides
	// on the record rather than on one of its results.
	if !strings.Contains(out[1], `"verb":"created"`) ||
		!strings.Contains(out[1], `"parent_call_id":"toolu_parent"`) {
		t.Errorf("structured outcome lost: %s", out[1])
	}
	// A blocked call is flagged apart from an ordinary failure, and keeps
	// its error flag: it is both, and the finer verdict is the new one.
	if !strings.Contains(out[2], `"blocked":true`) || !strings.Contains(out[2], `"is_error":true`) {
		t.Errorf("blocked call not flagged: %s", out[2])
	}

	for _, want := range []string{
		`"duration_ms":7324`,
		`"api_duration_ms":5706`,
		`"num_turns":2`,
		`"cache_read_tokens":60280`,
		`"cache_write_tokens":6835`,
		`"model_usage":[{"model":"claude-haiku-4-5-20251001"`,
		`"permission_denials":[{"tool_name":"Write","call_id":"toolu_02"}]`,
	} {
		if !strings.Contains(out[3], want) {
			t.Errorf("result missing %s: %s", want, out[3])
		}
	}
	// The ordinary reasons are on the wire even though the pane does not
	// print them: a client other than the TUI may well want them.
	if !strings.Contains(out[3], `"stop_reason":"end_turn"`) ||
		!strings.Contains(out[3], `"terminal_reason":"completed"`) {
		t.Errorf("stop reasons lost: %s", out[3])
	}
}

// TestNormalizeOmitsUnreportedResultMetadata is the other half of the wire
// contract: an adapter that reports none of task 066's fields sends none of
// their keys, so a client can tell "unreported" from "zero".
func TestNormalizeOmitsUnreportedResultMetadata(t *testing.T) {
	var buf bytes.Buffer
	parser := claude.New(func() string { return "" }).NewLineParser()
	line := `{"type":"result","subtype":"success","result":"done"}` + "\n"
	if err := normalizeTranscript(&buf, strings.NewReader(line), parser); err != nil {
		t.Fatalf("normalizeTranscript: %v", err)
	}
	for _, absent := range []string{
		"duration_ms", "api_duration_ms", "num_turns", "stop_reason",
		"terminal_reason", "cache_read_tokens", "cache_write_tokens",
		"model_usage", "permission_denials", "parent_call_id",
	} {
		if strings.Contains(buf.String(), absent) {
			t.Errorf("unreported %s reached the wire: %s", absent, buf.String())
		}
	}
}

// TestNormalizePlanAndCommandOutput pins the wire names of the two records
// task 070 added, and the one line that produces two of them: codex reports
// a command's outcome and the body it printed on the same `item.completed`,
// and the two are separate records because clients show them at different
// verbosity levels.
func TestNormalizePlanAndCommandOutput(t *testing.T) {
	var buf bytes.Buffer
	parser := codex.New(func() string { return "" }).NewLineParser()
	lines := strings.Join([]string{
		`{"type":"item.updated","item":{"id":"item_1","type":"todo_list",` +
			`"items":[{"text":"first","completed":true},{"text":"second","completed":false}]}}`,
		`{"type":"item.completed","item":{"id":"item_2","type":"command_execution",` +
			`"command":"ls","aggregated_output":"total 8\n","exit_code":0,"status":"completed"}}`,
	}, "\n") + "\n"
	if err := normalizeTranscript(&buf, strings.NewReader(lines), parser); err != nil {
		t.Fatalf("normalizeTranscript: %v", err)
	}
	out := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(out) != 3 {
		t.Fatalf("records = %d, want 3 (plan, tool_result, command_output):\n%s", len(out), buf.String())
	}
	want := []string{
		`{"type":"agent.plan","items":[{"text":"first","completed":true},{"text":"second"}],"plan_call_id":"item_1"}`,
		`{"type":"agent.tool_result","results":[{"call_id":"item_2","name":"command_execution","summary":"exit 0"}]}`,
		`{"type":"agent.command_output","output":"total 8\n","call_id":"item_2","name":"command_execution"}`,
	}
	for i, w := range want {
		if out[i] != w {
			t.Errorf("record %d =\n%s\nwant\n%s", i, out[i], w)
		}
	}
}

// TestNormalizeOmitsUnreportedPlanFields is the other half: an adapter that
// reports none of task 070's fields emits none of the keys, so a client can
// tell "unreported" from "zero" and from "empty".
func TestNormalizeOmitsUnreportedPlanFields(t *testing.T) {
	var buf bytes.Buffer
	parser := claude.New(func() string { return "" }).NewLineParser()
	line := `{"type":"result","subtype":"success","result":"done"}` + "\n"
	if err := normalizeTranscript(&buf, strings.NewReader(line), parser); err != nil {
		t.Fatalf("normalizeTranscript: %v", err)
	}
	for _, absent := range []string{"reasoning_tokens", "items", "plan_call_id", "output", "truncated"} {
		if strings.Contains(buf.String(), absent) {
			t.Errorf("unreported %s reached the wire: %s", absent, buf.String())
		}
	}
}

// TestNormalizeCodexUsageFields: codex's five usage counters land on
// agent.result, two of them in the keys task 066 already defined.
func TestNormalizeCodexUsageFields(t *testing.T) {
	var buf bytes.Buffer
	parser := codex.New(func() string { return "" }).NewLineParser()
	line := `{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":7,` +
		`"cache_write_input_tokens":3,"output_tokens":5,"reasoning_output_tokens":2}}` + "\n"
	if err := normalizeTranscript(&buf, strings.NewReader(line), parser); err != nil {
		t.Fatalf("normalizeTranscript: %v", err)
	}
	for _, want := range []string{
		`"input_tokens":10`, `"output_tokens":5`, `"cache_read_tokens":7`,
		`"cache_write_tokens":3`, `"reasoning_tokens":2`,
	} {
		if !strings.Contains(buf.String(), want) {
			t.Errorf("missing %s in %s", want, buf.String())
		}
	}
}
