package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// The `mcp-callback` scenario (task 057.9): a step that finds the vincent MCP
// server the adapter wired it to and calls back into the daemon over it, so
// per-step wiring is proven from a running process rather than from argv
// alone.
//
// The client is hand-rolled rather than the go-sdk's, for two reasons (task
// 057.9 decision 1). Task 057 decision 2 confines the SDK to internal/mcp. And
// none of the real CLIs speak MCP through the Go SDK, so an SDK client talking
// to the SDK server would hide exactly the framing fault they would hit — the
// same reason the m10 gate's client is curl.

// mcpServerName is the registration name every dialect looks the daemon up
// by. It is internal/mcp.ServerName spelled out rather than imported: this
// program is the independent reader of what an adapter wrote, and a rename on
// that side must fail here instead of following along.
const mcpServerName = "vincent"

// mcpProtocolVersion is the revision initialize asks for — the one the m10
// gate's curl client asks for too.
const mcpProtocolVersion = "2025-06-18"

const (
	defaultMCPStatus      = "fakeagent called back over MCP"
	defaultMCPStatusFinal = "fakeagent called back again after the answer"
)

// mcpEndpoint is what a dialect's carrier resolved to.
type mcpEndpoint struct {
	URL   string
	Token string
}

// mcpCallback runs the scenario up to the point where the dialect's own
// success stream takes over. Any failure — a carrier missing a piece, a
// refused connection, a tool error — ends the run on stderr with a nonzero
// exit, never as a silent success: a run that skipped the callback would
// otherwise look exactly like one that made it.
func mcpCallback(d dialect, rd *bufio.Reader) {
	dir, err := os.Getwd()
	if err != nil {
		mcpFail("read the working directory", err)
	}
	ep, err := discoverMCP(d, os.Args[1:], os.Getenv, dir)
	if err != nil {
		mcpFail("find the vincent MCP server", err)
	}
	client, err := dialMCP(ep)
	if err != nil {
		mcpFail("connect to "+ep.URL, err)
	}
	if err := reportStatus(client, envOr("FAKEAGENT_MCP_STATUS", defaultMCPStatus)); err != nil {
		mcpFail("report a status", err)
	}
	// Only claude has mid-run input (§7.4); codex and cursor never park, so
	// there is no awaiting_input to survive and the variable is ignored.
	if d != dialectClaude || os.Getenv("FAKEAGENT_MCP_ASK") != "1" {
		return
	}
	emitText("question answered: " + awaitAnswer(rd))
	// The same session, after the task sat in awaiting_input: the attempt is
	// still live, so its secret must still authenticate.
	if err := reportStatus(client, envOr("FAKEAGENT_MCP_STATUS_FINAL", defaultMCPStatusFinal)); err != nil {
		mcpFail("report a status after the answer", err)
	}
}

func mcpFail(what string, err error) {
	fmt.Fprintf(os.Stderr, "fakeagent: mcp-callback: %s: %v\n", what, err)
	os.Exit(1)
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

// reportStatus calls step_status for the step this process is, addressed the
// way `vincent status` addresses it: §8.5's VINCENT_TASK_ID and
// VINCENT_STEP_ID.
func reportStatus(c *mcpClient, message string) error {
	raw := os.Getenv("VINCENT_TASK_ID")
	taskID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return fmt.Errorf("VINCENT_TASK_ID %q is not a task id", raw)
	}
	stepID := os.Getenv("VINCENT_STEP_ID")
	if stepID == "" {
		return errors.New("VINCENT_STEP_ID is unset")
	}
	_, err = c.callTool("step_status", map[string]any{
		"id":      taskID,
		"step_id": stepID,
		"body":    map[string]any{"message": message},
	})
	return err
}

// discoverMCP reads the MCP server this run was wired to from the carrier the
// dialect's real CLI reads it from (task 057 decision 8). Each refuses a
// carrier missing a piece rather than guessing, because the real CLI would
// not use it either.
func discoverMCP(d dialect, args []string, getenv func(string) string, dir string) (mcpEndpoint, error) {
	switch d {
	case dialectCodex:
		return codexMCP(args, getenv)
	case dialectCursor:
		return cursorMCP(args, dir)
	default:
		return claudeMCP(args, dir)
	}
}

// claudeMCP reads `--mcp-config`, which claude takes as inline JSON or as a
// file path, and requires `--strict-mcp-config` beside it: without that flag
// claude would also load the user's own servers into the step (§9.2).
func claudeMCP(args []string, dir string) (mcpEndpoint, error) {
	value, ok := flagValue(args, "--mcp-config")
	if !ok {
		return mcpEndpoint{}, errors.New("no --mcp-config on argv")
	}
	if !slices.Contains(args, "--strict-mcp-config") {
		return mcpEndpoint{}, errors.New("--mcp-config without --strict-mcp-config would load the user's own servers too")
	}
	body := []byte(value)
	if !strings.HasPrefix(strings.TrimSpace(value), "{") {
		path := value
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return mcpEndpoint{}, fmt.Errorf("read --mcp-config file: %w", err)
		}
		body = b
	}
	var cfg struct {
		Servers map[string]struct {
			Type    string            `json:"type"`
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return mcpEndpoint{}, fmt.Errorf("--mcp-config is not JSON: %w", err)
	}
	entry, ok := cfg.Servers[mcpServerName]
	if !ok {
		return mcpEndpoint{}, fmt.Errorf("--mcp-config has no %q server", mcpServerName)
	}
	if entry.Type != "http" {
		return mcpEndpoint{}, fmt.Errorf("server %q has type %q, want http", mcpServerName, entry.Type)
	}
	return endpointFrom(entry.URL, entry.Headers)
}

// codexMCP reads the `-c mcp_servers.vincent.*` overrides, whose values are
// TOML, and the bearer token from the environment variable
// `bearer_token_env_var` names — codex's indirection, and the only place the
// token reaches this dialect.
func codexMCP(args []string, getenv func(string) string) (mcpEndpoint, error) {
	prefix := "mcp_servers." + mcpServerName + "."
	overrides := map[string]string{}
	for i := 0; i < len(args); i++ {
		if (args[i] != "-c" && args[i] != "--config") || i+1 >= len(args) {
			continue
		}
		i++
		key, raw, ok := strings.Cut(args[i], "=")
		if !ok || !strings.HasPrefix(key, prefix) {
			continue
		}
		v, err := tomlString(raw)
		if err != nil {
			return mcpEndpoint{}, fmt.Errorf("-c %s: %w", key, err)
		}
		overrides[strings.TrimPrefix(key, prefix)] = v
	}
	url := overrides["url"]
	if url == "" {
		return mcpEndpoint{}, fmt.Errorf("no -c %surl override", prefix)
	}
	envName := overrides["bearer_token_env_var"]
	if envName == "" {
		return mcpEndpoint{}, fmt.Errorf("no -c %sbearer_token_env_var override", prefix)
	}
	token := getenv(envName)
	if token == "" {
		return mcpEndpoint{}, fmt.Errorf("bearer_token_env_var names %s, which is unset", envName)
	}
	return mcpEndpoint{URL: url, Token: token}, nil
}

// cursorMCP reads the workspace `.cursor/mcp.json` in the working directory,
// and requires `--approve-mcps`: a headless cursor-agent never starts a
// workspace server nobody approved (§9.7).
func cursorMCP(args []string, dir string) (mcpEndpoint, error) {
	if !slices.Contains(args, "--approve-mcps") {
		return mcpEndpoint{}, errors.New("no --approve-mcps on argv; a workspace MCP server would never start")
	}
	body, err := os.ReadFile(filepath.Join(dir, ".cursor", "mcp.json"))
	if err != nil {
		return mcpEndpoint{}, fmt.Errorf("read workspace mcp config: %w", err)
	}
	var cfg struct {
		Servers map[string]struct {
			URL     string            `json:"url"`
			Headers map[string]string `json:"headers"`
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal(body, &cfg); err != nil {
		return mcpEndpoint{}, fmt.Errorf(".cursor/mcp.json is not JSON: %w", err)
	}
	entry, ok := cfg.Servers[mcpServerName]
	if !ok {
		return mcpEndpoint{}, fmt.Errorf(".cursor/mcp.json has no %q server", mcpServerName)
	}
	return endpointFrom(entry.URL, entry.Headers)
}

func endpointFrom(url string, headers map[string]string) (mcpEndpoint, error) {
	if url == "" {
		return mcpEndpoint{}, fmt.Errorf("server %q has no url", mcpServerName)
	}
	token, ok := strings.CutPrefix(headers["Authorization"], "Bearer ")
	if !ok || token == "" {
		return mcpEndpoint{}, fmt.Errorf("server %q carries no bearer Authorization header", mcpServerName)
	}
	return mcpEndpoint{URL: url, Token: token}, nil
}

// flagValue returns the value of `--flag value` or `--flag=value`.
func flagValue(args []string, flag string) (string, bool) {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1], true
		}
		if v, ok := strings.CutPrefix(a, flag+"="); ok {
			return v, true
		}
	}
	return "", false
}

// tomlString decodes a TOML basic ("…") or literal ('…') string. It is
// stricter than strconv.Unquote on purpose: Go's quoting can emit escapes TOML
// does not have (`\x`, `\a`, `\v`), and codex would not read those.
func tomlString(raw string) (string, error) {
	if len(raw) >= 2 && raw[0] == '\'' && raw[len(raw)-1] == '\'' {
		s := raw[1 : len(raw)-1]
		if strings.ContainsAny(s, "'\n") {
			return "", fmt.Errorf("%s is not a TOML literal string", raw)
		}
		return s, nil
	}
	if len(raw) < 2 || raw[0] != '"' || raw[len(raw)-1] != '"' {
		return "", fmt.Errorf("%s is not a TOML string", raw)
	}
	s := raw[1 : len(raw)-1]
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			return "", fmt.Errorf("unescaped quote in %s", raw)
		case (c < 0x20 && c != '\t') || c == 0x7f:
			return "", fmt.Errorf("control character in %s", raw)
		case c != '\\':
			b.WriteByte(c)
			continue
		}
		i++
		if i >= len(s) {
			return "", fmt.Errorf("trailing backslash in %s", raw)
		}
		switch s[i] {
		case 'b':
			b.WriteByte('\b')
		case 't':
			b.WriteByte('\t')
		case 'n':
			b.WriteByte('\n')
		case 'f':
			b.WriteByte('\f')
		case 'r':
			b.WriteByte('\r')
		case '"', '\\':
			b.WriteByte(s[i])
		case 'u', 'U':
			n := 4
			if s[i] == 'U' {
				n = 8
			}
			if i+n >= len(s) {
				return "", fmt.Errorf("short \\%c escape in %s", s[i], raw)
			}
			code, err := strconv.ParseUint(s[i+1:i+1+n], 16, 32)
			if err != nil || !utf8.ValidRune(rune(code)) {
				return "", fmt.Errorf("bad \\%c escape in %s", s[i], raw)
			}
			b.WriteRune(rune(code))
			i += n
		default:
			return "", fmt.Errorf("\\%c is not a TOML escape (in %s)", s[i], raw)
		}
	}
	return b.String(), nil
}

// mcpClient is the smallest streamable-HTTP MCP client that does the job:
// initialize and keep the session id, send notifications/initialized, call a
// tool. It reads a response in either framing the transport allows — a plain
// application/json body or a text/event-stream frame.
type mcpClient struct {
	endpoint mcpEndpoint
	http     *http.Client
	session  string
	protocol string
	nextID   int64
}

func dialMCP(ep mcpEndpoint) (*mcpClient, error) {
	c := &mcpClient{endpoint: ep, http: &http.Client{Timeout: 30 * time.Second}}
	res, err := c.call("initialize", map[string]any{
		"protocolVersion": mcpProtocolVersion,
		"capabilities":    map[string]any{},
		"clientInfo":      map[string]any{"name": "fakeagent", "version": defaultVersion},
	})
	if err != nil {
		return nil, fmt.Errorf("initialize: %w", err)
	}
	var init struct {
		ProtocolVersion string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(res, &init); err != nil {
		return nil, fmt.Errorf("initialize result: %w", err)
	}
	c.protocol = init.ProtocolVersion
	if err := c.notify("notifications/initialized"); err != nil {
		return nil, fmt.Errorf("notifications/initialized: %w", err)
	}
	return c, nil
}

// callTool calls a tool and returns its text. A result with isError set is an
// error carrying the tool's own text, which is where §13.1's envelope is.
func (c *mcpClient) callTool(name string, args map[string]any) (string, error) {
	res, err := c.call("tools/call", map[string]any{"name": name, "arguments": args})
	if err != nil {
		return "", fmt.Errorf("%s: %w", name, err)
	}
	var out struct {
		IsError bool `json:"isError"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(res, &out); err != nil {
		return "", fmt.Errorf("%s result: %w", name, err)
	}
	var texts []string
	for _, block := range out.Content {
		if block.Type == "text" {
			texts = append(texts, block.Text)
		}
	}
	text := strings.Join(texts, "\n")
	if out.IsError {
		return "", fmt.Errorf("%s reported an error: %s", name, text)
	}
	return text, nil
}

type rpcResponse struct {
	ID     json.RawMessage `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (c *mcpClient) call(method string, params any) (json.RawMessage, error) {
	c.nextID++
	resp, err := c.post(map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method, "params": params})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	msg, err := readRPCResponse(resp, c.nextID)
	if err != nil {
		return nil, err
	}
	if msg.Error != nil {
		return nil, fmt.Errorf("JSON-RPC error %d: %s", msg.Error.Code, msg.Error.Message)
	}
	return msg.Result, nil
}

func (c *mcpClient) notify(method string) error {
	resp, err := c.post(map[string]any{"jsonrpc": "2.0", "method": method, "params": map[string]any{}})
	if err != nil {
		return err
	}
	_, err = io.Copy(io.Discard, resp.Body)
	return errors.Join(err, resp.Body.Close())
}

func (c *mcpClient) post(msg map[string]any) (*http.Response, error) {
	body, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, c.endpoint.URL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.endpoint.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if c.session != "" {
		req.Header.Set("Mcp-Session-Id", c.session)
	}
	if c.protocol != "" {
		req.Header.Set("Mcp-Protocol-Version", c.protocol)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if s := resp.Header.Get("Mcp-Session-Id"); s != "" {
		c.session = s
	}
	return resp, nil
}

// readRPCResponse finds the response to request id in either framing.
func readRPCResponse(resp *http.Response, id int64) (rpcResponse, error) {
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/event-stream") {
		return readSSEResponse(resp.Body, id)
	}
	var msg rpcResponse
	if err := json.NewDecoder(resp.Body).Decode(&msg); err != nil {
		return rpcResponse{}, fmt.Errorf("decode %q response: %w", ct, err)
	}
	if !idIs(msg.ID, id) {
		return rpcResponse{}, fmt.Errorf("response id %s, want %d", msg.ID, id)
	}
	return msg, nil
}

// readSSEResponse reads events until one carries the response to id, skipping
// anything else the server sends first. Lines are read to '\n' and have their
// CR stripped by hand: SSE frames are CRLF-delimited, and a CR left on a
// `data:` line rides into the JSON — the trap the m10 gate documents for curl.
func readSSEResponse(r io.Reader, id int64) (rpcResponse, error) {
	rd := bufio.NewReader(r)
	var data []string
	dispatch := func() (rpcResponse, bool) {
		payload := strings.Join(data, "\n")
		data = nil
		var msg rpcResponse
		if payload == "" || json.Unmarshal([]byte(payload), &msg) != nil || !idIs(msg.ID, id) {
			return rpcResponse{}, false
		}
		return msg, true
	}
	for {
		line, err := rd.ReadString('\n')
		line = strings.TrimRight(line, "\r\n")
		if v, ok := strings.CutPrefix(line, "data:"); ok {
			data = append(data, strings.TrimPrefix(v, " "))
		}
		if line == "" || err != nil {
			if msg, ok := dispatch(); ok {
				return msg, nil
			}
		}
		if err != nil {
			return rpcResponse{}, fmt.Errorf("event stream ended with no response to request %d: %w", id, err)
		}
	}
}

func idIs(raw json.RawMessage, id int64) bool {
	var n int64
	return json.Unmarshal(raw, &n) == nil && n == id
}
