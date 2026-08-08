package claude

// Tests for the §7.4 input protocol: the version gate, control-message
// normalization against fixtures captured from real claude 2.1.226 runs,
// answer translation goldens, and live round-trips through the fake agent.

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/agent"
)

func TestSupportsInputGate(t *testing.T) {
	tests := []struct {
		version string
		want    bool
	}{
		{"2.1.0", true},
		{"2.1.224", true},
		{"2.1.226", true},
		{"2.9.3", true},
		{"2.0.14", false},
		{"3.0.0", false},
		{"3.1.4", false},
		{"1.9.9", false},
		{"2.1.226 (Claude Code)", true}, // raw --version output
		{"nightly", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := supportsInput(tt.version); got != tt.want {
			t.Errorf("supportsInput(%q) = %v, want %v", tt.version, got, tt.want)
		}
	}
}

func TestDetectVersionOutsideGate(t *testing.T) {
	t.Setenv("FAKEAGENT_VERSION", "1.0.5")
	av, err := fakeAdapter(t).Detect(t.Context())
	if err != nil {
		t.Fatalf("Detect: %v", err)
	}
	if !av.Found || av.Version != "1.0.5" {
		t.Fatalf("Found=%v Version=%q, want found 1.0.5", av.Found, av.Version)
	}
	if av.SupportsInput {
		t.Error("SupportsInput = true for 1.0.5; the gate is [2.1.0, 3.0.0)")
	}
}

// TestRunDegradesOutsideGate proves a version outside the gate runs exactly
// as before: raw-text prompt, no input flags, prompt round-trips.
func TestRunDegradesOutsideGate(t *testing.T) {
	t.Setenv("FAKEAGENT_VERSION", "1.0.5")
	h := startRun(t, "success")
	drain(t, h)
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.IsError || !strings.Contains(res.ResultText, "test prompt for success") {
		t.Errorf("degraded run failed: isError=%v result=%q", res.IsError, res.ResultText)
	}
	if err := h.Respond(agent.InputResponse{}); err == nil ||
		!strings.Contains(err.Error(), "without input support") {
		t.Errorf("Respond error = %v, want the no-input-support rejection", err)
	}
}

// fixtureEvents runs every line of a captured stream through the run parser.
func fixtureEvents(t *testing.T, name string) ([]agent.Event, *run) {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("open fixture: %v", err)
	}
	defer f.Close()
	r := &run{inputMode: true}
	var events []agent.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), maxLineBytes)
	for sc.Scan() {
		line := append([]byte(nil), sc.Bytes()...)
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		events = append(events, r.parseStreamLine(line))
	}
	return events, r
}

func inputRequests(events []agent.Event) []agent.Event {
	var out []agent.Event
	for _, ev := range events {
		if ev.Type == agent.EventInputRequest {
			out = append(out, ev)
		}
	}
	return out
}

func TestFixtureQuestionStream(t *testing.T) {
	events, r := fixtureEvents(t, "stream_question_2.1.226.jsonl")
	reqs := inputRequests(events)
	if len(reqs) != 1 {
		t.Fatalf("got %d input requests, want 1", len(reqs))
	}
	req := reqs[0].Request
	if req == nil {
		t.Fatalf("request is nil (protocol error): %s", reqs[0].Message)
	}
	if req.Kind != "question" || len(req.Questions) != 1 {
		t.Fatalf("kind=%q questions=%d, want question/1", req.Kind, len(req.Questions))
	}
	q := req.Questions[0]
	if q.Text != "Which color do you prefer?" || q.Header != "Color" || q.MultiSelect {
		t.Errorf("question = %+v, want the captured shape", q)
	}
	if !reflect.DeepEqual(q.Options, []string{"Red", "Blue"}) {
		t.Errorf("options = %v, want [Red Blue]", q.Options)
	}
	if len(req.Raw) == 0 {
		t.Error("Raw is empty; clients need the native payload")
	}
	if r.pending == nil || r.pending.kind != "question" {
		t.Fatalf("pending = %+v, want the question tracked", r.pending)
	}
	last := events[len(events)-1]
	if last.Type != agent.EventResult || last.Result == nil || last.Result.IsError {
		t.Errorf("stream must end in a success result, got %+v", last)
	}
	if last.Result.ResultText != "Red" {
		t.Errorf("ResultText = %q, want the answered color Red", last.Result.ResultText)
	}
}

func TestFixturePermissionStreams(t *testing.T) {
	for _, name := range []string{
		"stream_permission_deny_2.1.226.jsonl",
		"stream_permission_allow_2.1.226.jsonl",
	} {
		t.Run(name, func(t *testing.T) {
			events, r := fixtureEvents(t, name)
			reqs := inputRequests(events)
			if len(reqs) != 1 {
				t.Fatalf("got %d input requests, want 1", len(reqs))
			}
			req := reqs[0].Request
			if req == nil {
				t.Fatalf("request is nil: %s", reqs[0].Message)
			}
			if req.Kind != "permission" || req.Permission == nil {
				t.Fatalf("kind=%q, want permission", req.Kind)
			}
			if req.Permission.Tool != "Write" || req.Permission.Summary != "hello.txt" {
				t.Errorf("permission = %+v, want Write/hello.txt", req.Permission)
			}
			if r.pending == nil || r.pending.kind != "permission" {
				t.Fatalf("pending = %+v, want the permission tracked", r.pending)
			}
			last := events[len(events)-1]
			if last.Type != agent.EventResult || last.Result.IsError {
				t.Errorf("stream must end in a success result, got %+v", last)
			}
		})
	}
}

// respondGolden asserts the exact wire line Respond would write.
func respondGolden(t *testing.T, pend *pendingRequest, resp agent.InputResponse, wantInner map[string]any) {
	t.Helper()
	line, err := buildControlResponse(pend, resp)
	if err != nil {
		t.Fatalf("buildControlResponse: %v", err)
	}
	var got struct {
		Type     string `json:"type"`
		Response struct {
			Subtype   string         `json:"subtype"`
			RequestID string         `json:"request_id"`
			Response  map[string]any `json:"response"`
		} `json:"response"`
	}
	if err := json.Unmarshal(line, &got); err != nil {
		t.Fatalf("unmarshal response line: %v", err)
	}
	if got.Type != "control_response" || got.Response.Subtype != "success" ||
		got.Response.RequestID != pend.id {
		t.Errorf("envelope = %+v, want control_response/success/%s", got, pend.id)
	}
	if !reflect.DeepEqual(got.Response.Response, wantInner) {
		t.Errorf("inner response =\n%v\nwant\n%v", got.Response.Response, wantInner)
	}
}

func TestRespondTranslation(t *testing.T) {
	questionPend := &pendingRequest{
		id:       "req-1",
		kind:     "question",
		toolName: askUserQuestionTool,
		input:    json.RawMessage(`{"questions":[{"question":"Which color?","options":[{"label":"Red"}],"multiSelect":false}]}`),
	}
	permissionPend := &pendingRequest{
		id:       "req-2",
		kind:     "permission",
		toolName: "Write",
		input:    json.RawMessage(`{"file_path":"hello.txt","content":"hi"}`),
	}
	questions := []any{map[string]any{
		"question": "Which color?", "options": []any{map[string]any{"label": "Red"}}, "multiSelect": false,
	}}
	allow, deny := true, false

	t.Run("single answer", func(t *testing.T) {
		respondGolden(t, questionPend,
			agent.InputResponse{Answers: map[string][]string{"Which color?": {"Red"}}},
			map[string]any{"behavior": "allow", "updatedInput": map[string]any{
				"questions": questions,
				"answers":   map[string]any{"Which color?": "Red"},
			}})
	})
	t.Run("multi-select answers stay an array", func(t *testing.T) {
		respondGolden(t, questionPend,
			agent.InputResponse{Answers: map[string][]string{"Which color?": {"Red", "Blue"}}},
			map[string]any{"behavior": "allow", "updatedInput": map[string]any{
				"questions": questions,
				"answers":   map[string]any{"Which color?": []any{"Red", "Blue"}},
			}})
	})
	t.Run("canned response rides updatedInput.response", func(t *testing.T) {
		respondGolden(t, questionPend,
			agent.InputResponse{Response: "no user is available; decide with your best judgment"},
			map[string]any{"behavior": "allow", "updatedInput": map[string]any{
				"questions": questions,
				"response":  "no user is available; decide with your best judgment",
			}})
	})
	t.Run("permission allow re-sends the input", func(t *testing.T) {
		respondGolden(t, permissionPend,
			agent.InputResponse{Allow: &allow},
			map[string]any{"behavior": "allow", "updatedInput": map[string]any{
				"file_path": "hello.txt", "content": "hi",
			}})
	})
	t.Run("permission deny carries the message", func(t *testing.T) {
		respondGolden(t, permissionPend,
			agent.InputResponse{Allow: &deny, Response: "no user is available; permission denied"},
			map[string]any{"behavior": "deny", "message": "no user is available; permission denied"})
	})
	t.Run("permission nil-allow denies", func(t *testing.T) {
		respondGolden(t, permissionPend,
			agent.InputResponse{},
			map[string]any{"behavior": "deny", "message": "permission denied"})
	})
}

func TestControlProtocolViolations(t *testing.T) {
	r := &run{inputMode: true}
	tests := []struct {
		name string
		line string
	}{
		{"unknown subtype", `{"type":"control_request","request_id":"x","request":{"subtype":"mcp_message"}}`},
		{"missing request", `{"type":"control_request","request_id":"x"}`},
		{"missing request_id", `{"type":"control_request","request":{"subtype":"can_use_tool","tool_name":"Write"}}`},
		{"question without questions", `{"type":"control_request","request_id":"x","request":{"subtype":"can_use_tool","tool_name":"AskUserQuestion","input":{}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := r.parseStreamLine([]byte(tt.line))
			if ev.Type != agent.EventInputRequest || ev.Request != nil {
				t.Errorf("event = %+v, want a nil-Request input_request (protocol error)", ev)
			}
			if ev.Message == "" {
				t.Error("protocol error event carries no message")
			}
		})
	}
}

func TestSecondRequestWhilePendingIsViolation(t *testing.T) {
	r := &run{inputMode: true}
	first := `{"type":"control_request","request_id":"a","request":{"subtype":"can_use_tool","tool_name":"Write","input":{}}}`
	second := `{"type":"control_request","request_id":"b","request":{"subtype":"can_use_tool","tool_name":"Edit","input":{}}}`
	if ev := r.parseStreamLine([]byte(first)); ev.Request == nil {
		t.Fatalf("first request rejected: %s", ev.Message)
	}
	ev := r.parseStreamLine([]byte(second))
	if ev.Type != agent.EventInputRequest || ev.Request != nil {
		t.Errorf("second concurrent request = %+v, want a protocol violation", ev)
	}
}

func TestControlCancelRequest(t *testing.T) {
	r := &run{inputMode: true}
	req := `{"type":"control_request","request_id":"a","request":{"subtype":"can_use_tool","tool_name":"Write","input":{}}}`
	if ev := r.parseStreamLine([]byte(req)); ev.Request == nil {
		t.Fatalf("request rejected: %s", ev.Message)
	}
	if ev := r.parseStreamLine([]byte(`{"type":"control_cancel_request","request_id":"other"}`)); ev.Type != agent.EventUnknown {
		t.Errorf("non-matching cancel = %v, want unknown (tolerated)", ev.Type)
	}
	if r.pending == nil {
		t.Fatal("pending cleared by a non-matching cancel")
	}
	if ev := r.parseStreamLine([]byte(`{"type":"control_cancel_request","request_id":"a"}`)); ev.Type != agent.EventInputCanceled {
		t.Errorf("matching cancel = %v, want input_canceled", ev.Type)
	}
	if r.pending != nil {
		t.Error("pending not cleared by the matching cancel")
	}
}

// TestAskQuestionRoundTrip drives the full live path against the fake agent:
// request event → Respond with answers → output echoes them → clean result.
func TestAskQuestionRoundTrip(t *testing.T) {
	h := startRun(t, "ask-question")
	var req *agent.InputRequest
	for ev := range h.Events() {
		if ev.Type == agent.EventInputRequest {
			req = ev.Request
			break
		}
	}
	if req == nil {
		t.Fatal("no input request surfaced")
	}
	if req.Kind != "question" || len(req.Questions) != 1 || req.Questions[0].Text != "Which color do you prefer?" {
		t.Fatalf("request = %+v, want the fake's question", req)
	}
	err := h.Respond(agent.InputResponse{
		Answers: map[string][]string{"Which color do you prefer?": {"Red"}},
	})
	if err != nil {
		t.Fatalf("Respond: %v", err)
	}
	drain(t, h)
	res, err := h.Wait()
	if err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if res.IsError || res.ExitCode != 0 {
		t.Fatalf("exit=%d isError=%v (%s), want success", res.ExitCode, res.IsError, res.ErrorMessage)
	}
	if !strings.Contains(res.ResultText, `"Red"`) {
		t.Errorf("ResultText %q does not echo the answer", res.ResultText)
	}
}

func TestAskPermissionRoundTrip(t *testing.T) {
	for _, tt := range []struct {
		name  string
		allow bool
		want  string
	}{
		{"allow", true, "allow"},
		{"deny", false, "deny: no thanks"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			h := startRun(t, "ask-permission")
			var req *agent.InputRequest
			for ev := range h.Events() {
				if ev.Type == agent.EventInputRequest {
					req = ev.Request
					break
				}
			}
			if req == nil || req.Kind != "permission" || req.Permission.Tool != "Write" {
				t.Fatalf("request = %+v, want a Write permission request", req)
			}
			resp := agent.InputResponse{Allow: &tt.allow}
			if !tt.allow {
				resp.Response = "no thanks"
			}
			if err := h.Respond(resp); err != nil {
				t.Fatalf("Respond: %v", err)
			}
			drain(t, h)
			res, err := h.Wait()
			if err != nil {
				t.Fatalf("Wait: %v", err)
			}
			if res.IsError || !strings.Contains(res.ResultText, tt.want) {
				t.Errorf("result %q (isError=%v), want it to contain %q", res.ResultText, res.IsError, tt.want)
			}
		})
	}
}

func TestBadInputRequestSurfacesProtocolError(t *testing.T) {
	h := startRun(t, "bad-input-request")
	var violation *agent.Event
	for ev := range h.Events() {
		if ev.Type == agent.EventInputRequest {
			e := ev
			violation = &e
			break
		}
	}
	if violation == nil {
		t.Fatal("no protocol-error event surfaced")
	}
	if violation.Request != nil {
		t.Errorf("Request = %+v, want nil (unparseable)", violation.Request)
	}
	if err := h.Kill(); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	drain(t, h)
	_, _ = h.Wait()
}