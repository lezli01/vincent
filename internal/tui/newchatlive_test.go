package tui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
)

// newChatLiveRoot is the shell a human has on screen on a fresh installation
// that has registered two repositories and started no chat: connected to the
// real handlers, on the chats board, with an empty board — which is the state
// §15's new-chat form is met in first.
//
// It is deliberately wired to the real API rather than to a stub, because the
// claim under test is about the seam between them: the daemon answers
// `GET /v1/projects` correctly and the answer is dropped inside the TUI.
func newChatLiveRoot(t *testing.T) *root {
	t.Helper()
	const token = "new-chat-token"

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	ctx := context.Background()
	for _, name := range []string{"alpha", "beta"} {
		p := &store.Project{Name: name, Path: "/nowhere/" + name, DefaultBranch: "main"}
		if err := st.CreateProject(ctx, p); err != nil {
			t.Fatalf("CreateProject(%s): %v", name, err)
		}
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
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	u, err := url.Parse(ts.URL)
	if err != nil {
		t.Fatalf("parse test server url: %v", err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatalf("test server port: %v", err)
	}

	cn := connector{
		resolveDataDir: func() (string, error) { return t.TempDir(), nil },
		readRuntime: func(string) (daemon.RuntimeInfo, error) {
			return daemon.RuntimeInfo{PID: 1, Port: port, StartedAt: time.Now()}, nil
		},
		checkHealth: daemon.CheckHealth,
		newClient: func(string) (*apiclient.Client, error) {
			return apiclient.New(ts.URL, token), nil
		},
		startDetached: func() (int, error) { t.Fatal("auto-start must not trigger"); return 0, nil },
		startTimeout:  time.Second,
		pollInterval:  time.Millisecond,
	}

	m := newRoot(testCtx(t), cn, ackedDir(t))
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	msg := runCmd(t, m.Init(), 10*time.Second)
	if _, ok := msg.(connectedMsg); !ok {
		t.Fatalf("probe = %T, want connectedMsg", msg)
	}
	m.Update(msg)
	m.switchTo(viewChats)
	if m.active != viewChats {
		t.Fatalf("the fixture is on %v, want the chats board", m.active)
	}
	return m
}

// chatsForm is the open new-chat form, re-read from the root after every
// Update because the panel is stored back into the view table each time.
func chatsForm(t *testing.T, m *root) *newChatForm {
	t.Helper()
	v, ok := m.views[viewChats].(*chatsView)
	if !ok {
		t.Fatalf("the chats slot holds %T", m.views[viewChats])
	}
	if v.create == nil {
		t.Fatal("the new-chat form is not open")
	}
	return v.create
}

// TestNewChatFormPickersPopulateThroughRoot holds §15's new-chat form: "`←`/`→`
// choose on the project and agent fields" (amended 2026-08-31, task 067).
//
// It drives the form through the **root** model, which is the layer a real
// keystroke and a real fetch both take. `newChatForm.init` produces a
// `newChatFieldsMsg`, the root broadcasts it, and `chatsView.update` has no
// case for it — so the projects the daemon listed are dropped, `f.projects`
// stays nil, and `cycle` early-returns on every press. The existing coverage
// in bindings_test.go calls `applyFields` directly, which is why a form that
// never populates in production has a passing `left` probe.
func TestNewChatFormPickersPopulateThroughRoot(t *testing.T) {
	m := newChatLiveRoot(t)

	_, cmd := m.Update(registryKey(t, "n"))
	if cmd == nil {
		t.Fatal("n opened no fetch for the pickers' contents")
	}
	chatsForm(t, m) // the form is up

	// Run the command the way the runtime does, then hand its message back
	// to the root — the two halves of one delivery.
	msg := runCmd(t, cmd, 10*time.Second)
	fields, ok := msg.(newChatFieldsMsg)
	if !ok {
		t.Fatalf("the form's fetch produced %T, want newChatFieldsMsg", msg)
	}
	if fields.err != nil {
		t.Fatalf("GET /v1/projects failed: %v", fields.err)
	}
	if len(fields.projects) != 2 {
		t.Fatalf("the daemon listed %d projects, want the 2 registered", len(fields.projects))
	}
	m.Update(msg)

	f := chatsForm(t, m)
	if got := len(f.projects); got != 2 {
		t.Fatalf("the form holds %d projects after the daemon's answer landed, want 2 — "+
			"newChatFieldsMsg is produced by newChatForm.init and consumed by nothing", got)
	}

	// The form opens on the title and the six fields wrap, so five tabs land
	// the cursor on the project row.
	for range 5 {
		m.Update(registryKey(t, "tab"))
	}
	f = chatsForm(t, m)
	if f.focus != 0 {
		t.Fatalf("five tabs left the cursor on field %d, want the project row", f.focus)
	}

	before := f.projectID
	m.Update(registryKey(t, "right"))
	f = chatsForm(t, m)
	if f.projectID == before {
		t.Fatalf("→ on the project row chose project %d again; §15: ← → choose here", before)
	}
	if name := f.projectName(); name != "alpha" && name != "beta" {
		t.Fatalf("the project row renders %q, want one of the registered names", name)
	}
}
