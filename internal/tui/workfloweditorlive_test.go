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

// The editor against the real handlers over httptest — the *live_test.go
// convention that keeps client and server wire types from drifting. Every
// link is real: real registry, real watcher, real write endpoints, real
// client, real shell.

const editorLiveYAML = `# publish — keep this comment.
name: publish
description: Build then publish.

steps:
  - id: build
    type: command
    run: make build
`

// editorLive stands the whole chain up and returns the root, its pump and
// the global workflow directory.
func editorLive(t *testing.T) (*root, *pump, string) {
	t.Helper()
	const token = "workflow-editor-live-token"
	watchCtx := testCtx(t)

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
	if err := os.MkdirAll(globalDir, 0o750); err != nil {
		t.Fatalf("mkdir workflows: %v", err)
	}
	writeFile(t, filepath.Join(globalDir, "publish.yaml"), editorLiveYAML)

	agents := agent.NewRegistry()
	registry := workflow.NewRegistry(globalDir, workflow.Options{KnownAgents: agents.Names()}, nil)
	registry.SetProjects(map[int64]string{project.ID: repo})
	registry.Reload()
	registryEventHook(t, st, registry)
	if err := registry.Watch(watchCtx); err != nil {
		t.Fatalf("Watch: %v", err)
	}

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
	p.until(10*time.Second, "the event stream to go live", func() bool { return m.streamLive })
	_, cmd = m.Update(selectViewMsg{id: viewWorkflows})
	p.push(cmd)
	p.until(10*time.Second, "the workflows view to load", func() bool {
		return strings.Contains(content(m), "publish")
	})
	return m, p, globalDir
}

func pressKey(t *testing.T, m *root, p *pump, key string) {
	t.Helper()
	_, cmd := m.Update(tea.KeyPressMsg{Code: rune(key[0]), Text: key})
	p.push(cmd)
}

// TestWorkflowEditorEditsThroughToRegistryReload walks the operation the
// issue is about: open the form on an entry, change a field, and see the
// change land in the file — with the file's comments still there.
func TestWorkflowEditorEditsThroughToRegistryReload(t *testing.T) {
	m, p, globalDir := editorLive(t)
	w := m.views[viewWorkflows].(*workflowsView)
	selectWorkflow(t, m, p, "publish")

	pressKey(t, m, p, "i")
	p.until(10*time.Second, "the editor to load the schema and the definition", func() bool {
		return w.editor != nil && w.editor.def != nil
	})
	// The form draws rows, never YAML: that is the whole argument for it.
	if strings.Contains(content(m), "steps:") {
		t.Errorf("the editor rendered YAML:\n%s", content(m))
	}
	if !strings.Contains(content(m), "description") {
		t.Errorf("no description row:\n%s", content(m))
	}

	row := rowIndex(t, w, "description")
	w.editor.cursor = row
	_, cmd := w.editorActivate(), tea.Cmd(nil)
	_ = cmd
	if w.editor.input == nil {
		t.Fatal("enter on a text row did not focus a field")
	}
	w.editor.input.SetValue("Publish it.")
	_, cmd = w.updateEditorInput(tea.KeyPressMsg{Code: tea.KeyEnter})
	p.push(cmd)

	path := filepath.Join(globalDir, "publish.yaml")
	p.until(15*time.Second, "the edit to reach the file", func() bool {
		b, err := os.ReadFile(path)
		return err == nil && strings.Contains(string(b), "Publish it.")
	})
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	// The comment is the fidelity guarantee, on the bytes.
	if !strings.Contains(string(b), "# publish — keep this comment.") {
		t.Errorf("the header comment did not survive the save:\n%s", b)
	}
	if !strings.Contains(string(b), "run: make build") {
		t.Errorf("an untouched step changed:\n%s", b)
	}
}

// TestWorkflowEditorRefusalKeepsTheValueOnScreen: a value the daemon refuses
// is rendered against its field with the refused value still visible, which
// is what makes the error actionable.
func TestWorkflowEditorRefusalKeepsTheValueOnScreen(t *testing.T) {
	m, p, _ := editorLive(t)
	w := m.views[viewWorkflows].(*workflowsView)
	selectWorkflow(t, m, p, "publish")
	pressKey(t, m, p, "i")
	p.until(10*time.Second, "the editor to load", func() bool {
		return w.editor != nil && w.editor.def != nil
	})
	// An empty name is a §8.1 violation the endpoint refuses; nothing is
	// written and the form says so.
	w.editor.cursor = rowIndex(t, w, "name")
	w.editorActivate()
	w.editor.input.SetValue("not a name")
	_, cmd := w.updateEditorInput(tea.KeyPressMsg{Code: tea.KeyEnter})
	p.push(cmd)
	p.until(10*time.Second, "the refusal to render", func() bool { return w.editor.err != "" })
	if !strings.Contains(content(m), "not a name") {
		t.Errorf("the refused value is not on screen beside its error:\n%s", content(m))
	}
}

// TestWorkflowEditorCreateAndFork covers both write paths: a create in the
// global scope, and a fork of a built-in down into a project scope where it
// shadows the original per §5.2.
func TestWorkflowEditorCreateAndFork(t *testing.T) {
	m, p, globalDir := editorLive(t)
	w := m.views[viewWorkflows].(*workflowsView)

	pressKey(t, m, p, "a")
	if w.create == nil {
		t.Fatal("a did not open the create prompt")
	}
	w.create.name.SetValue("fresh")
	p.push(w.submitCreate())
	p.until(15*time.Second, "the new file to appear", func() bool {
		_, err := os.Stat(filepath.Join(globalDir, "fresh.yaml"))
		return err == nil
	})
	p.until(10*time.Second, "the editor to open on it", func() bool {
		return w.editor != nil && w.editor.key.name == "fresh"
	})
	w.editor = nil

	// Fork: the copy keeps the source's own name:, which is what makes it
	// shadow the original.
	selectWorkflow(t, m, p, "adhoc")
	pressKey(t, m, p, "f")
	if w.create == nil || !w.create.fork {
		t.Fatal("f did not open the fork prompt")
	}
	// Scope 1 is the project block; a fork is for forking down.
	if len(w.create.scopes) < 2 || w.create.scopes[1].projectID == 0 {
		t.Fatalf("no project scope to fork into: %+v", w.create.scopes)
	}
	w.create.scope = 1
	w.create.name.SetValue("adhoc")
	p.push(w.submitCreate())
	p.until(15*time.Second, "the fork to land in the project scope", func() bool {
		return w.editor != nil && w.editor.scope == scopeProject
	})
	// §5.2 shadowing is by name, so the copy keeps the source's own name:.
	if w.editor.key.name != "adhoc" {
		t.Errorf("fork name = %q, want the source's own name", w.editor.key.name)
	}
	if !strings.Contains(w.editor.file, filepath.Join(".vincent", "workflows")) {
		t.Errorf("fork file = %q, want the project scope directory", w.editor.file)
	}
}

// selectWorkflow moves the registry cursor onto a named entry.
func selectWorkflow(t *testing.T, m *root, p *pump, name string) {
	t.Helper()
	w := m.views[viewWorkflows].(*workflowsView)
	p.until(10*time.Second, "the entry to appear", func() bool {
		for _, l := range w.lines() {
			if l.entry != nil && l.entry.Name == name {
				return true
			}
		}
		return false
	})
	for i, l := range w.lines() {
		if l.entry != nil && l.entry.Name == name {
			w.cursor = i
			return
		}
	}
	t.Fatalf("no %s entry", name)
}

// rowIndex finds the editor row a field name owns.
func rowIndex(t *testing.T, w *workflowsView, name string) int {
	t.Helper()
	for i, r := range w.editor.rows {
		if r.field.Name == name {
			return i
		}
	}
	t.Fatalf("no %s row: %+v", name, w.editor.rows)
	return 0
}
