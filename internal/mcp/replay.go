package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// call is one tool invocation's decoded arguments: the path parameters, the
// body, and the query.
type call struct {
	Body  json.RawMessage   `json:"body,omitempty"`
	Query map[string]string `json:"query,omitempty"`
	// IdempotencyKey rides §13.1's replay-protection header. It is an
	// *argument* rather than a header because a tool call has no header
	// surface at all: without it the one §13.1 guarantee an MCP client could
	// not reach would be the one that exists for a client whose response got
	// lost — which is exactly the client an agent is.
	IdempotencyKey string `json:"idempotency_key,omitempty"`
	// params carries the path parameters by name; the SDK hands us raw JSON,
	// so they are decoded from the same object.
	params map[string]string
}

// decodeCall reads one route's arguments out of the raw tool payload.
func decodeCall(r Route, raw json.RawMessage) (call, error) {
	var c call
	if len(raw) == 0 {
		raw = json.RawMessage("{}")
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		return c, fmt.Errorf("arguments are not a JSON object: %w", err)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		return c, fmt.Errorf("arguments are not a JSON object: %w", err)
	}
	c.params = map[string]string{}
	for _, p := range pathParams(r.Path) {
		v, ok := obj[p]
		if !ok {
			return c, fmt.Errorf("missing required argument %q", p)
		}
		s, err := scalarString(v)
		if err != nil {
			return c, fmt.Errorf("argument %q: %w", p, err)
		}
		c.params[p] = s
	}
	return c, nil
}

// scalarString renders a JSON scalar as the path segment it stands for. A
// number arrives as a number from a well-behaved client and as a string from
// one that stringified it; both name the same task, so both are accepted.
func scalarString(v json.RawMessage) (string, error) {
	var s string
	if err := json.Unmarshal(v, &s); err == nil {
		return s, nil
	}
	var n json.Number
	if err := json.Unmarshal(v, &n); err == nil {
		return n.String(), nil
	}
	return "", fmt.Errorf("want a string or a number, got %s", string(v))
}

// requestFor builds the in-process request one tool call replays.
func requestFor(ctx context.Context, r Route, c call) (*http.Request, error) {
	path := r.Path
	for name, val := range c.params {
		path = strings.ReplaceAll(path, "{"+name+"}", url.PathEscape(val))
	}
	target := path
	if len(c.Query) > 0 {
		q := url.Values{}
		for k, v := range c.Query {
			q.Set(k, v)
		}
		target += "?" + q.Encode()
	}
	var body []byte
	if len(c.Body) > 0 && !bytes.Equal(bytes.TrimSpace(c.Body), []byte("null")) {
		body = c.Body
	}
	req, err := http.NewRequestWithContext(ctx, r.Method, target, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build %s %s: %w", r.Method, target, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
		req.ContentLength = int64(len(body))
	}
	if c.IdempotencyKey != "" {
		req.Header.Set("Idempotency-Key", c.IdempotencyKey)
	}
	// A loopback host is required: the handler chain reads r.Host for nothing
	// today, but net/http requires a non-empty one on a server-side request.
	req.Host = "127.0.0.1"
	req.RemoteAddr = "127.0.0.1:0"
	return req, nil
}

// recorder is the http.ResponseWriter a replayed request writes into. It is a
// deliberate few lines rather than httptest.ResponseRecorder: httptest is a
// testing package, and this runs in the daemon.
type recorder struct {
	status int
	header http.Header
	body   bytes.Buffer
	// limit bounds what one tool call may return into an agent's context. A
	// transcript or a diff can be megabytes, and the route's own paging
	// parameters are the right way to ask for less; this is the backstop that
	// keeps a forgotten `limit` from becoming an unbounded response.
	limit   int
	dropped bool
}

func (rec *recorder) Header() http.Header {
	if rec.header == nil {
		rec.header = http.Header{}
	}
	return rec.header
}

func (rec *recorder) WriteHeader(status int) {
	if rec.status == 0 {
		rec.status = status
	}
}

func (rec *recorder) Write(p []byte) (int, error) {
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	if room := rec.limit - rec.body.Len(); room < len(p) {
		if room > 0 {
			rec.body.Write(p[:room])
		}
		rec.dropped = true
		// The full length is reported written: a handler that saw a short
		// write would report a transcript_io_error-shaped failure about a
		// response nobody asked to be complete.
		return len(p), nil
	}
	return rec.body.Write(p) //nolint:wrapcheck // bytes.Buffer.Write never errors
}

// maxToolBytes bounds one tool call's response body. It is generous enough for
// a task list or a step list and small enough that a whole transcript comes
// back truncated with a note rather than filling the caller's context.
const maxToolBytes = 256 * 1024

// errorEnvelope is §13.1's error shape. Only the fields a tool error carries
// forward are decoded; `details` is kept verbatim so `details.state` reaches
// the client exactly as the route wrote it.
type errorEnvelope struct {
	Error struct {
		Code    string          `json:"code"`
		Message string          `json:"message"`
		Details json.RawMessage `json:"details,omitempty"`
	} `json:"error"`
}

// dispatch replays one tool call against the API handler and turns the
// response into a tool result.
//
// An HTTP error becomes a tool result with IsError set, never a protocol
// error: the model has to be able to read `state` off a 409 and pick a
// different action, and a protocol error is not something it can branch on.
func (s *Server) dispatch(ctx context.Context, r Route, raw json.RawMessage) (*mcp.CallToolResult, error) {
	c, err := decodeCall(r, raw)
	if err != nil {
		return toolError("invalid_arguments", err.Error(), nil), nil
	}
	req, err := requestFor(ctx, r, c)
	if err != nil {
		return toolError("invalid_arguments", err.Error(), nil), nil
	}
	rec := &recorder{limit: maxToolBytes}
	s.deps.Handler.ServeHTTP(rec, req)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	body := rec.body.Bytes()
	if rec.status >= http.StatusBadRequest {
		var env errorEnvelope
		if json.Unmarshal(body, &env) == nil && env.Error.Code != "" {
			return toolError(env.Error.Code, env.Error.Message, env.Error.Details), nil
		}
		return toolError("http_"+strconv.Itoa(rec.status), strings.TrimSpace(string(body)), nil), nil
	}
	text := string(body)
	if rec.dropped {
		text += "\n\n[truncated at " + strconv.Itoa(maxToolBytes) +
			" bytes; use the route's offset/limit query parameters to page]"
	}
	if strings.TrimSpace(text) == "" {
		text = "{}"
	}
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
}

// toolError renders §13.1's error envelope as an MCP tool error. The envelope
// is passed through rather than flattened: `details.state` is what a client
// branches on after a 409, and a prose rendering would lose it.
func toolError(code, message string, details json.RawMessage) *mcp.CallToolResult {
	payload := map[string]any{"code": code, "message": message}
	if len(details) > 0 {
		payload["details"] = details
	}
	b, err := json.Marshal(map[string]any{"error": payload})
	if err != nil {
		b = []byte(`{"error":{"code":"internal","message":"error envelope is not encodable"}}`)
	}
	return &mcp.CallToolResult{
		IsError: true,
		Content: []mcp.Content{&mcp.TextContent{Text: string(b)}},
	}
}
