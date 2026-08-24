package api

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// newQuotaServer serves /v1/agents and /v1/info over one installed adapter
// and a real store, so the quota block is read the way the daemon reads it.
func newQuotaServer(t *testing.T) (*httptest.Server, *store.Store) {
	t.Helper()
	fake := agenttest.BuildFakeAgent(t)
	reg := agent.NewRegistry(claude.New(func() string { return fake }))
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
		Catalog: agent.NewCatalogCache(reg),
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return ts, st
}

func fetchAgentQuota(t *testing.T, ts *httptest.Server) *quotaResponse {
	t.Helper()
	resp, body := doRequest(t, ts, http.MethodGet, "/v1/agents", testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("agents: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Agents []agentResponse `json:"agents"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode agents: %v", err)
	}
	if len(out.Agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(out.Agents))
	}
	return out.Agents[0].Quota
}

func fetchInfoQuota(t *testing.T, ts *httptest.Server) *quotaResponse {
	t.Helper()
	resp, body := doRequest(t, ts, http.MethodGet, "/v1/info", testToken)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("info: %d %s", resp.StatusCode, body)
	}
	var out struct {
		Agents []AgentStatus `json:"agents"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		t.Fatalf("decode info: %v", err)
	}
	if len(out.Agents) != 1 {
		t.Fatalf("agents = %d, want 1", len(out.Agents))
	}
	return out.Agents[0].Quota
}

// TestAgentQuotaNullUntilObserved: `null`, never a zeroed block. A zero here
// would read as "empty quota", which is the opposite of what it means, and it
// is the state every adapter is in until a window actually closes.
func TestAgentQuotaNullUntilObserved(t *testing.T) {
	ts, _ := newQuotaServer(t)
	if q := fetchAgentQuota(t, ts); q != nil {
		t.Errorf("/v1/agents quota = %+v before anything was observed, want null", q)
	}
	if q := fetchInfoQuota(t, ts); q != nil {
		t.Errorf("/v1/info quota = %+v before anything was observed, want null", q)
	}
}

// TestAgentQuotaBlockOnBothEndpoints: the board header reads /v1/info while
// the new-task form reads /v1/agents, so both carry the block and neither can
// disagree with the other about the same adapter.
func TestAgentQuotaBlockOnBothEndpoints(t *testing.T) {
	ts, st := newQuotaServer(t)
	observed := time.Now().UTC()
	resets := observed.Add(15 * time.Minute)
	if _, err := st.UpsertAgentQuota(t.Context(), &store.AgentQuota{
		Agent: "claude", ObservedAt: observed, ResetsAt: resets,
		ResetsAtReported: true, Source: store.QuotaSourceObserved,
	}); err != nil {
		t.Fatalf("UpsertAgentQuota: %v", err)
	}

	for name, got := range map[string]*quotaResponse{
		"/v1/agents": fetchAgentQuota(t, ts),
		"/v1/info":   fetchInfoQuota(t, ts),
	} {
		if got == nil {
			t.Fatalf("%s quota is null after an observation", name)
		}
		if !got.Spent {
			t.Errorf("%s spent = false inside the window", name)
		}
		if !got.ResetsAtReported || got.Source != store.QuotaSourceObserved {
			t.Errorf("%s = %+v, want a CLI-reported, observed window", name, got)
		}
		if got.ResetsAt != resets.Format(time.RFC3339) {
			t.Errorf("%s resets_at = %q, want %q", name, got.ResetsAt, resets.Format(time.RFC3339))
		}
		// Permanently null today, declared so clients are written once
		// against the final shape (§9.2, §9.3, §9.7).
		if got.UsedPercent != nil || got.Window != nil {
			t.Errorf("%s used_percent/window = %v/%v, want null — nothing can fill them",
				name, got.UsedPercent, got.Window)
		}
	}
}

// TestAgentQuotaSpentFalseAfterTheWindowReopens: a lapsed reset does not
// delete the row, so `spent: false` with observed_at and resets_at intact is
// how "this window closed at 14:05 and has since reopened" is said.
func TestAgentQuotaSpentFalseAfterTheWindowReopens(t *testing.T) {
	ts, st := newQuotaServer(t)
	observed := time.Now().UTC().Add(-time.Hour)
	resets := observed.Add(15 * time.Minute)
	if _, err := st.UpsertAgentQuota(t.Context(), &store.AgentQuota{
		Agent: "claude", ObservedAt: observed, ResetsAt: resets,
		ResetsAtReported: false, Source: store.QuotaSourceObserved,
	}); err != nil {
		t.Fatalf("UpsertAgentQuota: %v", err)
	}

	got := fetchAgentQuota(t, ts)
	if got == nil {
		t.Fatal("quota is null; a lapsed window is still an observation")
	}
	if got.Spent {
		t.Error("spent = true for a window that reset an hour ago")
	}
	if got.ObservedAt != observed.Format(time.RFC3339) || got.ResetsAt != resets.Format(time.RFC3339) {
		t.Errorf("quota = %+v, want the observation kept as context", got)
	}
	if got.ResetsAtReported {
		t.Error("resets_at_reported = true; this reset was the recheck interval's estimate")
	}
}
