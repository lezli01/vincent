package tui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/events"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// TestChatExportFormAgainstTheRealAPI is task 156's TUI leg against the real
// handlers: the form opens on a chat, writes the daemon's Markdown to the
// prefilled path in the TUI's working directory, offers the Markdown to the
// clipboard through the task 076 write seam, and shows a write it could not
// make rather than dropping it.
func TestChatExportFormAgainstTheRealAPI(t *testing.T) {
	const token = "chat-export-token"
	st, err := storetest.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	broker := events.New()
	t.Cleanup(broker.Close)
	st.SetEventHook(broker.Publish)

	ctx := context.Background()
	p := &store.Project{Name: "alpha", Path: filepath.Join(t.TempDir(), "a"), DefaultBranch: "main"}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	c := &store.Chat{
		ProjectID: p.ID, Title: "Fix the build", State: chatstate.Idle,
		Agent: "claude", PermissionMode: "full-auto", Branch: "vincent/fix-the-build",
	}
	if err := st.CreateChat(ctx, c); err != nil {
		t.Fatalf("CreateChat: %v", err)
	}
	turn, err := st.CreateChatTurn(ctx, c.ID, "why is the build red?")
	if err != nil {
		t.Fatalf("CreateChatTurn: %v", err)
	}
	turn.State, turn.ResultText = chatstate.TurnDone, "A missing import in **main.go**."
	if err := st.UpdateChatTurn(ctx, turn); err != nil {
		t.Fatalf("UpdateChatTurn: %v", err)
	}

	s := api.New(api.Deps{
		Token:       token,
		Config:      config.Default,
		Dirs:        config.Dirs{Data: t.TempDir()},
		StartedAt:   time.Now(),
		ListenAddr:  "127.0.0.1:0",
		RequestStop: func() {},
		Logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store:       st,
		Broker:      broker,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	wrote := stubClipboardSync(t)

	cwd := t.TempDir()
	m := newRoot(testCtx(t), connector{}, ackedDir(t))
	m.startup.cwd = cwd
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	_, cmd := m.Update(connectedMsg{client: apiclient.New(ts.URL, token), dataDir: t.TempDir()})
	pm := newPump(t, m, cmd)
	press := func(key string) {
		_, cmd := m.Update(synthKey(key))
		pm.push(cmd)
	}

	pm.push(openChatExportCmd(c.ID, c.Title))
	pm.until(10*time.Second, "the export form to open", func() bool { return m.exportForm != nil })
	want := "." + string(filepath.Separator) + "chat-" + strconv.FormatInt(c.ID, 10) + "-fix-the-build.md"
	if got := m.exportForm.path.Value(); got != want {
		t.Fatalf("path prefilled %q, want %q", got, want)
	}

	press("enter")
	pm.until(10*time.Second, "the export to be written", func() bool {
		return m.exportForm != nil && (m.exportForm.written != "" || m.exportForm.err != nil)
	})
	if m.exportForm.err != nil {
		t.Fatalf("export failed: %v", m.exportForm.err)
	}
	path := filepath.Join(cwd, "chat-"+strconv.FormatInt(c.ID, 10)+"-fix-the-build.md")
	if m.exportForm.written != path {
		t.Fatalf("wrote %q, want %q", m.exportForm.written, path)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the export: %v", err)
	}
	for _, s := range []string{"# Chat " + strconv.FormatInt(c.ID, 10) + ": Fix the build", "why is the build red?", "A missing import in **main.go**."} {
		if !strings.Contains(string(body), s) {
			t.Fatalf("the export lacks %q:\n%s", s, body)
		}
	}
	if !strings.Contains(content(m), "copy the Markdown") {
		t.Fatalf("the written form does not offer the Markdown copy: %q", content(m))
	}

	press("down")
	press("enter")
	pm.until(10*time.Second, "the Markdown to reach the clipboard", func() bool { return len(wrote.texts()) == 1 })
	if got := wrote.texts()[0]; got != sanitizeText(string(body)) {
		t.Fatalf("copied %q, want the written Markdown", got)
	}
	if m.exportForm != nil {
		t.Fatal("the copy left the form open")
	}

	// A path that cannot be written is shown in the form, which stays open
	// for the path to be fixed.
	pm.push(openChatExportCmd(c.ID, c.Title))
	pm.until(10*time.Second, "the export form to reopen", func() bool { return m.exportForm != nil })
	m.exportForm.path.SetValue(filepath.Join(cwd, "missing", "out.md"))
	press("enter")
	pm.until(10*time.Second, "the write failure to show", func() bool {
		return m.exportForm != nil && m.exportForm.err != nil
	})
	if !strings.Contains(content(m), "export failed") {
		t.Fatalf("the failure is not on screen: %q", content(m))
	}
}

// TestChatExportFormatSwapsTheExtension: changing the format keeps the
// suggested path's extension in step, and after an edit swaps only that.
func TestChatExportFormatSwapsTheExtension(t *testing.T) {
	f := newChatExportForm(3, "Hello world", "")
	f.cycleFormat(1)
	if got := f.path.Value(); got != "."+string(filepath.Separator)+"chat-3-hello-world.html" {
		t.Fatalf("html path %q", got)
	}
	f.path.SetValue("notes/export.html")
	f.pathEdited = true
	f.cycleFormat(1)
	if got := f.path.Value(); got != "notes/export.json" {
		t.Fatalf("edited path became %q, want only its extension swapped", got)
	}
}

// syncClipboard records clipboard writes under a lock. A live test pushes the
// copy command onto its own goroutine, the way the runtime does, while the
// test goroutine polls for the write — stubClipboard's bare slice suits only
// tests that run the command inline.
type syncClipboard struct {
	mu    sync.Mutex
	wrote []string
}

func (c *syncClipboard) texts() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.wrote...)
}

// stubClipboardSync points the write seam at a locked recorder for one test.
func stubClipboardSync(t *testing.T) *syncClipboard {
	t.Helper()
	c := &syncClipboard{}
	prev := clipboardWrite
	clipboardWrite = func(text string) error {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.wrote = append(c.wrote, text)
		return nil
	}
	t.Cleanup(func() { clipboardWrite = prev })
	return c
}
