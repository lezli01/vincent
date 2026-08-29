package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

// stubHandler answers every request with a fixed status and body, and records
// what it was asked for.
type stubHandler struct {
	status int
	body   string
	got    *http.Request
	gotRaw []byte
}

func (h *stubHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.got = r
	buf := make([]byte, 4096)
	n, _ := r.Body.Read(buf)
	h.gotRaw = buf[:n]
	w.WriteHeader(h.status)
	_, _ = w.Write([]byte(h.body))
}

func routeFor(t *testing.T, tool string) Route {
	t.Helper()
	for _, r := range routes {
		if r.Tool == tool {
			return r
		}
	}
	t.Fatalf("no route for tool %q", tool)
	return Route{}
}

// TestDispatchBuildsTheRequest proves the replay reaches the right method,
// path and body — the mechanism decision 3 rests on.
func TestDispatchBuildsTheRequest(t *testing.T) {
	t.Parallel()
	h := &stubHandler{status: http.StatusOK, body: `{"id":7}`}
	s := New(Deps{Handler: h})

	res, err := s.dispatch(t.Context(), routeFor(t, "task_repair"),
		json.RawMessage(`{"id":7,"body":{"prompt":"try again"}}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if res.IsError {
		t.Fatalf("result is an error: %s", text(t, res))
	}
	if h.got.Method != http.MethodPost || h.got.URL.Path != "/v1/tasks/7/repair" {
		t.Errorf("replayed %s %s, want POST /v1/tasks/7/repair", h.got.Method, h.got.URL.Path)
	}
	if got := string(h.gotRaw); got != `{"prompt":"try again"}` {
		t.Errorf("body = %s, want the tool's body verbatim", got)
	}
	if ct := h.got.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// TestDispatchPassesQueryThrough covers the GET half: a route's own paging
// parameters have to reach it, because they are how a client asks for less
// than the truncation bound.
func TestDispatchPassesQueryThrough(t *testing.T) {
	t.Parallel()
	h := &stubHandler{status: http.StatusOK, body: `{}`}
	s := New(Deps{Handler: h})
	if _, err := s.dispatch(t.Context(), routeFor(t, "task_transcript"),
		json.RawMessage(`{"id":3,"run_id":"11","query":{"format":"normalized","limit":"50"}}`)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if h.got.URL.Path != "/v1/tasks/3/steps/11/transcript" {
		t.Errorf("path = %s", h.got.URL.Path)
	}
	q := h.got.URL.Query()
	if q.Get("format") != "normalized" || q.Get("limit") != "50" {
		t.Errorf("query = %v, want format and limit through", q)
	}
}

// TestDispatchPreservesTheErrorEnvelope is what decision 3 buys: a 409 from an
// invalid transition reaches the client still carrying details.state, so a
// model can branch on it rather than re-read prose.
func TestDispatchPreservesTheErrorEnvelope(t *testing.T) {
	t.Parallel()
	h := &stubHandler{
		status: http.StatusConflict,
		body:   `{"error":{"code":"invalid_action","message":"cannot approve","details":{"state":"running"}}}`,
	}
	s := New(Deps{Handler: h})
	res, err := s.dispatch(t.Context(), routeFor(t, "task_approve"), json.RawMessage(`{"id":4}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !res.IsError {
		t.Fatal("a 409 must reach the client as a tool error")
	}
	var env struct {
		Error struct {
			Code    string          `json:"code"`
			Message string          `json:"message"`
			Details json.RawMessage `json:"details"`
		} `json:"error"`
	}
	if err := json.Unmarshal([]byte(text(t, res)), &env); err != nil {
		t.Fatalf("tool error is not the §13.1 envelope: %v", err)
	}
	if env.Error.Code != "invalid_action" {
		t.Errorf("code = %q, want invalid_action", env.Error.Code)
	}
	if !strings.Contains(string(env.Error.Details), `"state":"running"`) {
		t.Errorf("details = %s, want details.state through", env.Error.Details)
	}
}

// TestDispatchPreservesFieldBounds proves a §13.1 field-bound refusal still
// names the field and the limit through the tool path.
func TestDispatchPreservesFieldBounds(t *testing.T) {
	t.Parallel()
	h := &stubHandler{
		status: http.StatusBadRequest,
		body:   `{"error":{"code":"validation_failed","message":"title exceeds 512 bytes"}}`,
	}
	s := New(Deps{Handler: h})
	res, _ := s.dispatch(t.Context(), routeFor(t, "task_create"), json.RawMessage(`{"body":{"title":"x"}}`))
	if !res.IsError || !strings.Contains(text(t, res), "title exceeds 512 bytes") {
		t.Errorf("tool error = %s, want the field and the limit", text(t, res))
	}
}

// TestDispatchForwardsIdempotencyBehaviour: the header is the route's, so a
// replayed key behaves exactly as it does over HTTP — including the 409 the
// second send gets.
func TestDispatchRejectsMissingPathParam(t *testing.T) {
	t.Parallel()
	s := New(Deps{Handler: &stubHandler{status: http.StatusOK, body: "{}"}})
	res, err := s.dispatch(t.Context(), routeFor(t, "task_get"), json.RawMessage(`{}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if !res.IsError || !strings.Contains(text(t, res), "missing required argument") {
		t.Errorf("result = %s, want a refusal naming the missing argument", text(t, res))
	}
}

// TestDispatchTruncatesHugeResponses: a transcript can be megabytes and a tool
// result lands in an agent's context, so the backstop has to say so rather
// than silently hand back a prefix.
func TestDispatchTruncatesHugeResponses(t *testing.T) {
	t.Parallel()
	h := &stubHandler{status: http.StatusOK, body: strings.Repeat("x", maxToolBytes+2048)}
	s := New(Deps{Handler: h})
	res, err := s.dispatch(t.Context(), routeFor(t, "task_transcript"),
		json.RawMessage(`{"id":1,"run_id":"1"}`))
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	body := text(t, res)
	if len(body) > maxToolBytes+256 {
		t.Errorf("result is %d bytes, want it bounded at %d plus the note", len(body), maxToolBytes)
	}
	if !strings.Contains(body, "truncated at") {
		t.Error("a truncated result must say so")
	}
}

// TestEveryToolHasAnObjectSchema is what the SDK requires of AddTool, asserted
// over the generated schemas rather than trusted.
func TestEveryToolHasAnObjectSchema(t *testing.T) {
	t.Parallel()
	for _, r := range append(Routes(), Route{Tool: WaitTool}) {
		schema := waitSchema()
		if r.Tool != WaitTool {
			schema = inputSchema(r)
		}
		var got map[string]any
		if err := json.Unmarshal(schema, &got); err != nil {
			t.Errorf("%s: schema is not JSON: %v", r.Tool, err)
			continue
		}
		if got["type"] != "object" {
			t.Errorf("%s: schema type = %v, want object", r.Tool, got["type"])
		}
	}
}

// TestStepEndpointIdentity proves the per-step endpoint resolves a call to the
// step that opened it, and refuses a wrong or retired secret. Identity is what
// makes the wait tool's refusal correct (decision 6).
func TestStepEndpointIdentity(t *testing.T) {
	t.Parallel()
	s := New(Deps{Handler: &stubHandler{status: http.StatusOK, body: "{}"}})
	sess, err := s.OpenStep(11, 42, "build")
	if err != nil {
		t.Fatalf("OpenStep: %v", err)
	}
	req := stepRequest(t, sess.URLPath(), sess.Secret)
	got, ok := s.steps.authenticate(req)
	if !ok || got.TaskID != 42 {
		t.Fatalf("authenticate = %+v, %v; want the session for task 42", got, ok)
	}
	if id, viaMCP := CreatorTaskID(withStep(context.Background(), got)); !viaMCP || id != 42 {
		t.Errorf("CreatorTaskID = %d, %v; want 42, true", id, viaMCP)
	}
	if _, bad := s.steps.authenticate(stepRequest(t, sess.URLPath(), "wrong")); bad {
		t.Error("a wrong secret authenticated")
	}
	s.CloseStep(11)
	if _, alive := s.steps.authenticate(req); alive {
		t.Error("a retired session still authenticates; the secret must die with the step")
	}
}

// TestCreatorTaskIDIsAbsentOnTheSharedEndpoint: a call on `/mcp` holds no §11
// slot, so it has no creator and the wait tool's refusal does not apply.
func TestCreatorTaskIDIsAbsentOnTheSharedEndpoint(t *testing.T) {
	t.Parallel()
	if _, ok := CreatorTaskID(context.Background()); ok {
		t.Error("a context with no step session reported a creator")
	}
}

func stepRequest(t *testing.T, path, secret string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+secret)
	return req
}

func text(t *testing.T, res *sdk.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		t.Fatal("result carries no content")
	}
	tc, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("content is %T, want text", res.Content[0])
	}
	return tc.Text
}

// TestDispatchForwardsIdempotencyKey: §13.1's replay protection exists for a
// client whose response got lost, which is exactly what an agent is. A tool
// call has no header surface, so the key is an argument — and this proves it
// reaches the route as the header the route reads.
func TestDispatchForwardsIdempotencyKey(t *testing.T) {
	t.Parallel()
	h := &stubHandler{status: http.StatusOK, body: `{"id":1}`}
	s := New(Deps{Handler: h})
	if _, err := s.dispatch(t.Context(), routeFor(t, "task_create"),
		json.RawMessage(`{"idempotency_key":"abc-123","body":{"project_id":1}}`)); err != nil {
		t.Fatalf("dispatch: %v", err)
	}
	if got := h.got.Header.Get("Idempotency-Key"); got != "abc-123" {
		t.Errorf("Idempotency-Key = %q, want abc-123", got)
	}
}

// TestIdempotencyKeyIsOnlyOnTaskCreate: the argument is advertised on the one
// route that honours it. An argument a route ignores is a worse lie than a
// missing one.
func TestIdempotencyKeyIsOnlyOnTaskCreate(t *testing.T) {
	t.Parallel()
	for _, r := range Routes() {
		var schema struct {
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(inputSchema(r), &schema); err != nil {
			t.Fatalf("%s: %v", r.Tool, err)
		}
		_, has := schema.Properties["idempotency_key"]
		want := r.Tool == "task_create"
		if has != want {
			t.Errorf("%s advertises idempotency_key = %v, want %v", r.Tool, has, want)
		}
	}
}
