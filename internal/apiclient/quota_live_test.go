package apiclient_test

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
)

// quotaAdapter is a minimal installed Adapter. It is local rather than the
// fake agent because nothing here spawns anything — the reading arrives
// through the push route.
type quotaAdapter struct{ name string }

func (a *quotaAdapter) Name() string { return a.name }

func (a *quotaAdapter) Detect(context.Context) (agent.Availability, error) {
	return agent.Availability{Found: true, Path: "/bin/" + a.name, Version: "1"}, nil
}

func (a *quotaAdapter) Options(context.Context) (agent.Options, error) { return agent.Options{}, nil }

func (a *quotaAdapter) Path() (string, error)  { return "/bin/" + a.name, nil }
func (a *quotaAdapter) Curated() agent.Options { return agent.Options{} }

func (a *quotaAdapter) NewLineParser() agent.LineParser {
	return func(raw []byte) agent.Event { return agent.Event{Type: agent.EventUnknown, Raw: raw} }
}

func (a *quotaAdapter) Start(context.Context, agent.RunSpec) (agent.RunHandle, error) {
	return nil, context.Canceled
}

func newQuotaClient(t *testing.T) *apiclient.Client {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := api.New(api.Deps{
		Token:       testToken,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Catalog:     agent.NewCatalogCache(agent.NewRegistry(&quotaAdapter{name: "claude"})),
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return apiclient.New(ts.URL, testToken)
}

// TestReportAgentQuotaOverTheWire runs the push and the read against the real
// handlers, which is the only thing that proves the client's request type and
// the server's DTO agree about `windows` — a rename on either side would
// silently drop every window and leave the scalars nulled.
func TestReportAgentQuotaOverTheWire(t *testing.T) {
	c := newQuotaClient(t)
	soon := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	later := time.Now().Add(72 * time.Hour).UTC().Truncate(time.Second)

	err := c.ReportAgentQuota(t.Context(), "claude", apiclient.AgentQuotaReport{
		Source: apiclient.QuotaSourceClaudeStatusLine,
		Windows: []apiclient.AgentQuotaReportWindow{
			{Name: "five_hour", UsedPercent: 28, Window: "5h", ResetsAt: &soon},
			{Name: "seven_day", UsedPercent: 53, Window: "7d", ResetsAt: &later},
		},
	})
	if err != nil {
		t.Fatalf("ReportAgentQuota: %v", err)
	}

	agents, err := c.ListAgents(t.Context(), false)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	a, ok := agents.Find("claude")
	if !ok {
		t.Fatal("claude missing from the catalog")
	}
	q := a.Quota
	if q == nil {
		t.Fatal("quota = nil after a push")
	}
	switch {
	case q.Source != apiclient.QuotaSourceClaudeStatusLine:
		t.Errorf("source = %q, want %q", q.Source, apiclient.QuotaSourceClaudeStatusLine)
	case q.UsedPercent == nil || *q.UsedPercent != 53:
		t.Errorf("used_percent = %v, want the tighter 53", q.UsedPercent)
	case q.Window == nil || *q.Window != "7d":
		t.Errorf("window = %v, want 7d", q.Window)
	case len(q.Windows) != 2:
		t.Fatalf("windows = %+v, want both", q.Windows)
	case q.Windows[0].Name != "five_hour" || q.Windows[0].UsedPercent != 28:
		t.Errorf("windows[0] = %+v, want the five-hour window intact", q.Windows[0])
	case !q.Windows[1].ResetsAt.Equal(later) || !q.Windows[1].ResetsAtReported:
		t.Errorf("windows[1] = %+v, want the reported reset %s", q.Windows[1], later)
	}

	// The client re-derives Spent, and for a reading it must derive it from
	// the percentage: this window's reset is two hours out, and the observed
	// derivation would call it spent.
	if q.SpentAt(time.Now()) {
		t.Error("SpentAt = true at 53% with a future reset — the badge would never go out")
	}
	if a.QuotaSpent(time.Now()) {
		t.Error("QuotaSpent disagrees with SpentAt")
	}

	// An unknown adapter is a 404 the client surfaces rather than swallows.
	err = c.ReportAgentQuota(t.Context(), "nope", apiclient.AgentQuotaReport{
		Source:  apiclient.QuotaSourceClaudeStatusLine,
		Windows: []apiclient.AgentQuotaReportWindow{{Name: "five_hour", UsedPercent: 1}},
	})
	if err == nil {
		t.Fatal("ReportAgentQuota on an unknown adapter = nil, want a 404")
	}
}

// TestAgentQuotaSpentSplit pins the derivation on the client side, where the
// board badge is actually drawn. Getting it wrong the other way is the whole
// hazard: a reported window's reset is always in the future, so the observed
// comparison would light the badge permanently for every reporting adapter.
func TestAgentQuotaSpentSplit(t *testing.T) {
	now := time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	pct := func(v float64) *float64 { return &v }

	for _, tc := range []struct {
		name  string
		quota *apiclient.AgentQuota
		want  bool
	}{
		{"observed, still shut", &apiclient.AgentQuota{
			Source: apiclient.QuotaSourceObserved, ResetsAt: now.Add(time.Hour),
		}, true},
		{"observed, reopened", &apiclient.AgentQuota{
			Source: apiclient.QuotaSourceObserved, ResetsAt: now.Add(-time.Hour),
		}, false},
		{"reported, under", &apiclient.AgentQuota{
			Source: apiclient.QuotaSourceClaudeStatusLine, UsedPercent: pct(99.9),
			ResetsAt: now.Add(time.Hour),
		}, false},
		{"reported, spent", &apiclient.AgentQuota{
			Source: apiclient.QuotaSourceCodexAppServer, UsedPercent: pct(100),
			ResetsAt: now.Add(time.Hour),
		}, true},
		{"reported without a percentage", &apiclient.AgentQuota{
			Source: apiclient.QuotaSourceCodexAppServer, ResetsAt: now.Add(time.Hour),
		}, false},
		// A daemon too old to send a source is read as an observation, which
		// is what it was sending.
		{"no source", &apiclient.AgentQuota{ResetsAt: now.Add(time.Hour)}, true},
		{"no quota", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.quota.SpentAt(now); got != tc.want {
				t.Errorf("SpentAt = %v, want %v", got, tc.want)
			}
		})
	}
}
