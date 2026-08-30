package api

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lezli01/vincent/internal/mcp"
)

// bearerRoundTripper presents the §13.1 daemon token, which is how an MCP
// client authenticates against `/mcp` (task 057 decision 1).
type bearerRoundTripper struct {
	base  http.RoundTripper
	token string
}

func (rt bearerRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) {
	clone := r.Clone(r.Context())
	clone.Header.Set("Authorization", "Bearer "+rt.token)
	return rt.base.RoundTrip(clone) //nolint:wrapcheck // a transport must not wrap
}

// TestMCPLiveToolList wires a real MCP client to the real handler over
// httptest — the *live_test.go convention that keeps client and server wire
// types from drifting. What drifts here is the tool schemas: a schema the SDK
// refuses is a schema no client can call, and that is not visible from a
// table-driven test of the table.
func TestMCPLiveToolList(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, nil)
	httpClient := ts.Client()
	httpClient.Transport = bearerRoundTripper{base: httpClient.Transport, token: testToken}

	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(t.Context(),
		&sdk.StreamableClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatalf("connect to /mcp: %v", err)
	}
	defer func() { _ = sess.Close() }()

	res, err := sess.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatalf("tools/list: %v", err)
	}
	got := make([]string, 0, len(res.Tools))
	for _, tool := range res.Tools {
		got = append(got, tool.Name)
		if tool.Description == "" {
			t.Errorf("tool %q has no description; it is what a model reads to decide", tool.Name)
		}
	}
	slices.Sort(got)
	want := mcp.Names()
	if !slices.Equal(got, want) {
		t.Errorf("tools = %v, want %v", got, want)
	}
	for _, absent := range []string{"daemon_stop", "daemon_backup", "project_delete", "gc", "doctor_fix"} {
		if slices.Contains(got, absent) {
			t.Errorf("%q is served; destructive admin is deliberately not a tool", absent)
		}
	}
}

// TestMCPLiveToolCall calls a tool end to end and gets the route's own JSON
// back — which is the whole of decision 3: the same handler ran.
func TestMCPLiveToolCall(t *testing.T) {
	t.Parallel()
	ts, _ := newTestServer(t, nil)
	httpClient := ts.Client()
	httpClient.Transport = bearerRoundTripper{base: httpClient.Transport, token: testToken}

	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(t.Context(),
		&sdk.StreamableClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatalf("connect to /mcp: %v", err)
	}
	defer func() { _ = sess.Close() }()

	res, err := sess.CallTool(t.Context(), &sdk.CallToolParams{Name: "health"})
	if err != nil {
		t.Fatalf("tools/call health: %v", err)
	}
	if res.IsError {
		t.Fatalf("health returned an error: %+v", res.Content)
	}
	if len(res.Content) == 0 {
		t.Fatal("health returned no content")
	}
	text, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("content is %T, want text", res.Content[0])
	}
	if !strings.Contains(text.Text, `"status":"ok"`) {
		t.Errorf("health = %s, want the route's own body", text.Text)
	}
}
