package tui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// rewindFixtureTurns is a chat of five turns: turn 1 from before checkpoints,
// turn 2 rewindable in every mode (its conversation seeded), turn 3 blocked
// on foreign commits for code only, turn 4 already rewound, turn 5 live.
func rewindFixtureTurns() []apiclient.ChatTurn {
	at := time.Date(2026, 11, 21, 10, 0, 0, 0, time.UTC)
	return []apiclient.ChatTurn{
		{Seq: 1, Prompt: "first", State: "succeeded", Rewind: &apiclient.ChatTurnRewind{
			Code: reasonCheckpointUnavailable, Conversation: reasonCheckpointUnavailable, Fork: reasonCheckpointUnavailable,
		}},
		{Seq: 2, Prompt: "second\nwith two lines", State: "succeeded", Rewind: &apiclient.ChatTurnRewind{ConversationSeeded: true}},
		{Seq: 3, Prompt: "third", State: "succeeded", Rewind: &apiclient.ChatTurnRewind{Code: reasonForeignCommits}},
		{Seq: 4, Prompt: "rewound one", State: "succeeded", RewoundAt: &at, RewindID: "r1", Rewind: &apiclient.ChatTurnRewind{
			Code: reasonTurnRewound, Conversation: reasonTurnRewound, Fork: reasonTurnRewound,
		}},
		{Seq: 5, Prompt: "live", State: "running", Rewind: &apiclient.ChatTurnRewind{
			Code: reasonTurnLive, Conversation: reasonTurnLive, Fork: reasonTurnLive,
		}},
	}
}

func rewindFixture() *chatView {
	v := chatViewFixture()
	v.chat.RewoundStates = []apiclient.ChatRewoundState{
		{K: 1, Ref: "refs/vincent/0123456789abcdef/chats/1/rewound-1", SHA: "abc"},
		{K: 2, Ref: "refs/vincent/0123456789abcdef/chats/1/rewound-2", SHA: "def", Blocked: reasonForeignCommits},
	}
	v.applyLoaded(chatLoadedMsg{id: 1, chat: v.chat, turns: rewindFixtureTurns()})
	return v
}

// pickerRows is the picker's rendered rows, stripped, without its title.
func pickerRows(p *chatRewindPicker) []string {
	out := p.render(200)[2:] // the title, then the other modes (#1080)
	for i := range out {
		out[i] = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ansi.Strip(out[i])), "▸"))
	}
	return out
}

func TestRewindPickerRowsAndReasons(t *testing.T) {
	v := rewindFixture()
	v.openRewindPicker(false)
	p := v.rewind
	if p == nil {
		t.Fatal("the picker did not open")
	}
	want := []string{
		"turn 1  first — no checkpoint",
		"turn 2  second with two lines — seeded, not resumed",
		"turn 3  third — commits not made by this chat",
		"turn 5  live — a turn is live",
		"forward to the code before rewind 1 (code only)",
		"forward to the code before rewind 2 (code only) — commits not made by this chat",
	}
	if got := pickerRows(p); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("rows in mode both:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if p.cursor != 3 {
		t.Fatalf("cursor = %d, want the newest turn (row 3)", p.cursor)
	}

	// tab cycles to code, then conversation: turn 3 only refuses code, and
	// turn 2's conversation says it would be seeded.
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if p.mode != rewindCode {
		t.Fatalf("mode after tab = %q, want code", p.mode)
	}
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if p.mode != rewindConversation {
		t.Fatalf("mode after two tabs = %q, want conversation", p.mode)
	}
	rows := pickerRows(p)
	if rows[1] != "turn 2  second with two lines — seeded, not resumed" || rows[2] != "turn 3  third" {
		t.Fatalf("conversation rows:\n%s", strings.Join(rows, "\n"))
	}
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if p.mode != rewindCode {
		t.Fatalf("shift+tab did not go back to code: %q", p.mode)
	}
}

func TestRewindPickerReasonWords(t *testing.T) {
	for code, want := range map[string]string{
		reasonCheckpointUnavailable: "no checkpoint",
		reasonForeignCommits:        "commits not made by this chat",
		reasonTurnLive:              "a turn is live",
		reasonTurnRewound:           "already rewound",
		reasonChatTerminal:          "chat closed",
		"something_new":             "something_new",
	} {
		if got := rewindReasonText(code); got != want {
			t.Errorf("rewindReasonText(%q) = %q, want %q", code, got, want)
		}
	}
}

func TestForkPickerUsesForkAvailability(t *testing.T) {
	v := rewindFixture()
	v.openRewindPicker(true)
	rows := pickerRows(v.rewind)
	want := []string{
		"turn 1  first — no checkpoint",
		"turn 2  second with two lines",
		"turn 3  third",
		"turn 5  live — a turn is live",
	}
	if strings.Join(rows, "\n") != strings.Join(want, "\n") {
		t.Fatalf("fork rows:\n%s", strings.Join(rows, "\n"))
	}
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if v.rewind.mode != rewindBoth {
		t.Fatal("tab changed a fork picker's mode")
	}
}

func TestRewindPickerRefusesADisabledRow(t *testing.T) {
	v := rewindFixture()
	v.client = offlineClient()
	v.openRewindPicker(false)
	// The cursor is on turn 5, which is live.
	if _, cmd := v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter}); cmd != nil {
		t.Fatal("enter on a live turn sent a request")
	}
	if v.rewind == nil || v.rewind.err != "cannot rewind here: a turn is live" {
		t.Fatalf("picker = %+v, want it open saying why", v.rewind)
	}
}

// rewindStub is a daemon answering only the rewind, fork and chat routes,
// recording each body it was sent.
type rewindStub struct {
	mu     sync.Mutex
	bodies map[string]string
}

func newRewindStub(t *testing.T) (*rewindStub, *apiclient.Client) {
	t.Helper()
	s := &rewindStub{bodies: map[string]string{}}
	mux := http.NewServeMux()
	record := func(r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.bodies[r.URL.Path] = strings.TrimSpace(string(b))
		s.mu.Unlock()
	}
	mux.HandleFunc("POST /v1/chats/1/rewind", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		_ = json.NewEncoder(w).Encode(apiclient.RewindResult{
			Chat: apiclient.Chat{ID: 1}, RewindID: "r2", Ref: "refs/vincent/0123456789abcdef/chats/1/rewound-3",
			Skipped: []string{"locked.db"}, Seeded: true, RewoundTurns: []int{2, 3},
		})
	})
	mux.HandleFunc("POST /v1/chats/1/fork", func(w http.ResponseWriter, r *http.Request) {
		record(r)
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(apiclient.ForkResult{Chat: apiclient.Chat{ID: 9, ProjectID: 7}, Seeded: true})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, apiclient.New(ts.URL, "token")
}

func (s *rewindStub) body(path string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bodies[path]
}

func TestRewindSendsTheChosenTurnAndMode(t *testing.T) {
	stub, client := newRewindStub(t)
	v := rewindFixture()
	v.client = client
	v.openRewindPicker(false)
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyUp})
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyUp})  // turn 2
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyTab}) // code
	_, cmd := v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil || !v.rewind.busy {
		t.Fatal("enter on turn 2 sent nothing")
	}
	msg := cmd()
	if got := stub.body("/v1/chats/1/rewind"); got != `{"turn":2,"mode":"code"}` {
		t.Fatalf("rewind body = %s", got)
	}
	rw, ok := msg.(chatRewoundMsg)
	if !ok || rw.err != nil {
		t.Fatalf("answer = %#v", msg)
	}
	if reload := v.applyRewound(rw); reload == nil {
		t.Fatal("a rewind did not reload the chat")
	}
	if v.rewind != nil {
		t.Fatal("the picker stayed open after the rewind")
	}
	for _, want := range []string{"2 turns hidden", "seeded, not resumed", "locked.db"} {
		if !strings.Contains(v.status().text, want) {
			t.Fatalf("note %q does not say %q", v.status().text, want)
		}
	}
}

func TestRewindForwardSendsTheSavedState(t *testing.T) {
	stub, client := newRewindStub(t)
	v := rewindFixture()
	v.client = client
	v.openRewindPicker(false)
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyDown}) // rewound-1
	_, cmd := v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on a forward row sent nothing")
	}
	cmd()
	if got := stub.body("/v1/chats/1/rewind"); got != `{"rewound":1}` {
		t.Fatalf("forward body = %s", got)
	}
}

func TestForkSendsTheTurnAndOpensTheNewChat(t *testing.T) {
	stub, client := newRewindStub(t)
	v := rewindFixture()
	v.client = client
	v.back = viewChats
	v.openRewindPicker(true)
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyUp}) // turn 3
	_, cmd := v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter on turn 3 sent no fork")
	}
	msg := cmd()
	if got := stub.body("/v1/chats/1/fork"); got != `{"turn":3}` {
		t.Fatalf("fork body = %s", got)
	}
	open := v.applyRewound(msg.(chatRewoundMsg))
	if open == nil {
		t.Fatal("a fork did not open the new chat")
	}
	o, ok := open().(openChatMsg)
	if !ok || o.id != 9 || o.projectID != 7 || o.back != viewChats || !strings.Contains(o.note, "seeded, not resumed") {
		t.Fatalf("open = %#v", open())
	}
}

func TestRewindRefusalStaysOnThePicker(t *testing.T) {
	v := rewindFixture()
	v.openRewindPicker(false)
	v.rewind.busy = true
	v.applyRewound(chatRewoundMsg{chatID: 1, err: &apiclient.Error{Status: 409, Code: reasonForeignCommits, Message: "x"}})
	if v.rewind == nil || v.rewind.busy || v.rewind.err != "commits not made by this chat" {
		t.Fatalf("picker = %+v", v.rewind)
	}
}

func TestRewoundTurnsHiddenUntilToggled(t *testing.T) {
	v := rewindFixture()
	v.width, v.height = 100, 40
	body := ansi.Strip(strings.Join(v.bodyLines(100), "\n"))
	if strings.Contains(body, "rewound one") || strings.Contains(body, "↶ rewound") {
		t.Fatalf("a rewound turn is drawn by default:\n%s", body)
	}
	if len(v.turns) != 4 || len(v.everyTurn()) != 5 {
		t.Fatalf("turns %d / all %d, want 4 / 5", len(v.turns), len(v.everyTurn()))
	}

	rows := v.paletteExtras()
	var toggle *paletteEntry
	for i := range rows {
		if strings.Contains(rows[i].label, "show rewound turns") {
			toggle = &rows[i]
		}
	}
	if toggle == nil {
		t.Fatalf("no toggle on the palette: %+v", rows)
	}
	v.update(toggle.action())
	if !v.showRewound {
		t.Fatal("the toggle did not show rewound turns")
	}
	lines := v.bodyLines(100)
	joined := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(joined, "rewound one") || !strings.Contains(joined, "↶ rewound") {
		t.Fatalf("the toggled body lacks the rewound turn:\n%s", joined)
	}
	// Dimmed: the marker line carries the dim style and nothing else.
	for _, l := range lines {
		if strings.Contains(ansi.Strip(l), "↶ rewound 2026") && l != styleDim.Render(ansi.Strip(l)) {
			t.Fatalf("the rewound marker is not dimmed: %q", l)
		}
	}
	v.toggleRewound()
	if v.showRewound || len(v.turns) != 4 {
		t.Fatal("the toggle did not hide them again")
	}
}

func TestNoToggleWithoutRewoundTurns(t *testing.T) {
	v := chatViewFixture()
	v.applyLoaded(chatLoadedMsg{id: 1, chat: v.chat, turns: rewindFixtureTurns()[:2]})
	for _, r := range v.paletteExtras() {
		if strings.Contains(r.label, "rewound") {
			t.Fatalf("a toggle with nothing rewound: %q", r.label)
		}
	}
}

func TestSeededTurnAndPendingResumeAreLabelled(t *testing.T) {
	v := chatViewFixture()
	v.chat.ResumePending = "seeded"
	v.applyLoaded(chatLoadedMsg{id: 1, chat: v.chat, turns: []apiclient.ChatTurn{
		{Seq: 1, Prompt: "hi", State: "succeeded", Seeded: true},
	}})
	body := ansi.Strip(strings.Join(v.bodyLines(140), "\n"))
	if strings.Count(body, "seeded, not resumed") != 2 {
		t.Fatalf("want the seeded turn and the pending resume labelled:\n%s", body)
	}
}

func TestRewindPaletteRows(t *testing.T) {
	v := rewindFixture()
	var fork *paletteEntry
	for _, r := range v.paletteExtras() {
		if strings.HasPrefix(r.label, "fork from turn…") {
			fork = &r
		}
	}
	if fork == nil {
		t.Fatal("no fork row on the palette")
	}
	v.update(fork.action())
	if v.rewind == nil || !v.rewind.fork {
		t.Fatal("the fork row did not open the fork picker")
	}
	found := false
	for _, b := range bindingsFor(ctxChat) {
		if b.op == keymap.Rewind && strings.HasPrefix(b.label, "rewind…") {
			found = true
		}
	}
	if !found {
		t.Fatal("the chat's palette has no rewind… row")
	}
}

// TestNoForkRowOnAnEndedChat is review F8: the daemon refuses to fork a
// terminal chat (chat_terminal, decision 8), so the palette does not offer
// it — every row of the picker would be disabled.
func TestNoForkRowOnAnEndedChat(t *testing.T) {
	for _, state := range []string{"archived", "closed", "handed_off"} {
		v := rewindFixture()
		v.chat.State = state
		for _, r := range v.paletteExtras() {
			if strings.HasPrefix(r.label, "fork from turn…") {
				t.Errorf("%s chat: the palette offers %q", state, r.label)
			}
		}
	}
}

func TestRewindKeyNotOfferedOnAClosedChat(t *testing.T) {
	v := rewindFixture()
	v.chat.State = "closed"
	for _, b := range v.liveBindings(bindingsFor(ctxChat)) {
		if b.op == keymap.Rewind {
			t.Fatal("rewind offered on a closed chat")
		}
	}
}
