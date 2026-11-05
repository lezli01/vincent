package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/jira"
	"github.com/lezli01/vincent/internal/jira/fakejira"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// Task 142.12: GET /v1/projects/{id}/jira, probed against the fake Jira
// over httptest.

// jiraProbeToken is distinctive so a leak is findable by grep.
const (
	jiraProbeUser  = "probe-user@example.com"
	jiraProbeToken = "jira-probe-secret-7f3c9a1e"
)

type jiraHarness struct {
	ts  *httptest.Server
	st  *store.Store
	cfg config.Config
	env []string
}

// newJiraHarness serves the API with cfg (default when zero-valued fields
// are wanted, pass config.Default()) and env as the process environment.
func newJiraHarness(t *testing.T, cfg config.Config, env []string) *jiraHarness {
	t.Helper()
	st, err := storetest.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	h := &jiraHarness{st: st, cfg: cfg, env: env}
	s := New(Deps{
		Token:       testToken,
		Config:      func() config.Config { return h.cfg },
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Environ:     func() []string { return h.env },
	})
	h.ts = httptest.NewServer(s.Handler())
	t.Cleanup(h.ts.Close)
	return h
}

func (h *jiraHarness) project(t *testing.T, src store.IssueSource) int64 {
	t.Helper()
	p := &store.Project{
		Name: "p" + strconv.FormatInt(projectSeq.Add(1), 10), Path: t.TempDir(),
		DefaultBranch: "main", IssueSource: src,
	}
	if err := h.st.CreateProject(t.Context(), p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	return p.ID
}

func (h *jiraHarness) probe(t *testing.T, id int64) (jiraResponse, []byte) {
	t.Helper()
	resp, body := doRequest(t, h.ts, http.MethodGet, "/v1/projects/"+strconv.FormatInt(id, 10)+"/jira", testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("probe = %d, want 200: %s", resp.StatusCode, body)
	}
	var out jiraResponse
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	return out, body
}

func probeEnv() []string {
	return []string{"JIRA_USER=" + jiraProbeUser, "JIRA_API_TOKEN=" + jiraProbeToken}
}

// fakeSite starts the fake Jira with the probe credential, wrapped by mw
// when non-nil, and a scenario file the test may write.
func fakeSite(t *testing.T, mw func(http.Handler) http.Handler) (site, scenario string) {
	t.Helper()
	dir := t.TempDir()
	scenario = filepath.Join(dir, "scenario")
	var h http.Handler = &fakejira.Store{ScenarioFile: scenario, User: jiraProbeUser, Token: jiraProbeToken}
	if mw != nil {
		h = mw(h)
	}
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts.URL, scenario
}

func jiraBinding(site, key string) store.IssueSource {
	return store.IssueSource{Provider: store.IssueProviderJira, JiraSite: site, JiraProjectKey: key}
}

func TestJiraProbeAvailable(t *testing.T) {
	site, _ := fakeSite(t, nil)
	h := newJiraHarness(t, config.Default(), probeEnv())
	got, body := h.probe(t, h.project(t, jiraBinding(site, "VIN")))
	if !got.Available || got.Reason != "" || !got.Enabled {
		t.Fatalf("probe = %s, want available", body)
	}
	if got.Site != site || got.ProjectKey != "VIN" || got.ProjectID != "10000" {
		t.Errorf("binding fields = %+v", got)
	}
	if got.Server == nil || got.Server.DeploymentType != "Cloud" || got.Server.Version == "" {
		t.Errorf("server = %+v", got.Server)
	}
	if got.Account == nil || got.Account.DisplayName == "" {
		t.Errorf("account = %+v", got.Account)
	}
	// The reason is always present, empty when ok.
	var raw map[string]any
	_ = json.Unmarshal(body, &raw)
	if r, ok := raw["reason"]; !ok || r != "" {
		t.Errorf("reason field = %v (present %v), want \"\"", r, ok)
	}
}

func TestJiraProbeReasons(t *testing.T) {
	forbidProject := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasPrefix(r.URL.Path, "/rest/api/3/project/") {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_, _ = w.Write([]byte(`{"errorMessages":["You do not have permission"]}`))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
	garbage := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{not json`))
		})
	}

	for _, tc := range []struct {
		name     string
		mw       func(http.Handler) http.Handler
		scenario string
		key      string
		env      []string
		reason   string
		server   bool
		account  bool
	}{
		{name: "unauthorized", scenario: fakejira.ScenarioUnauthorized, key: "VIN", env: probeEnv(), reason: jira.ReasonUnauthorized},
		{name: "rate limited", scenario: fakejira.ScenarioRateLimited, key: "VIN", env: probeEnv(), reason: jira.ReasonRateLimited},
		{name: "scenario unreachable", scenario: fakejira.ScenarioUnreachable, key: "VIN", env: probeEnv(), reason: jira.ReasonUnreachable},
		{name: "wrong token", key: "VIN", env: []string{"JIRA_USER=" + jiraProbeUser, "JIRA_API_TOKEN=nope"}, reason: jira.ReasonUnauthorized},
		{name: "project missing", key: "NOPE", env: probeEnv(), reason: jira.ReasonNotFound, server: true, account: true},
		{name: "project forbidden", mw: forbidProject, key: "VIN", env: probeEnv(), reason: jira.ReasonForbidden, server: true, account: true},
		{name: "bad response", mw: garbage, key: "VIN", env: probeEnv(), reason: jira.ReasonBadResponse},
		{name: "no token", key: "VIN", env: []string{"JIRA_USER=" + jiraProbeUser}, reason: jira.ReasonNoCredential},
		{name: "no variables", key: "VIN", env: nil, reason: jira.ReasonNoCredential},
	} {
		t.Run(tc.name, func(t *testing.T) {
			site, scenario := fakeSite(t, tc.mw)
			if tc.scenario != "" {
				if err := os.WriteFile(scenario, []byte(tc.scenario+"\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			h := newJiraHarness(t, config.Default(), tc.env)
			got, body := h.probe(t, h.project(t, jiraBinding(site, tc.key)))
			if got.Available || got.Reason != tc.reason || got.Message == "" {
				t.Fatalf("probe = %s, want reason %s with a message", body, tc.reason)
			}
			if (got.Server != nil) != tc.server || (got.Account != nil) != tc.account {
				t.Errorf("server %v account %v, want %v %v: %s", got.Server, got.Account, tc.server, tc.account, body)
			}
			if got.Site != site || got.ProjectKey != tc.key {
				t.Errorf("binding = %q %q, want %q %q", got.Site, got.ProjectKey, site, tc.key)
			}
			if strings.Contains(string(body), jiraProbeToken) {
				t.Errorf("probe body carries the token: %s", body)
			}
		})
	}
}

// A site with nothing listening: the connection is refused, no HTTP answer.
func TestJiraProbeClosedListener(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	site := "http://" + ln.Addr().String()
	_ = ln.Close()
	h := newJiraHarness(t, config.Default(), probeEnv())
	got, body := h.probe(t, h.project(t, jiraBinding(site, "VIN")))
	if got.Available || got.Reason != jira.ReasonUnreachable || got.Server != nil {
		t.Fatalf("probe = %s, want unreachable", body)
	}
	if strings.Contains(string(body), jiraProbeToken) {
		t.Errorf("probe body carries the token: %s", body)
	}
}

// A site that accepts the connection and never answers: the probe gives up
// inside its own budget and answers unreachable, well before the loopback
// client's ten-second deadline would replace that answer with a timeout
// (review F3 on #943).
func TestJiraProbeSiteThatNeverAnswers(t *testing.T) {
	if jiraProbeBudget >= 10*time.Second {
		t.Fatalf("jiraProbeBudget %v does not sit under the CLI's 10 s request deadline", jiraProbeBudget)
	}
	defer func(b time.Duration) { jiraProbeBudget = b }(jiraProbeBudget)
	jiraProbeBudget = 200 * time.Millisecond
	release := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	t.Cleanup(ts.Close)
	t.Cleanup(func() { close(release) })
	h := newJiraHarness(t, config.Default(), probeEnv())
	start := time.Now()
	got, body := h.probe(t, h.project(t, jiraBinding(ts.URL, "VIN")))
	if got.Available || got.Reason != jira.ReasonUnreachable {
		t.Fatalf("probe = %s, want unreachable", body)
	}
	if took := time.Since(start); took > 5*time.Second {
		t.Errorf("probe took %v, want it held to its budget", took)
	}
}

// Disabled and not-Jira projects answer without calling Jira at all.
func TestJiraProbeMakesNoCallWhenOffOrNotJira(t *testing.T) {
	var calls atomic.Int32
	site, _ := fakeSite(t, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			next.ServeHTTP(w, r)
		})
	})
	cfg := config.Default()
	cfg.Jira.Enabled = false
	h := newJiraHarness(t, cfg, probeEnv())

	got, body := h.probe(t, h.project(t, jiraBinding(site, "VIN")))
	if got.Enabled || got.Available || got.Reason != syncReasonJiraDisabled || got.Site != site {
		t.Errorf("disabled probe = %s, want reason %s", body, syncReasonJiraDisabled)
	}

	h.cfg.Jira.Enabled = true
	for _, src := range []store.IssueSource{
		{Provider: store.IssueProviderNone},
		{Provider: store.IssueProviderGitHub},
		{Provider: store.IssueProviderDerive},
	} {
		got, body := h.probe(t, h.project(t, src))
		if got.Available || got.Reason != jiraReasonNotJira || got.Site != "" {
			t.Errorf("%s probe = %s, want reason %s", src.Provider, body, jiraReasonNotJira)
		}
	}
	if n := calls.Load(); n != 0 {
		t.Errorf("the probe called Jira %d times", n)
	}
}

func TestJiraProbeUnknownProject(t *testing.T) {
	h := newJiraHarness(t, config.Default(), probeEnv())
	resp, body := doRequest(t, h.ts, http.MethodGet, "/v1/projects/9999/jira", testToken)
	wantError(t, resp, body, http.StatusNotFound, CodeNotFound)
}

// The token reaches no probe answer and no MCP result: neither project_jira
// nor config_get, even with the token in environment.set (which /v1/config
// serves in full over HTTP by decision — task 060 decision 2 — and the MCP
// rendering masks).
func TestJiraProbeTokenNeverLeaksToMCP(t *testing.T) {
	site, scenario := fakeSite(t, nil)
	cfg := config.Default()
	cfg.Environment.Set = map[string]string{"JIRA_USER": jiraProbeUser, "JIRA_API_TOKEN": jiraProbeToken}
	h := newJiraHarness(t, cfg, nil)
	id := h.project(t, jiraBinding(site, "VIN"))

	httpClient := h.ts.Client()
	httpClient.Transport = bearerRoundTripper{base: httpClient.Transport, token: testToken}
	client := sdk.NewClient(&sdk.Implementation{Name: "test", Version: "0"}, nil)
	sess, err := client.Connect(t.Context(),
		&sdk.StreamableClientTransport{Endpoint: h.ts.URL + "/mcp", HTTPClient: httpClient}, nil)
	if err != nil {
		t.Fatalf("connect to /mcp: %v", err)
	}
	defer func() { _ = sess.Close() }()
	call := func(name string, args map[string]any) string {
		t.Helper()
		res, err := sess.CallTool(t.Context(), &sdk.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatalf("tools/call %s: %v", name, err)
		}
		var b strings.Builder
		for _, c := range res.Content {
			if text, ok := c.(*sdk.TextContent); ok {
				b.WriteString(text.Text)
			}
		}
		return b.String()
	}

	for _, sc := range []string{"", fakejira.ScenarioUnauthorized, fakejira.ScenarioRateLimited, fakejira.ScenarioUnreachable} {
		if err := os.WriteFile(scenario, []byte(sc+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		got, body := h.probe(t, id)
		if sc == "" && !got.Available {
			t.Fatalf("probe with the credential in environment.set = %s, want available", body)
		}
		if strings.Contains(string(body), jiraProbeToken) {
			t.Errorf("scenario %q: probe body carries the token", sc)
		}
		out := call("project_jira", map[string]any{"id": id})
		if !strings.Contains(out, `"site"`) {
			t.Errorf("scenario %q: project_jira answered %q", sc, out)
		}
		if strings.Contains(out, jiraProbeToken) {
			t.Errorf("scenario %q: project_jira carries the token", sc)
		}
	}
	if out := call("config_get", nil); strings.Contains(out, jiraProbeToken) || !strings.Contains(out, "JIRA_API_TOKEN") {
		t.Errorf("config_get leaked the token or lost the name: %s", out)
	}
}
