package tui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// compareFixture is compare 3 of chat 7 from before turn 2: chat 12 on
// claude, restricted, and chat 13 on codex, which could not be, both idle
// with an answer.
func compareFixture() *compareView {
	v := newCompareView()
	v.now = func() time.Time { return time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC) }
	v.chatID = 12
	v.cmp = &apiclient.ChatCompare{
		ID: 3, SourceChatID: 7, Seq: 2, Prompt: "which way?",
		Left: apiclient.CompareSide{ChatID: i64(12), Agent: "claude", PermissionMode: "restricted"},
		Right: apiclient.CompareSide{
			ChatID: i64(13), Agent: "codex", PermissionMode: "full-auto",
			ModeNote: `agent "codex" cannot keep a resumed chat turn restricted`,
		},
	}
	at := time.Date(2026, 11, 22, 11, 0, 0, 0, time.UTC)
	for i, side := range []struct {
		id     int64
		agent  string
		answer string
	}{{12, "claude", "go left"}, {13, "codex", "go right"}} {
		p := previewFrom(&apiclient.Chat{ID: side.id, ProjectID: 1, Agent: side.agent, State: "idle"},
			[]apiclient.ChatTurn{{Seq: 1, Prompt: "which way?", State: "done", ResultText: side.answer, StartedAt: at}}, nil, nil)
		v.sides[i] = &p
	}
	return v
}

// TestCompareSplitView (task 171 decision 9): both answers side by side,
// each with its agent and the mode it really ran under — full-auto with why.
func TestCompareSplitView(t *testing.T) {
	v := compareFixture()
	got := ansi.Strip(v.render(140, 30))
	for _, want := range []string{
		"compare #3", "chat #7, from before turn 2", "prompt: which way?",
		"go left", "go right", "claude · restricted", "codex · full-auto",
		"cannot keep a resumed", "[ keep this side", "] keep this side",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("split view lacks %q:\n%s", want, got)
		}
	}
	// Side by side: both answers on one line band, not stacked.
	for _, l := range strings.Split(got, "\n") {
		if strings.Contains(l, "go left") && !strings.Contains(l, "go right") {
			left := strings.Index(got, "go left")
			right := strings.Index(got, "go right")
			if right-left > 200 {
				t.Errorf("the sides are not side by side:\n%s", got)
			}
		}
	}
	// Narrow: stacked, both still there.
	narrow := ansi.Strip(v.render(60, 40))
	if !strings.Contains(narrow, "go left") || !strings.Contains(narrow, "go right") ||
		strings.Index(narrow, "go right") < strings.Index(narrow, "go left") {
		t.Errorf("narrow view:\n%s", narrow)
	}
}

// TestCompareKeepAsksAndRefuses: keep asks first, any key but y declines,
// and keeping is not offered while a side works or once one was kept.
func TestCompareKeepAsksAndRefuses(t *testing.T) {
	v := compareFixture()
	v.updateKey(tea.KeyPressMsg{Code: ']', Text: "]"})
	if v.confirm != 1 || !strings.Contains(v.note, "keep right (codex)? the left side (claude)") {
		t.Fatalf("confirm %d note %q", v.confirm, v.note)
	}
	if _, cmd := v.updateKey(tea.KeyPressMsg{Code: 'n', Text: "n"}); cmd != nil || v.confirm != -1 || v.note != "nothing kept" {
		t.Fatalf("n: confirm %d note %q", v.confirm, v.note)
	}

	v.sides[1].chat.State = "running"
	v.updateKey(tea.KeyPressMsg{Code: '[', Text: "["})
	if v.confirm != -1 || !v.noteBad || !strings.Contains(v.note, "must finish") {
		t.Fatalf("while running: confirm %d note %q", v.confirm, v.note)
	}

	v = compareFixture()
	v.cmp.KeptChatID = i64(12)
	v.updateKey(tea.KeyPressMsg{Code: '[', Text: "["})
	if v.confirm != -1 || v.note != "a side was already kept" {
		t.Fatalf("kept: confirm %d note %q", v.confirm, v.note)
	}
	got := ansi.Strip(v.render(140, 30))
	if !strings.Contains(got, "✓ kept #12") || !strings.Contains(got, "discarded — archived, branch deleted") {
		t.Errorf("kept view:\n%s", got)
	}
}

// TestCompareKeepSendsAndRereads: y sends the keep for the chosen side's
// chat, and the answer is noted and the pair read again.
func TestCompareKeepSendsAndRereads(t *testing.T) {
	var (
		mu   sync.Mutex
		hits []string
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chats/13/compare/keep", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		hits = append(hits, r.URL.Path)
		mu.Unlock()
		_ = json.NewEncoder(w).Encode(apiclient.KeepResult{
			Compare: apiclient.ChatCompare{ID: 3, KeptChatID: i64(13)}, Kept: apiclient.Chat{ID: 13},
			Archived: &apiclient.Chat{ID: 12, State: "archived"}, BranchDeleted: true,
		})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	v := compareFixture()
	v.client = apiclient.New(ts.URL, "t")
	v.updateKey(tea.KeyPressMsg{Code: ']', Text: "]"})
	_, cmd := v.updateKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
	if cmd == nil || !v.busy {
		t.Fatal("y sent nothing")
	}
	msg, ok := cmd().(compareKeptMsg)
	if !ok || msg.err != nil {
		t.Fatalf("keep answer = %#v", msg)
	}
	if reread := v.applyKept(msg); reread == nil {
		t.Fatal("a keep did not read the pair again")
	}
	if v.note != "kept chat 13; chat 12 archived, its branch deleted" || v.busy {
		t.Fatalf("note %q busy %v", v.note, v.busy)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(hits) != 1 {
		t.Fatalf("keep requests = %v", hits)
	}
}

// TestComparePickerStartsBothAndOpensTheSplitView (task 171): the compare
// picker offers only other agents, sends the source's agent and the chosen
// one with the composer's draft as the prompt, and opens the split view.
func TestComparePickerStartsBothAndOpensTheSplitView(t *testing.T) {
	var (
		mu   sync.Mutex
		body apiclient.CompareRequest
	)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chats/1/compare", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Unlock()
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(apiclient.CompareResult{
			Compare: apiclient.ChatCompare{ID: 3}, Left: apiclient.CompareStarted{Chat: apiclient.Chat{ID: 12}},
			Right: apiclient.CompareStarted{Chat: apiclient.Chat{ID: 13}},
		})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	v := rewindFixture()
	v.client = apiclient.New(ts.URL, "t")
	v.agents = map[string]apiclient.Agent{
		"claude": {Name: "claude", Available: true, SupportsResume: boolp(true)},
		"codex":  {Name: "codex", Available: true, SupportsResume: boolp(true)},
	}
	var row *paletteEntry
	for _, e := range v.rewindPaletteExtras() {
		if strings.HasPrefix(e.label, "compare two agents") {
			row = &e
		}
	}
	if row == nil {
		t.Fatal("no compare row with another agent available")
	}
	v.update(row.action())
	if v.rewind == nil || !v.rewind.compare || len(v.rewind.agents) != 1 || v.rewind.agents[0] != "codex" {
		t.Fatalf("compare picker = %+v", v.rewind)
	}
	v.composer.SetValue("which is safer?")
	v.updateKey(tea.KeyPressMsg{Code: tea.KeyUp}) // turn 3
	_, cmd := v.updateKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("enter started nothing (err %q)", v.rewind.err)
	}
	_, open := v.update(cmd())
	o, ok := drain(open).(openCompareMsg)
	if !ok || o.chatID != 12 || o.back != viewChat {
		t.Fatalf("after compare: %#v", drain(open))
	}
	mu.Lock()
	defer mu.Unlock()
	if body.Turn != 3 || body.Prompt != "which is safer?" || len(body.Agents) != 2 ||
		body.Agents[0] != "claude" || body.Agents[1] != "codex" {
		t.Fatalf("compare body = %+v", body)
	}

	// No other agent: no compare row.
	v = rewindFixture()
	for _, e := range v.rewindPaletteExtras() {
		if strings.HasPrefix(e.label, "compare two agents") {
			t.Fatal("compare offered with no other agent")
		}
	}
}

// TestCompareSideOffersTheSplitView: a fork that is a compare side offers
// "compare side by side" once the daemon says so; a plain fork does not.
func TestCompareSideOffersTheSplitView(t *testing.T) {
	v := chatViewFixture()
	v.chat.ForkedFrom = &apiclient.ChatForkedFrom{ChatID: 7, Seq: 2}
	has := func() bool {
		for _, e := range v.rewindPaletteExtras() {
			if strings.HasPrefix(e.label, "compare side by side") {
				return true
			}
		}
		return false
	}
	if has() {
		t.Fatal("offered before the daemon said it is a side")
	}
	v.update(chatCompareOfMsg{chatID: 1, cmp: &apiclient.ChatCompare{ID: 3}})
	if !has() {
		t.Fatal("not offered on a compare side")
	}
}
