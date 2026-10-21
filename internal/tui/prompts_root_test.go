package tui

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The tests in this file hold issue #790: a prompt that asks before
// destroying something owns the keyboard until it is answered, its question
// is drawn, its outcome reaches the board that asked, and a refused chat send
// keeps the draft. They drive keys through newRoot rather than a view's
// updateKey, because the defect is in the layer between the two: a view that
// does not report capturesInput lets the root's global keys run first.

// promptRoot is a connected, sized root showing view id, which holds v.
func promptRoot(t *testing.T, id viewID, v panel) *root {
	t.Helper()
	m := newRoot(testCtx(t), fakeConnector(), ackedDir(t))
	m.phase = phaseConnected
	m.views[id] = v
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m.switchTo(id)
	if m.active != id {
		t.Fatalf("fixture is on %v, want %v", m.active, id)
	}
	return m
}

// isQuitCmd reports whether cmd is the root's global quit, which it returns
// unwrapped. It compares rather than runs cmd: a view's command may block.
func isQuitCmd(cmd tea.Cmd) bool {
	return cmd != nil && reflect.ValueOf(cmd).Pointer() == reflect.ValueOf(tea.Quit).Pointer()
}

// TestDestructivePromptsHoldTheKeyboardThroughRoot is the acceptance's first
// two criteria: with a delete (or archive) question up, `q` and `:` neither
// quit nor open the palette, the view does not change, and `n` answers "no"
// rather than opening New task with the prompt still armed behind it. §15
// view 10: "`n` does nothing, and no other key answers it".
func TestDestructivePromptsHoldTheKeyboardThroughRoot(t *testing.T) {
	cases := []struct {
		name    string
		id      viewID
		build   func() panel
		arm     func(t *testing.T, m *root)
		pending func(m *root) bool
	}{
		{
			name:  "archived tasks",
			id:    viewArchived,
			build: func() panel { return testArchivedBoard() },
			arm:   func(_ *testing.T, m *root) { m.Update(key("D")) },
			pending: func(m *root) bool {
				return m.views[viewArchived].(*board).delPrompt != nil
			},
		},
		{
			name:  "archived chats",
			id:    viewArchivedChats,
			build: func() panel { return archivedChatsFixture() },
			arm:   func(_ *testing.T, m *root) { m.Update(key("D")) },
			pending: func(m *root) bool {
				return m.views[viewArchivedChats].(*chatsView).delPrompt != nil
			},
		},
		{
			name: "projects",
			id:   viewProjects,
			build: func() panel {
				p := newProjectsView()
				loadedProjects(p, []apiclient.Project{testProject(1, "vincent")}, nil)
				return p
			},
			arm: func(_ *testing.T, m *root) { m.Update(key("D")) },
			pending: func(m *root) bool {
				return m.views[viewProjects].(*projectsView).confirm != nil
			},
		},
		{
			name:  "live chats archive",
			id:    viewChats,
			build: func() panel { return chatsFixture() },
			arm: func(_ *testing.T, m *root) {
				m.views[viewChats].(*chatsView).confirm = &archivePrompt{
					id: 1, title: "first", text: `archive "first" and remove its worktree? (y/n)`,
				}
			},
			pending: func(m *root) bool {
				return m.views[viewChats].(*chatsView).confirm != nil
			},
		},
		{
			name: "pull requests unlink",
			id:   viewPullRequests,
			build: func() panel {
				return pullRequestsFixture(testPull(11, "claimed", claimedBy(7, "auto")))
			},
			arm: func(_ *testing.T, m *root) { m.Update(key("u")) },
			pending: func(m *root) bool {
				return m.views[viewPullRequests].(*pullRequestsView).confirm != nil
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := promptRoot(t, tc.id, tc.build())
			tc.arm(t, m)
			if !tc.pending(m) {
				t.Fatal("the fixture did not raise the prompt")
			}
			// The footer shows the prompt's answers while it is up.
			if foot := ansi.Strip(m.footerLine()); !strings.Contains(foot, "y ") || !strings.Contains(foot, "n no") {
				t.Errorf("the footer does not show the prompt's keys: %q", foot)
			}

			// `q` either answers the prompt or is ignored by it; it never
			// reaches the root's quit.
			if _, cmd := m.Update(key("q")); isQuitCmd(cmd) {
				t.Fatal("q quit the TUI with the prompt up")
			}
			if m.active != tc.id {
				t.Fatalf("q left %v for %v with the prompt up", tc.id, m.active)
			}

			tc.arm(t, m) // q may have answered "no"; ask again
			m.Update(key(":"))
			if m.palette != nil {
				t.Fatal(": opened the palette over the prompt")
			}
			if m.active != tc.id {
				t.Fatalf(": left %v for %v with the prompt up", tc.id, m.active)
			}

			// ctrl+p is `:` for a surface capturing text, and is hoisted
			// above the capture gate — but not over a question: the palette
			// navigates, and the question would wait armed behind it (review
			// F3). Nor does the footer offer it beside the answers.
			tc.arm(t, m)
			if foot := ansi.Strip(m.footerLine()); strings.Contains(foot, "commands") {
				t.Errorf("the prompt footer offers the palette: %q", foot)
			}
			m.Update(tea.KeyPressMsg{Code: 'p', Mod: tea.ModCtrl})
			if m.palette != nil {
				t.Fatal("ctrl+p opened the palette over the prompt")
			}
			if m.active != tc.id {
				t.Fatalf("ctrl+p left %v for %v with the prompt up", tc.id, m.active)
			}

			tc.arm(t, m)
			if !tc.pending(m) {
				t.Fatal("the prompt is not up before n")
			}
			m.Update(key("n"))
			if m.active != tc.id {
				t.Fatalf("n left %v for %v; n is the prompt's \"no\"", tc.id, m.active)
			}
			if tc.pending(m) {
				t.Fatal("n did not answer the prompt")
			}
		})
	}
}

// TestProjectFormHoldsTheKeyboardThroughRoot: with the project form open and
// no field being typed into, `n` must not leave the view with the form still
// open behind it.
func TestProjectFormHoldsTheKeyboardThroughRoot(t *testing.T) {
	p := newProjectsView()
	loadedProjects(p, []apiclient.Project{testProject(1, "vincent")}, nil)
	m := promptRoot(t, viewProjects, p)
	m.Update(key("a"))
	if p.form == nil {
		t.Fatal("a did not open the project form")
	}
	m.Update(key("n"))
	if m.active != viewProjects {
		t.Fatalf("n with the project form open went to %v", m.active)
	}
	if _, cmd := m.Update(key("q")); isQuitCmd(cmd) {
		t.Fatal("q with the project form open quit the TUI")
	}
}

// TestArchivedChatsDeleteQuestionIsDrawn: `D` on the archived chats board
// raises a prompt that swallows every key but its answers, so it must be on
// screen — at 80 columns, with all three answers.
func TestArchivedChatsDeleteQuestionIsDrawn(t *testing.T) {
	v := archivedChatsFixture()
	v.updateKey(registryKey(t, "D"))
	if v.delPrompt == nil {
		t.Fatal("D did not raise the delete prompt")
	}
	out := ansi.Strip(v.render(80, 24))
	// The board lists newest first, so the cursor opens on #2.
	for _, want := range []string{"delete chat 2 permanently?", "[y]", "[b]", "[n] no"} {
		if !strings.Contains(out, want) {
			t.Errorf("the archived chats board at 80 columns does not show %q:\n%s", want, out)
		}
	}
}

// TestChatDeleteResultReachesTheChatsBoard: a chat delete's outcome updates
// the chats note, and the task boards' marks are not touched by ids that
// happen to be shared with the chat's.
func TestChatDeleteResultReachesTheChatsBoard(t *testing.T) {
	for _, tc := range []struct {
		name string
		msg  deleteResultMsg
	}{
		{"success", deleteResultMsg{chats: true, total: 1, done: []int64{1}}},
		{"failure", deleteResultMsg{chats: true, total: 1, failed: []bulkFailure{{id: 1, err: errors.New("boom")}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			chats := archivedChatsFixture()
			m := promptRoot(t, viewArchivedChats, chats)
			tasks := testArchivedBoard()
			tasks.marks = markSet{1}
			m.views[viewArchived] = tasks

			chats.note = "deleting…"
			m.Update(tc.msg)

			if chats.note == "deleting…" {
				t.Error("the chats note still reads deleting… after the result landed")
			}
			if !tasks.marks.has(1) {
				t.Error("a chat delete dropped task 1 from the archived tasks board's selection")
			}
			if tasks.delNote != "" {
				t.Errorf("a chat delete reported on the archived tasks board: %q", tasks.delNote)
			}
		})
	}
}

// TestArchivedTasksPromptAndReportFitEightyColumns: the question's answer keys
// and the report's failed: clause survive an 80-column terminal.
func TestArchivedTasksPromptAndReportFitEightyColumns(t *testing.T) {
	b := testArchivedBoard()
	m := promptRoot(t, viewArchived, b)
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	m.Update(key("D"))
	if b.delPrompt == nil {
		t.Fatal("D did not raise the delete prompt")
	}
	out := ansi.Strip(content(m))
	for _, want := range []string{"[y]", "[b]", "[n] no"} {
		if !strings.Contains(out, want) {
			t.Errorf("the archived delete question at 80 columns does not show %q:\n%s", want, out)
		}
	}

	b.updateKey(registryKey(t, "n")) // directly: through the root, n is the bug above
	b.update(deleteResultMsg{
		total: 3, done: []int64{3},
		refused: []bulkFailure{{id: 2, err: errors.New("a lane is holding on")}},
		failed:  []bulkFailure{{id: 1, err: errors.New("connection refused")}},
	})
	out = ansi.Strip(content(m))
	for _, want := range []string{"failed:", "connection refused"} {
		if !strings.Contains(out, want) {
			t.Errorf("the archived delete report at 80 columns does not show %q:\n%s", want, out)
		}
	}
}

// TestChatViewKeepsTheDraftOnARefusedSend extends TestChatViewRefusesCapReached:
// a send the daemon refuses, or one that never reached it, leaves what the
// human typed in the composer byte for byte.
func TestChatViewKeepsTheDraftOnARefusedSend(t *testing.T) {
	const draft = "  please fix the\nflaky test  "
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"chat_cap_reached", &apiclient.Error{Status: 409, Code: "chat_cap_reached"}},
		{"invalid_state", &apiclient.Error{Status: 409, Code: "invalid_state"}},
		{"transport", errors.New("dial tcp 127.0.0.1:1: connection refused")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := chatViewFixture()
			v.client = &apiclient.Client{}
			v.composer.SetValue(draft)
			if cmd := v.sendCmd(); cmd == nil {
				t.Fatal("an idle chat with a draft sent nothing")
			}
			v.applySent(chatSentMsg{chatID: 1, err: tc.err})
			if got := v.composer.Value(); got != draft {
				t.Fatalf("after a %s refusal the composer holds %q, want %q", tc.name, got, draft)
			}
			if !v.noteBad || v.note == "" {
				t.Fatalf("the refusal rendered as %q (bad=%v)", v.note, v.noteBad)
			}
		})
	}
}

// TestChatViewRefusesEnterLocallyWhenNotIdle: `send` is legal only from idle
// (chatstate), so enter on any other chat sends nothing, keeps the draft and
// says why.
func TestChatViewRefusesEnterLocallyWhenNotIdle(t *testing.T) {
	for _, state := range []string{"running", "awaiting_input", "closed", "archived"} {
		t.Run(state, func(t *testing.T) {
			v := chatViewFixture()
			v.client = &apiclient.Client{}
			v.chat.State = state
			v.composer.SetValue("hello")
			if _, cmd := v.updateKey(registryKey(t, "enter")); cmd != nil {
				t.Fatalf("enter on a %s chat issued a command", state)
			}
			if got := v.composer.Value(); got != "hello" {
				t.Fatalf("enter on a %s chat changed the draft to %q", state, got)
			}
			if v.note == "" {
				t.Fatalf("enter on a %s chat set no note", state)
			}
		})
	}
}

// acceptSend lands the daemon's acceptance of the send in flight: the draft
// leaves the composer then, and not before (#790). Like sendCmd's message
// it carries the trimmed draft it sent.
func acceptSend(v *chatView) {
	v.applySent(chatSentMsg{chatID: v.chatID, sent: strings.TrimSpace(v.composer.Value())})
}

// TestChatViewSendsADraftOnce: the draft stays in the composer while its send
// is in flight, so a second enter must not send it again.
func TestChatViewSendsADraftOnce(t *testing.T) {
	v := chatViewFixture()
	v.client = &apiclient.Client{}
	v.composer.SetValue("hello")
	if cmd := v.sendCmd(); cmd == nil {
		t.Fatal("an idle chat with a draft sent nothing")
	}
	if cmd := v.sendCmd(); cmd != nil {
		t.Fatal("a second enter sent the draft again while the first was in flight")
	}
	acceptSend(v)
	if got := v.composer.Value(); got != "" {
		t.Fatalf("the composer still holds %q after the daemon took the turn", got)
	}
}

// TestChatViewIgnoresASendResultForAChatItLeft: a send's result that lands
// after the view moved to another chat is not that chat's — neither its
// refusal note nor its acceptance's reload and cleared composer (review F4).
func TestChatViewIgnoresASendResultForAChatItLeft(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"refused", &apiclient.Error{Status: 409, Code: "chat_cap_reached"}},
		{"accepted", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := chatViewFixture()
			v.client = &apiclient.Client{}
			v.composer.SetValue("for chat one")
			if cmd := v.sendCmd(); cmd == nil {
				t.Fatal("an idle chat with a draft sent nothing")
			}
			client := v.client
			v.client = nil // open would subscribe to a stream nobody serves
			v.open(2)
			v.client = client
			v.chat = &apiclient.Chat{ID: 2, ProjectID: 7, Title: "another", State: "idle", Agent: "claude"}
			v.composer.SetValue("for chat two")
			if cmd := v.applySent(chatSentMsg{chatID: 1, err: tc.err}); cmd != nil {
				t.Error("chat 1's send result reloaded chat 2")
			}
			if v.note != "" {
				t.Errorf("chat 1's send result set chat 2's note: %q", v.note)
			}
			if got := v.composer.Value(); got != "for chat two" {
				t.Errorf("chat 1's send result changed chat 2's draft to %q", got)
			}
		})
	}
}

// TestChatViewKeepsTextTypedWhileASendIsInFlight: the composer stays
// editable while a send is pending, so the daemon's acceptance takes out
// what was sent and nothing typed after it (review F8).
func TestChatViewKeepsTextTypedWhileASendIsInFlight(t *testing.T) {
	for _, tc := range []struct{ name, typed, want string }{
		{"untouched", "  first  ", ""},
		{"extended", "first\nsecond thought", "second thought"},
		{"edited", "fist", "fist"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := chatViewFixture()
			v.client = &apiclient.Client{}
			v.composer.SetValue("first")
			if cmd := v.sendCmd(); cmd == nil {
				t.Fatal("an idle chat with a draft sent nothing")
			}
			v.composer.SetValue(tc.typed)
			v.applySent(chatSentMsg{chatID: 1, sent: "first"})
			if got := v.composer.Value(); got != tc.want {
				t.Fatalf("after acceptance the composer holds %q, want %q", got, tc.want)
			}
		})
	}
}
