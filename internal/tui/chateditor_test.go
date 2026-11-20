package tui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// TestSplitEditorCommand is task 163 decision 4: $EDITOR is split the way a
// shell splits a simple command, so a quoted path with a space survives.
func TestSplitEditorCommand(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		windows bool
		want    []string
	}{
		{"plain", "vim", false, []string{"vim"}},
		{"flags", "code --wait", false, []string{"code", "--wait"}},
		{"extra whitespace", "  nano \t -w ", false, []string{"nano", "-w"}},
		{"double-quoted path", `"/Applications/My Editor.app/bin/ed" --wait -n`, false, []string{"/Applications/My Editor.app/bin/ed", "--wait", "-n"}},
		{"single quotes", `'/opt/my editor/ed' '-a b'`, false, []string{"/opt/my editor/ed", "-a b"}},
		{"posix backslash escape", `/opt/my\ editor/ed -x`, false, []string{"/opt/my editor/ed", "-x"}},
		{"windows backslash path", `"C:\Program Files\Notepad++\notepad++.exe" -multiInst`, true, []string{`C:\Program Files\Notepad++\notepad++.exe`, "-multiInst"}},
		{"windows unquoted path", `C:\tools\vim.exe -p`, true, []string{`C:\tools\vim.exe`, "-p"}},
		{"unbalanced quote falls back", `"/opt/my editor/ed --wait`, false, []string{`"/opt/my`, "editor/ed", "--wait"}},
		{"adjacent quoted parts join", `ed"it"'or'`, false, []string{"editor"}},
		{"empty quotes are a word", `ed ""`, false, []string{"ed", ""}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := splitEditorCommand(tc.in, tc.windows); !slices.Equal(got, tc.want) {
				t.Fatalf("splitEditorCommand(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

var (
	fakeEditorOnce sync.Once
	fakeEditorPath string
	fakeEditorErr  error
)

// buildFakeEditor compiles cmd/fakeeditor once per test process, into a
// directory whose name holds a space so $VISUAL has to be quoted to reach it.
func buildFakeEditor(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain not available")
	}
	fakeEditorOnce.Do(func() {
		_, self, _, _ := runtime.Caller(0)
		root := filepath.Dir(filepath.Dir(filepath.Dir(self)))
		dir, err := os.MkdirTemp("", "vincent fakeeditor ")
		if err != nil {
			fakeEditorErr = err
			return
		}
		out := filepath.Join(dir, "fakeeditor")
		if runtime.GOOS == "windows" {
			out += ".exe"
		}
		cmd := exec.Command("go", "build", "-o", out, "./cmd/fakeeditor")
		cmd.Dir = root
		if b, err := cmd.CombinedOutput(); err != nil {
			fakeEditorErr = &exec.Error{Name: string(b), Err: err}
			return
		}
		fakeEditorPath = out
	})
	if fakeEditorErr != nil {
		t.Fatalf("build fakeeditor: %v", fakeEditorErr)
	}
	return fakeEditorPath
}

// runSync is an execFunc that runs the real command to completion — no
// terminal is handed over, but the process, its argv and its exit status
// are the real ones.
func runSync(c *exec.Cmd, cb tea.ExecCallback) tea.Cmd {
	return func() tea.Msg { return cb(c.Run()) }
}

// TestChatComposerEditorRoundTrip is the issue's acceptance criterion: ctrl+g
// hands the draft to a real editor process found through a quoted $VISUAL,
// and what it leaves comes back into the composer, unsent.
func TestChatComposerEditorRoundTrip(t *testing.T) {
	bin := buildFakeEditor(t)
	t.Setenv("VISUAL", `"`+bin+`" --wait`)
	t.Setenv("EDITOR", "")

	press := func(t *testing.T, v *chatView) {
		t.Helper()
		_, cmd := v.updateKey(registryKey(t, "ctrl+g"))
		if cmd == nil {
			t.Fatalf("ctrl+g returned no command (note %q)", v.note)
		}
		msg, ok := cmd().(chatEditorMsg)
		if !ok {
			t.Fatal("ctrl+g's command did not report an editing session")
		}
		v.update(msg)
	}
	fixture := func(draft string) *chatView {
		v := chatViewFixture()
		v.exec = runSync
		v.composer.SetValue(draft)
		return v
	}

	t.Run("replaced", func(t *testing.T) {
		argsFile := filepath.Join(t.TempDir(), "args")
		t.Setenv("FAKEEDITOR_MODE", "replace")
		t.Setenv("FAKEEDITOR_TEXT", "a longer prompt\nover two lines")
		t.Setenv("FAKEEDITOR_ARGS", argsFile)
		v := fixture("short")
		press(t, v)
		if got := v.composer.Value(); got != "a longer prompt\nover two lines" {
			t.Fatalf("composer = %q, want the editor's text without its trailing newline", got)
		}
		if v.noteBad {
			t.Fatalf("a good round trip noted a failure: %q", v.note)
		}
		raw, err := os.ReadFile(argsFile)
		if err != nil {
			t.Fatal(err)
		}
		args := strings.Split(strings.TrimRight(strings.ReplaceAll(string(raw), "\r\n", "\n"), "\n"), "\n")
		if len(args) != 2 || args[0] != "--wait" || !strings.HasSuffix(args[1], ".md") {
			t.Fatalf("the editor got argv %q, want --wait and the .md file", args)
		}
		if _, err := os.Stat(args[1]); !os.IsNotExist(err) {
			t.Fatalf("the temp file %s outlived the session (%v)", args[1], err)
		}
	})
	t.Run("unchanged", func(t *testing.T) {
		t.Setenv("FAKEEDITOR_MODE", "keep")
		v := fixture("keep me")
		press(t, v)
		if got := v.composer.Value(); got != "keep me" {
			t.Fatalf("composer = %q, want the draft untouched", got)
		}
	})
	t.Run("emptied", func(t *testing.T) {
		t.Setenv("FAKEEDITOR_MODE", "empty")
		v := fixture("keep me too")
		press(t, v)
		if got := v.composer.Value(); got != "keep me too" {
			t.Fatalf("composer = %q, want an emptied file to leave the draft alone", got)
		}
	})
	t.Run("editor fails", func(t *testing.T) {
		t.Setenv("FAKEEDITOR_MODE", "fail")
		v := fixture("still here")
		press(t, v)
		if got := v.composer.Value(); got != "still here" {
			t.Fatalf("composer = %q, want the draft kept", got)
		}
		if !v.noteBad || !strings.Contains(v.note, "editor") {
			t.Fatalf("note = %q (bad %v), want the failure on the status line", v.note, v.noteBad)
		}
	})
}

// TestChatComposerEditorSendsNothing: the result replaces the draft and the
// cursor ends after it, and nothing is sent — enter still does that.
func TestChatComposerEditorSendsNothing(t *testing.T) {
	v := chatViewFixture()
	v.composer.SetValue("draft")
	cmd := v.applyEditorDraft(chatEditorMsg{chatID: 1, original: "draft", text: "edited\n"})
	if v.composer.Value() != "edited" {
		t.Fatalf("composer = %q", v.composer.Value())
	}
	if v.sending {
		t.Fatal("applying the editor's text started a send")
	}
	if cmd != nil {
		if _, ok := cmd().(chatSentMsg); ok {
			t.Fatal("applying the editor's text sent the message")
		}
	}
	// A newline the draft already ended with is the human's and stays.
	if got := trimEditorNewline("b\n", "a\n"); got != "b\n" {
		t.Fatalf("trimEditorNewline = %q", got)
	}
	if got := trimEditorNewline("a\r\n", "b"); got != "a" {
		t.Fatalf("trimEditorNewline CRLF = %q", got)
	}
}

// TestChatComposerEditorEditsTheQueuedMessage: while ↑ has a queued message
// in the composer, ctrl+g edits that text and enter still saves it.
func TestChatComposerEditorEditsTheQueuedMessage(t *testing.T) {
	v := chatViewFixture()
	v.queue = &apiclient.ChatQueue{Messages: []apiclient.ChatQueuedMessage{{ID: 4, Prompt: "queued"}}}
	if !v.startQueueEdit() {
		t.Fatal("no queued message to edit")
	}
	v.applyEditorDraft(chatEditorMsg{chatID: 1, queued: 4, original: "queued", text: "queued, edited\n"})
	if v.editingQueued != 4 || v.composer.Value() != "queued, edited" {
		t.Fatalf("editing %d with %q", v.editingQueued, v.composer.Value())
	}
	// A session that began on another queued message is dropped, not
	// written over this one.
	v.applyEditorDraft(chatEditorMsg{chatID: 1, queued: 9, original: "x", text: "stray"})
	if v.composer.Value() != "queued, edited" || !v.noteBad {
		t.Fatalf("a stale session overwrote the queued edit: %q", v.composer.Value())
	}
}

// TestChatMarkdownOpensTheDaemonsDocument drives alt+v against the real
// handler: the document the daemon rendered is what the editor is handed, in
// a read-only .md file that is removed once the editor exits.
func TestChatMarkdownOpensTheDaemonsDocument(t *testing.T) {
	const token = "chat-markdown-token"
	dataDir := t.TempDir()
	st, err := storetest.Open(filepath.Join(dataDir, "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	p := &store.Project{Name: "alpha", Path: filepath.Join(t.TempDir(), "a"), DefaultBranch: "main"}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	c := &store.Chat{
		ProjectID: p.ID, Title: "export me", State: chatstate.Idle,
		Agent: "claude", PermissionMode: "full-auto", Branch: "vincent/chat-export",
	}
	if err := st.CreateChat(ctx, c); err != nil {
		t.Fatal(err)
	}
	turn, err := st.CreateChatTurn(ctx, c.ID, "what is in here?")
	if err != nil {
		t.Fatal(err)
	}
	turn.State = chatstate.TurnDone
	turn.ResultText = "Mostly Go."
	if err := st.UpdateChatTurn(ctx, turn); err != nil {
		t.Fatal(err)
	}
	s := api.New(api.Deps{
		Token: token, Config: config.Default, StartedAt: time.Now(), ListenAddr: "127.0.0.1:0",
		RequestStop: func() {}, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Store: st, Dirs: config.Dirs{Data: dataDir},
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)

	v := chatViewFixture()
	v.chatID = c.ID
	v.chat.ID = c.ID
	v.client = apiclient.New(ts.URL, token)
	var (
		opened  string
		content string
		mode    os.FileMode
	)
	v.exec = func(cmd *exec.Cmd, cb tea.ExecCallback) tea.Cmd {
		opened = cmd.Args[len(cmd.Args)-1]
		b, err := os.ReadFile(opened)
		if err != nil {
			t.Fatalf("read the opened file: %v", err)
		}
		content = string(b)
		fi, err := os.Stat(opened)
		if err != nil {
			t.Fatal(err)
		}
		mode = fi.Mode().Perm()
		return func() tea.Msg { return cb(nil) }
	}

	_, cmd := v.updateKey(registryKey(t, "alt+v"))
	if cmd == nil {
		t.Fatalf("alt+v fetched nothing (note %q)", v.note)
	}
	fetched, ok := cmd().(chatMarkdownMsg)
	if !ok || fetched.err != nil {
		t.Fatalf("fetch = %#v", fetched)
	}
	_, cmd = v.update(fetched)
	if cmd == nil {
		t.Fatalf("the fetched document opened nothing (note %q)", v.note)
	}
	closed := drainOne(cmd)
	if _, ok := closed.(chatMarkdownClosedMsg); !ok {
		t.Fatalf("the editor's exit reported %#v", closed)
	}
	if !strings.HasSuffix(opened, ".md") {
		t.Fatalf("opened %q, want a .md file", opened)
	}
	for _, want := range []string{"export me", "what is in here?", "Mostly Go."} {
		if !strings.Contains(content, want) {
			t.Fatalf("the opened file lacks %q:\n%s", want, content)
		}
	}
	if runtime.GOOS != "windows" && mode != 0o400 {
		t.Fatalf("the opened file's mode = %v, want 0400", mode)
	}
	if _, err := os.Stat(opened); !os.IsNotExist(err) {
		t.Fatalf("the file outlived the editor (%v)", err)
	}
	if v.composer.Value() != "" {
		t.Fatalf("alt+v reached the composer: %q", v.composer.Value())
	}
}

// drainOne runs a command that may be a batch and returns the first message
// that is not itself a batch.
func drainOne(cmd tea.Cmd) tea.Msg {
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c == nil {
				continue
			}
			if m := drainOne(c); m != nil {
				return m
			}
		}
		return nil
	}
	return msg
}
