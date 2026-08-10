package tui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/workflow"
)

// bareLiveYAML names no agent anywhere: the §8.6 level-4 case the registry
// listing cannot answer.
const bareLiveYAML = `name: bare
description: One step, no agent named.
steps:
  - id: work
    type: agent
    prompt: do the thing
`

// TestWorkflowsViewNamesTheAdapterDefault is T4.7's done-when for the
// registry view, proved against the real handlers: a step with no agent
// names the adapter that will run it.
//
// It is a live test rather than a unit test because the whole point of the
// task is that this answer belongs to the daemon — §8.6 resolution stays
// server-side (PR L decision). A stubbed resolution would assert the TUI
// renders what it was handed, which was never in doubt; what needed proving
// is that the daemon and the client agree on the wire.
func TestWorkflowsViewNamesTheAdapterDefault(t *testing.T) {
	const token = "resolve-live-token"

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	repo := testrepo.Init(t, "main")
	project := &store.Project{Name: "app", Path: repo, DefaultBranch: "main"}
	if err := st.CreateProject(context.Background(), project); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}

	globalDir := filepath.Join(t.TempDir(), "workflows")
	if err := os.MkdirAll(globalDir, 0o755); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	writeFile(t, filepath.Join(globalDir, "bare.yaml"), bareLiveYAML)

	agents := agent.NewRegistry()
	registry := workflow.NewRegistry(globalDir, workflow.Options{KnownAgents: agents.Names()}, nil)
	registry.Reload()

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
		Agents:      agents,
		Workflows:   registry,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	m := newRoot(testCtx(t), liveConnector(t, ts.URL, token), ackedDir(t))
	msg := runCmd(t, m.Init(), 10*time.Second)
	if _, ok := msg.(connectedMsg); !ok {
		t.Fatalf("probe = %T, want connectedMsg", msg)
	}
	_, cmd := m.Update(msg)
	p := newPump(t, m, cmd)

	_, cmd = m.Update(selectViewMsg{id: viewWorkflows})
	p.push(cmd)
	p.until(10*time.Second, "the bare workflow to reach the view", func() bool {
		return strings.Contains(content(m), "bare")
	})

	// enter expands the entry under the cursor into its step list.
	_, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	p.push(cmd)
	p.until(10*time.Second, "the resolved adapter to be named", func() bool {
		return strings.Contains(content(m), "→ "+agent.DefaultAgent)
	})
	if strings.Contains(content(m), "adapter default") {
		t.Errorf("the step is still described instead of named:\n%s", content(m))
	}
}
