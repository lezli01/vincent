package cli

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/agent/codex"
	"github.com/lezli01/vincent/internal/agent/cursor"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/store"
)

// `vincent agents` runs against the real API handlers and a catalog probing
// real fakeagent binaries, on the transcript command's harness: the command
// only renders what GET /v1/agents merged, so only the endpoint can say what
// that is.

// agentsTableRow returns the table line for one adapter.
func agentsTableRow(t *testing.T, out, name string) string {
	t.Helper()
	for line := range strings.SplitSeq(out, "\n") {
		if strings.HasPrefix(line, name+" ") {
			return line
		}
	}
	t.Fatalf("no row for %s in:\n%s", name, out)
	return ""
}

func TestAgentsCommandAgainstTheRealAPI(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	missing := filepath.Join(t.TempDir(), "no-codex-here")
	h := newLiveHarness(t, withAgentCatalog(agent.NewRegistry(
		claude.New(func() string { return fake }),
		codex.New(func() string { return missing }),
		cursor.New(func() string { return fake }),
	)))
	ctx := t.Context()
	c := h.client(t)

	// The wire carries whole seconds, so the expected times do too.
	now := time.Now().UTC().Truncate(time.Second)
	codexReset := now.Add(2 * time.Hour)
	cursorReset := now.Add(15 * time.Minute)
	claudeReset := now.Add(3 * time.Hour)

	// Two observations the daemon watched happen — one reset the CLI stated,
	// one vincent estimated — and a reading claude's status line pushed.
	for _, q := range []*store.AgentQuota{
		{
			Agent: "codex", ObservedAt: now.Add(-10 * time.Minute), ResetsAt: codexReset,
			ResetsAtReported: true, Source: store.QuotaSourceObserved,
		},
		{
			Agent: "cursor", ObservedAt: now.Add(-5 * time.Minute), ResetsAt: cursorReset,
			Source: store.QuotaSourceObserved,
		},
	} {
		if _, err := h.st.UpsertAgentQuota(ctx, q); err != nil {
			t.Fatalf("UpsertAgentQuota(%s): %v", q.Agent, err)
		}
	}
	if err := c.ReportAgentQuota(ctx, "claude", apiclient.AgentQuotaReport{
		Source: apiclient.QuotaSourceClaudeStatusLine,
		Windows: []apiclient.AgentQuotaReportWindow{
			{Name: "five_hour", UsedPercent: 28.47, Window: "5h", ResetsAt: &claudeReset},
			{Name: "seven_day", UsedPercent: 62, Window: "7d"},
		},
	}); err != nil {
		t.Fatalf("ReportAgentQuota: %v", err)
	}

	want, err := c.ListAgents(ctx, false)
	if err != nil {
		t.Fatalf("ListAgents: %v", err)
	}
	claudeAgent, ok := want.Find("claude")
	if !ok || claudeAgent.Quota == nil {
		t.Fatalf("claude carries no quota block: %+v", claudeAgent)
	}

	t.Run("the table", func(t *testing.T) {
		out, errOut, code := runCLI(t, "agents")
		if code != 0 {
			t.Fatalf("agents: code %d, stderr %q", code, errOut)
		}
		if header := strings.SplitN(out, "\n", 2)[0]; strings.Join(strings.Fields(header), " ") !=
			strings.Join(agentsHeader, " ") {
			t.Errorf("header = %q, want %q", header, agentsHeader)
		}
		for name, wants := range map[string][]string{
			"claude": {
				claudeAgent.Version,
				"claude status line · 5h 28.5% → " + agentLocalTime(claudeReset) +
					" · 7d 62% · read " + agentLocalTime(claudeAgent.Quota.ObservedAt),
			},
			"codex":  {"spent → " + agentLocalTime(codexReset), "not found"},
			"cursor": {"spent ≈ " + agentLocalTime(cursorReset)},
		} {
			row := agentsTableRow(t, out, name)
			for _, w := range wants {
				if !strings.Contains(row, w) {
					t.Errorf("%s row does not carry %q:\n%s", name, w, row)
				}
			}
		}
		// A missing adapter has no login to report.
		if cells := strings.Fields(agentsTableRow(t, out, "codex")); len(cells) < 4 ||
			cells[1] != "-" || cells[2] != "-" || cells[3] != "-" {
			t.Errorf("codex row should dash VERSION, BUILD and LOGIN: %q", cells)
		}
	})

	t.Run("--json is the endpoint's array", func(t *testing.T) {
		out, errOut, code := runCLI(t, "agents", "--json")
		if code != 0 {
			t.Fatalf("agents --json: code %d, stderr %q", code, errOut)
		}
		if !strings.HasPrefix(strings.TrimSpace(out), "[") {
			t.Fatalf("--json is not a bare array: %q", out)
		}
		var got apiclient.Agents
		if err := json.Unmarshal([]byte(out), &got); err != nil {
			t.Fatalf("decode --json: %v\n%s", err, out)
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("--json differs from ListAgents:\n got %+v\nwant %+v", got, want)
		}
	})

	t.Run("--refresh reaches the handler", func(t *testing.T) {
		refreshes, cached := h.agentsRefreshes.Load(), h.agentsCached.Load()
		if _, errOut, code := runCLI(t, "agents"); code != 0 {
			t.Fatalf("agents: code %d, stderr %q", code, errOut)
		}
		if got := h.agentsRefreshes.Load() - refreshes; got != 0 {
			t.Errorf("plain `agents` sent %d refresh request(s), want 0", got)
		}
		if got := h.agentsCached.Load() - cached; got != 1 {
			t.Errorf("plain `agents` sent %d cached request(s), want 1", got)
		}

		refreshes, cached = h.agentsRefreshes.Load(), h.agentsCached.Load()
		if _, errOut, code := runCLI(t, "agents", "--refresh"); code != 0 {
			t.Fatalf("agents --refresh: code %d, stderr %q", code, errOut)
		}
		if got := h.agentsRefreshes.Load() - refreshes; got != 1 {
			t.Errorf("`agents --refresh` sent %d refresh request(s), want 1", got)
		}
		if got := h.agentsCached.Load() - cached; got != 0 {
			t.Errorf("`agents --refresh` sent %d cached request(s), want 0", got)
		}
	})
}

// An adapter that is missing or logged out is information, not failure: the
// command exits 0 whenever the daemon answered (task 041 decision 4).
func TestAgentsCommandExitsZeroWhenNoAdapterCanRun(t *testing.T) {
	fake := agenttest.BuildFakeAgent(t)
	missing := filepath.Join(t.TempDir(), "nothing-here")
	t.Setenv("FAKEAGENT_CURSOR_LOGGED_OUT", "1")
	newLiveHarness(t, withAgentCatalog(agent.NewRegistry(
		claude.New(func() string { return missing }),
		codex.New(func() string { return missing }),
		cursor.New(func() string { return fake }),
	)))

	out, errOut, code := runCLI(t, "agents")
	if code != 0 {
		t.Fatalf("agents with no usable adapter: code %d, want 0 (stderr %q)", code, errOut)
	}
	for _, name := range []string{"claude", "codex"} {
		if row := agentsTableRow(t, out, name); !strings.Contains(row, "not found") {
			t.Errorf("%s row does not say it is not found:\n%s", name, row)
		}
	}
	if row := agentsTableRow(t, out, "cursor"); !strings.Contains(row, "NOT LOGGED IN") {
		t.Errorf("cursor row does not say it is logged out:\n%s", row)
	}
	// No record of a usage window is unknown, never ok.
	for _, name := range []string{"claude", "codex", "cursor"} {
		if row := agentsTableRow(t, out, name); !strings.Contains(row, "unknown") {
			t.Errorf("%s row does not render a missing quota as unknown:\n%s", name, row)
		}
	}
}
