package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/cursor"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/scheduler"
	"github.com/lezli01/vincent/internal/store"
)

// reportStub is a minimal installed Adapter for the reported-quota tests. It
// is local rather than agenttest's fake agent because nothing here spawns
// anything: a reading either arrives through SetQuota or through the
// QuotaReporter this optionally satisfies.
type reportStub struct {
	name  string
	quota *agent.ReportedQuota
}

func (s *reportStub) Name() string { return s.name }

func (s *reportStub) Detect(context.Context) (agent.Availability, error) {
	return agent.Availability{Found: true, Path: "/bin/" + s.name, Version: "1"}, nil
}

func (s *reportStub) Options(context.Context) (agent.Options, error) { return agent.Options{}, nil }
func (s *reportStub) Path() (string, error)                          { return "/bin/" + s.name, nil }
func (s *reportStub) Curated() agent.Options                         { return agent.Options{} }

func (s *reportStub) NewLineParser() agent.LineParser {
	return func(raw []byte) agent.Event { return agent.Event{Type: agent.EventUnknown, Raw: raw} }
}

func (s *reportStub) Start(context.Context, agent.RunSpec) (agent.RunHandle, error) {
	return nil, context.Canceled
}

// reporterStub is a reportStub that answers a quota probe (codex's shape).
type reporterStub struct{ reportStub }

func (s *reporterStub) Quota(context.Context) (*agent.ReportedQuota, error) { return s.quota, nil }

// newReportServer serves /v1/agents, /v1/info and the push route over the
// given adapters and a real store.
func newReportServer(t *testing.T, adapters ...agent.Adapter) (*httptest.Server, *store.Store) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := New(Deps{
		Token: testToken, Config: config.Default, StartedAt: time.Now(),
		ListenAddr: "127.0.0.1:0", RequestStop: func() {},
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:   st,
		Catalog: agent.NewCatalogCache(agent.NewRegistry(adapters...)),
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

func doJSON(t *testing.T, method, url string, body any) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		var buf bytes.Buffer
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
		rdr = &buf
	}
	req, err := http.NewRequestWithContext(t.Context(), method, url, rdr)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return resp
}

// quotaOf reads one adapter's quota block from GET /v1/agents.
func quotaOf(t *testing.T, ts *httptest.Server, name string) map[string]any {
	t.Helper()
	resp := doJSON(t, http.MethodGet, ts.URL+"/v1/agents", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /v1/agents = %d", resp.StatusCode)
	}
	var body struct {
		Agents []struct {
			Name  string         `json:"name"`
			Quota map[string]any `json:"quota"`
		} `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, a := range body.Agents {
		if a.Name == name {
			return a.Quota
		}
	}
	t.Fatalf("agent %q missing from /v1/agents", name)
	return nil
}

func pushReport(t *testing.T, ts *httptest.Server, name string, body any) *http.Response {
	t.Helper()
	return doJSON(t, http.MethodPost, ts.URL+"/v1/agents/"+name+"/quota", body)
}

// reportBody is the wire body of the push route, spelled as a map so the test
// asserts the JSON contract rather than the server's own struct.
func reportBody(source string, windows ...map[string]any) map[string]any {
	return map[string]any{"source": source, "windows": windows}
}

func window(name string, pct float64, label string, resets *time.Time) map[string]any {
	w := map[string]any{"name": name, "used_percent": pct, "window": label}
	if resets != nil {
		w["resets_at"] = resets.UTC().Format(time.RFC3339)
	}
	return w
}

// TestReportedQuotaTightestWindow: two windows both ride the response, and the
// scalars carry the one closest to stopping work. A client written against
// task 026's shape reads `used_percent` and gets the number that matters.
func TestReportedQuotaTightestWindow(t *testing.T) {
	ts, _ := newReportServer(t, &reportStub{name: "claude"})
	soon := time.Now().Add(2 * time.Hour)
	later := time.Now().Add(72 * time.Hour)
	resp := pushReport(t, ts, "claude", reportBody(agent.QuotaSourceClaudeStatusLine,
		window("five_hour", 28, "5h", &soon),
		window("seven_day", 53, "7d", &later)))
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("POST quota = %d, want 204", resp.StatusCode)
	}

	q := quotaOf(t, ts, "claude")
	if got := q["used_percent"]; got != 53.0 {
		t.Errorf("used_percent = %v, want the tighter 53", got)
	}
	if got := q["window"]; got != "7d" {
		t.Errorf("window = %v, want 7d", got)
	}
	if got := q["source"]; got != agent.QuotaSourceClaudeStatusLine {
		t.Errorf("source = %v, want %s", got, agent.QuotaSourceClaudeStatusLine)
	}
	if got := q["resets_at"]; got != later.UTC().Format(time.RFC3339) {
		t.Errorf("resets_at = %v, want the tighter window's %s", got, later.UTC().Format(time.RFC3339))
	}
	ws, ok := q["windows"].([]any)
	if !ok || len(ws) != 2 {
		t.Fatalf("windows = %v, want both windows", q["windows"])
	}
	if first := ws[0].(map[string]any); first["name"] != "five_hour" || first["used_percent"] != 28.0 {
		t.Errorf("windows[0] = %v, want the untightest window kept in full", first)
	}
}

// TestReportedQuotaSpentFromPercent is the single most dangerous detail in the
// change. A reported window's reset is always in the *future* — that is what
// an open window means — so the observed derivation (`now < resets_at`) would
// answer true for every reading vincent ever receives and light the board's
// badge permanently for every user whose adapter reports.
func TestReportedQuotaSpentFromPercent(t *testing.T) {
	future := time.Now().Add(2 * time.Hour)
	for _, tc := range []struct {
		name string
		pct  float64
		want bool
	}{
		{"nearly spent", 99.9, false},
		{"exactly spent", 100, true},
		{"over", 120, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ts, _ := newReportServer(t, &reportStub{name: "claude"})
			pushReport(t, ts, "claude", reportBody(agent.QuotaSourceClaudeStatusLine,
				window("five_hour", tc.pct, "5h", &future)))
			if got := quotaOf(t, ts, "claude")["spent"]; got != tc.want {
				t.Errorf("spent = %v at %v%% with a future reset, want %v", got, tc.pct, tc.want)
			}
		})
	}
}

// TestObservedQuotaSpentFromReset pins the other half of the split: an
// observation is still spent while its reset is in the future, and still
// reports its scalars as null.
func TestObservedQuotaSpentFromReset(t *testing.T) {
	ts, st := newReportServer(t, &reportStub{name: "claude"})
	seedObservation(t, st, "claude", time.Now().Add(time.Hour))

	q := quotaOf(t, ts, "claude")
	switch {
	case q["spent"] != true:
		t.Errorf("spent = %v for an unexpired observation, want true", q["spent"])
	case q["source"] != store.QuotaSourceObserved:
		t.Errorf("source = %v, want observed", q["source"])
	case q["used_percent"] != nil:
		t.Errorf("used_percent = %v, want null — an observation measures nothing", q["used_percent"])
	case q["window"] != nil:
		t.Errorf("window = %v, want null", q["window"])
	}
	if ws, ok := q["windows"].([]any); !ok || len(ws) != 0 {
		t.Errorf("windows = %v, want [] — always an array, never null", q["windows"])
	}
}

// TestReportedSupersedesObserved and its fallbacks: a reading measures a
// window still open and wins; an observation is what is left when there is no
// reading, or when every dated window of one has since reopened.
func TestReportedSupersedesObserved(t *testing.T) {
	observedReset := time.Now().Add(time.Hour)

	t.Run("reported wins", func(t *testing.T) {
		ts, st := newReportServer(t, &reportStub{name: "claude"})
		seedObservation(t, st, "claude", observedReset)
		future := time.Now().Add(2 * time.Hour)
		pushReport(t, ts, "claude", reportBody(agent.QuotaSourceClaudeStatusLine,
			window("five_hour", 28, "5h", &future)))
		if q := quotaOf(t, ts, "claude"); q["source"] != agent.QuotaSourceClaudeStatusLine {
			t.Errorf("source = %v, want the reading to supersede the observation", q["source"])
		}
	})

	t.Run("observed without a reading", func(t *testing.T) {
		ts, st := newReportServer(t, &reportStub{name: "claude"})
		seedObservation(t, st, "claude", observedReset)
		if q := quotaOf(t, ts, "claude"); q["source"] != store.QuotaSourceObserved {
			t.Errorf("source = %v, want observed", q["source"])
		}
	})

	t.Run("observed when the reading has elapsed", func(t *testing.T) {
		ts, st := newReportServer(t, &reportStub{name: "claude"})
		seedObservation(t, st, "claude", observedReset)
		past := time.Now().Add(-time.Minute)
		pushReport(t, ts, "claude", reportBody(agent.QuotaSourceClaudeStatusLine,
			window("five_hour", 28, "5h", &past)))
		if q := quotaOf(t, ts, "claude"); q["source"] != store.QuotaSourceObserved {
			t.Errorf("source = %v, want the elapsed reading to fall back to the observation", q["source"])
		}
	})

	t.Run("undated reading never elapses", func(t *testing.T) {
		ts, st := newReportServer(t, &reportStub{name: "claude"})
		seedObservation(t, st, "claude", observedReset)
		pushReport(t, ts, "claude", reportBody(agent.QuotaSourceClaudeStatusLine,
			window("five_hour", 28, "5h", nil)))
		q := quotaOf(t, ts, "claude")
		if q["source"] != agent.QuotaSourceClaudeStatusLine {
			t.Fatalf("source = %v, want the undated reading to stand", q["source"])
		}
		if q["resets_at_reported"] != false {
			t.Errorf("resets_at_reported = %v, want false for a window that named no reset", q["resets_at_reported"])
		}
	})
}

// TestQuotaProbeFromReporter proves the other arrival path: an adapter
// satisfying QuotaReporter is asked by the cache, with no push involved.
func TestQuotaProbeFromReporter(t *testing.T) {
	stub := &reporterStub{reportStub{name: "codex", quota: &agent.ReportedQuota{
		Source: agent.QuotaSourceCodexAppServer, ReportedAt: time.Now(),
		Windows: []agent.ReportedWindow{{
			Name: "primary", UsedPercent: 61, Window: "5h",
			ResetsAt: time.Now().Add(time.Hour),
		}},
	}}}
	ts, _ := newReportServer(t, stub)

	q := quotaOf(t, ts, "codex")
	if q["source"] != agent.QuotaSourceCodexAppServer || q["used_percent"] != 61.0 {
		t.Errorf("quota = %v, want the probed codex reading", q)
	}
}

// TestCursorQuotaUnchanged is the regression this feature most plausibly
// breaks. cursor has no quota surface (§9.7) and must render byte for byte as
// it did under task 026 — including the `windows: []` normalization, which is
// the one addition every existing client sees.
func TestCursorQuotaUnchanged(t *testing.T) {
	ts, st := newReportServer(t, cursor.New(func() string { return "" }))
	reset := time.Date(2024, 9, 2, 14, 20, 0, 0, time.UTC)
	observed := reset.Add(-time.Hour)
	if _, err := st.UpsertAgentQuota(t.Context(), &store.AgentQuota{
		Agent: "cursor", ObservedAt: observed, ResetsAt: reset,
		ResetsAtReported: true, Source: store.QuotaSourceObserved,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := json.Marshal(quotaOf(t, ts, "cursor"))
	if err != nil {
		t.Fatal(err)
	}
	// The window is long past, so `spent` is false with the observation
	// intact — task 026's "last ran out at, and has since recovered".
	want := `{"observed_at":"2024-09-02T13:20:00Z","resets_at":"2024-09-02T14:20:00Z",` +
		`"resets_at_reported":true,"source":"observed","spent":false,` +
		`"used_percent":null,"window":null,"windows":[]}`
	if string(got) != want {
		t.Errorf("cursor quota block changed\n got: %s\nwant: %s", got, want)
	}
}

// TestQuotaReportRouteErrors: the push route's refusals.
func TestQuotaReportRouteErrors(t *testing.T) {
	ts, _ := newReportServer(t, &reportStub{name: "claude"})
	future := time.Now().Add(time.Hour)
	good := window("five_hour", 28, "5h", &future)

	for _, tc := range []struct {
		name   string
		agent  string
		body   any
		status int
		code   string
	}{
		{
			"unknown adapter", "nope", reportBody(agent.QuotaSourceClaudeStatusLine, good),
			http.StatusNotFound, CodeNotFound,
		},
		{
			"no source", "claude", reportBody("", good),
			http.StatusBadRequest, CodeValidationFailed,
		},
		{
			"no windows", "claude", reportBody(agent.QuotaSourceClaudeStatusLine),
			http.StatusBadRequest, CodeValidationFailed,
		},
		{"unnamed window", "claude", reportBody(agent.QuotaSourceClaudeStatusLine,
			window("", 28, "5h", &future)), http.StatusBadRequest, CodeValidationFailed},
		{"negative percent", "claude", reportBody(agent.QuotaSourceClaudeStatusLine,
			window("five_hour", -1, "5h", &future)), http.StatusBadRequest, CodeValidationFailed},
		// DisallowUnknownFields is API-wide (decodeJSONLimit), so a field
		// this route does not know is refused the way every other route
		// refuses one.
		{
			"unknown field", "claude",
			map[string]any{"source": "x", "nope": 1},
			http.StatusBadRequest, CodeValidationFailed,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			resp := pushReport(t, ts, tc.agent, tc.body)
			if resp.StatusCode != tc.status {
				t.Fatalf("status = %d, want %d", resp.StatusCode, tc.status)
			}
			var body errorBody
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != tc.code {
				t.Errorf("code = %q, want %q", body.Error.Code, tc.code)
			}
		})
	}

	// A body that is not JSON at all takes the same envelope.
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost,
		ts.URL+"/v1/agents/claude/quota", bytes.NewBufferString("{nope"))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("malformed JSON = %d, want 400", resp.StatusCode)
	}
}

// TestQuotaReportEmitsOnChangeOnly: `agent.quota_changed` is a durable event
// and a status line re-renders on every prompt. An identical reading must emit
// nothing, exactly as the upsert path already behaves — and the event must not
// wake the scheduler, because quota is display, never admission.
func TestQuotaReportEmitsOnChangeOnly(t *testing.T) {
	ts, st := newReportServer(t, &reportStub{name: "claude"})
	future := time.Now().Add(time.Hour).UTC().Truncate(time.Second)

	push := func(pct float64) {
		t.Helper()
		if resp := pushReport(t, ts, "claude", reportBody(agent.QuotaSourceClaudeStatusLine,
			window("five_hour", pct, "5h", &future))); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("POST quota = %d, want 204", resp.StatusCode)
		}
	}
	push(28)
	push(28)
	push(31)

	quota, err := st.ListEvents(t.Context(), store.EventFilter{
		Types: []string{store.EventAgentQuotaChanged},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(quota) != 2 {
		t.Fatalf("emitted %d quota events for 3 pushes (2 distinct), want 2", len(quota))
	}
	var payload struct {
		Agent    string  `json:"agent"`
		Spent    bool    `json:"spent"`
		ResetsAt *string `json:"resets_at"`
		Source   *string `json:"source"`
	}
	if err := json.Unmarshal(quota[0].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	switch {
	case payload.Agent != "claude":
		t.Errorf("payload.agent = %q, want claude", payload.Agent)
	case payload.Spent:
		t.Error("payload.spent = true at 28%")
	case payload.ResetsAt == nil || *payload.ResetsAt != future.Format(time.RFC3339):
		t.Errorf("payload.resets_at = %v, want %s", payload.ResetsAt, future.Format(time.RFC3339))
	case payload.Source == nil || *payload.Source != agent.QuotaSourceClaudeStatusLine:
		t.Errorf("payload.source = %v, want %s", payload.Source, agent.QuotaSourceClaudeStatusLine)
	}
	if scheduler.WakeOn(&quota[0]) {
		t.Error("scheduler.WakeOn(agent.quota_changed) = true; quota is display, never admission")
	}
}

// TestQuotaAgreesAcrossEndpoints: /v1/agents and /v1/info serve the same block
// from one read of the catalog, so the board header and the new-task form
// cannot disagree about an adapter.
func TestQuotaAgreesAcrossEndpoints(t *testing.T) {
	ts, st := newReportServer(t, &reportStub{name: "claude"}, &reportStub{name: "cursor"})
	seedObservation(t, st, "cursor", time.Now().Add(time.Hour))
	future := time.Now().Add(2 * time.Hour)
	pushReport(t, ts, "claude", reportBody(agent.QuotaSourceClaudeStatusLine,
		window("five_hour", 28, "5h", &future)))

	agents := map[string]any{}
	for _, name := range []string{"claude", "cursor"} {
		agents[name] = quotaOf(t, ts, name)
	}

	resp := doJSON(t, http.MethodGet, ts.URL+"/v1/info", nil)
	var info struct {
		Agents []struct {
			Name  string         `json:"name"`
			Quota map[string]any `json:"quota"`
		} `json:"agents"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		t.Fatal(err)
	}
	if len(info.Agents) != 2 {
		t.Fatalf("/v1/info listed %d agents, want 2", len(info.Agents))
	}
	for _, a := range info.Agents {
		want, err := json.Marshal(agents[a.Name])
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(a.Quota)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Errorf("%s: /v1/info and /v1/agents disagree\n info: %s\nagents: %s", a.Name, got, want)
		}
	}
}

func seedObservation(t *testing.T, st *store.Store, name string, resets time.Time) {
	t.Helper()
	if _, err := st.UpsertAgentQuota(t.Context(), &store.AgentQuota{
		Agent: name, ObservedAt: time.Now().Add(-time.Minute), ResetsAt: resets,
		ResetsAtReported: true, Source: store.QuotaSourceObserved,
	}); err != nil {
		t.Fatalf("seed observation: %v", err)
	}
}
