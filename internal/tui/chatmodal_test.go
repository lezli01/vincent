package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Task 170's two `tui.chat` preferences wired into the chat view: the vim
// layer over the composer and the status line in the header. The components
// themselves are tested in chatvim_test.go and chatstatus_test.go.

// withChatPrefs installs tui.chat for one test and restores the shipped
// defaults after it.
func withChatPrefs(t *testing.T, statusLine string, vim bool) {
	t.Helper()
	setChatPrefs(statusLine, vim)
	t.Cleanup(func() { setChatPrefs("", false) })
}

// leaves reports whether cmd is the chat's esc back to its opener.
func leaves(t *testing.T, cmd tea.Cmd) bool {
	t.Helper()
	if cmd == nil {
		return false
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			if c == nil {
				continue
			}
			if _, ok := c().(selectViewMsg); ok {
				return true
			}
		}
		return false
	}
	_, ok := msg.(selectViewMsg)
	return ok
}

// TestChatVimEscPeelOrder: esc peels insert → normal, then whatever it
// peeled before the layer existed — the search line, the answer card, and
// last the chat itself.
func TestChatVimEscPeelOrder(t *testing.T) {
	withChatPrefs(t, "", true)

	// The answer card is above the layer: esc puts it away and the
	// composer stays in insert mode.
	v := chatViewFixture()
	v.applyLoaded(chatLoadedMsg{id: 1, chat: &apiclient.Chat{
		ID: 1, Title: "a chat", State: "awaiting_input", Agent: "claude",
		PendingInput: []byte(`{"kind":"question","questions":[{"text":"Which?","options":["a","b"]}]}`),
	}})
	if v.form == nil {
		t.Fatal("no answer card")
	}
	_, cmd := v.updateKey(registryKey(t, "esc"))
	if v.form != nil || v.formAway == nil || v.vim.mode() != vimInsert || leaves(t, cmd) {
		t.Fatalf("esc over the card: form %v, away %v, mode %v", v.form != nil, v.formAway != nil, v.vim.mode())
	}

	// The search line is above it too.
	v = chatViewFixture()
	v.client = offlineClient()
	v.render(100, 30)
	v.openSearchLine()
	_, cmd = v.updateKey(registryKey(t, "esc"))
	if v.search.typing || v.vim.mode() != vimInsert || leaves(t, cmd) {
		t.Fatalf("esc on the search line: typing %v, mode %v", v.search.typing, v.vim.mode())
	}
	v.updateKey(registryKey(t, "esc")) // the conversation gives the keyboard back
	if v.convFocus {
		t.Fatal("the conversation kept the keyboard")
	}

	// In the composer: insert → normal, and only then out of the chat.
	pressAll(t, v, "h", "i")
	_, cmd = v.updateKey(registryKey(t, "esc"))
	if v.vim.mode() != vimNormal || leaves(t, cmd) {
		t.Fatalf("the first esc: mode %v, left %v", v.vim.mode(), cmd != nil)
	}
	if !strings.Contains(ansi.Strip(v.render(100, 30)), "NORMAL") {
		t.Fatal("the composer's edge does not say NORMAL")
	}
	_, cmd = v.updateKey(registryKey(t, "esc"))
	if !leaves(t, cmd) {
		t.Fatal("esc in normal mode did not leave the chat")
	}
}

// TestChatVimNormalModeCapturesInput: normal mode types nothing, and still
// no single-key global binding fires from the composer — the composer is
// being edited, and `q` is a vim key there, not quit.
func TestChatVimNormalModeCapturesInput(t *testing.T) {
	withChatPrefs(t, "", true)
	v := chatViewFixture()
	pressAll(t, v, "h", "i", "esc")
	if v.vim.mode() != vimNormal || !v.capturesInput() || v.bindingContext() != ctxChat {
		t.Fatalf("normal mode: captures %v, context %q", v.capturesInput(), v.bindingContext())
	}
	_, cmd := v.updateKey(registryKey(t, "q"))
	if cmd != nil || v.composer.Value() != "hi" {
		t.Fatalf("q in normal mode: cmd %v, draft %q", cmd != nil, v.composer.Value())
	}

	// Through the root too: `?` would open the help on any other surface.
	m := connectedChatsRoot(t)
	m.views[viewChat] = v
	m.active = viewChat
	m.Update(registryKey(t, "?"))
	if m.help {
		t.Fatal("? opened the help from a composer in normal mode")
	}

	// Enter sends in normal mode as in insert.
	v.client = offlineClient()
	if _, cmd := v.updateKey(registryKey(t, "enter")); cmd == nil || !v.sending {
		t.Fatal("enter in normal mode did not send")
	}
}

// TestChatVimOffLeavesTheComposerAlone: the shipped default answers esc as
// it always has, and types every letter.
func TestChatVimOffLeavesTheComposerAlone(t *testing.T) {
	v := chatViewFixture()
	pressAll(t, v, "h", "i")
	if v.composer.Value() != "hi" {
		t.Fatalf("draft %q", v.composer.Value())
	}
	if !strings.Contains(ansi.Strip(v.render(100, 30)), "message") || strings.Contains(ansi.Strip(v.render(100, 30)), "INSERT") {
		t.Fatal("the composer's edge names a mode with vim off")
	}
	if _, cmd := v.updateKey(registryKey(t, "esc")); !leaves(t, cmd) {
		t.Fatal("esc did not leave the chat with vim off")
	}
}

// TestChatVimDraftPersistsInEitherMode: an edit in normal mode is a draft
// edit like any other (task 164) — the stored draft is the composer's text,
// whichever mode made it.
func TestChatVimDraftPersistsInEitherMode(t *testing.T) {
	withChatPrefs(t, "", true)
	v := chatViewFixture()
	v.client = offlineClient()
	v.draft.loaded = true // as the open's draft read leaves it
	pressAll(t, v, "h", "e", "y")
	if u := v.draftUpdate(); u == nil || u.Draft == nil || *u.Draft != "hey" {
		t.Fatalf("insert mode's draft: %+v", u)
	}
	v.updateKey(registryKey(t, "esc"))
	_, cmd := v.updateKey(registryKey(t, "x"))
	if v.composer.Value() != "he" || cmd == nil {
		t.Fatalf("x in normal mode: draft %q, save armed %v", v.composer.Value(), cmd != nil)
	}
	if u := v.draftUpdate(); u == nil || u.Draft == nil || *u.Draft != "he" {
		t.Fatalf("normal mode's draft: %+v", u)
	}
}

// TestChatVimInlinePickersInsertOnly: the `@` and skill lists open while
// typing, and a switch to normal mode closes them.
func TestChatVimInlinePickersInsertOnly(t *testing.T) {
	withChatPrefs(t, "", true)
	v := chatViewFixture()
	v.client = offlineClient()
	v.files.open = true
	v.updateKey(registryKey(t, "esc"))
	if v.files.open || v.vim.mode() != vimNormal {
		t.Fatalf("normal mode kept the file list open (mode %v)", v.vim.mode())
	}
	if cmd := v.syncInline(); cmd != nil || v.files.open || v.skills.open {
		t.Fatal("normal mode re-derived an inline list")
	}
}

// TestChatVimResetsOnOpen: a chat opens in insert mode.
func TestChatVimResetsOnOpen(t *testing.T) {
	withChatPrefs(t, "", true)
	v := chatViewFixture()
	v.updateKey(registryKey(t, "esc"))
	v.open(2)
	if v.vim.mode() != vimInsert {
		t.Fatal("a newly opened chat is in normal mode")
	}
}

func headerFixture() *chatView {
	v := chatViewFixture()
	v.chat.Branch = "vincent/chat-1-a-chat"
	v.chat.Model = "opus"
	ctx, win := int64(50_000), int64(200_000)
	v.turns = []apiclient.ChatTurn{{Seq: 1, State: "done", ContextTokens: &ctx, ContextWindow: &win}}
	return v
}

// TestChatHeaderDefaultUnchanged: with no status line the header is
// byte-for-byte the one that shipped, at every width.
func TestChatHeaderDefaultUnchanged(t *testing.T) {
	for _, w := range []int{40, 80, 120, 200} {
		v := headerFixture()
		before := v.headerLine(w)
		withChatPrefs(t, "", false)
		if got := v.headerLine(w); got != before {
			t.Fatalf("width %d: the default header changed\n got %q\nwant %q", w, got, before)
		}
		plain := ansi.Strip(before)
		if !strings.Contains(plain, "1 turn") {
			t.Fatalf("width %d: the default header lost its turn count: %q", w, plain)
		}
	}
	// A template that fails is no status line, not an error in the header.
	v := headerFixture()
	want := v.headerLine(120)
	withChatPrefs(t, "{{.NoSuchField}}", false)
	if got := v.headerLine(120); got != want {
		t.Fatalf("a broken template changed the header:\n%q", ansi.Strip(got))
	}
}

// TestChatHeaderStatusLine: the template replaces the gauge and the turn
// count as one cell; the title, state, agent, branch and reading marks stay.
func TestChatHeaderStatusLine(t *testing.T) {
	withChatPrefs(t, "{{.Model}} · {{.ContextPercent}}% ctx", false)
	v := headerFixture()
	v.following = false
	got := ansi.Strip(v.headerLine(140))
	for _, want := range []string{"a chat", "idle", "claude", "vincent/chat-1-a-chat", "⏸", "opus · 25% ctx"} {
		if !strings.Contains(got, want) {
			t.Errorf("the header lacks %q: %q", want, got)
		}
	}
	if strings.Contains(got, "1 turn") {
		t.Errorf("the turn count stayed beside the status line: %q", got)
	}

	// Narrow, it degrades on one line: never wrapped, never past the width.
	withChatPrefs(t, strings.Repeat("a long status line ", 10), false)
	for _, w := range []int{30, 50, 80} {
		line := v.headerLine(w)
		if strings.Contains(line, "\n") || ansi.StringWidth(line) > w {
			t.Fatalf("width %d: %q (%d wide)", w, ansi.Strip(line), ansi.StringWidth(line))
		}
		if !strings.Contains(ansi.Strip(line), "idle") {
			t.Fatalf("width %d: the state gave way to the status line: %q", w, ansi.Strip(line))
		}
	}
}
