package tui

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// Task 126.11 (issue #555): the `@` file picker above the chat composer. The
// draft opens it — there is no key at all — typing filters it, and accepting a
// row writes the daemon's own mention text into the draft, which is the only
// moment anything here touches the composer.
//
// It mirrors chatskills_test.go's inline suite one for one, and reuses its
// helpers: typeIntoChat, pressChat, runChatCmds and chatCmdMsgs.

// claudeFiles is a listing from an adapter that mentions and expands, with a
// path for every tier the ranking distinguishes and one whose mention is
// quoted because the path has a space in it (task 126 decision 1).
func claudeFiles() *apiclient.ChatFiles {
	return &apiclient.ChatFiles{
		ChatID: 1, Agent: "claude", WorkDir: "/tmp/wt",
		MentionSigil: "@", MentionPosition: "anywhere", MentionExpands: true,
		Files: []apiclient.ChatFile{
			{Path: "docs/main.md", Mention: "@docs/main.md"},
			{Path: "internal/tui/chatview.go", Mention: "@internal/tui/chatview.go"},
			{Path: "main.go", Mention: "@main.go"},
			{Path: "dir with space/notes.md", Mention: `@"dir with space/notes.md"`},
		},
	}
}

// rankedFiles is git's order deliberately reversed against the ranking, so a
// test of the tiers says whether rankChatFiles sorted or the server did.
func rankedFiles() *apiclient.ChatFiles {
	return &apiclient.ChatFiles{
		ChatID: 1, Agent: "claude", MentionSigil: "@", MentionPosition: "anywhere",
		MentionExpands: true,
		Files: []apiclient.ChatFile{
			{Path: "docs/status-tui.md", Mention: "@docs/status-tui.md"},     // substring
			{Path: "internal/tui/chat.go", Mention: "@internal/tui/chat.go"}, // segment prefix
			{Path: "cmd/tui-notes.md", Mention: "@cmd/tui-notes.md"},         // basename prefix
			{Path: "docs/other.md", Mention: "@docs/other.md"},               // no match
		},
	}
}

// manyFiles is a listing of n rows, for the tests that care about the row
// count rather than about any row's content.
func manyFiles(n int, truncated bool) *apiclient.ChatFiles {
	d := &apiclient.ChatFiles{
		ChatID: 1, Agent: "claude", MentionSigil: "@", MentionPosition: "anywhere",
		MentionExpands: true, Truncated: truncated,
		Files: make([]apiclient.ChatFile, n),
	}
	for i := range n {
		path := fmt.Sprintf("src/file-%06d.go", i)
		d.Files[i] = apiclient.ChatFile{Path: path, Mention: "@" + path}
	}
	return d
}

// chatFilesFixture is a chat workspace with a listing already in hand, so a
// typed `@` opens the picker without a round trip.
func chatFilesFixture(data *apiclient.ChatFiles) *chatView {
	v := chatViewFixture()
	v.client = offlineClient()
	v.files.data = data
	return v
}

// fileRowPaths is the path each row the picker currently holds draws.
func fileRowPaths(v *chatView) []string {
	out := make([]string, 0, len(v.files.rows))
	for _, r := range v.files.rows {
		out = append(out, r.display)
	}
	return out
}

// TestChatFilesOpensOnTheTypedSigil is the trigger: the token under the cursor
// begins with `@` and carries one more rune, so the list opens filtered by
// what follows it, with nothing highlighted and the draft exactly as typed.
func TestChatFilesOpensOnTheTypedSigil(t *testing.T) {
	v := chatFilesFixture(claudeFiles())
	typeIntoChat(t, v, "look at @main")
	if !v.files.open {
		t.Fatal("a typed @main opened nothing")
	}
	if v.files.filter != "main" {
		t.Fatalf("the filter is %q, want the token minus the sigil", v.files.filter)
	}
	want := []string{"docs/main.md", "main.go"}
	if got := fileRowPaths(v); !slices.Equal(got, want) {
		t.Fatalf("the list holds %v, want %v", got, want)
	}
	if v.files.cursor != -1 {
		t.Fatalf("the list opened on row %d, want nothing highlighted", v.files.cursor)
	}
	if got := v.composer.Value(); got != "look at @main" {
		t.Fatalf("the draft is %q — the list writes nothing until a row is accepted", got)
	}
	if got := v.bindingContext(); got != ctxChatFiles {
		t.Fatalf("the binding context is %q, want %q", got, ctxChatFiles)
	}
}

// TestChatFilesBareSigilOpensNothing is task 126 decision 41 and the token
// rule behind it: `@` alone waits for a character — which is what keeps
// `cc @someone` and a repository-sized list off the screen — and `user@host`
// is not a mention at all, because the token does not begin with the sigil.
func TestChatFilesBareSigilOpensNothing(t *testing.T) {
	t.Run("a bare @ waits for a character", func(t *testing.T) {
		v := chatFilesFixture(claudeFiles())
		typeIntoChat(t, v, "cc @")
		if v.files.open {
			t.Fatal("a bare @ opened the picker")
		}
		typeIntoChat(t, v, "m")
		if !v.files.open {
			t.Fatal("@m did not open the picker")
		}
	})
	t.Run("a mid-token @ is not a mention", func(t *testing.T) {
		v := chatFilesFixture(claudeFiles())
		typeIntoChat(t, v, "mail user@main.go")
		if v.files.open {
			t.Fatal("user@main.go opened the picker")
		}
	})
	t.Run("a GitHub handle says nothing at all", func(t *testing.T) {
		v := chatFilesFixture(claudeFiles())
		typeIntoChat(t, v, "cc @lezli01")
		if v.files.open || v.note != "" {
			t.Fatalf("a handle spoke up: open=%v note=%q", v.files.open, v.note)
		}
		if plain := ansi.Strip(v.render(100, 30)); strings.Contains(plain, "nothing matches") {
			t.Fatalf("a handle drew the no-match line:\n%s", plain)
		}
	})
}

// TestChatFilesAcceptReplacesTheToken: the token the human typed is what the
// mention stands in for, the daemon's bytes go in unchanged — quoting and all
// — and the cursor lands after the one trailing space.
func TestChatFilesAcceptReplacesTheToken(t *testing.T) {
	for _, tc := range []struct {
		name, typed, want string
	}{
		{"a plain path", "@main.g", "@main.go "},
		{"a quoted path", "@dir", `@"dir with space/notes.md" `},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := chatFilesFixture(claudeFiles())
			typeIntoChat(t, v, " fix the bug")
			pressChat(t, v, "left", len(" fix the bug"))
			typeIntoChat(t, v, tc.typed)
			if !v.files.open {
				t.Fatalf("%s did not open the picker", tc.typed)
			}
			v.updateKey(registryKey(t, "tab"))
			if got, want := v.composer.Value(), tc.want+" fix the bug"; got != want {
				t.Fatalf("the draft is %q, want %q", got, want)
			}
			if got := v.composer.Column(); got != len([]rune(tc.want)) {
				t.Fatalf("the cursor is at column %d, want just past the space", got)
			}
			if v.files.open {
				t.Fatal("accepting left the picker open")
			}
		})
	}
}

// TestChatFilesEnter: with nothing highlighted `enter` still sends the message
// as typed; with a row highlighted it accepts that row.
func TestChatFilesEnter(t *testing.T) {
	t.Run("no highlight sends", func(t *testing.T) {
		v := chatFilesFixture(claudeFiles())
		typeIntoChat(t, v, "read @main")
		_, cmd := v.updateKey(registryKey(t, "enter"))
		if cmd == nil {
			t.Fatal("enter with nothing highlighted did not send")
		}
		if got := v.composer.Value(); got != "" {
			t.Fatalf("the composer still holds %q after the send", got)
		}
		if v.files.open {
			t.Fatal("the picker stayed open over the send")
		}
	})
	t.Run("a highlight accepts", func(t *testing.T) {
		v := chatFilesFixture(claudeFiles())
		typeIntoChat(t, v, "read @main")
		pressChat(t, v, "down", 2)
		_, cmd := v.updateKey(registryKey(t, "enter"))
		if cmd != nil {
			t.Fatal("enter on a highlighted row sent the message as well as accepting it")
		}
		if got, want := v.composer.Value(), "read @main.go "; got != want {
			t.Fatalf("the draft is %q, want %q", got, want)
		}
	})
}

// TestChatFilesEscSuppressesAndReturnsTheArrows is task 124 decision 90's cost
// and the way out of it, on this list: while it is up both arrows walk the
// matches, and after `esc` both edit the draft again and the list stays shut
// until the token changes.
func TestChatFilesEscSuppressesAndReturnsTheArrows(t *testing.T) {
	v := chatFilesFixture(claudeFiles())
	typeIntoChat(t, v, "one\nread @main")
	if !v.files.open {
		t.Fatal("@main on the second line did not open the picker")
	}
	pressChat(t, v, "down", 1)
	if v.files.cursor != 0 {
		t.Fatalf("down left the highlight on %d, want the first row", v.files.cursor)
	}
	pressChat(t, v, "up", 1)
	if v.files.cursor != 0 {
		t.Fatalf("up from the first row left the highlight on %d", v.files.cursor)
	}
	if got := v.composer.Line(); got != 1 {
		t.Fatalf("the arrows moved the draft's cursor to line %d while the list was up", got)
	}

	v.updateKey(registryKey(t, "esc"))
	if v.files.open {
		t.Fatal("esc left the picker open")
	}
	if got := v.composer.Value(); got != "one\nread @main" {
		t.Fatalf("esc changed the draft to %q", got)
	}
	pressChat(t, v, "left", 1)
	pressChat(t, v, "right", 1)
	if v.files.open {
		t.Fatal("a cursor move inside the suppressed token reopened the picker")
	}
	typeIntoChat(t, v, ".")
	if !v.files.open {
		t.Fatal("a changed token did not lift the suppression")
	}

	v.updateKey(registryKey(t, "esc"))
	pressChat(t, v, "up", 1)
	if got := v.composer.Line(); got != 0 {
		t.Fatalf("after esc, up left the draft's cursor on line %d — the arrows are the draft's again", got)
	}
}

// TestChatFilesNoMatchIsSilent: every `@` token is path-shaped, which is what
// task 124 decision 92 refuses to nag about, so an unmatched one draws nothing
// and says nothing — and never blocks the send.
func TestChatFilesNoMatchIsSilent(t *testing.T) {
	v := chatFilesFixture(claudeFiles())
	typeIntoChat(t, v, "see @zzz")
	if v.files.open {
		t.Fatal("an unmatched token kept the picker open")
	}
	if v.note != "" || v.skills.inlineNote != "" {
		t.Fatalf("an unmatched token was nagged about: %q / %q", v.note, v.skills.inlineNote)
	}
	if plain := ansi.Strip(v.render(100, 30)); strings.Contains(plain, "files") {
		t.Fatalf("an unmatched token drew a list:\n%s", plain)
	}
	_, cmd := v.updateKey(registryKey(t, "enter"))
	if cmd == nil {
		t.Fatal("an unmatched token blocked the send")
	}
}

// TestChatFilesRanking holds the three tiers, their order, and the separator
// rule: once the query carries one, the basename tier is skipped and matching
// is against the whole path. Both `/` and `\` are separators, and the
// assertion runs identically on all three CI legs — nothing here consults
// filepath.Separator.
func TestChatFilesRanking(t *testing.T) {
	files := rankedFiles().Files

	t.Run("basename then segment then substring", func(t *testing.T) {
		want := []string{"cmd/tui-notes.md", "internal/tui/chat.go", "docs/status-tui.md"}
		if got := rankedPaths(files, "tui"); !slices.Equal(got, want) {
			t.Fatalf("`tui` ranked %v, want %v", got, want)
		}
	})
	t.Run("a separator skips the basename tier", func(t *testing.T) {
		// With `/tui` typed nothing is a basename match any more, so git's
		// own order among the substring hits is what is left.
		want := []string{"internal/tui/chat.go", "cmd/tui-notes.md"}
		if got := rankedPaths(files, "/tui"); !slices.Equal(got, want) {
			t.Fatalf("`/tui` ranked %v, want %v", got, want)
		}
	})
	t.Run("a typed path prefix wins", func(t *testing.T) {
		want := []string{"internal/tui/chat.go"}
		if got := rankedPaths(files, "internal/tui"); !slices.Equal(got, want) {
			t.Fatalf("`internal/tui` ranked %v, want %v", got, want)
		}
	})
	t.Run("a backslash separates on every platform", func(t *testing.T) {
		if got, want := rankedPaths(files, `internal\tui`), rankedPaths(files, "internal/tui"); !slices.Equal(got, want) {
			t.Fatalf("a backslash query ranked %v, a slash one %v", got, want)
		}
		if got, want := rankedPaths(files, `\tui`), rankedPaths(files, "/tui"); !slices.Equal(got, want) {
			t.Fatalf("a backslash query ranked %v, a slash one %v", got, want)
		}
	})
	t.Run("case folds and an empty query keeps git's order", func(t *testing.T) {
		if got, want := rankedPaths(files, "TUI"), rankedPaths(files, "tui"); !slices.Equal(got, want) {
			t.Fatalf("an upper-case query ranked %v, want %v", got, want)
		}
		want := []string{"docs/status-tui.md", "internal/tui/chat.go", "cmd/tui-notes.md", "docs/other.md"}
		if got := rankedPaths(files, ""); !slices.Equal(got, want) {
			t.Fatalf("an empty query ranked %v, want git's own order %v", got, want)
		}
	})
}

// rankedPaths is rankChatFiles' answer as paths, which is what a ranking
// assertion is actually about.
func rankedPaths(files []apiclient.ChatFile, q string) []string {
	out := make([]string, 0, len(files))
	for _, i := range rankChatFiles(files, q) {
		out = append(out, files[i].Path)
	}
	return out
}

// TestChatFilesTitleTellsTruncationFromNoMatch is issue #553 decision 2 on
// this list, and the first time the cap's counter earns its keep (task 126
// decision 45): "your file is not listed" and "your file does not match" must
// not look the same, and the daemon's own cut is a third thing again.
func TestChatFilesTitleTellsTruncationFromNoMatch(t *testing.T) {
	t.Run("a capped build counts itself", func(t *testing.T) {
		v := chatFilesFixture(manyFiles(1615, false))
		typeIntoChat(t, v, "@src")
		if got := len(v.files.rows); got != chatFileMentionMax {
			t.Fatalf("the build kept %d rows, want the cap's %d", got, chatFileMentionMax)
		}
		if v.files.matched != 1615 {
			t.Fatalf("the build matched %d rows, want all 1615", v.files.matched)
		}
		plain := ansi.Strip(v.render(100, 30))
		if !strings.Contains(plain, "50 of 1615") {
			t.Fatalf("the capped title line does not count itself:\n%s", plain)
		}
	})
	t.Run("a truncated listing says so", func(t *testing.T) {
		v := chatFilesFixture(manyFiles(3, true))
		typeIntoChat(t, v, "@src")
		plain := ansi.Strip(v.render(100, 30))
		if !strings.Contains(plain, "showing the first 3") {
			t.Fatalf("a truncated listing drew no warning:\n%s", plain)
		}
	})
	t.Run("a whole listing says neither", func(t *testing.T) {
		v := chatFilesFixture(manyFiles(3, false))
		typeIntoChat(t, v, "@src")
		plain := ansi.Strip(v.render(100, 30))
		if strings.Contains(plain, " of ") || strings.Contains(plain, "showing the first") {
			t.Fatalf("a whole listing counted itself:\n%s", plain)
		}
	})
}

// TestChatFilesCapKeepsTheBestMatches: the cap slices after rankChatFiles, so
// what survives is the top of the ranking and never a prefix of the listing —
// which is what makes a capped picker usable on a repository at all.
func TestChatFilesCapKeepsTheBestMatches(t *testing.T) {
	const substrings = chatFileMentionMax + 5
	data := &apiclient.ChatFiles{
		ChatID: 1, Agent: "claude", MentionSigil: "@", MentionPosition: "anywhere",
		MentionExpands: true,
	}
	for i := range substrings {
		path := fmt.Sprintf("docs/status-tui-%03d.md", i)
		data.Files = append(data.Files, apiclient.ChatFile{Path: path, Mention: "@" + path})
	}
	// Last in git's order, and the only basename match: a cap that sliced
	// before the ranking would drop exactly this row.
	data.Files = append(data.Files, apiclient.ChatFile{Path: "cmd/tui-notes.md", Mention: "@cmd/tui-notes.md"})

	v := chatFilesFixture(data)
	typeIntoChat(t, v, "@tui")
	if got := len(v.files.rows); got != chatFileMentionMax {
		t.Fatalf("the build kept %d rows, want the cap's %d", got, chatFileMentionMax)
	}
	if got := v.files.rows[0].insert; got != "@cmd/tui-notes.md" {
		t.Fatalf("the top row is %q, want the top-ranked match", got)
	}
	if want := substrings + 1; v.files.matched != want {
		t.Fatalf("the list matched %d rows, want all %d", v.files.matched, want)
	}
	if plain := ansi.Strip(v.render(100, 30)); !strings.Contains(plain, fmt.Sprintf("%d of %d", chatFileMentionMax, substrings+1)) {
		t.Fatalf("the capped title line does not count itself:\n%s", plain)
	}
}

// TestChatFilesAtMostOneListIsDrawn is the exclusion made a test rather than
// left an accident. Two open lists at pane height 30 would be twenty of its
// thirty lines, `room` would sit on its max(…, 1) floor, and the conversation
// would be gone.
func TestChatFilesAtMostOneListIsDrawn(t *testing.T) {
	const h = 30
	v := chatFilesFixture(claudeFiles())
	v.skills.data = codexSkills()
	v.turns = []apiclient.ChatTurn{{ID: 1, Seq: 1, State: "done", Prompt: "hi"}}

	typeIntoChat(t, v, "$rev @main")
	if !v.files.open || v.skills.open {
		t.Fatalf("with the cursor in @main: files=%v skills=%v", v.files.open, v.skills.open)
	}
	filesFrame := ansi.Strip(v.render(100, h))
	if !strings.Contains(filesFrame, "main.go") || strings.Contains(filesFrame, "$review") {
		t.Fatalf("the cursor is in the mention and the skills list drew:\n%s", filesFrame)
	}

	// Walk back into `$rev`; the skills list takes over and this one shuts.
	pressChat(t, v, "left", len(" @main"))
	if !v.skills.open || v.files.open {
		t.Fatalf("with the cursor in $rev: files=%v skills=%v", v.files.open, v.skills.open)
	}
	skillsFrame := ansi.Strip(v.render(100, h))
	if !strings.Contains(skillsFrame, "$review") || strings.Contains(skillsFrame, "main.go") {
		t.Fatalf("the cursor is in the invocation and the file list drew:\n%s", skillsFrame)
	}

	// And the frame is one list's either way: #299's budget is unchanged.
	for _, frame := range []string{filesFrame, skillsFrame} {
		if got := len(strings.Split(frame, "\n")); got != h {
			t.Fatalf("a frame with one list open is %d lines, want %d", got, h)
		}
	}
}

// TestChatFilesStandsDownForTheAdaptersWord holds both halves of the wire's
// authority: an adapter that claims `@` as its *invoke* sigil keeps it, and
// one that reports no mention sigil is offered no picker at all.
func TestChatFilesStandsDownForTheAdaptersWord(t *testing.T) {
	t.Run("an invoke sigil of @ wins", func(t *testing.T) {
		skills := claudeSkills()
		skills.InvokeSigil, skills.InvokePosition = "@", "anywhere"
		for i := range skills.Skills {
			skills.Skills[i].Invocation = "@" + skills.Skills[i].Name
		}
		v := chatFilesFixture(claudeFiles())
		v.skills.data = skills
		typeIntoChat(t, v, "@main")
		if v.files.open {
			t.Fatal("the file picker opened over an adapter that owns @")
		}
	})
	t.Run("an empty mention sigil offers no picker", func(t *testing.T) {
		data := claudeFiles()
		data.MentionSigil, data.MentionPosition = "", ""
		for i := range data.Files {
			data.Files[i].Mention = ""
		}
		v := chatFilesFixture(data)
		typeIntoChat(t, v, "@main")
		if v.files.open || v.note != "" {
			t.Fatalf("an adapter that cannot mention spoke up: open=%v note=%q", v.files.open, v.note)
		}
	})
}

// TestChatFilesSanitizesHostileMention is task 124 decision 71's guard
// reused: a mention carrying a C0/C1 control or an ANSI escape is drawn
// disabled and cannot be accepted, while a path with a space in it — the
// ordinary case on macOS — is a normal row.
func TestChatFilesSanitizesHostileMention(t *testing.T) {
	data := claudeFiles()
	data.Files = []apiclient.ChatFile{
		{Path: "evil.md", Mention: "@evil\nrm -rf /"},
		{Path: "clear.md", Mention: "@clear\x1b[2J.md"},
	}
	v := chatFilesFixture(data)
	typeIntoChat(t, v, "@e")
	if !v.files.open {
		t.Fatal("the fixture never opened the picker")
	}
	frame := v.render(120, 30)
	if strings.Contains(frame, "\x1b[2J") {
		t.Fatal("an ANSI escape from a mention reached the frame")
	}
	if got := strings.Count(frame, "\n"); got != 29 {
		t.Fatalf("the frame has %d newlines, want the 29 a 30-line pane has", got)
	}
	for i := range v.files.rows {
		if !v.files.rows[i].disabled {
			t.Fatalf("row %d (%q) is not disabled", i, v.files.rows[i].insert)
		}
	}
	pressChat(t, v, "down", 1)
	v.updateKey(registryKey(t, "enter"))
	if got := v.composer.Value(); got != "@e" {
		t.Fatalf("a disabled row wrote %q into the draft", got)
	}
	if !v.noteBad {
		t.Fatalf("accepting a disabled row said %q, want a refusal", v.note)
	}
}

// TestChatFilesCacheDropsOnAChatEvent: the daemon's `chat.*` event is the only
// signal a turn may have changed the workspace, and the next open asks again —
// but the cache is never dropped out from under a list that is on screen.
func TestChatFilesCacheDropsOnAChatEvent(t *testing.T) {
	event := chatNoteMsg{chatID: 1, note: apiclient.EventNote{
		Event: apiclient.Event{Type: "chat.turn_finished"},
	}}

	t.Run("a shut picker forgets", func(t *testing.T) {
		v := chatFilesFixture(claudeFiles())
		v.applyChatNote(event)
		if v.files.data != nil {
			t.Fatal("the cached listing survived a chat event")
		}
	})
	t.Run("an open picker keeps its rows", func(t *testing.T) {
		v := chatFilesFixture(claudeFiles())
		typeIntoChat(t, v, "@main")
		v.applyChatNote(event)
		if v.files.data == nil || !v.files.open {
			t.Fatalf("the listing was dropped under an open picker: data=%v open=%v",
				v.files.data != nil, v.files.open)
		}
	})
	t.Run("the next open asks again", func(t *testing.T) {
		v := chatFilesFixture(claudeFiles())
		v.applyChatNote(event)
		_, cmd := v.updateKey(tea.KeyPressMsg{Code: '@', Text: "@"})
		if cmd == nil {
			t.Fatal("the first key after the drop produced no command at all")
		}
		_, cmd = v.updateKey(tea.KeyPressMsg{Code: 'm', Text: "m"})
		if !hasChatFilesFetch(cmd) {
			t.Fatal("the next open did not re-fetch the listing")
		}
	})
	t.Run("opening a chat zeroes both lists", func(t *testing.T) {
		v := chatFilesFixture(claudeFiles())
		v.skills.data = claudeSkills()
		v.open(1)
		if v.files.data != nil || v.skills.data != nil {
			t.Fatalf("open kept data: files=%v skills=%v", v.files.data != nil, v.skills.data != nil)
		}
	})
}

// hasChatFilesFetch reports a command that would ask for a file listing. The
// fetch is the only one of this view's commands that answers a chatFilesMsg.
func hasChatFilesFetch(cmd tea.Cmd) bool {
	for _, msg := range chatCmdMsgs(cmd) {
		if _, ok := msg.(chatFilesMsg); ok {
			return true
		}
	}
	return false
}

// TestChatFilesFetchIsOnceAndIsNotRetried is task 126 decision 46. The sync
// runs on *every* composer update, so without the guard typing `@src/m` would
// fire one request per keystroke — and a failure that re-fired on the next
// keystroke would do it again for as long as someone kept typing.
func TestChatFilesFetchIsOnceAndIsNotRetried(t *testing.T) {
	t.Run("a keystroke storm fires one request", func(t *testing.T) {
		v, calls := chatFilesServer(t, func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(claudeFiles())
		})
		typeChatFilesLive(t, v, "@src/m")
		if n := calls.Load(); n != 1 {
			t.Fatalf("typing @src/m made %d requests, want exactly one", n)
		}
	})
	t.Run("a failure is not retried while the token grows", func(t *testing.T) {
		v, calls := chatFilesServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		})
		typeChatFilesLive(t, v, "@src/m")
		if n := calls.Load(); n != 1 {
			t.Fatalf("a failed fetch was re-fired: %d requests", n)
		}
		if v.files.open || v.note != "" {
			t.Fatalf("a failed fetch spoke up: open=%v note=%q", v.files.open, v.note)
		}
		// A different token is a different question, and it is asked.
		typeChatFilesLive(t, v, " @docs")
		if n := calls.Load(); n != 2 {
			t.Fatalf("a new token made %d requests in total, want a second one", n)
		}
	})
}

// chatFilesServer is a workspace pointed at a daemon that answers every
// request with handler, and the count of what it was asked.
func chatFilesServer(t *testing.T, handler http.HandlerFunc) (*chatView, *atomic.Int64) {
	t.Helper()
	var calls atomic.Int64
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		handler(w, r)
	}))
	t.Cleanup(ts.Close)
	v := chatViewFixture()
	v.client = apiclient.New(ts.URL, "token")
	return v, &calls
}

// typeChatFilesLive types text and runs each press's commands, so a fetch out
// and its answer back in both happen before the next key.
func typeChatFilesLive(t *testing.T, v *chatView, text string) {
	t.Helper()
	for _, r := range text {
		_, cmd := v.updateKey(tea.KeyPressMsg{Code: r, Text: string(r)})
		runChatCmds(v, cmd)
	}
}

// TestChatFilesWheelIsANoOpWhileOpen: popups stay keyboard (§15 Mouse), and
// the conversation scrolls again the moment the picker closes. The file
// sibling of TestChatSkillsWheelIsANoOpWhileOpen.
func TestChatFilesWheelIsANoOpWhileOpen(t *testing.T) {
	v := scrollableChat(t)
	v.files.data = claudeFiles()
	at := v.vp.YOffset()

	typeIntoChat(t, v, "@main")
	if !v.files.open {
		t.Fatal("@main did not open the picker")
	}
	v.update(wheelTick(true))
	if got := v.vp.YOffset(); got != at {
		t.Fatalf("the wheel scrolled the conversation to %d while the picker was open, want %d", got, at)
	}
	v.updateKey(registryKey(t, "esc"))
	v.update(wheelTick(true))
	if got := v.vp.YOffset(); got != at-1 {
		t.Fatalf("the wheel moved the closed-picker conversation to %d, want %d", got, at-1)
	}
}

// TestChatFilesHeightComesOutOfTheBody holds #299: the picker is spent out of
// the body's budget, one slice element per rendered line, and the frame's
// total height never changes — nor does it as the highlight moves.
func TestChatFilesHeightComesOutOfTheBody(t *testing.T) {
	const h = 30
	v := chatFilesFixture(manyFiles(200, false))
	v.turns = []apiclient.ChatTurn{{ID: 1, Seq: 1, State: "done", Prompt: "hi"}}
	closed := strings.Split(v.render(100, h), "\n")
	if len(closed) != h {
		t.Fatalf("a closed picker renders %d lines, want %d", len(closed), h)
	}
	typeIntoChat(t, v, "@src")
	if !v.files.open {
		t.Fatal("@src did not open the picker")
	}
	if got := len(strings.Split(v.render(100, h), "\n")); got != h {
		t.Fatalf("an open picker renders %d lines, want %d", got, h)
	}
	if got, want := len(v.files.render(100, h)), v.files.titledCore().height(h); got != want {
		t.Fatalf("render drew %d lines, height claims %d", got, want)
	}
	for range 3 {
		v.updateKey(registryKey(t, "down"))
		if got := len(strings.Split(v.render(100, h), "\n")); got != h {
			t.Fatalf("moving the highlight made the frame %d lines, want %d", got, h)
		}
	}
	// The one reserved line carries the highlighted row's whole path, which is
	// what a row line that had to truncate one cannot.
	if got := v.files.reserve; got != 1 {
		t.Fatalf("the picker reserves %d lines, want the one a path needs", got)
	}
}

// TestChatFilesRenderOnlyStylesTheVisibleRows is issue #553's property on this
// list: a frame costs the window, not the row set. Counted through the row
// renderer's seam, which the file list must leave installable.
func TestChatFilesRenderOnlyStylesTheVisibleRows(t *testing.T) {
	const paneHeight, width = 30, 100
	calls := 0
	var l chatFileList
	l.open, l.data = true, manyFiles(100_000, false)
	l.rowLine = func(r chatInlineRow, selected bool, w int) string {
		calls++
		return chatFilePathRowLine(r, selected, w)
	}
	l.core()
	l.build()
	if len(l.rows) != chatFileMentionMax {
		t.Fatalf("the list built %d rows, want the cap's %d", len(l.rows), chatFileMentionMax)
	}
	win := l.titledCore().window(paneHeight)
	for _, cursor := range []int{-1, 0, len(l.rows) / 2, len(l.rows) - 1} {
		l.cursor, calls = cursor, 0
		got := l.render(width, paneHeight)
		if calls != win {
			t.Fatalf("cursor %d styled %d rows, want the window's %d", cursor, calls, win)
		}
		if want := l.titledCore().height(paneHeight); len(got) != want {
			t.Fatalf("cursor %d rendered %d lines, want %d", cursor, len(got), want)
		}
	}
}

// TestChatFilesReEditingAWhitespaceMentionIsSilent is task 126 decision 42.
// chatDraftTokenAt is whitespace-delimited, so walking back into
// `@"dir with space/notes.md"` puts the cursor on a token that is only a piece
// of it; accepting there would write the mention a second time and produce one
// that will not resolve.
func TestChatFilesReEditingAWhitespaceMentionIsSilent(t *testing.T) {
	v := chatFilesFixture(claudeFiles())
	typeIntoChat(t, v, "read @dir")
	v.updateKey(registryKey(t, "tab"))
	want := `read @"dir with space/notes.md" `
	if got := v.composer.Value(); got != want {
		t.Fatalf("the draft is %q, want %q", got, want)
	}
	// Walk back into the mention's first piece — `@"dir` — and stay quiet.
	pressChat(t, v, "left", len(` with space/notes.md" `))
	if v.files.open {
		t.Fatal("the cursor inside an accepted mention reopened the picker")
	}
	// Editing the mention away releases the suppression: the piece is no
	// longer part of anything the draft holds.
	v.composer.SetValue("read @di")
	v.updateKey(tea.KeyPressMsg{Code: 'r', Text: "r"})
	if !v.files.open {
		t.Fatal("a mention edited out of the draft left the picker suppressed")
	}
}

// TestChatFilesNeverOpensUnder holds every state a typed `@` must stay out of.
func TestChatFilesNeverOpensUnder(t *testing.T) {
	t.Run("the answer popup", func(t *testing.T) {
		v := chatFilesFixture(claudeFiles())
		v.form = newAnswerForm(questionRequest())
		typeIntoChat(t, v, "@main")
		if v.files.open {
			t.Fatal("a typed @ opened the picker under the §7.4 popup")
		}
	})
	t.Run("the close confirmation", func(t *testing.T) {
		v := chatFilesFixture(claudeFiles())
		task := int64(9)
		v.chat.LinkedTaskID = &task
		typeIntoChat(t, v, "@main")
		if !v.files.open {
			t.Fatal("the fixture never opened the picker")
		}
		v.askClose()
		if !v.closing || v.files.open {
			t.Fatalf("the confirmation left the picker open=%v", v.files.open)
		}
	})
	t.Run("a terminal chat", func(t *testing.T) {
		v := chatFilesFixture(claudeFiles())
		v.chat.State = "archived"
		typeIntoChat(t, v, "@main")
		if v.files.open {
			t.Fatal("a typed @ opened the picker on a terminal chat")
		}
	})
	t.Run("the skills browse list", func(t *testing.T) {
		v := chatFilesFixture(claudeFiles())
		v.skills.data = claudeSkills()
		openChatSkills(t, v, "tab")
		runChatCmds(v, v.paste("@main"))
		if v.files.open {
			t.Fatal("a pasted @ opened the picker under the browse list")
		}
	})
}

// TestChatFilesPasteOpensThePicker: the list is derived from the draft, not
// from the keys that made it.
func TestChatFilesPasteOpensThePicker(t *testing.T) {
	v := chatFilesFixture(claudeFiles())
	runChatCmds(v, v.paste("@main"))
	if !v.files.open {
		t.Fatal("a pasted @main opened nothing")
	}
	if v.files.filter != "main" {
		t.Fatalf("the pasted token filtered to %q", v.files.filter)
	}
}

// TestChatFilesMentionVerdictNote is task 126 decision 43: the note line
// carries the verdict and only in the negative, which is task 124 decision
// 19's bad-news-only rule.
func TestChatFilesMentionVerdictNote(t *testing.T) {
	t.Run("an expanding adapter says nothing", func(t *testing.T) {
		v := chatFilesFixture(claudeFiles())
		typeIntoChat(t, v, "@main")
		if got := v.files.mentionNote(); got != "" {
			t.Fatalf("an expanding adapter said %q", got)
		}
	})
	t.Run("a non-expanding one says so", func(t *testing.T) {
		data := claudeFiles()
		data.Agent, data.MentionExpands = "cursor", false
		v := chatFilesFixture(data)
		typeIntoChat(t, v, "@main")
		want := "cursor does not expand an @ mention — it sees the path and may read the file itself"
		if got := v.files.mentionNote(); got != want {
			t.Fatalf("the note is %q, want %q", got, want)
		}
		if plain := ansi.Strip(v.render(100, 30)); !strings.Contains(plain, want) {
			t.Fatalf("the note is not drawn:\n%s", plain)
		}
	})
	t.Run("a shut picker says nothing either way", func(t *testing.T) {
		data := claudeFiles()
		data.MentionExpands = false
		v := chatFilesFixture(data)
		if got := v.files.mentionNote(); got != "" {
			t.Fatalf("a shut picker said %q", got)
		}
	})
}

// TestChatFilesHelpIsItsOwnSurface: `?` over an open picker must describe the
// picker's keys. A pane promising the skills list's `tab` would be a lie —
// task 124 decision 94's reason for a context of its own.
func TestChatFilesHelpIsItsOwnSurface(t *testing.T) {
	v := chatFilesFixture(claudeFiles())
	typeIntoChat(t, v, "@main")
	help := ansi.Strip(helpText(v.bindingContext(), true))
	if !strings.Contains(help, "complete the @ token with the highlighted file") {
		t.Fatalf("the picker's help does not carry its own rows:\n%s", help)
	}
	if strings.Contains(help, "highlighted skill") {
		t.Fatalf("the picker's help promises the skills list's keys:\n%s", help)
	}
}

// TestChatFilesZeroValueStillReservesItsLine holds issue #554 decision 5 on
// this list: the reserve, the cap and the row renderer are fields of the
// data-neutral core that core() sets on every call, never at construction.
// Nothing constructs a chatFileList — chatView.open assigns a bare literal.
func TestChatFilesZeroValueStillReservesItsLine(t *testing.T) {
	const paneHeight, width = 30, 100
	var l chatFileList
	l.open, l.data = true, manyFiles(2, false)
	if l.reserve != 0 || l.cap != 0 || l.rowLine != nil {
		t.Fatal("the bare literal already carried the core's fields")
	}
	l.build()
	if l.cap != chatFileMentionMax {
		t.Fatalf("build left the cap at %d, want %d", l.cap, chatFileMentionMax)
	}
	got := l.render(width, paneHeight)
	if want := 1 + l.titledCore().window(paneHeight) + 1; len(got) != want {
		t.Fatalf("a zero-valued list rendered %d lines, want %d", len(got), want)
	}
	l.cursor = 0
	plain := ansi.Strip(strings.Join(l.render(width, paneHeight), "\n"))
	if !strings.Contains(plain, "src/file-000000.go") {
		t.Fatalf("the highlighted row's path was not drawn into the reserve:\n%s", plain)
	}
}
