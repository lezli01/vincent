package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// These are internal tests, unlike the rest of the package's: config
// discovery and the client are what a refusal is made of, and driving each
// refusal through a compiled binary would test the exit code rather than the
// reason. The adapter tests (TestStartMCPCallback) run the compiled scenario
// against the real server.

const testMCPURL = "http://127.0.0.1:7777/mcp/step/9"

func noEnv(string) string { return "" }

func checkDiscovery(t *testing.T, ep mcpEndpoint, err error, wantErr string) {
	t.Helper()
	if wantErr != "" {
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Fatalf("err = %v, want one mentioning %q", err, wantErr)
		}
		return
	}
	if err != nil {
		t.Fatalf("discoverMCP: %v", err)
	}
	if ep != (mcpEndpoint{URL: testMCPURL, Token: "s3cret"}) {
		t.Errorf("endpoint = %+v, want %s with token s3cret", ep, testMCPURL)
	}
}

// TestMCPDiscoverClaude reads the carrier internal/agent/claude renders:
// inline `--mcp-config` JSON beside `--strict-mcp-config`.
func TestMCPDiscoverClaude(t *testing.T) {
	t.Parallel()
	inline := `{"mcpServers":{"vincent":{"type":"http","url":"` + testMCPURL +
		`","headers":{"Authorization":"Bearer s3cret"}}}}`
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mcp.json"), []byte(inline), 0o600); err != nil {
		t.Fatal(err)
	}
	strict := "--strict-mcp-config"
	tests := []struct {
		name    string
		args    []string
		wantErr string
	}{
		{"inline config", []string{"-p", "--mcp-config", inline, strict}, ""},
		{"config file", []string{"--mcp-config", "mcp.json", strict}, ""},
		{"without strict", []string{"--mcp-config", inline}, strict},
		{"no config", []string{"-p", strict}, "no --mcp-config"},
		{"not http", []string{"--mcp-config", strings.Replace(inline, `"http"`, `"sse"`, 1), strict}, "want http"},
		{"another name", []string{"--mcp-config", strings.Replace(inline, `"vincent"`, `"other"`, 1), strict}, `no "vincent" server`},
		{"no bearer", []string{"--mcp-config", strings.Replace(inline, "Bearer ", "Basic ", 1), strict}, "bearer"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ep, err := discoverMCP(dialectClaude, tt.args, noEnv, dir)
			checkDiscovery(t, ep, err, tt.wantErr)
		})
	}
}

// TestMCPDiscoverCodex reads `-c mcp_servers.vincent.*` TOML overrides and the
// token from the variable they name, which is the only place codex's token is.
func TestMCPDiscoverCodex(t *testing.T) {
	t.Parallel()
	url := `mcp_servers.vincent.url="` + testMCPURL + `"`
	envVar := `mcp_servers.vincent.bearer_token_env_var="VINCENT_MCP_TOKEN"`
	withToken := func(name string) string {
		if name == "VINCENT_MCP_TOKEN" {
			return "s3cret"
		}
		return ""
	}
	tests := []struct {
		name    string
		args    []string
		getenv  func(string) string
		wantErr string
	}{
		{"overrides and env", []string{"exec", "--json", "-c", url, "-c", envVar, "-"}, withToken, ""},
		{"token variable unset", []string{"exec", "-c", url, "-c", envVar}, noEnv, "unset"},
		{"no url", []string{"exec", "-c", envVar}, withToken, "url override"},
		{"no variable name", []string{"exec", "-c", url}, withToken, "bearer_token_env_var override"},
		{"value not TOML", []string{"exec", "-c", "mcp_servers.vincent.url=" + testMCPURL, "-c", envVar}, withToken, "not a TOML string"},
		{"another server", []string{"exec", "-c", strings.Replace(url, "vincent", "other", 1), "-c", envVar}, withToken, "url override"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ep, err := discoverMCP(dialectCodex, tt.args, tt.getenv, t.TempDir())
			checkDiscovery(t, ep, err, tt.wantErr)
		})
	}
}

// TestMCPTOMLString pins the decoding codex applies to a `-c` value, including
// the Go escapes it would not read.
func TestMCPTOMLString(t *testing.T) {
	t.Parallel()
	good := map[string]string{
		`"plain"`:            "plain",
		`"a\"b\\c"`:          `a"b\c`,
		`"tab\there"`:        "tab\there",
		`"\u00e9\U0001F600"`: "é😀",
		`'C:\literal'`:       `C:\literal`,
	}
	for raw, want := range good {
		if got, err := tomlString(raw); err != nil || got != want {
			t.Errorf("tomlString(%s) = %q, %v; want %q", raw, got, err, want)
		}
	}
	for _, raw := range []string{`plain`, `"\x41"`, `"\a"`, `"unterminated`, `"trailing\"`, `"\u12"`, `"in"side"`} {
		if got, err := tomlString(raw); err == nil {
			t.Errorf("tomlString(%s) = %q, want an error", raw, got)
		}
	}
}

// TestMCPDiscoverCursor reads the workspace `.cursor/mcp.json` and requires
// `--approve-mcps` beside it.
func TestMCPDiscoverCursor(t *testing.T) {
	t.Parallel()
	body := `{"mcpServers":{"vincent":{"url":"` + testMCPURL + `","headers":{"Authorization":"Bearer s3cret"}}}}`
	withFile := t.TempDir()
	if err := os.MkdirAll(filepath.Join(withFile, ".cursor"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(withFile, ".cursor", "mcp.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"-p", "--output-format", "stream-json", "--trust", "--approve-mcps"}
	tests := []struct {
		name    string
		args    []string
		dir     string
		wantErr string
	}{
		{"workspace file", args, withFile, ""},
		{"without approve", []string{"-p", "--trust"}, withFile, "--approve-mcps"},
		{"no file", args, t.TempDir(), "workspace mcp config"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ep, err := discoverMCP(dialectCursor, tt.args, noEnv, tt.dir)
			checkDiscovery(t, ep, err, tt.wantErr)
		})
	}
}

// TestMCPClientFraming drives the client against a server answering in each
// framing streamable HTTP allows. The SSE leg terminates every line with CRLF
// and sends an unrelated notification ahead of the response, so both the CR
// and the skip are exercised.
func TestMCPClientFraming(t *testing.T) {
	t.Parallel()
	for _, sse := range []bool{false, true} {
		name := "json"
		if sse {
			name = "sse"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			srv := newFramingServer(t, sse)
			c, err := dialMCP(mcpEndpoint{URL: srv.URL, Token: "s3cret"})
			if err != nil {
				t.Fatalf("dialMCP: %v", err)
			}
			text, err := c.callTool("step_status", map[string]any{"message": "hello"})
			if err != nil || text != `{"message":"hello"}` {
				t.Errorf("callTool = %q, %v; want the tool's text", text, err)
			}
			if _, err := c.callTool("step_status", map[string]any{"message": "boom"}); err == nil ||
				!strings.Contains(err.Error(), "refused") {
				t.Errorf("an isError result returned %v, want an error carrying the tool's text", err)
			}
			want := "initialize,notifications/initialized,tools/call,tools/call"
			if got := strings.Join(srv.methods(), ","); got != want {
				t.Errorf("server saw %s, want %s", got, want)
			}
		})
	}
}

type framingServer struct {
	*httptest.Server
	mu  sync.Mutex
	got []string
}

func (s *framingServer) methods() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.got...)
}

// newFramingServer is a stand-in MCP server that checks what a streamable-HTTP
// client must send — the bearer token, an Accept naming both framings, and
// after initialize the session id and protocol version it was given.
func newFramingServer(t *testing.T, sse bool) *framingServer {
	t.Helper()
	srv := &framingServer{}
	srv.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params struct {
				Arguments map[string]any `json:"arguments"`
			} `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		accept := r.Header.Get("Accept")
		if r.Header.Get("Authorization") != "Bearer s3cret" ||
			!strings.Contains(accept, "application/json") || !strings.Contains(accept, "text/event-stream") {
			http.Error(w, "bad auth or accept", http.StatusUnauthorized)
			return
		}
		if req.Method != "initialize" && (r.Header.Get("Mcp-Session-Id") != "sess-1" ||
			r.Header.Get("Mcp-Protocol-Version") != mcpProtocolVersion) {
			http.Error(w, "no session", http.StatusBadRequest)
			return
		}
		srv.mu.Lock()
		srv.got = append(srv.got, req.Method)
		srv.mu.Unlock()

		var result any
		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "sess-1")
			result = map[string]any{"protocolVersion": mcpProtocolVersion}
		case "tools/call":
			msg, _ := req.Params.Arguments["message"].(string)
			text, _ := json.Marshal(map[string]string{"message": msg})
			if msg == "boom" {
				text = []byte(`{"error":{"code":"refused","message":"no"}}`)
			}
			result = map[string]any{
				"isError": msg == "boom",
				"content": []any{map[string]any{"type": "text", "text": string(text)}},
			}
		default: // a notification: accepted, no body
			w.WriteHeader(http.StatusAccepted)
			return
		}
		payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		if !sse {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(payload)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message\r\ndata: {\"jsonrpc\":\"2.0\",\"method\":\"notifications/message\",\"params\":{}}\r\n\r\n")
		_, _ = io.WriteString(w, "event: message\r\nid: 1\r\ndata: "+string(payload)+"\r\n\r\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}
