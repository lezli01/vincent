package cli

import (
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The renderers are pure functions of (apiclient.Agent, now), so every cell
// `vincent agents` can print is pinned here without a server. What the
// endpoint actually sends is agents_live_test.go's job.

func TestAgentQuotaCell(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	observed := now.Add(-20 * time.Minute)
	future := now.Add(90 * time.Minute)
	week := now.Add(5 * 24 * time.Hour)
	lt := agentLocalTime
	pct := func(v float64) *float64 { return &v }
	str := func(s string) *string { return &s }

	cases := []struct {
		name string
		q    *apiclient.AgentQuota
		want string
	}{
		{
			// No record is not fine, and it is not empty (task 026 decision 5).
			name: "no block is unknown",
			q:    nil,
			want: "unknown",
		},
		{
			name: "a spent observation with a stated reset",
			q: &apiclient.AgentQuota{
				Spent: true, ObservedAt: observed, ResetsAt: future, ResetsAtReported: true,
				Source: apiclient.QuotaSourceObserved, Windows: []apiclient.AgentQuotaWindow{},
			},
			want: "spent → " + lt(future),
		},
		{
			// vincent's estimate must not read as something the CLI said.
			name: "a spent observation with an estimated reset",
			q: &apiclient.AgentQuota{
				Spent: true, ObservedAt: observed, ResetsAt: future,
				Source: apiclient.QuotaSourceObserved, Windows: []apiclient.AgentQuotaWindow{},
			},
			want: "spent ≈ " + lt(future),
		},
		{
			name: "a lapsed observation",
			q: &apiclient.AgentQuota{
				ObservedAt: now.Add(-3 * time.Hour), ResetsAt: now.Add(-time.Hour), ResetsAtReported: true,
				Source: apiclient.QuotaSourceObserved, Windows: []apiclient.AgentQuotaWindow{},
			},
			want: "ok · last spent " + lt(now.Add(-3*time.Hour)),
		},
		{
			// The wire's Spent is the daemon's answer at fetch time; the
			// cell re-derives it, so a window that reopened since says so.
			name: "an observation the wire still calls spent, lapsed by now",
			q: &apiclient.AgentQuota{
				Spent: true, ObservedAt: observed, ResetsAt: now.Add(-time.Minute),
				Source: apiclient.QuotaSourceObserved, Windows: []apiclient.AgentQuotaWindow{},
			},
			want: "ok · last spent " + lt(observed),
		},
		{
			name: "a two-window reading",
			q: &apiclient.AgentQuota{
				UsedPercent: pct(62), Window: str("7d"), ObservedAt: observed,
				ResetsAt: week, ResetsAtReported: true, Source: apiclient.QuotaSourceCodexAppServer,
				Windows: []apiclient.AgentQuotaWindow{
					{Name: "primary", UsedPercent: 28.47, Window: "5h", ResetsAt: future, ResetsAtReported: true},
					{Name: "secondary", UsedPercent: 62, Window: "7d", ResetsAt: week, ResetsAtReported: true},
				},
			},
			want: "codex app-server · 5h 28.5% → " + lt(future) + " · 7d 62% → " + lt(week) +
				" · read " + lt(observed),
		},
		{
			// A window with no label falls back to its wire name; one with no
			// reset prints no time at all, never the zero clock.
			name: "a window with no reset and no label",
			q: &apiclient.AgentQuota{
				UsedPercent: pct(40), ObservedAt: observed, Source: apiclient.QuotaSourceClaudeStatusLine,
				Windows: []apiclient.AgentQuotaWindow{{Name: "five_hour", UsedPercent: 40}},
			},
			want: "claude status line · five_hour 40% · read " + lt(observed),
		},
		{
			name: "an unknown source prints verbatim",
			q: &apiclient.AgentQuota{
				UsedPercent: pct(5), ObservedAt: observed, Source: "gemini_quota_api",
				Windows: []apiclient.AgentQuotaWindow{{Name: "daily", UsedPercent: 5, Window: "1d"}},
			},
			want: "gemini_quota_api · 1d 5% · read " + lt(observed),
		},
		{
			name: "a spent reading prints its percentage as it is",
			q: &apiclient.AgentQuota{
				Spent: true, UsedPercent: pct(100), Window: str("5h"), ObservedAt: observed,
				ResetsAt: future, ResetsAtReported: true, Source: apiclient.QuotaSourceClaudeStatusLine,
				Windows: []apiclient.AgentQuotaWindow{
					{Name: "five_hour", UsedPercent: 100, Window: "5h", ResetsAt: future, ResetsAtReported: true},
				},
			},
			want: "claude status line · 5h 100% → " + lt(future) + " · read " + lt(observed),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := agentQuotaCell(tc.q, now)
			if got != tc.want {
				t.Errorf("agentQuotaCell:\n got %q\nwant %q", got, tc.want)
			}
			if strings.Contains(got, "0001-01-01") || strings.Contains(got, "00:00:00Z") {
				t.Errorf("agentQuotaCell rendered the zero time: %q", got)
			}
		})
	}
}

func TestAgentLoginCell(t *testing.T) {
	yes, no := true, false
	cases := []struct {
		name string
		a    apiclient.Agent
		want string
	}{
		// §9.5: an adapter that cannot tell is never accused.
		{"cannot tell", apiclient.Agent{Available: true}, "unknown"},
		{"logged out", apiclient.Agent{Available: true, LoggedIn: &no}, "NOT LOGGED IN"},
		{"logged in", apiclient.Agent{Available: true, LoggedIn: &yes}, "ok"},
		{"not installed", apiclient.Agent{LoggedIn: &no}, "-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := agentLoginCell(tc.a); got != tc.want {
				t.Errorf("agentLoginCell = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAgentRowNotesAreBadNewsOnly(t *testing.T) {
	now := time.Date(2026, 9, 16, 12, 0, 0, 0, time.UTC)
	yes := true
	healthy := apiclient.Agent{
		Name: "claude", Available: true, Version: "2.1.3", LoggedIn: &yes,
		InputVerdict:      apiclient.InputVerdictSupported,
		VersionVerdict:    apiclient.VersionVerdictTested,
		RestrictedVerdict: apiclient.RestrictedVerdictSupported,
	}

	t.Run("a healthy adapter", func(t *testing.T) {
		want := []string{"claude", "2.1.3", "tested", "ok", "unknown", ""}
		if got := agentRow(healthy, now); strings.Join(got, "|") != strings.Join(want, "|") {
			t.Errorf("agentRow = %q, want %q", got, want)
		}
	})

	probeErr := "exit status 1"
	cases := []struct {
		name   string
		mutate func(*apiclient.Agent)
		col    int
		want   string
	}{
		{"untested build", func(a *apiclient.Agent) { a.VersionVerdict = apiclient.VersionVerdictUntested }, 2, "untested"},
		{"incompatible build", func(a *apiclient.Agent) { a.VersionVerdict = apiclient.VersionVerdictIncompatible }, 2, "incompatible"},
		{"no verdict", func(a *apiclient.Agent) { a.VersionVerdict = "" }, 2, "-"},
		{"no version", func(a *apiclient.Agent) { a.Version = "" }, 1, "-"},
		{"no mid-run input", func(a *apiclient.Agent) { a.InputVerdict = apiclient.InputVerdictUnsupported }, 5, "no mid-run input"},
		// Unknown is not a no, exactly as the daemon's own gate reads it.
		{"input unknown", func(a *apiclient.Agent) { a.InputVerdict = apiclient.InputVerdictUnknown }, 5, ""},
		{
			"no restricted mode",
			func(a *apiclient.Agent) { a.RestrictedVerdict = apiclient.RestrictedVerdictUnsupported },
			5, "no restricted mode on " + runtime.GOOS,
		},
		{"option probe failed", func(a *apiclient.Agent) { a.ProbeError = &probeErr }, 5, "option probe failed (curated catalog)"},
		{
			"not found",
			func(a *apiclient.Agent) {
				a.Available, a.Version, a.VersionVerdict, a.Error = false, "", "", `exec: "codex": not found`
			},
			5, `not found: exec: "codex": not found`,
		},
		{
			"everything wrong at once",
			func(a *apiclient.Agent) {
				a.Available, a.InputVerdict = false, apiclient.InputVerdictUnsupported
				a.RestrictedVerdict, a.ProbeError = apiclient.RestrictedVerdictUnsupported, &probeErr
			},
			5, "not found; no mid-run input; no restricted mode on " + runtime.GOOS +
				"; option probe failed (curated catalog)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a := healthy
			tc.mutate(&a)
			row := agentRow(a, now)
			if len(row) != len(agentsHeader) {
				t.Fatalf("row has %d cells, header %d: %q", len(row), len(agentsHeader), row)
			}
			if row[tc.col] != tc.want {
				t.Errorf("%s = %q, want %q (row %q)", agentsHeader[tc.col], row[tc.col], tc.want, row)
			}
		})
	}
}
