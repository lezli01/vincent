package tui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Task 124.13 (issue #509): the skill list above the chat composer. `tab`
// opens it, typing filters it, and accepting a row inserts the adapter's own
// invocation into the draft — which is the only moment anything here writes
// to the composer.

// claudeSkills is a `leading`/`/` answer with one of each row the ranking
// distinguishes: a name prefix, a plugin skill found by its bare name and by
// an alias, and a row only a description substring reaches.
func claudeSkills() *apiclient.ChatSkills {
	probed := time.Date(2026, 8, 31, 11, 58, 0, 0, time.UTC)
	return &apiclient.ChatSkills{
		ChatID: 1, Agent: "claude", ListVerdict: "supported", ProbedAt: &probed,
		InvokeVerdict: "supported", InvokeSigil: "/", InvokePosition: "leading",
		Skills: []apiclient.ChatSkill{
			{
				Name: "tdd", Invocation: "/tdd", Description: "Test-driven development",
				ArgumentHint: "[target]", Scope: "project",
			},
			{
				Name: "myplugin:deploy-app", Invocation: "/myplugin:deploy-app",
				Description: "Ship the app", Plugin: "myplugin", Aliases: []string{"ship"},
			},
			{Name: "notes", Invocation: "/notes", Description: "Write deploy notes"},
		},
	}
}

// codexSkills is an `anywhere`/`$` answer, including the linked form task 124
// decision 30 requires for a duplicated name — whose path has spaces in it.
func codexSkills() *apiclient.ChatSkills {
	probed := time.Date(2026, 8, 31, 11, 59, 0, 0, time.UTC)
	return &apiclient.ChatSkills{
		ChatID: 1, Agent: "codex", ListVerdict: "supported", ProbedAt: &probed,
		InvokeVerdict: "supported", InvokeSigil: "$", InvokePosition: "anywhere",
		Skills: []apiclient.ChatSkill{
			{Name: "review", Invocation: "$review", Description: "Review the diff"},
			{
				Name: "review", Description: "The repo's own review skill",
				Invocation: "[$review](/Users/you/Library/Application Support/vincent/data/worktrees/7/.agents/skills/review/SKILL.md)",
			},
		},
	}
}

// chatSkillsFixture is a chat workspace with an answer already in hand, so a
// press opens the list without a round trip.
func chatSkillsFixture(data *apiclient.ChatSkills) *chatView {
	v := chatViewFixture()
	v.client = offlineClient()
	v.skills.data = data
	return v
}

// openChatSkills presses key and asserts the list came up.
func openChatSkills(t *testing.T, v *chatView, key string) {
	t.Helper()
	v.updateKey(registryKey(t, key))
	if !v.skills.open {
		t.Fatalf("%s did not open the skill list (note %q)", key, v.note)
	}
}

// skillRowNames is the invocation of each row the list currently holds.
func skillRowNames(v *chatView) []string {
	out := make([]string, 0, len(v.skills.rows))
	for _, r := range v.skills.rows {
		out = append(out, r.invocation)
	}
	return out
}

// TestChatSkillsTabAndF2Open holds decision 69's two keys on both invoke
// positions, and decision 70's rule that looking at the list never edits the
// draft.
func TestChatSkillsTabAndF2Open(t *testing.T) {
	for _, key := range []string{"tab", "f2"} {
		for _, data := range []*apiclient.ChatSkills{claudeSkills(), codexSkills()} {
			t.Run(key+"/"+data.InvokePosition, func(t *testing.T) {
				v := chatSkillsFixture(data)
				v.composer.SetValue("fix the bug")
				openChatSkills(t, v, key)
				if got := v.composer.Value(); got != "fix the bug" {
					t.Fatalf("opening the list changed the draft to %q", got)
				}
				if v.skills.cursor != -1 {
					t.Fatalf("the list opened on row %d, want nothing highlighted", v.skills.cursor)
				}
				if len(v.skills.rows) != len(data.Skills) {
					t.Fatalf("the list opened showing %d rows, want every one of %d",
						len(v.skills.rows), len(data.Skills))
				}
			})
		}
	}
}

// TestChatSkillsEscIsOneLayer: the first esc closes the list and leaves the
// draft byte for byte, the second goes back to the chats board (§15's layer
// stack).
func TestChatSkillsEscIsOneLayer(t *testing.T) {
	v := chatSkillsFixture(claudeSkills())
	v.composer.SetValue("fix the bug")
	openChatSkills(t, v, "tab")
	v.updateKey(registryKey(t, "t")) // filter, not the draft
	if v.skills.filter != "t" {
		t.Fatalf("typing left the filter %q", v.skills.filter)
	}
	if got := v.composer.Value(); got != "fix the bug" {
		t.Fatalf("typing into the filter reached the draft: %q", got)
	}

	_, cmd := v.updateKey(registryKey(t, "esc"))
	if v.skills.open {
		t.Fatal("esc left the list open")
	}
	if cmd != nil {
		t.Fatal("esc on the open list also left the chat workspace")
	}
	if got := v.composer.Value(); got != "fix the bug" {
		t.Fatalf("esc changed the draft to %q", got)
	}
	if v.skills.filter != "" {
		t.Fatalf("esc kept the filter %q", v.skills.filter)
	}

	_, cmd = v.updateKey(registryKey(t, "esc"))
	if cmd == nil {
		t.Fatal("the second esc did not go back to the chats board")
	}
	if msg, ok := cmd().(selectViewMsg); !ok || msg.id != viewChats {
		t.Fatalf("the second esc produced %#v, want the chats board", cmd())
	}
}

// TestChatSkillsFilterRanks holds the three tiers and the plugin bare-name
// match issue #509 records from Claude Code.
func TestChatSkillsFilterRanks(t *testing.T) {
	cases := []struct {
		typed string
		want  []string
	}{
		// `deploy` is a bare plugin name (tier 2) and a description
		// substring of /notes (tier 3).
		{"deploy", []string{"/myplugin:deploy-app", "/notes"}},
		// `ship` is an alias of the plugin skill only.
		{"ship", []string{"/myplugin:deploy-app"}},
		// `t` is a name prefix of /tdd, and a description substring of the
		// other two — the name prefix leads.
		{"t", []string{"/tdd", "/myplugin:deploy-app", "/notes"}},
		{"zzz", nil},
	}
	for _, c := range cases {
		t.Run(c.typed, func(t *testing.T) {
			v := chatSkillsFixture(claudeSkills())
			openChatSkills(t, v, "tab")
			for _, r := range c.typed {
				v.updateKey(keyPress(string(r)))
			}
			got := skillRowNames(v)
			if strings.Join(got, ",") != strings.Join(c.want, ",") {
				t.Fatalf("%q matched %v, want %v", c.typed, got, c.want)
			}
		})
	}
}

// TestChatSkillsEnterWithNoHighlightSends: the list never changes what enter
// does until the human moves into it.
func TestChatSkillsEnterWithNoHighlightSends(t *testing.T) {
	v := chatSkillsFixture(claudeSkills())
	v.composer.SetValue("hello")
	openChatSkills(t, v, "tab")
	_, cmd := v.updateKey(registryKey(t, "enter"))
	if cmd == nil {
		t.Fatal("enter with nothing highlighted did not send the message")
	}
	if got := v.composer.Value(); got != "" {
		t.Fatalf("the composer still holds %q after the send", got)
	}
	if v.skills.open {
		t.Fatal("the list stayed open over the send")
	}
}

// TestChatSkillsAcceptWritesTheDraftOnce covers the three accept paths and
// both invoke positions: the draft is written exactly once, the rest of it
// becoming the arguments.
func TestChatSkillsAcceptWritesTheDraftOnce(t *testing.T) {
	cases := []struct {
		name  string
		data  *apiclient.ChatSkills
		draft string
		keys  []string
		want  string
	}{
		{"tab takes the top match", claudeSkills(), "", []string{"tab"}, "/tdd "},
		{
			"down then enter", claudeSkills(), "",
			[]string{"down", "down", "enter"},
			"/myplugin:deploy-app ",
		},
		{
			"the draft becomes the arguments", claudeSkills(), "fix the bug",
			[]string{"tab"},
			"/tdd fix the bug",
		},
		// `anywhere` inserts at the cursor, which SetValue leaves at the end.
		{
			"anywhere inserts at the cursor", codexSkills(), "look at this: ",
			[]string{"tab"},
			"look at this: $review ",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := chatSkillsFixture(c.data)
			v.composer.SetValue(c.draft)
			openChatSkills(t, v, "tab")
			for _, k := range c.keys {
				v.updateKey(registryKey(t, k))
			}
			if got := v.composer.Value(); got != c.want {
				t.Fatalf("the draft is %q, want %q", got, c.want)
			}
			if v.skills.open {
				t.Fatal("accepting left the list open (decision 74)")
			}
		})
	}
}

// TestChatSkillsAcceptLeavesTheCursorAfterTheSpace: what is typed next is
// the skill's arguments, not more of its name.
func TestChatSkillsAcceptLeavesTheCursorAfterTheSpace(t *testing.T) {
	v := chatSkillsFixture(claudeSkills())
	openChatSkills(t, v, "tab")
	v.updateKey(registryKey(t, "tab"))
	v.updateKey(keyPress("x"))
	if got := v.composer.Value(); got != "/tdd x" {
		t.Fatalf("the draft is %q, want the next key past the space", got)
	}
}

// TestChatSkillsBackspaceClosesAnEmptyFilter is the way out the human walked
// in by.
func TestChatSkillsBackspaceClosesAnEmptyFilter(t *testing.T) {
	v := chatSkillsFixture(claudeSkills())
	v.composer.SetValue("hi")
	openChatSkills(t, v, "tab")
	v.updateKey(keyPress("t"))
	v.updateKey(registryKey(t, "backspace"))
	if !v.skills.open || v.skills.filter != "" {
		t.Fatalf("the first backspace closed the list or kept %q", v.skills.filter)
	}
	v.updateKey(registryKey(t, "backspace"))
	if v.skills.open {
		t.Fatal("backspace on an empty filter left the list open")
	}
	if got := v.composer.Value(); got != "hi" {
		t.Fatalf("backspace reached the draft: %q", got)
	}
}

// TestChatSkillsStates holds every note the workspace gives for an answer it
// cannot draw a list from, and the two it can.
func TestChatSkillsStates(t *testing.T) {
	probeErr := "claude exited 2"
	cases := []struct {
		name     string
		data     *apiclient.ChatSkills
		wantOpen bool
		wantNote string
		wantBad  bool
	}{
		{"an empty supported list opens", &apiclient.ChatSkills{
			Agent: "claude", ListVerdict: "supported", InvokeVerdict: "supported",
			InvokeSigil: "/", InvokePosition: "leading",
		}, true, "", false},
		{"unsupported says why, and how to invoke by hand", &apiclient.ChatSkills{
			Agent: "cursor", ListVerdict: "unsupported", InvokeVerdict: "supported",
			InvokeSigil: "/", InvokePosition: "leading",
			UnavailableReason: "cursor does not report its skills — vincent does not guess them",
		}, false, "cursor does not report its skills — vincent does not guess them — type /name to invoke one", false},
		{"unknown shows the probe error", &apiclient.ChatSkills{
			Agent: "claude", ListVerdict: "unknown", InvokeVerdict: "supported",
			ProbeError: &probeErr,
		}, false, "could not list skills: claude exited 2", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := chatSkillsFixture(c.data)
			v.updateKey(registryKey(t, "tab"))
			if v.skills.open != c.wantOpen {
				t.Fatalf("the list open = %v, want %v", v.skills.open, c.wantOpen)
			}
			if v.note != c.wantNote || v.noteBad != c.wantBad {
				t.Fatalf("the note is %q (bad=%v), want %q (bad=%v)",
					v.note, v.noteBad, c.wantNote, c.wantBad)
			}
			if c.wantOpen && len(c.data.Skills) == 0 {
				frame := ansi.Strip(v.render(100, 30))
				if !strings.Contains(frame, "claude reported no skills for this chat") {
					t.Fatalf("the empty list says nothing:\n%s", frame)
				}
			}
		})
	}
}

// TestChatSkillsLoadingRow is what the list draws while the probe runs.
func TestChatSkillsLoadingRow(t *testing.T) {
	v := chatViewFixture()
	v.client = offlineClient()
	if cmd := v.openSkills(); cmd == nil {
		t.Fatal("tab on an unlisted chat asked the daemon nothing")
	}
	if !v.skills.open || !v.skills.loading {
		t.Fatal("the list did not open in its loading state")
	}
	frame := ansi.Strip(v.render(100, 30))
	if !strings.Contains(frame, "asking the agent which skills it has…") {
		t.Fatalf("the loading row is missing:\n%s", frame)
	}
}

// TestChatSkillsCannotInvoke: a list that can be read and not picked from
// opens read-only, and accepting is refused with the reason. `unknown`
// behaves as `unsupported` here (decision 72).
func TestChatSkillsCannotInvoke(t *testing.T) {
	for _, verdict := range []string{"unsupported", "unknown"} {
		t.Run(verdict, func(t *testing.T) {
			data := claudeSkills()
			data.InvokeVerdict = verdict
			data.UnavailableReason = "this agent cannot be told to run a skill"
			v := chatSkillsFixture(data)
			openChatSkills(t, v, "tab")
			v.updateKey(registryKey(t, "tab"))
			if got := v.composer.Value(); got != "" {
				t.Fatalf("a read-only list wrote %q into the draft", got)
			}
			if !v.noteBad || v.note != "this agent cannot be told to run a skill" {
				t.Fatalf("the refusal is %q (bad=%v)", v.note, v.noteBad)
			}
		})
	}
}

// TestChatSkillsTerminalChatMakesNoRequest: the pre-check is local, the way
// close and hand-off already refuse.
func TestChatSkillsTerminalChatMakesNoRequest(t *testing.T) {
	var calls atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	v := chatViewFixture()
	v.client = apiclient.New(ts.URL, "token")
	v.chat.State = "archived"
	_, cmd := v.updateKey(registryKey(t, "tab"))
	if cmd != nil {
		drain(cmd)
	}
	if v.skills.open {
		t.Fatal("a terminal chat opened the list")
	}
	if v.note != "this chat has ended — no turn will run here" || !v.noteBad {
		t.Fatalf("the refusal is %q (bad=%v)", v.note, v.noteBad)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("the refusal made %d requests, want none", n)
	}
}

// TestChatSkillsNeverOpensUnderAPopup: the §7.4 answer popup and the close
// confirmation each own the keyboard.
func TestChatSkillsNeverOpensUnderAPopup(t *testing.T) {
	t.Run("the answer popup", func(t *testing.T) {
		v := chatSkillsFixture(claudeSkills())
		v.form = newAnswerForm(questionRequest())
		v.updateKey(registryKey(t, "tab"))
		if v.skills.open {
			t.Fatal("tab opened the list under the §7.4 popup")
		}
	})
	t.Run("the close confirmation", func(t *testing.T) {
		v := chatSkillsFixture(claudeSkills())
		task := int64(9)
		v.chat.LinkedTaskID = &task
		v.askClose()
		if !v.closing {
			t.Fatal("the fixture is not showing the confirmation")
		}
		v.updateKey(registryKey(t, "tab"))
		if v.skills.open {
			t.Fatal("tab opened the list under the close confirmation")
		}
	})
}

// TestChatSkillsWheelIsANoOpWhileOpen: popups stay keyboard (§15 Mouse), and
// the conversation scrolls again the moment the list closes.
func TestChatSkillsWheelIsANoOpWhileOpen(t *testing.T) {
	v := scrollableChat(t)
	v.skills.data = claudeSkills()
	at := v.vp.YOffset()

	openChatSkills(t, v, "tab")
	v.update(wheelTick(true))
	if got := v.vp.YOffset(); got != at {
		t.Fatalf("the wheel scrolled the conversation to %d while the list was open, want %d", got, at)
	}
	v.updateKey(registryKey(t, "esc"))
	v.update(wheelTick(true))
	if got := v.vp.YOffset(); got != at-1 {
		t.Fatalf("the wheel moved the closed-list conversation to %d, want %d", got, at-1)
	}
}

// TestChatSkillsSanitizesHostileText is the security half. Agent-supplied
// names and descriptions are repository content (§16): an escape never
// reaches the frame, and an invocation carrying a control cannot be typed
// into the draft.
func TestChatSkillsSanitizesHostileText(t *testing.T) {
	data := claudeSkills()
	data.Skills = []apiclient.ChatSkill{
		{Name: "evil", Invocation: "/evil", Description: "clear\x1b[2Jthe\nscreen"},
		{Name: "worse", Invocation: "/worse\nrm -rf /", Description: "a second command"},
	}
	v := chatSkillsFixture(data)
	openChatSkills(t, v, "tab")

	frame := v.render(120, 30)
	if strings.Contains(frame, "\x1b[2J") {
		t.Fatal("an ANSI escape from a skill description reached the frame")
	}
	for _, line := range strings.Split(frame, "\n") {
		if strings.Contains(ansi.Strip(line), "rm -rf /") && strings.Contains(ansi.Strip(line), "/worse") {
			continue // flattened onto one line is fine; a second line is not
		}
	}
	if got := strings.Count(frame, "\n"); got != 29 {
		t.Fatalf("the frame has %d newlines, want the 29 a 30-line pane has", got)
	}

	v.updateKey(registryKey(t, "down"))
	v.updateKey(registryKey(t, "down")) // the hostile row
	row, _ := v.skills.pick()
	if !row.disabled {
		t.Fatal("a row whose invocation holds a newline is not disabled")
	}
	v.updateKey(registryKey(t, "enter"))
	if got := v.composer.Value(); got != "" {
		t.Fatalf("a disabled row wrote %q into the draft", got)
	}
	if !v.noteBad {
		t.Fatalf("accepting a disabled row said %q, want a refusal", v.note)
	}
}

// TestChatSkillsLinkedFormIsEnabled is decision 71's regression guard: a
// codex invocation whose path has spaces in it is a normal row, and it is
// inserted byte for byte.
func TestChatSkillsLinkedFormIsEnabled(t *testing.T) {
	data := codexSkills()
	want := data.Skills[1].Invocation
	v := chatSkillsFixture(data)
	openChatSkills(t, v, "tab")
	v.updateKey(registryKey(t, "down"))
	v.updateKey(registryKey(t, "down"))
	row, ok := v.skills.pick()
	if !ok || row.disabled {
		t.Fatalf("the linked form is disabled: %q", row.reason)
	}
	v.updateKey(registryKey(t, "enter"))
	if got := v.composer.Value(); got != want+" " {
		t.Fatalf("the draft is %q, want %q byte for byte", got, want+" ")
	}
}

// TestChatSkillsRenderIsLegibleWithoutColour: the `› ` marker alone says
// which row is highlighted (§15 Colour).
func TestChatSkillsRenderIsLegibleWithoutColour(t *testing.T) {
	v := chatSkillsFixture(claudeSkills())
	openChatSkills(t, v, "tab")
	v.updateKey(registryKey(t, "down"))
	plain := ansi.Strip(v.render(100, 30))
	var marked []string
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "› ") {
			marked = append(marked, strings.TrimSpace(line))
		}
	}
	if len(marked) != 1 || !strings.HasPrefix(marked[0], "› /tdd") {
		t.Fatalf("the marked rows are %v, want exactly the highlighted one", marked)
	}
}

// TestChatSkillsNarrowPaneKeepsTheName: at 40 columns the description goes
// before the invocation is truncated.
func TestChatSkillsNarrowPaneKeepsTheName(t *testing.T) {
	v := chatSkillsFixture(claudeSkills())
	openChatSkills(t, v, "tab")
	plain := ansi.Strip(v.render(38, 30))
	if !strings.Contains(plain, "/myplugin:deploy-app") {
		t.Fatalf("a narrow pane truncated the invocation:\n%s", plain)
	}
	if strings.Contains(plain, "Ship the app") {
		t.Fatalf("a narrow pane kept the description:\n%s", plain)
	}
}

// TestChatSkillsHeightComesOutOfTheBody holds #299: the list is spent out of
// the body's budget, one slice element per rendered line, and the frame's
// total height never changes — nor does it as the highlight moves
// (decision 75).
func TestChatSkillsHeightComesOutOfTheBody(t *testing.T) {
	v := chatSkillsFixture(claudeSkills())
	v.turns = []apiclient.ChatTurn{{ID: 1, Seq: 1, State: "done", Prompt: "hi"}}
	const h = 30
	closed := strings.Split(v.render(100, h), "\n")
	if len(closed) != h {
		t.Fatalf("a closed list renders %d lines, want %d", len(closed), h)
	}
	openChatSkills(t, v, "tab")
	open := strings.Split(v.render(100, h), "\n")
	if len(open) != h {
		t.Fatalf("an open list renders %d lines, want %d", len(open), h)
	}
	if v.skills.height(h) <= 0 {
		t.Fatalf("the open list claims %d lines", v.skills.height(h))
	}
	for range 3 {
		v.updateKey(registryKey(t, "down"))
		if got := len(strings.Split(v.render(100, h), "\n")); got != h {
			t.Fatalf("moving the highlight made the frame %d lines, want %d", got, h)
		}
	}
}

// TestChatSkillsLateAnswerIsDropped: the probe is not loopback-fast, so an
// answer for another chat or for a list that has been hidden is the ordinary
// case, not an edge (decision 76).
func TestChatSkillsLateAnswerIsDropped(t *testing.T) {
	v := chatViewFixture()
	v.client = offlineClient()
	v.openSkills()
	v.hideSkills()
	v.applySkills(chatSkillsMsg{chatID: v.chatID, skills: claudeSkills()})
	if v.skills.data != nil || v.skills.open {
		t.Fatal("an answer that arrived after the list was hidden was applied")
	}

	v.openSkills()
	v.applySkills(chatSkillsMsg{chatID: v.chatID + 1, skills: claudeSkills()})
	if v.skills.data != nil {
		t.Fatal("an answer for another chat was applied")
	}
}

// Task 124.14 (issue #510): the same list, opened by the draft itself. The
// composer keeps the keyboard here — these probes press real keys into
// updateKey and never poke the struct, because the trigger *is* what the
// composer holds after a press.

// typeIntoChat presses each rune of text through the view's key handler, the
// way a human reaches the inline list. `\n` is the composer's own newline
// key (ctrl+j, issue #500) rather than a rune, because that is what makes a
// multi-line draft today.
func typeIntoChat(t *testing.T, v *chatView, text string) {
	t.Helper()
	for _, r := range text {
		var msg tea.KeyPressMsg
		switch r {
		case '\n':
			msg = tea.KeyPressMsg{Code: 'j', Mod: tea.ModCtrl}
		case ' ':
			msg = tea.KeyPressMsg{Code: ' ', Text: " "}
		default:
			msg = tea.KeyPressMsg{Code: r, Text: string(r)}
		}
		v.updateKey(msg)
	}
}

// pressChat presses one registry key.
func pressChat(t *testing.T, v *chatView, key string, times int) {
	t.Helper()
	for range times {
		v.updateKey(registryKey(t, key))
	}
}

// runChatCmds runs cmd — unwrapping tea.Batch, which updateKey now returns —
// and feeds every message it produces back into the view, so a test can
// watch a fetch out and its answer back in.
func runChatCmds(v *chatView, cmd tea.Cmd) {
	for _, msg := range chatCmdMsgs(cmd) {
		v.update(msg)
	}
}

func chatCmdMsgs(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, chatCmdMsgs(c)...)
		}
		return out
	}
	if msg == nil {
		return nil
	}
	return []tea.Msg{msg}
}

// TestChatSkillsInlineOpensOnTheTypedSigil is the trigger: the token under
// the cursor carries the wire's sigil, so the list opens filtered by what
// follows it, with nothing highlighted and the draft exactly as typed. The
// plugin skill is found by its bare name, as it is under `tab`.
func TestChatSkillsInlineOpensOnTheTypedSigil(t *testing.T) {
	v := chatSkillsFixture(claudeSkills())
	typeIntoChat(t, v, "/deploy")
	if !v.skills.open || v.skills.mode != skillModeInline {
		t.Fatalf("typing the sigil left the list open=%v mode=%v", v.skills.open, v.skills.mode)
	}
	if v.skills.filter != "deploy" {
		t.Fatalf("the inline filter is %q, want the token minus the sigil", v.skills.filter)
	}
	want := []string{"/myplugin:deploy-app", "/notes"}
	if got := skillRowNames(v); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("the inline list holds %v, want %v", got, want)
	}
	if v.skills.cursor != -1 {
		t.Fatalf("the inline list opened on row %d, want nothing highlighted", v.skills.cursor)
	}
	if got := v.composer.Value(); got != "/deploy" {
		t.Fatalf("the draft is %q — the list writes nothing until a row is accepted", got)
	}
	if got := v.bindingContext(); got != ctxChatSkillsInline {
		t.Fatalf("the binding context is %q, want %q", got, ctxChatSkillsInline)
	}
}

// TestChatSkillsInlinePosition holds each adapter's own rule: claude honours
// `/name` only as the first token of the message, codex honours `$name`
// anywhere in it.
func TestChatSkillsInlinePosition(t *testing.T) {
	t.Run("leading refuses a token that is not the first", func(t *testing.T) {
		v := chatSkillsFixture(claudeSkills())
		typeIntoChat(t, v, "hello /deploy")
		if v.skills.open {
			t.Fatalf("a mid-message /deploy opened the list under `leading`")
		}
	})
	t.Run("leading refuses a cursor outside the first token", func(t *testing.T) {
		v := chatSkillsFixture(claudeSkills())
		typeIntoChat(t, v, "/deploy hello")
		if v.skills.open {
			t.Fatal("the cursor sits in `hello`, and the list opened anyway")
		}
	})
	t.Run("anywhere takes a token on the third line", func(t *testing.T) {
		v := chatSkillsFixture(codexSkills())
		typeIntoChat(t, v, "one\ntwo\nplease $rev")
		if !v.skills.open || v.skills.mode != skillModeInline {
			t.Fatalf("a $rev on line 2 left the list open=%v", v.skills.open)
		}
		if got := v.composer.Line(); got != 2 {
			t.Fatalf("the cursor is on line %d, want the third line", got)
		}
		if v.skills.filter != "rev" {
			t.Fatalf("the inline filter is %q, want %q", v.skills.filter, "rev")
		}
	})
}

// TestChatSkillsInlineBareSigil is decision 93 in both directions: a bare
// `/` under `leading` means the whole list, a bare `$` mid-prose is a shell
// variable and means nothing.
func TestChatSkillsInlineBareSigil(t *testing.T) {
	t.Run("leading opens every row", func(t *testing.T) {
		v := chatSkillsFixture(claudeSkills())
		typeIntoChat(t, v, "/")
		if !v.skills.open {
			t.Fatal("a bare / under `leading` opened nothing")
		}
		if got := len(v.skills.rows); got != 3 {
			t.Fatalf("a bare / showed %d rows, want every one", got)
		}
	})
	t.Run("anywhere waits for a character", func(t *testing.T) {
		v := chatSkillsFixture(codexSkills())
		typeIntoChat(t, v, "echo $")
		if v.skills.open {
			t.Fatal("a bare $ mid-prose opened the list")
		}
		typeIntoChat(t, v, "r")
		if !v.skills.open {
			t.Fatal("$r did not open the list")
		}
	})
}

// TestChatSkillsInlinePathIsSilent is decision 92: a path under the sigil
// neither keeps a list open nor earns a hint, and a name-shaped token that
// matches nothing earns one — which never blocks the send.
func TestChatSkillsInlinePathIsSilent(t *testing.T) {
	t.Run("a path says nothing", func(t *testing.T) {
		v := chatSkillsFixture(claudeSkills())
		typeIntoChat(t, v, "/tmp/notes.md")
		if v.skills.open {
			t.Fatal("a file path kept the list open")
		}
		if v.skills.inlineNote != "" || v.note != "" {
			t.Fatalf("a file path was nagged about: %q / %q", v.skills.inlineNote, v.note)
		}
	})
	t.Run("a name is hinted and still sends", func(t *testing.T) {
		v := chatSkillsFixture(claudeSkills())
		typeIntoChat(t, v, "/tdds")
		if v.skills.open {
			t.Fatal("an unmatched token kept the list open")
		}
		want := "/tdds is not a skill claude reported for this chat — it is sent as typed"
		if v.skills.inlineNote != want {
			t.Fatalf("the hint is %q, want %q", v.skills.inlineNote, want)
		}
		if frame := ansi.Strip(v.render(100, 30)); !strings.Contains(frame, want) {
			t.Fatalf("the hint is not drawn:\n%s", frame)
		}
		_, cmd := v.updateKey(registryKey(t, "enter"))
		if cmd == nil {
			t.Fatal("the hint blocked the send — it is a hint, never a block")
		}
	})
	t.Run("anywhere is never hinted", func(t *testing.T) {
		v := chatSkillsFixture(codexSkills())
		typeIntoChat(t, v, "cost $zzz")
		if v.skills.open || v.skills.inlineNote != "" {
			t.Fatalf("an `anywhere` sigil was hinted: %q", v.skills.inlineNote)
		}
	})
}

// TestChatSkillsInlineExactMatchThenSpaceHides: the invocation is typed out
// in full and a space follows it, so what comes next is its arguments.
func TestChatSkillsInlineExactMatchThenSpaceHides(t *testing.T) {
	v := chatSkillsFixture(claudeSkills())
	typeIntoChat(t, v, "/tdd")
	if !v.skills.open {
		t.Fatal("/tdd did not open the list")
	}
	typeIntoChat(t, v, " ")
	if v.skills.open {
		t.Fatal("a space after the exact invocation left the list open")
	}
	// And with the cursor walked back into the finished token it stays shut.
	pressChat(t, v, "left", 1)
	if v.skills.open {
		t.Fatal("the cursor back inside a completed invocation reopened the list")
	}
}

// TestChatSkillsInlineEscSuppressesAndReturnsTheArrows is decision 90's cost
// and the way out of it: while the list is up both arrows walk the matches
// and the draft does not move, and after `esc` both edit the draft again and
// the list stays shut until the token changes.
func TestChatSkillsInlineEscSuppressesAndReturnsTheArrows(t *testing.T) {
	v := chatSkillsFixture(codexSkills())
	typeIntoChat(t, v, "one\n$rev")
	if !v.skills.open {
		t.Fatal("$rev on the second line did not open the list")
	}
	pressChat(t, v, "down", 1)
	if v.skills.cursor != 0 {
		t.Fatalf("down left the highlight on %d, want the first row", v.skills.cursor)
	}
	pressChat(t, v, "down", 1)
	if v.skills.cursor != 1 {
		t.Fatalf("a second down left the highlight on %d", v.skills.cursor)
	}
	pressChat(t, v, "up", 1)
	if v.skills.cursor != 0 {
		t.Fatalf("up left the highlight on %d, want back on the first row", v.skills.cursor)
	}
	if got := v.composer.Line(); got != 1 {
		t.Fatalf("the arrows moved the draft's cursor to line %d while the list was up", got)
	}

	v.updateKey(registryKey(t, "esc"))
	if v.skills.open {
		t.Fatal("esc left the inline list open")
	}
	if got := v.composer.Value(); got != "one\n$rev" {
		t.Fatalf("esc changed the draft to %q", got)
	}
	// Walking the cursor about inside the same token keeps it suppressed;
	// one more character makes it a different token, and the list is
	// offered again.
	pressChat(t, v, "left", 1)
	pressChat(t, v, "right", 1)
	if v.skills.open {
		t.Fatal("a cursor move inside the suppressed token reopened the list")
	}
	typeIntoChat(t, v, "i")
	if !v.skills.open {
		t.Fatal("a changed token did not lift the suppression")
	}

	// And with the list shut again, the arrows edit the draft.
	v.updateKey(registryKey(t, "esc"))
	pressChat(t, v, "up", 1)
	if got := v.composer.Line(); got != 0 {
		t.Fatalf("after esc, up left the draft's cursor on line %d — the arrows are the draft's again", got)
	}
}

// TestChatSkillsInlineTabReplacesTheToken is decision 95: the token the
// human typed is what the invocation stands in for, so `/dep` + the top
// match is `/myplugin:deploy-app`, never `/dep/myplugin:deploy-app`. The
// cursor lands after the space and the rest of the draft is byte-identical.
func TestChatSkillsInlineTabReplacesTheToken(t *testing.T) {
	v := chatSkillsFixture(claudeSkills())
	typeIntoChat(t, v, " fix the bug")
	pressChat(t, v, "left", len(" fix the bug"))
	typeIntoChat(t, v, "/dep")
	if !v.skills.open {
		t.Fatal("/dep at the head of the draft did not open the list")
	}
	v.updateKey(registryKey(t, "tab"))
	// The accepted invocation always brings its own trailing space, so the
	// space that was already there stays where it was.
	if got, want := v.composer.Value(), "/myplugin:deploy-app  fix the bug"; got != want {
		t.Fatalf("the draft is %q, want %q", got, want)
	}
	if got := v.composer.Column(); got != len("/myplugin:deploy-app ") {
		t.Fatalf("the cursor is at column %d, want just past the space", got)
	}
	typeIntoChat(t, v, "x")
	if got, want := v.composer.Value(), "/myplugin:deploy-app x fix the bug"; got != want {
		t.Fatalf("the next key landed wrong: %q, want %q", got, want)
	}
	if v.skills.open {
		t.Fatal("accepting left the inline list open")
	}
}

// TestChatSkillsInlineEnter: with nothing highlighted `enter` still sends
// the message as typed; with a row highlighted it accepts that row.
func TestChatSkillsInlineEnter(t *testing.T) {
	t.Run("no highlight sends", func(t *testing.T) {
		v := chatSkillsFixture(claudeSkills())
		typeIntoChat(t, v, "/dep")
		_, cmd := v.updateKey(registryKey(t, "enter"))
		if cmd == nil {
			t.Fatal("enter with nothing highlighted did not send")
		}
		if got := v.composer.Value(); got != "" {
			t.Fatalf("the composer still holds %q after the send", got)
		}
		if v.skills.open {
			t.Fatal("the list stayed open over the send")
		}
	})
	t.Run("a highlight accepts", func(t *testing.T) {
		v := chatSkillsFixture(claudeSkills())
		typeIntoChat(t, v, "/dep")
		pressChat(t, v, "down", 2)
		_, cmd := v.updateKey(registryKey(t, "enter"))
		if cmd != nil {
			t.Fatal("enter on a highlighted row sent the message as well as accepting it")
		}
		if got, want := v.composer.Value(), "/notes "; got != want {
			t.Fatalf("the draft is %q, want %q", got, want)
		}
	})
}

// TestChatSkillsInlineAcceptNote is issue #510's item 3: what the skill
// takes, dimmed under the composer until the invocation leaves the draft.
func TestChatSkillsInlineAcceptNote(t *testing.T) {
	v := chatSkillsFixture(claudeSkills())
	typeIntoChat(t, v, "/td")
	v.updateKey(registryKey(t, "tab"))
	want := "/tdd [target] — Test-driven development"
	if v.skills.inlineNote != want {
		t.Fatalf("the accept note is %q, want %q", v.skills.inlineNote, want)
	}
	if frame := ansi.Strip(v.render(100, 30)); !strings.Contains(frame, want) {
		t.Fatalf("the accept note is not drawn:\n%s", frame)
	}
	// Typing the skill's arguments keeps it; editing the invocation itself
	// is what takes it away.
	typeIntoChat(t, v, "auth")
	if v.skills.inlineNote != want {
		t.Fatalf("typing the arguments cleared the note: %q", v.skills.inlineNote)
	}
	v.composer.SetValue("/td")
	v.updateKey(tea.KeyPressMsg{Code: 'd', Text: "d"})
	if v.skills.inlineNote != "" {
		t.Fatalf("the note outlived its invocation: %q", v.skills.inlineNote)
	}
}

// TestChatSkillsInlinePasteOpensTheList: the list is derived from the draft,
// not from the keys that made it.
func TestChatSkillsInlinePasteOpensTheList(t *testing.T) {
	v := chatSkillsFixture(claudeSkills())
	v.paste("/dep")
	if !v.skills.open || v.skills.mode != skillModeInline {
		t.Fatalf("a pasted /dep left the list open=%v", v.skills.open)
	}
	if v.skills.filter != "dep" {
		t.Fatalf("the pasted token filtered to %q", v.skills.filter)
	}
}

// TestChatSkillsInlineNeverOpensUnder holds every state a typed sigil must
// stay out of: the §7.4 popup, the close confirmation, a terminal chat and
// an adapter whose verdicts do not allow it.
func TestChatSkillsInlineNeverOpensUnder(t *testing.T) {
	t.Run("the answer popup", func(t *testing.T) {
		v := chatSkillsFixture(claudeSkills())
		v.form = newAnswerForm(questionRequest())
		typeIntoChat(t, v, "/dep")
		if v.skills.open {
			t.Fatal("a typed sigil opened the list under the §7.4 popup")
		}
	})
	t.Run("the close confirmation", func(t *testing.T) {
		v := chatSkillsFixture(claudeSkills())
		task := int64(9)
		v.chat.LinkedTaskID = &task
		typeIntoChat(t, v, "/dep")
		if !v.skills.open {
			t.Fatal("the fixture never opened the list")
		}
		v.askClose()
		if !v.closing || v.skills.open {
			t.Fatalf("the confirmation left the list open=%v", v.skills.open)
		}
	})
	t.Run("a terminal chat", func(t *testing.T) {
		v := chatSkillsFixture(claudeSkills())
		v.chat.State = "archived"
		typeIntoChat(t, v, "/dep")
		if v.skills.open {
			t.Fatal("a typed sigil opened the list on a terminal chat")
		}
	})
	t.Run("an unsupported listing", func(t *testing.T) {
		data := claudeSkills()
		data.ListVerdict = "unsupported"
		v := chatSkillsFixture(data)
		typeIntoChat(t, v, "/dep")
		if v.skills.open || v.skills.inlineNote != "" || v.note != "" {
			t.Fatalf("an unsupported listing spoke up: open=%v note=%q/%q",
				v.skills.open, v.skills.inlineNote, v.note)
		}
	})
	t.Run("an adapter that cannot invoke", func(t *testing.T) {
		data := claudeSkills()
		data.InvokeVerdict, data.InvokeSigil = "unsupported", ""
		v := chatSkillsFixture(data)
		typeIntoChat(t, v, "/dep")
		if v.skills.open {
			t.Fatal("a typed sigil opened the list for an adapter that cannot invoke")
		}
	})
}

// TestChatSkillsInlineFetchIsSilentAndLatched is decision 91: the first
// inline open may ask the daemon, a failure writes nothing and is not
// re-fired on the next keystroke, and `tab` on the same view still explains
// why.
func TestChatSkillsInlineFetchIsSilentAndLatched(t *testing.T) {
	var calls atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	v := chatViewFixture()
	v.client = apiclient.New(ts.URL, "token")
	for _, r := range "/dep" {
		_, cmd := v.updateKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		runChatCmds(v, cmd)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("the typed sigil made %d requests, want exactly one", n)
	}
	if v.skills.open || v.skills.inlineNote != "" || v.note != "" {
		t.Fatalf("a failed inline probe spoke up: open=%v note=%q/%q",
			v.skills.open, v.skills.inlineNote, v.note)
	}
	if !v.skills.probeFailed {
		t.Fatal("the failed probe was not latched off")
	}
	for _, r := range "loy" {
		_, cmd := v.updateKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		runChatCmds(v, cmd)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("more keystrokes re-fired the probe: %d requests", n)
	}

	// `tab` is the human asking, so it asks again and says what happened.
	_, cmd := v.updateKey(registryKey(t, "tab"))
	runChatCmds(v, cmd)
	if n := calls.Load(); n != 2 {
		t.Fatalf("tab made %d requests in total, want a second one", n)
	}
	if !v.noteBad || !strings.HasPrefix(v.note, "could not list skills: ") {
		t.Fatalf("tab said %q (bad=%v), want the explaining note", v.note, v.noteBad)
	}
}

// TestChatSkillsInlineHelpIsItsOwnSurface is decision 94: `backspace` edits
// the draft here, so the `?` pane must not promise browse's filter row.
func TestChatSkillsInlineHelpIsItsOwnSurface(t *testing.T) {
	v := chatSkillsFixture(claudeSkills())
	typeIntoChat(t, v, "/dep")
	inline := ansi.Strip(helpText(v.bindingContext(), true))
	if !strings.Contains(inline, "give ↑/↓ back to editing the draft") {
		t.Fatalf("the inline help does not carry its own rows:\n%s", inline)
	}
	if strings.Contains(inline, "shorten the filter") {
		t.Fatalf("the inline help promises browse's backspace:\n%s", inline)
	}

	b := chatSkillsFixture(claudeSkills())
	openChatSkills(t, b, "tab")
	browse := ansi.Strip(helpText(b.bindingContext(), true))
	if !strings.Contains(browse, "shorten the filter") {
		t.Fatalf("the browse help lost its backspace row:\n%s", browse)
	}
}
