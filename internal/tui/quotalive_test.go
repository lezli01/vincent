package tui

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
)

// quotaLiveHarness is the shell against the real API handlers: a real store
// writing real `agent.quota_changed` events through a real broker into the
// shell's own SSE stream. Task 026's client half is a chain — store event →
// SSE → board refetch → header badge — and only an end-to-end run tests the
// links between the pieces rather than the pieces.
type quotaLiveHarness struct {
	st *store.Store
	m  *root
	p  *pump
}

func newQuotaLiveHarness(t *testing.T) *quotaLiveHarness {
	t.Helper()
	const token = "quota-live-token"

	dataDir := t.TempDir()
	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	fake := agenttest.BuildFakeAgent(t)
	reg := agent.NewRegistry(claude.New(func() string { return fake }))
	s := api.New(api.Deps{
		Token:       token,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Broker:      broker,
		Git:         gitx.New(),
		Agents:      reg,
		Catalog:     agent.NewCatalogCache(reg),
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	if err := acknowledgeNotice(dataDir); err != nil {
		t.Fatalf("acknowledgeNotice: %v", err)
	}
	m := newRoot(testCtx(t), liveConnectorIn(t, ts.URL, token, dataDir), dataDir)
	m.width, m.height = 160, 60
	msg := runCmd(t, m.Init(), 10*time.Second)
	if _, ok := msg.(connectedMsg); !ok {
		t.Fatalf("probe = %T, want connectedMsg", msg)
	}
	_, cmd := m.Update(msg)
	p := newPump(t, m, cmd)
	p.until(10*time.Second, "the event stream to go live", func() bool { return m.streamLive })
	return &quotaLiveHarness{st: st, m: m, p: p}
}

// observe writes the observation the engine would write when an agent step
// stops on a spent quota.
func (h *quotaLiveHarness) observe(t *testing.T, agentName string, in time.Duration, reported bool) time.Time {
	t.Helper()
	now := time.Now().UTC()
	resets := now.Add(in)
	if _, err := h.st.UpsertAgentQuota(t.Context(), &store.AgentQuota{
		Agent: agentName, ObservedAt: now, ResetsAt: resets,
		ResetsAtReported: reported, Source: store.QuotaSourceObserved,
	}); err != nil {
		t.Fatalf("UpsertAgentQuota: %v", err)
	}
	return resets
}

// TestBoardHeaderBadgeFollowsTheQuotaEvent is the whole client chain in one
// assertion: the daemon records a spent window, publishes
// `agent.quota_changed`, and the board — which had already fetched /v1/info
// and would otherwise never ask again — refetches and grows the badge.
func TestBoardHeaderBadgeFollowsTheQuotaEvent(t *testing.T) {
	h := newQuotaLiveHarness(t)

	h.p.until(10*time.Second, "the board header to show the adapter", func() bool {
		return strings.Contains(content(h.m), "claude ✓")
	})

	resets := h.observe(t, "claude", 15*time.Minute, true)
	want := quotaMark + resets.Local().Format("15:04")
	h.p.until(10*time.Second, "the quota badge to reach the board header", func() bool {
		return strings.Contains(content(h.m), want)
	})
	if out := content(h.m); strings.Contains(out, "claude ✓") {
		t.Errorf("the board still ticks an adapter that is out of quota:\n%s", out)
	}
}

// TestDaemonViewShowsTheQuotaLine covers the surface that has room to
// explain: the reset with its provenance, and — for an adapter nothing has
// been observed for — "unknown" said out loud rather than left absent.
func TestDaemonViewShowsTheQuotaLine(t *testing.T) {
	h := newQuotaLiveHarness(t)

	_, cmd := h.m.Update(selectViewMsg{id: viewDaemon})
	h.p.push(cmd)
	h.p.until(10*time.Second, "the daemon view to load", func() bool {
		return strings.Contains(content(h.m), "adapters") &&
			strings.Contains(content(h.m), "quota unknown")
	})

	// An estimate, not a fact: no CLI named this reset, so the view must not
	// spell it with the arrow it uses for one the CLI stated.
	resets := h.observe(t, "claude", 15*time.Minute, false)
	_, cmd = h.m.Update(selectViewMsg{id: viewHome})
	h.p.push(cmd)
	_, cmd = h.m.Update(selectViewMsg{id: viewDaemon})
	h.p.push(cmd)
	stamp := resets.Local().Format("15:04")
	h.p.until(10*time.Second, "the quota line to reach the daemon view", func() bool {
		return strings.Contains(content(h.m), "usage limit ≈ "+stamp)
	})
	if out := content(h.m); strings.Contains(out, "usage limit → "+stamp) {
		t.Errorf("a computed reset is rendered as one the CLI stated:\n%s", out)
	}
}

// TestNewTaskWarnsOnASpentQuotaAndStillSubmits is the acceptance criterion
// that matters most, and the one task 003 decision 4 pins: the form warns at
// the moment of choice and does not refuse. Admission is untouched — the task
// is created, and if the window is still shut when it is admitted it parks on
// the ordinary `usage_limit` hold, which is task 003's job, not this one's.
func TestNewTaskWarnsOnASpentQuotaAndStillSubmits(t *testing.T) {
	h := newNewTaskLiveHarness(t)

	resets := time.Now().UTC().Add(15 * time.Minute)
	if _, err := h.st.UpsertAgentQuota(t.Context(), &store.AgentQuota{
		Agent: "claude", ObservedAt: time.Now().UTC(), ResetsAt: resets,
		ResetsAtReported: true, Source: store.QuotaSourceObserved,
	}); err != nil {
		t.Fatalf("UpsertAgentQuota: %v", err)
	}

	h.sendKey(tea.KeyPressMsg{Code: 'n', Text: "n"})
	n := h.form(t)
	h.p.until(10*time.Second, "the form to load its catalogs", func() bool { return n.loaded })

	moveTo(n, ntWorkflow)
	h.sendKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	for i, o := range n.pick.options {
		if o.value == "implement" {
			n.pick.cursor = i
		}
	}
	h.sendKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	h.p.until(10*time.Second, "the resolution to arrive", func() bool {
		_, ok := n.resolved()
		return ok
	})

	// The workflow's agent steps resolve to claude, so the advisory names the
	// window even though the agent override is unset — §8.6 level 4 is a real
	// adapter, and the daemon's own resolution is what says which.
	want := "usage limit until " + resets.Local().Format("15:04")
	if got := n.agentSummary(); !strings.Contains(got, want) {
		t.Errorf("agent summary = %q, want it to carry %q", got, want)
	}

	// And it submits. Nothing here refuses, and nothing here waits.
	moveTo(n, ntTitle)
	h.sendKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	h.typeText("work against a spent window")
	h.sendKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	moveTo(n, ntCreate)
	h.sendKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	h.p.until(10*time.Second, "the task to be created", func() bool {
		tasks, err := h.st.ListTasks(t.Context(), store.TaskFilter{})
		return err == nil && len(tasks) == 1
	})
}
