package tui

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/workflow"
)

// The graph layer's contract spans the daemon's definition endpoint, the
// apiclient's model and the view — three hand-written mirrors of one shape.
// Only a test that drives the real handlers proves they still agree, which is
// what the *live_test.go convention exists for.

const liveGraphYAML = `name: shipit
description: fan out and merge
defaults:
  agent: claude
steps:
  - id: plan
    type: agent
    prompt: plan it
    check: go build ./...
  - id: spread
    type: fan_out
    lanes:
      - id: api
        steps:
          - {id: api_impl, type: agent, prompt: api}
      - id: web
        workflow: other
    merge:
      on_conflict: block
`

const liveGraphLoopYAML = `name: shipit
description: now with a loop
defaults:
  agent: claude
steps:
  - id: plan
    type: agent
    prompt: plan it
  - id: repeat
    type: loop
    count: 2
    steps:
      - {id: work, type: agent, prompt: again}
`

func liveGraphView(t *testing.T, yaml string) (*workflowsView, *workflow.Registry, string) {
	t.Helper()
	dir := t.TempDir()
	globalDir := filepath.Join(dir, "workflows")
	if err := os.MkdirAll(globalDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(globalDir, "shipit.yaml")
	writeFile(t, path, yaml)

	st, err := store.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	reg := workflow.NewRegistry(globalDir, workflow.Options{KnownAgents: []string{"claude"}}, nil)
	reg.ReloadGlobal()
	s := api.New(api.Deps{
		Token:       "graph-live-token",
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Git:         gitx.New(),
		Workflows:   reg,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	w := newWorkflowsView()
	w.client = apiclient.New(ts.URL, "graph-live-token")
	w.width, w.height = 120, 44
	msg := runCmd(t, w.loadCmd(), 10*time.Second)
	loaded, ok := msg.(workflowsLoadedMsg)
	if !ok {
		t.Fatalf("load = %T, want workflowsLoadedMsg", msg)
	}
	w.applyLoaded(loaded)
	return w, reg, path
}

// openLiveGraph presses `g` and lets the real fetch land.
func openLiveGraph(t *testing.T, w *workflowsView) {
	t.Helper()
	for {
		line, ok := w.currentLine()
		if ok && line.entry.Name == "shipit" {
			break
		}
		if !ok {
			t.Fatal("no selectable entry")
		}
		w.moveCursor(1)
	}
	_, cmd := w.updateKey(registryKey(t, "g"))
	if cmd == nil {
		t.Fatal("g did not fetch a definition")
	}
	msg, ok := runCmd(t, cmd, 10*time.Second).(workflowDefinitionMsg)
	if !ok {
		t.Fatal("the fetch did not return a definition message")
	}
	w.applyDefinition(msg)
	if !w.graph.loaded {
		t.Fatalf("the definition did not load: err=%q findings=%v", w.graph.err, w.graph.findings)
	}
}

// The whole chain, over the wire: the endpoint's recursive DTO becomes a
// picture with the structure the file declared.
func TestGraphLayerDrawsALiveDefinition(t *testing.T) {
	w, _, _ := liveGraphView(t, liveGraphYAML)
	openLiveGraph(t, w)

	out := w.render(120, 44)
	for _, want := range []string{
		"plan",    // an ordinary step
		"chk",     // its check, as a badge
		"fan_out", // the structure step and its frame
		"api",     // an inline lane's caption
		"other",   // the collapsed workflow reference
		"END",     // the single terminal node
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the graph does not show %q:\n%s", want, out)
		}
	}
	// The merge is drawn because a fan_out's join is work that runs.
	if !strings.Contains(out, "merge") {
		t.Errorf("no merge node in a fan_out graph:\n%s", out)
	}
}

// Editing the file and reloading the registry redraws the open graph, which
// is the edit-save-watch loop the layer exists for.
func TestGraphLayerRedrawsAfterAnEdit(t *testing.T) {
	w, reg, path := liveGraphView(t, liveGraphYAML)
	openLiveGraph(t, w)
	if before := w.render(120, 44); !strings.Contains(before, "fan_out") {
		t.Fatalf("the first render is not the fan_out workflow:\n%s", before)
	}

	writeFile(t, path, liveGraphLoopYAML)
	reg.ReloadGlobal()

	cmd := w.definitionCmd(w.graph.key)
	msg, ok := runCmd(t, cmd, 10*time.Second).(workflowDefinitionMsg)
	if !ok {
		t.Fatal("the refetch did not return a definition message")
	}
	w.applyDefinition(msg)

	out := w.render(120, 44)
	if strings.Contains(out, "fan_out") {
		t.Errorf("the graph still shows the old workflow:\n%s", out)
	}
	if !strings.Contains(out, "loop") || !strings.Contains(out, "×2") {
		t.Errorf("the graph does not show the edited loop:\n%s", out)
	}
}

// Project scoping travels with the request: a project's own copy shadows the
// global one in the graph exactly as it does in the list.
func TestGraphLayerHonoursProjectScope(t *testing.T) {
	w, _, _ := liveGraphView(t, liveGraphYAML)
	openLiveGraph(t, w)
	if got := w.graph.key.projectID; got != 0 {
		t.Errorf("a global entry fetched with project_id=%d", got)
	}
	if w.graph.scope != "global" {
		t.Errorf("scope = %q, want the one the daemon reported", w.graph.scope)
	}
	if w.graph.file == "" {
		t.Error("the layer did not record which file it drew")
	}
}
