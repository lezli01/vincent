package tui

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/agent"
	"github.com/lezli01/vincent/internal/api"
	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/config"
	"github.com/lezli01/vincent/internal/store"
	"github.com/lezli01/vincent/internal/store/storetest"
)

// draftLive is a store and the real handlers over httptest, for task 162's
// draft, stash and prompt-history tests.
type draftLive struct {
	st      *store.Store
	client  *apiclient.Client
	project *store.Project
}

func newDraftLive(t *testing.T) *draftLive {
	t.Helper()
	const token = "chat-draft-token"
	st, err := storetest.Open(filepath.Join(t.TempDir(), "draft.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	p := &store.Project{Name: "proj", Path: t.TempDir(), DefaultBranch: "main"}
	if err := st.CreateProject(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	s := api.New(api.Deps{
		Token: token, Config: config.Default, StartedAt: time.Now(), ListenAddr: "127.0.0.1:0",
		RequestStop: func() {}, Logger: log, Store: st,
	})
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return &draftLive{st: st, client: apiclient.New(ts.URL, token), project: p}
}

// chat makes an idle chat whose finished turns sent prompts, in order.
func (d *draftLive) chat(t *testing.T, title string, prompts ...string) int64 {
	t.Helper()
	ctx := context.Background()
	c := &store.Chat{
		ProjectID: d.project.ID, Title: title, State: chatstate.Idle, Agent: "claude",
		PermissionMode: string(agent.FullAuto), BaseBranch: "main", Branch: "b-" + title,
	}
	if err := d.st.CreateChat(ctx, c); err != nil {
		t.Fatal(err)
	}
	for _, p := range prompts {
		turn, err := d.st.CreateChatTurn(ctx, c.ID, p)
		if err != nil {
			t.Fatal(err)
		}
		turn.State = chatstate.TurnDone
		if _, err := d.st.FinishChatTurn(ctx, turn, ""); err != nil {
			t.Fatal(err)
		}
	}
	return c.ID
}

// view is a fresh chat view on the daemon — what a relaunched TUI has.
func (d *draftLive) view() *chatView {
	v := newChatView(newLevelHolder(), newRawHolder(), newHyperlinkHolder())
	v.client = d.client
	return v
}

// openChat points v at chat id the way open does, without the stream (a
// blocking read no test pumps) and running the leaving chat's flush.
func openChat(t *testing.T, v *chatView, id int64) {
	t.Helper()
	if cmd := v.leaveDraft(); cmd != nil {
		runCmd(t, cmd, 30*time.Second)
	}
	v.resetDraft()
	v.chatID = id
	v.chat, v.turns = nil, nil
	v.composer.SetValue("")
	v.composer.Focus()
	loadChat(t, v)
}

func loadChat(t *testing.T, v *chatView) {
	t.Helper()
	msg, ok := runCmd(t, v.loadCmd(), 30*time.Second).(chatLoadedMsg)
	if !ok || msg.err != nil {
		t.Fatalf("load: %+v", msg)
	}
	v.applyLoaded(msg)
}

// typeDraft presses each rune of s into the composer.
func typeDraft(v *chatView, s string) {
	for _, r := range s {
		v.updateKey(tea.KeyPressMsg{Code: r, Text: string(r)})
	}
}

func (d *draftLive) stored(t *testing.T, id int64) *store.Chat {
	t.Helper()
	c, err := d.st.GetChat(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestChatDraftSurvivesSwitchAndRelaunch: a draft typed in chat A is there
// after opening chat B and coming back, and a fresh view — a relaunched TUI
// — restores it from the daemon.
func TestChatDraftSurvivesSwitchAndRelaunch(t *testing.T) {
	d := newDraftLive(t)
	a, b := d.chat(t, "a"), d.chat(t, "b")
	v := d.view()
	openChat(t, v, a)
	typeDraft(v, "half a thought")
	openChat(t, v, b) // leaving A flushes at once, no debounce
	if got := d.stored(t, a).Draft; got != "half a thought" {
		t.Fatalf("stored draft after leaving = %q", got)
	}
	if v.composer.Value() != "" {
		t.Fatalf("chat B opened with %q", v.composer.Value())
	}
	openChat(t, v, a)
	if got := v.composer.Value(); got != "half a thought" {
		t.Fatalf("back on A the composer holds %q", got)
	}

	fresh := d.view()
	openChat(t, fresh, a)
	if got := fresh.composer.Value(); got != "half a thought" {
		t.Fatalf("a fresh view restored %q", got)
	}
}

// TestChatDraftSendKeepsOrClears: a refused send keeps the stored draft, an
// accepted one clears it.
func TestChatDraftSendKeepsOrClears(t *testing.T) {
	d := newDraftLive(t)
	id := d.chat(t, "a")
	v := d.view()
	openChat(t, v, id)
	typeDraft(v, "do it")
	// No chat runner is wired, so the daemon refuses the send.
	sent := runCmd(t, v.sendCmd(), 30*time.Second).(chatSentMsg)
	if sent.err == nil {
		t.Fatal("the send was accepted with no chat runner")
	}
	if cmd := v.applySent(sent); cmd != nil {
		v.update(runCmd(t, cmd, 30*time.Second))
	}
	if got := d.stored(t, id).Draft; got != "do it" || v.composer.Value() != "do it" {
		t.Fatalf("after a refusal stored %q composer %q", got, v.composer.Value())
	}

	// An accepted send, as the daemon answers one.
	v.sending = false
	cmd := v.applySent(chatSentMsg{chatID: id, sent: "do it", result: &apiclient.ChatSend{}})
	flushBatch(t, v, cmd)
	if got := d.stored(t, id).Draft; got != "" || v.composer.Value() != "" {
		t.Fatalf("after acceptance stored %q composer %q", got, v.composer.Value())
	}
}

// flushBatch runs every command in cmd, feeding a draft save's answer back.
func flushBatch(t *testing.T, v *chatView, cmd tea.Cmd) {
	t.Helper()
	if cmd == nil {
		return
	}
	switch msg := runCmd(t, cmd, 30*time.Second).(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			flushBatch(t, v, c)
		}
	case chatDraftSavedMsg:
		v.applyDraftSaved(msg)
	}
}

// TestChatStashPersists: alt+s stashes into the daemon, a fresh view sees the
// stash, and alt+s on its empty composer pops it.
func TestChatStashPersists(t *testing.T) {
	d := newDraftLive(t)
	id := d.chat(t, "a")
	v := d.view()
	openChat(t, v, id)
	typeDraft(v, "for later")
	flushBatch(t, v, v.updateKeyCmd(registryKey(t, "alt+s")))
	if c := d.stored(t, id); c.Stash != "for later" || c.Draft != "" {
		t.Fatalf("stored draft %q stash %q", c.Draft, c.Stash)
	}
	fresh := d.view()
	openChat(t, fresh, id)
	flushBatch(t, fresh, fresh.updateKeyCmd(registryKey(t, "alt+s")))
	if fresh.composer.Value() != "for later" {
		t.Fatalf("pop gave %q", fresh.composer.Value())
	}
	if c := d.stored(t, id); c.Stash != "" || c.Draft != "for later" {
		t.Fatalf("after pop stored draft %q stash %q", c.Draft, c.Stash)
	}
}

// TestChatPromptHistoryLive: alt+h lists prompts from another chat, an
// archived one included, and enter inserts without sending.
func TestChatPromptHistoryLive(t *testing.T) {
	d := newDraftLive(t)
	old := d.chat(t, "old", "explain the parser")
	if _, err := d.st.SetChatState(context.Background(), old, chatstate.Archived); err != nil {
		t.Fatal(err)
	}
	id := d.chat(t, "now")
	v := d.view()
	openChat(t, v, id)
	cmd := v.updateKeyCmd(registryKey(t, "alt+h"))
	v.applyPrompts(runCmd(t, cmd, 30*time.Second).(chatPromptsMsg))
	if len(v.history.items) != 1 || v.history.items[0].ChatID != old {
		t.Fatalf("history = %+v", v.history.items)
	}
	typeDraft(v, "pars") // narrows through the trigram index
	v.applyPrompts(runCmd(t, v.fetchHistory(), 30*time.Second).(chatPromptsMsg))
	if len(v.history.items) != 1 {
		t.Fatalf("narrowed history = %+v", v.history.items)
	}
	v.updateKey(registryKey(t, "enter"))
	if v.history.open || v.composer.Value() != "explain the parser" || v.sending {
		t.Fatalf("enter: open=%v composer=%q sending=%v", v.history.open, v.composer.Value(), v.sending)
	}
	turns, err := d.st.ListChatTurns(context.Background(), id)
	if err != nil || len(turns) != 0 {
		t.Fatalf("the insert sent a turn: %v %v", turns, err)
	}
}
