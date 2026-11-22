package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// TestTurnZeroBlock (task 171 decision 4): a seeded fork shows where it came
// from and, under the turn-0 heading, the context its first turn receives;
// a natively resumed fork says so instead; a plain chat shows nothing.
func TestTurnZeroBlock(t *testing.T) {
	seeded := &apiclient.Chat{
		ID: 9, ResumePending: "seeded", PendingPreamble: "<earlier-turns>\nthe summary\n</earlier-turns>\n",
		ForkedFrom: &apiclient.ChatForkedFrom{ChatID: 4, Seq: 3, Agent: "claude"},
	}
	got := ansi.Strip(strings.Join(turnZeroLines(seeded, 80), "\n"))
	for _, want := range []string{
		"forked from #4 (claude), before turn 3", "turn 0 · seeded context (summary)",
		"the summary", "vincent chat preamble 9",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("seeded fork: no %q in\n%s", want, got)
		}
	}
	resumed := &apiclient.Chat{ID: 10, ResumePending: "resumed", ForkedFrom: &apiclient.ChatForkedFrom{ChatID: 4, Seq: 3}}
	got = ansi.Strip(strings.Join(turnZeroLines(resumed, 80), "\n"))
	if !strings.Contains(got, "forked from #4, before turn 3") || !strings.Contains(got, "resumed from turn 2") ||
		strings.Contains(got, "turn 0") {
		t.Errorf("resumed fork:\n%s", got)
	}
	if lines := turnZeroLines(&apiclient.Chat{ID: 1}, 80); len(lines) != 0 {
		t.Errorf("plain chat: %q", lines)
	}
	// A seeded rewind keeps its turns: its context is still shown, under
	// the pending line after the last turn, with the edit hint (review F2).
	rewound := chatViewFixture()
	rewound.chat.ResumePending = "seeded"
	rewound.chat.PendingPreamble = "<earlier-turns>\nthe rewound summary\n</earlier-turns>\n"
	rewound.applyLoaded(chatLoadedMsg{id: 1, chat: rewound.chat, turns: []apiclient.ChatTurn{
		{Seq: 1, Prompt: "kept turn", State: "succeeded"},
	}})
	got = ansi.Strip(strings.Join(rewound.bodyLines(100), "\n"))
	kept, pending, ctx := strings.Index(got, "kept turn"), strings.Index(got, "seeded, not resumed"), strings.Index(got, "turn 0 · seeded context (summary)")
	if kept < 0 || pending < kept || ctx < pending ||
		!strings.Contains(got, "the rewound summary") || !strings.Contains(got, "vincent chat preamble 1") {
		t.Errorf("seeded rewind with turns: want the turn, the pending line, then turn 0:\n%s", got)
	}
	long := &apiclient.Chat{ID: 11, ResumePending: "seeded", PendingPreamble: strings.Repeat("line\n", 100)}
	got = ansi.Strip(strings.Join(turnZeroLines(long, 80), "\n"))
	if !strings.Contains(got, "… 60 more lines") {
		t.Errorf("long preamble not bounded:\n%s", got)
	}
}

func boolp(b bool) *bool { return &b }

// TestForkPickerOffersOtherAgents (task 171 decision 2): the fork picker's
// agent choice is the source's own agent first, then every other available
// adapter that resumes, sorted; tab cycles it and enter sends it.
func TestForkPickerOffersOtherAgents(t *testing.T) {
	stub, client := newRewindStub(t)
	v := rewindFixture()
	v.client = client
	v.agents = map[string]apiclient.Agent{
		"claude":  {Name: "claude", Available: true, SupportsResume: boolp(true)},
		"cursor":  {Name: "cursor", Available: true, SupportsResume: boolp(true)},
		"codex":   {Name: "codex", Available: true, SupportsResume: boolp(true)},
		"copilot": {Name: "copilot", Available: false, SupportsResume: boolp(true)},
		"unknown": {Name: "unknown", Available: true},
		"nores":   {Name: "nores", Available: true, SupportsResume: boolp(false)},
	}
	v.openRewindPicker(true)
	if got := v.rewind.agents; !slices.Equal(got, []string{"", "codex", "cursor"}) {
		t.Fatalf("agents = %q", got)
	}
	if title := v.rewind.title(); !strings.Contains(title, "fork onto [same agent] codex cursor") {
		t.Fatalf("title = %q", title)
	}
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyTab})
	if title := v.rewind.title(); !strings.Contains(title, "[codex]") {
		t.Fatalf("after tab, title = %q", title)
	}
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyUp}) // turn 3
	_, cmd := v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter sent no fork")
	}
	cmd()
	if got := stub.body("/v1/chats/1/fork"); got != `{"turn":3,"agent":"codex"}` {
		t.Fatalf("fork body = %s", got)
	}

	// No other agent: the plain title, and shift+tab does nothing.
	v = rewindFixture()
	v.openRewindPicker(true)
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyTab, Mod: tea.ModShift})
	if v.rewind.forkAgent() != "" || strings.Contains(v.rewind.title(), "fork onto") {
		t.Fatalf("lone agent: agent %q, title %q", v.rewind.forkAgent(), v.rewind.title())
	}
}

// TestForkProvenanceOnCardAndHeader (task 171 decision 5): a fork's card and
// its conversation's header both say `forked from #N (agent)`.
func TestForkProvenanceOnCardAndHeader(t *testing.T) {
	c := apiclient.Chat{
		ID: 9, Title: "a fork", State: "idle", Agent: "codex",
		ForkedFrom: &apiclient.ChatForkedFrom{ChatID: 4, Seq: 3, Agent: "claude"},
	}
	cv := &chatsView{now: func() time.Time { return time.Date(2026, 11, 22, 0, 0, 0, 0, time.UTC) }}
	card := ansi.Strip(strings.Join(cv.chatCard(c, false, 40), "\n"))
	if !strings.Contains(card, "forked from #4 (claude)") || !strings.Contains(card, "a fork") {
		t.Fatalf("card:\n%s", card)
	}
	plain := c
	plain.ForkedFrom = nil
	if card := ansi.Strip(strings.Join(cv.chatCard(plain, false, 40), "\n")); strings.Contains(card, "forked") {
		t.Fatalf("plain card:\n%s", card)
	}

	v := chatViewFixture()
	v.chat = &c
	if h := ansi.Strip(v.headerLine(160)); !strings.Contains(h, "forked from #4 (claude)") {
		t.Fatalf("header = %q", h)
	}
}

// TestTurnZeroEditRoundTrip (task 171 decision 4): the palette's "edit turn
// 0…" hands the seeded context to a real editor process, and what it leaves
// is sent as the chat's pending_preamble; an emptied file clears it, an
// untouched one sends nothing.
func TestTurnZeroEditRoundTrip(t *testing.T) {
	bin := buildFakeEditor(t)
	t.Setenv("VISUAL", `"`+bin+`"`)
	t.Setenv("EDITOR", "")

	var (
		mu   sync.Mutex
		sent []string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("PATCH /v1/chats/1", func(w http.ResponseWriter, r *http.Request) {
		var req apiclient.PatchChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		sent = append(sent, *req.PendingPreamble)
		mu.Unlock()
		out := apiclient.Chat{ID: 1, State: "idle", Agent: "claude"}
		if *req.PendingPreamble != "" {
			out.ResumePending, out.PendingPreamble = "seeded", *req.PendingPreamble
		}
		_ = json.NewEncoder(w).Encode(out)
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	fixture := func() *chatView {
		v := chatViewFixture()
		v.exec = runSync
		v.client = apiclient.New(ts.URL, "token")
		v.chat.ResumePending, v.chat.PendingPreamble = "seeded", "the summary\n"
		return v
	}
	edit := func(t *testing.T, v *chatView) tea.Cmd {
		t.Helper()
		var row *paletteEntry
		for _, e := range v.rewindPaletteExtras() {
			if strings.HasPrefix(e.label, "edit turn 0") {
				row = &e
			}
		}
		if row == nil {
			t.Fatal("no edit turn 0 palette row on a seeded chat")
		}
		_, cmd := v.update(row.action())
		if cmd == nil {
			t.Fatalf("edit turn 0 started no editor (note %q)", v.note)
		}
		_, cmd = v.update(cmd())
		return cmd
	}
	save := func(t *testing.T, v *chatView, cmd tea.Cmd) {
		t.Helper()
		if cmd == nil {
			t.Fatalf("nothing was sent (note %q)", v.note)
		}
		v.update(cmd())
	}

	t.Run("replaced", func(t *testing.T) {
		t.Setenv("FAKEEDITOR_MODE", "replace")
		t.Setenv("FAKEEDITOR_TEXT", "my own summary")
		v := fixture()
		save(t, v, edit(t, v))
		if v.chat.PendingPreamble == "" || !strings.Contains(v.note, "turn 0 saved") {
			t.Fatalf("chat %+v, note %q", v.chat, v.note)
		}
	})
	t.Run("emptied clears", func(t *testing.T) {
		t.Setenv("FAKEEDITOR_MODE", "empty")
		v := fixture()
		save(t, v, edit(t, v))
		if v.chat.PendingPreamble != "" || !strings.Contains(v.note, "turn 0 cleared") {
			t.Fatalf("chat %+v, note %q", v.chat, v.note)
		}
	})
	t.Run("unchanged sends nothing", func(t *testing.T) {
		t.Setenv("FAKEEDITOR_MODE", "keep")
		v := fixture()
		if cmd := edit(t, v); cmd != nil {
			t.Fatal("an untouched file sent a PATCH")
		}
		if v.note != "turn 0 is unchanged" {
			t.Fatalf("note = %q", v.note)
		}
	})
	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 || !strings.HasPrefix(sent[0], "my own summary") || sent[1] != "" {
		t.Fatalf("sent = %q", sent)
	}

	// No seeded context, or a live turn: no row.
	for _, c := range []*apiclient.Chat{
		{ID: 1, State: "idle"},
		{ID: 1, State: "running", ResumePending: "seeded", PendingPreamble: "x"},
	} {
		v := chatViewFixture()
		v.chat = c
		for _, e := range v.rewindPaletteExtras() {
			if strings.HasPrefix(e.label, "edit turn 0") {
				t.Fatalf("edit row offered on %+v", c)
			}
		}
	}
}
