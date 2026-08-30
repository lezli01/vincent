package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lezli01/vincent/internal/config"
)

// callConfigGetTool calls the tool over a real MCP client, so what is asserted
// is what a model would actually receive — not what a helper reconstructed.
func callConfigGetTool(t *testing.T, ts *httptest.Server) string {
	t.Helper()
	httpClient := ts.Client()
	httpClient.Transport = bearerRoundTripper{base: httpClient.Transport, token: testToken}
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(t.Context(),
		&sdk.StreamableClientTransport{Endpoint: ts.URL + "/mcp", HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatalf("connect to /mcp: %v", err)
	}
	defer func() { _ = sess.Close() }()
	res, err := sess.CallTool(t.Context(), &sdk.CallToolParams{Name: "config_get"})
	if err != nil {
		t.Fatalf("tools/call config_get: %v", err)
	}
	if res.IsError {
		t.Fatalf("config_get returned an error: %+v", res.Content)
	}
	text, ok := res.Content[0].(*sdk.TextContent)
	if !ok {
		t.Fatalf("content is %T, want text", res.Content[0])
	}
	return text.Text
}

// Task 060 decision 3: the HTTP rendering of GET /v1/config serves the file in
// full, and the MCP rendering of the same route masks `environment.set`'s
// values and `notify.command`'s argv — because a tool result lands in an
// agent's context and in its transcript, which is not the loopback-plus-0600
// boundary the HTTP body sits behind.
//
// The assertion is that the two bodies differ in exactly those two fields and
// nowhere else. A redaction that quietly grew would fail it just as a
// redaction that stopped working would.
func TestMCPConfigGetRedactsOnlyTheTwoSecretFields(t *testing.T) {
	cfg := config.Default()
	cfg.Environment.Set = map[string]string{"API_TOKEN": "s3cret", "LANG": "C.UTF-8"}
	cfg.Notify.Command = []string{"/usr/local/bin/notify", "https://hooks.example/T0/B0/xoxb"}
	ts, _ := newTestServer(t, func() config.Config { return cfg })

	_, httpBody := doRequest(t, ts, http.MethodGet, "/v1/config", testToken)
	toolBody := callConfigGetTool(t, ts)

	var direct, viaTool map[string]any
	if err := json.Unmarshal(httpBody, &direct); err != nil {
		t.Fatalf("parse http body: %v", err)
	}
	if err := json.Unmarshal([]byte(toolBody), &viaTool); err != nil {
		t.Fatalf("parse tool body %q: %v", toolBody, err)
	}

	// The HTTP body is unredacted: that is decision 2, and a test that only
	// checked the mask would pass with both sides hidden.
	if got := direct["environment"].(map[string]any)["set"].(map[string]any)["API_TOKEN"]; got != "s3cret" {
		t.Errorf("the HTTP body redacted environment.set: %v", got)
	}
	if got := direct["notify"].(map[string]any)["command"].([]any)[1]; got != "https://hooks.example/T0/B0/xoxb" {
		t.Errorf("the HTTP body redacted notify.command: %v", got)
	}

	// The tool body masks the values and keeps the names.
	set := viaTool["environment"].(map[string]any)["set"].(map[string]any)
	for name, v := range set {
		if v != "[redacted]" {
			t.Errorf("environment.set[%s] reached the tool as %v", name, v)
		}
	}
	if len(set) != 2 {
		t.Errorf("the variable names were dropped as well as the values: %v", set)
	}
	for _, v := range viaTool["notify"].(map[string]any)["command"].([]any) {
		if v != "[redacted]" {
			t.Errorf("notify.command reached the tool as %v", v)
		}
	}

	// Nothing else moved.
	direct["environment"] = nil
	viaTool["environment"] = nil
	direct["notify"] = nil
	viaTool["notify"] = nil
	if !reflect.DeepEqual(direct, viaTool) {
		t.Errorf("the two renderings differ outside environment and notify:\nhttp=%v\ntool=%v", direct, viaTool)
	}
}
