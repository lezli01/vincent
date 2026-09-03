package tui

import (
	"bytes"
	"context"
	"fmt"
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
	writeFile(t, filepath.Join(globalDir, "prompt.yaml"), editorPromptYAML())
	// CRLF on purpose: task 065.2's byte-fidelity criterion covers line
	// endings, and a Windows author's file must come back with the endings it
	// arrived with.
	writeFile(t, filepath.Join(globalDir, "blocks.yaml"),
		strings.ReplaceAll(editorBlocksYAML, "\n", "\r\n"))

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

// editorPromptYAML is the file the data-loss regression is asserted on: a
// 60-line `prompt:` block scalar with blank lines and an embedded YAML
// example, which is exactly the value the one-line field used to flatten.
func editorPromptYAML() string {
	var b strings.Builder
	b.WriteString("# prompt — keep this comment.\n")
	b.WriteString("name: prompt\n")
	b.WriteString("description: A long prompt.\n\n")
	b.WriteString("steps:\n")
	b.WriteString("  - id: plan\n")
	b.WriteString("    type: agent\n")
	b.WriteString("    prompt: |\n")
	for i := 1; i <= 40; i++ {
		if i%7 == 0 {
			// Blank lines inside a block scalar are part of the value and are
			// the first thing a flattening round trip loses.
			b.WriteString("\n")
			continue
		}
		fmt.Fprintf(&b, "      Paragraph %d of the instruction.\n", i)
	}
	b.WriteString("\n")
	b.WriteString("      An example, in YAML:\n\n")
	b.WriteString("      steps:\n")
	b.WriteString("        - id: example\n")
	b.WriteString("          type: command\n")
	b.WriteString("          run: git status\n\n")
	b.WriteString("      That is the whole instruction.\n")
	b.WriteString("\n  # the check runs after the agent\n")
	b.WriteString("  - id: verify\n")
	b.WriteString("    type: command\n")
	b.WriteString("    run: git status\n")
	return b.String()
}

// editorBlocksYAML carries the three sequences the structural keys address:
// declared fields, top-level steps, and a fan_out's lanes.
const editorBlocksYAML = `# blocks — keep this comment.
name: blocks
description: Fields, a fan-out and its lanes.

fields:
  # the branch the lanes work on
  - name: branch
    type: string
  - name: mode
    type: enum
    values: [fast, slow]

steps:
  - id: setup
    type: command
    run: git status

  # the fan-out below is what the move key is exercised on
  - id: spread
    type: fan_out
    lanes:
      - id: alpha
        steps:
          - id: alpha-work
            type: command
            run: git status
      - id: beta
        steps:
          - id: beta-work
            type: command
            run: git status
`

// openEditorOn opens the structured editor on a named entry.
func openEditorOn(t *testing.T, m *root, p *pump, name string) *workflowsView {
	t.Helper()
	w := m.views[viewWorkflows].(*workflowsView)
	selectWorkflow(t, m, p, name)
	pressKey(t, m, p, "i")
	p.until(10*time.Second, "the editor to load "+name, func() bool {
		return w.editor != nil && w.editor.def != nil
	})
	return w
}

// enterRow puts the cursor on a row and presses enter through the real key
// path, which is what descends into a block or opens a value editor.
func enterRow(t *testing.T, m *root, p *pump, w *workflowsView, name string) {
	t.Helper()
	w.editor.cursor = rowIndex(t, w, name)
	pressKey(t, m, p, "enter")
}

// TestWorkflowEditorPromptPaneSaveWithoutTypingIsAByteNoOp is the regression
// for the data loss (issue #320, claim 2). Opening a 60-line block scalar and
// pressing save without typing used to rewrite it as one line, because the
// one-line field flattened the value it was seeded with and commitRow's
// "nothing changed" guard compared against the flattened text.
func TestWorkflowEditorPromptPaneSaveWithoutTypingIsAByteNoOp(t *testing.T) {
	m, p, globalDir := editorLive(t)
	w := openEditorOn(t, m, p, "prompt")
	path := filepath.Join(globalDir, "prompt.yaml")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}

	enterRow(t, m, p, w, "steps[0]")
	enterRow(t, m, p, w, "prompt")
	pane, ok := w.editor.overlay.(*wfEditorPane)
	if !ok {
		t.Fatalf("enter on a prompt opened %T, want the multi-line pane", w.editor.overlay)
	}
	if strings.Count(pane.Value(), "\n") < 40 {
		t.Fatalf("the pane opened on a flattened prompt:\n%q", pane.Value())
	}
	// Save, without having typed a character.
	_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	p.push(cmd)
	if w.editor.saving {
		t.Error("a clean pane sent a write")
	}
	if w.editor.overlay != nil {
		t.Error("ctrl+s left the pane open")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Errorf("the file changed on a save nobody typed into:\n--- before\n%s\n--- after\n%s", before, after)
	}
}

// A pane that *was* typed into writes the whole block scalar back, and the
// file's comments and its untouched step survive the write.
func TestWorkflowEditorPromptPaneSavesTheEditedBlock(t *testing.T) {
	m, p, globalDir := editorLive(t)
	w := openEditorOn(t, m, p, "prompt")
	enterRow(t, m, p, w, "steps[0]")
	enterRow(t, m, p, w, "prompt")
	pane := w.editor.overlay.(*wfEditorPane)
	next, _ := pane.Update(tea.KeyPressMsg{Code: 'Z', Text: "Z"})
	w.editor.overlay = next
	_, cmd := m.Update(tea.KeyPressMsg{Code: 's', Mod: tea.ModCtrl})
	p.push(cmd)

	path := filepath.Join(globalDir, "prompt.yaml")
	p.until(15*time.Second, "the edited prompt to reach the file", func() bool {
		b, err := os.ReadFile(path)
		return err == nil && strings.Contains(string(b), "Z")
	})
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "# prompt — keep this comment.") ||
		!strings.Contains(src, "# the check runs after the agent") {
		t.Errorf("a comment did not survive the save:\n%s", src)
	}
	if !strings.Contains(src, "prompt: |") {
		t.Errorf("the prompt is no longer a block scalar:\n%s", src)
	}
	if strings.Count(src, "Paragraph ") != 35 {
		t.Errorf("the block scalar lost lines:\n%s", src)
	}
	if !strings.Contains(src, "run: git status") {
		t.Errorf("the untouched step changed:\n%s", src)
	}
}

// choosePickerValue moves an open picker onto a value and presses enter.
func choosePickerValue(t *testing.T, m *root, p *pump, value string) {
	t.Helper()
	w := m.views[viewWorkflows].(*workflowsView)
	pick, ok := w.editor.overlay.(*wfEditorPicker)
	if !ok {
		t.Fatalf("no picker is open: %T", w.editor.overlay)
	}
	for i, idx := range pick.picker.matches {
		if pick.picker.options[idx].value == value {
			pick.picker.cursor = i
			pressKey(t, m, p, "enter")
			return
		}
	}
	t.Fatalf("the picker does not offer %q: %+v", value, pick.picker.options)
}

// blocksFidelity asserts the regions no operation touched: the header comment,
// the blank line before `steps:`, the lane bodies, and the CRLF endings the
// file was written with (task 065.2's criterion).
func blocksFidelity(t *testing.T, src string) {
	t.Helper()
	for _, want := range []string{
		"# blocks — keep this comment.\r\n",
		"  # the branch the lanes work on\r\n",
		"description: Fields, a fan-out and its lanes.\r\n\r\nfields:\r\n",
		"            run: git status\r\n",
	} {
		if !strings.Contains(src, want) {
			t.Errorf("an untouched region changed — %q is gone:\n%q", want, src)
		}
	}
	if strings.Contains(strings.ReplaceAll(src, "\r\n", ""), "\n") {
		t.Errorf("a bare LF appeared in a CRLF file:\n%q", src)
	}
}

// editorSettled waits for the re-read that follows a write. A caller that sent
// its next operation before this would be sending it with the version token
// the *previous* read handed back, which is the 409 the stale-write guard is
// for — so the wait is on the token changing, not on the rows.
func editorSettled(t *testing.T, p *pump, w *workflowsView, before, what string, ready func() bool) {
	t.Helper()
	p.until(15*time.Second, what, func() bool {
		// The version token changes when the write lands; the rows change one
		// message later, when the re-read the write triggered comes back. A
		// wait that stopped at the first would read the form the operation was
		// sent from.
		return w.editor != nil && !w.editor.saving && w.editor.version != before && ready()
	})
	if w.editor.err != "" {
		t.Fatalf("%s: %s", what, w.editor.err)
	}
}

// editorRowCount is the ready predicate a list operation is waited on.
func editorRowCount(w *workflowsView, n int) func() bool {
	return func() bool { return w.editor != nil && len(w.editor.rows) == n }
}

// TestWorkflowEditorInsertsAStepThatIsImmediatelyValid: `a` picks a type, and
// the PATCH it sends carries that type's required fields — so the write is
// accepted rather than refused for a step the editor itself created.
func TestWorkflowEditorInsertsAStepThatIsImmediatelyValid(t *testing.T) {
	m, p, globalDir := editorLive(t)
	w := openEditorOn(t, m, p, "blocks")
	w.editor.cursor = rowIndex(t, w, "steps[0]")
	pressKey(t, m, p, "a")
	choosePickerValue(t, m, p, "agent")

	path := filepath.Join(globalDir, "blocks.yaml")
	p.until(15*time.Second, "the new step to reach the file", func() bool {
		b, err := os.ReadFile(path)
		return err == nil && strings.Contains(string(b), "type: agent")
	})
	if w.editor.err != "" {
		t.Fatalf("the daemon refused the step the form built: %s", w.editor.err)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	src := string(b)
	if !strings.Contains(src, "prompt: |") {
		t.Errorf("the inserted agent step carries no prompt:\n%s", src)
	}
	// It landed after the step the cursor was on, not at the end.
	if idxAgent, idxSpread := strings.Index(src, "type: agent"), strings.Index(src, "id: spread"); idxAgent > idxSpread {
		t.Errorf("the step was appended rather than inserted after the cursor:\n%s", src)
	}
	blocksFidelity(t, src)
}

// A lane list and a declared-field list take the same three operations, and
// the file's untouched regions survive all of them.
func TestWorkflowEditorInsertsRemovesAndMovesLanesAndFields(t *testing.T) {
	m, p, globalDir := editorLive(t)
	w := openEditorOn(t, m, p, "blocks")
	path := filepath.Join(globalDir, "blocks.yaml")

	// Lanes: move beta above alpha.
	enterRow(t, m, p, w, "steps[1]")
	enterRow(t, m, p, w, "lanes")
	w.editor.cursor = rowIndex(t, w, "steps[1].lanes[1]")
	version := w.editor.version
	pressKey(t, m, p, "K")
	editorSettled(t, p, w, version, "the lane move", func() bool {
		return len(w.editor.rows) == 2 && w.editor.rows[0].label == "beta"
	})
	if got := readFileString(t, path); strings.Index(got, "id: beta") > strings.Index(got, "id: alpha") {
		t.Fatalf("the lanes did not move in the file:\n%s", got)
	}
	if w.editor.rows[0].label != "beta" {
		t.Fatalf("the form did not come back reordered: %+v", w.editor.rows)
	}

	// Lanes: add one, with the inline step §7.6 requires it to have.
	w.editor.cursor = rowIndex(t, w, "steps[1].lanes[0]")
	version = w.editor.version
	pressKey(t, m, p, "a")
	editorSettled(t, p, w, version, "the lane the form built", editorRowCount(w, 3))
	if got := readFileString(t, path); !strings.Contains(got, "id: lane-1") {
		t.Fatalf("the new lane is not in the file:\n%s", got)
	}

	// Declared fields: add one, then remove it again — with the confirmation.
	w.editor.path, w.editor.cursor = "", 0
	w.editor.rebuild()
	enterRow(t, m, p, w, "fields")
	w.editor.cursor = rowIndex(t, w, "fields[0]")
	version = w.editor.version
	pressKey(t, m, p, "a")
	editorSettled(t, p, w, version, "the declared field the form built", editorRowCount(w, 3))
	if got := readFileString(t, path); !strings.Contains(got, "name: field-1") {
		t.Fatalf("the new declared field is not in the file:\n%s", got)
	}
	w.editor.cursor = rowIndex(t, w, "fields[1]")
	version = w.editor.version
	pressKey(t, m, p, "d")
	if _, ok := w.editor.overlay.(*wfEditorConfirm); !ok {
		t.Fatalf("d removed without asking: overlay %T", w.editor.overlay)
	}
	pressKey(t, m, p, "y")
	editorSettled(t, p, w, version, "the removal", editorRowCount(w, 2))
	if got := readFileString(t, path); strings.Contains(got, "name: field-1") {
		t.Fatalf("the declared field is still in the file:\n%s", got)
	}

	blocksFidelity(t, readFileString(t, path))
}

// readFileString is the file as it stands now.
func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}
