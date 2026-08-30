package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/config"
)

const testToken = "test-token-0123456789abcdef"

// newTestServer returns a running test server and a channel receiving one
// element per RequestStop call (a channel, not a counter, so the assertion
// synchronizes with the handler goroutine). cfg lets tests inject a
// panicking Config dependency.
func newTestServer(t *testing.T, cfg func() config.Config) (*httptest.Server, chan struct{}) {
	t.Helper()
	if cfg == nil {
		cfg = config.Default
	}
	stops := make(chan struct{}, 8)
	s := New(Deps{
		Token:       testToken,
		Config:      cfg,
		StartedAt:   time.Now().Add(-time.Minute),
		ListenAddr:  "127.0.0.1:12345",
		RequestStop: func() { stops <- struct{}{} },
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, stops
}

func doRequest(t *testing.T, ts *httptest.Server, method, path, token string) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp, body
}

// wantError asserts the §13.1 envelope shape and code.
func wantError(t *testing.T, resp *http.Response, body []byte, status int, code string) {
	t.Helper()
	if resp.StatusCode != status {
		t.Fatalf("status = %d, want %d (body %s)", resp.StatusCode, status, body)
	}
	var e errorBody
	if err := json.Unmarshal(body, &e); err != nil {
		t.Fatalf("error body is not JSON: %v (%s)", err, body)
	}
	if e.Error.Code != code {
		t.Errorf("error code = %q, want %q", e.Error.Code, code)
	}
	if e.Error.Message == "" {
		t.Error("error message is empty")
	}
}

func TestHealthWithoutAuth(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	resp, body := doRequest(t, ts, http.MethodGet, "/v1/health", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var h struct {
		Status  string `json:"status"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &h); err != nil {
		t.Fatalf("parse health: %v", err)
	}
	if h.Status != "ok" || h.Version == "" {
		t.Errorf("health = %+v, want status ok and non-empty version", h)
	}
}

func TestAuthRejection(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	for name, token := range map[string]string{
		"missing header": "",
		"wrong token":    "not-the-token",
	} {
		resp, body := doRequest(t, ts, http.MethodGet, "/v1/info", token)
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", name, resp.StatusCode)
		}
		wantError(t, resp, body, http.StatusUnauthorized, CodeUnauthorized)
	}
}

func TestInfo(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	resp, body := doRequest(t, ts, http.MethodGet, "/v1/info", testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	var info infoResponse
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatalf("parse info: %v", err)
	}
	if info.Version == "" {
		t.Error("version is empty")
	}
	if info.PID <= 0 {
		t.Errorf("pid = %d, want > 0", info.PID)
	}
	if info.UptimeSeconds < 0 {
		t.Errorf("uptime_seconds = %d, want >= 0", info.UptimeSeconds)
	}
	if info.Listen != "127.0.0.1:12345" {
		t.Errorf("listen = %q", info.Listen)
	}
	if info.MaxParallelTasks != config.Default().MaxParallelTasks {
		t.Errorf("max_parallel_tasks = %d", info.MaxParallelTasks)
	}
}

func TestConfigView(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	resp, body := doRequest(t, ts, http.MethodGet, "/v1/config", testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	var cfg configResponse
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	want := config.Default()
	if cfg.Listen != want.Listen || cfg.LogLevel != want.LogLevel ||
		cfg.MaxParallelTasks != want.MaxParallelTasks ||
		cfg.TranscriptRetentionDays != want.TranscriptRetentionDays {
		t.Errorf("config = %+v, want defaults %+v", cfg, want)
	}
	if cfg.Defaults.AgentTimeout != want.Defaults.AgentTimeout.String() {
		t.Errorf("agent_timeout = %q, want %q", cfg.Defaults.AgentTimeout, want.Defaults.AgentTimeout)
	}
	if !strings.Contains(string(body), `"claude"`) {
		t.Error("agents.claude missing")
	}
	// Both halves of the §10 branch-cleanup pair, and both by name: a client
	// showing only the key that is on would describe a policy that cannot run
	// (task 008).
	if cfg.DeleteEmptyBranchOnArchive != want.DeleteEmptyBranchOnArchive ||
		cfg.DeleteRemoteBranchOnArchive != want.DeleteRemoteBranchOnArchive {
		t.Errorf("branch cleanup = %v/%v, want %v/%v",
			cfg.DeleteEmptyBranchOnArchive, cfg.DeleteRemoteBranchOnArchive,
			want.DeleteEmptyBranchOnArchive, want.DeleteRemoteBranchOnArchive)
	}
	for _, key := range []string{
		"delete_empty_branch_on_archive", "delete_remote_branch_on_archive", "max_task_cost_usd",
	} {
		if !strings.Contains(string(body), key) {
			t.Errorf("%s missing from the config view", key)
		}
	}
	if !strings.Contains(string(body), "max_parallel_tasks") {
		t.Error("response keys are not snake_case")
	}
}

// TestConfigViewServesTheTaskCostCap: the TUI reads no configuration from
// disk (§15), so a cap the daemon is enforcing has to arrive over this
// endpoint — including the zero that means it is off (task 033).
func TestConfigViewServesTheTaskCostCap(t *testing.T) {
	capped := func() config.Config {
		c := config.Default()
		c.MaxTaskCostUSD = 12.5
		return c
	}
	ts, _ := newTestServer(t, capped)
	resp, body := doRequest(t, ts, http.MethodGet, "/v1/config", testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body %s)", resp.StatusCode, body)
	}
	var cfg configResponse
	if err := json.Unmarshal(body, &cfg); err != nil {
		t.Fatalf("parse config: %v", err)
	}
	if cfg.MaxTaskCostUSD != 12.5 {
		t.Errorf("max_task_cost_usd = %v, want 12.5", cfg.MaxTaskCostUSD)
	}
}

func TestNotFoundEnvelope(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	resp, body := doRequest(t, ts, http.MethodGet, "/v1/definitely-not-a-thing", testToken)
	wantError(t, resp, body, http.StatusNotFound, CodeNotFound)
}

func TestMethodNotAllowedEnvelope(t *testing.T) {
	ts, _ := newTestServer(t, nil)
	resp, body := doRequest(t, ts, http.MethodGet, "/v1/daemon/stop", testToken)
	wantError(t, resp, body, http.StatusMethodNotAllowed, CodeMethodNotAllowed)
	if allow := resp.Header.Get("Allow"); allow != http.MethodPost {
		t.Errorf("Allow = %q, want POST", allow)
	}
}

func TestStopEndpoint(t *testing.T) {
	ts, stops := newTestServer(t, nil)
	resp, body := doRequest(t, ts, http.MethodPost, "/v1/daemon/stop", testToken)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body %s)", resp.StatusCode, body)
	}
	select {
	case <-stops:
	case <-time.After(2 * time.Second):
		t.Error("RequestStop was not called")
	}
}

func TestStopRequiresAuth(t *testing.T) {
	ts, stops := newTestServer(t, nil)
	resp, body := doRequest(t, ts, http.MethodPost, "/v1/daemon/stop", "")
	wantError(t, resp, body, http.StatusUnauthorized, CodeUnauthorized)
	select {
	case <-stops:
		t.Error("RequestStop was called despite the auth rejection")
	default:
	}
}

func TestPanicRecovery(t *testing.T) {
	ts, _ := newTestServer(t, func() config.Config { panic("boom") })
	resp, body := doRequest(t, ts, http.MethodGet, "/v1/info", testToken)
	wantError(t, resp, body, http.StatusInternalServerError, CodeInternal)
}
