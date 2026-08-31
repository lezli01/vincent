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

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/agent/agenttest"
	"github.com/lezli01/vincent/internal/agent/claude"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/daemon"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/gitx"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/testrepo"
	"github.com/lezli01/vincent/internal/worktree"
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

	// alpha is a real repository on a non-default branch name, so the base
	// row's claim — that an empty BaseBranch resolves to *this project's*
	// default — is falsifiable rather than accidentally right.
	ctx := context.Background()
	repos := map[string]string{
		"alpha": testrepo.Init(t, "trunk"),
		"beta":  testrepo.Init(t, "main"),
	}
	for _, name := range []string{"alpha", "beta"} {
		p := &store.Project{
			Name: name, Path: repos[name],
			DefaultBranch: map[string]string{"alpha": "trunk", "beta": "main"}[name],
		}
		if err := st.CreateProject(ctx, p); err != nil {
			t.Fatalf("CreateProject(%s): %v", name, err)
		}
	}

	// The adapter registry is the same one `GET /v1/agents` serves the form
	// from and the one `POST /v1/chats` gates resume support on: claude
	// pointed at fakeagent, which is what makes the catalogs in the pickers
	// the daemon's own rather than a stub's.
	fake := agenttest.BuildFakeAgent(t)
	dataDir := t.TempDir()
	git := gitx.New()
	agents := agent.NewRegistry(claude.New(func() string { return fake }))

	s := api.New(api.Deps{
		Token:       token,
		Config:      config.Default,
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Broker:      broker,
		Git:         git,
		Agents:      agents,
		Catalog:     agent.NewCatalogCache(agents),
		Worktrees:   worktree.NewManager(git, dataDir),
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

// TestNewChatFormPickersPopulateThroughRoot holds §15's new-chat form: `←`/`→`
// step the project and agent fields in place (amended 2026-08-31, task 067 and
// issue #281).
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

// TestNewChatFormPickersRenderTheServedCatalogs holds issue #281's seam: the
// lists the human chooses from are the catalogs `GET /v1/agents` and
// `GET /v1/projects` actually serve, and a model chosen from one of them
// travels to `POST /v1/chats` and comes back on the created chat — with the
// base branch the daemon resolved from the project, because the form sent
// none.
//
// It is wired to the real handlers rather than a stub for the usual reason:
// the claim is about the seam. A form that built its options from a hand-made
// `apiclient.Agent` would keep passing the day the wire type changed.
func TestNewChatFormPickersRenderTheServedCatalogs(t *testing.T) {
	m := newChatLiveRoot(t)

	_, cmd := m.Update(registryKey(t, "n"))
	if cmd == nil {
		t.Fatal("n opened no fetch for the lists' contents")
	}
	m.Update(runCmd(t, cmd, 10*time.Second))

	f := chatsForm(t, m)
	if len(f.agents) == 0 {
		t.Fatal("the form holds no adapter after GET /v1/agents landed")
	}

	// What the daemon serves, fetched again through the same client the form
	// used: the lists must be built from this and nothing else.
	served, err := m.client.ListAgents(t.Context(), false)
	if err != nil {
		t.Fatalf("GET /v1/agents: %v", err)
	}
	want, ok := served.Find(f.agentName())
	if !ok {
		t.Fatalf("the form selected %q, which GET /v1/agents does not list", f.agentName())
	}
	if len(want.Models) == 0 {
		t.Fatalf("the daemon serves no model catalog for %q, so the list under test would be empty", want.Name)
	}

	// Walk to the model row the way a human does — the form opens on the
	// title — and open its list.
	m.Update(registryKey(t, "tab"))
	m.Update(registryKey(t, "tab"))
	f = chatsForm(t, m)
	if f.focus != ncModel {
		t.Fatalf("two tabs left the cursor on row %d, want the model row", f.focus)
	}
	m.Update(registryKey(t, "enter"))
	f = chatsForm(t, m)
	if f.pick == nil {
		t.Fatal("enter on the model row opened no list")
	}
	if got, wantN := len(f.pick.options), len(want.Models)+1; got != wantN {
		t.Fatalf("the model list has %d rows, want the %d served models plus the (agent default) row",
			got, len(want.Models))
	}
	if f.pick.options[0].note != want.DefaultModel {
		t.Errorf("the default row is noted %q, want the served default model %q",
			f.pick.options[0].note, want.DefaultModel)
	}
	for i, o := range want.Models {
		got := f.pick.options[i+1]
		if got.value != o.Value || got.note != o.Source {
			t.Errorf("row %d offers %q/%q, want the served %q/%q", i+1, got.value, got.note, o.Value, o.Source)
		}
	}

	// Choose the first served model, name the chat, and create.
	m.Update(registryKey(t, "down"))
	m.Update(registryKey(t, "enter"))
	f = chatsForm(t, m)
	if f.model != want.Models[0].Value {
		t.Fatalf("the form holds model %q, want the one chosen from the served catalog", f.model)
	}
	f.title.SetValue("a live chat")

	_, cmd = m.Update(registryKey(t, "ctrl+s"))
	if cmd == nil {
		t.Fatal("ctrl+s posted nothing")
	}
	msg, ok := runCmd(t, cmd, 10*time.Second).(chatCreatedMsg)
	if !ok {
		t.Fatalf("ctrl+s produced %T, want chatCreatedMsg", msg)
	}
	if msg.err != nil {
		t.Fatalf("POST /v1/chats: %v", msg.err)
	}
	if msg.chat.Model != want.Models[0].Value {
		t.Errorf("the created chat runs model %q, want the chosen %q", msg.chat.Model, want.Models[0].Value)
	}
	// The base row was never touched, so the request carried no branch and
	// the daemon resolved the project's own default — which is `trunk` here
	// and not the `main` a hard-coded fallback would have produced.
	if msg.chat.BaseBranch != "trunk" {
		t.Errorf("the created chat is based on %q, want the project's default branch resolved by the daemon",
			msg.chat.BaseBranch)
	}
}
