package mcptest_test

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lezli01/vincent/internal/mcp/mcptest"
)

// bearer adds the step secret to every request, which is all a streamable-HTTP
// client needs to reach the per-step endpoint.
type bearer string

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}

// TestRecordsAStepStatusCall proves the fixture the adapter tests stand on:
// a step_status call made over a real per-step session reaches the stub route
// with its path values and body. The client is the SDK's, not cmd/fakeagent's,
// so the fixture is known-good independently of the client it will judge.
func TestRecordsAStepStatusCall(t *testing.T) {
	t.Parallel()
	srv := mcptest.New(t)
	client := sdk.NewClient(&sdk.Implementation{Name: "mcptest", Version: "1"}, nil)
	session, err := client.Connect(t.Context(), &sdk.StreamableClientTransport{
		Endpoint:             srv.MCP.URL,
		HTTPClient:           &http.Client{Transport: bearer(srv.MCP.Token)},
		DisableStandaloneSSE: true,
	}, nil)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer func() { _ = session.Close() }()

	res, err := session.CallTool(t.Context(), &sdk.CallToolParams{
		Name: "step_status",
		Arguments: map[string]any{
			"id": mcptest.TaskID, "step_id": mcptest.StepID, "body": map[string]any{"message": "hello"},
		},
	})
	if err != nil {
		t.Fatalf("call step_status: %v", err)
	}
	if res.IsError {
		t.Fatalf("step_status reported an error: %+v", res.Content)
	}
	want := mcptest.StatusCall{TaskID: strconv.FormatInt(mcptest.TaskID, 10), StepID: mcptest.StepID, Message: "hello"}
	if got := srv.StatusCalls(); len(got) != 1 || got[0] != want {
		t.Errorf("status calls = %+v, want exactly %+v", got, want)
	}
}

// TestRefusesAWrongSecret: the endpoint is the per-step one, with its own
// secret, and not an open door.
func TestRefusesAWrongSecret(t *testing.T) {
	t.Parallel()
	srv := mcptest.New(t)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.MCP.URL,
		strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer not-"+srv.MCP.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", resp.StatusCode)
	}
	if got := srv.StatusCalls(); len(got) != 0 {
		t.Errorf("status calls = %+v, want none", got)
	}
}
