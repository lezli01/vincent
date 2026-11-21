package tui

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/clipimage"
)

// Task 170's shell passthrough, image paste and their keys, below the API:
// the live half is chatpolish_live_test.go.

// shellFixture is chatFindFixture with two finished shell runs, one after
// turn 1 with a long output and one marked for the next prompt after turn 3.
func shellFixture(t *testing.T) *chatView {
	t.Helper()
	v := chatFindFixture(t)
	zero, one := 0, 1
	v.shell.runs = []apiclient.ChatShellRun{
		{ID: 10, ChatID: 1, Command: "go test ./...", State: apiclient.ChatShellExited, ExitCode: &one, AfterTurn: 1, Truncated: true},
		{ID: 11, ChatID: 1, Command: "git status", State: apiclient.ChatShellExited, ExitCode: &zero, AfterTurn: 3, Attach: true},
	}
	v.shell.out = map[int64]string{
		10: "line 1\nline 2\nline 3\nline 4\nline 5\nline 6\n",
		11: "clean\n",
	}
	v.shell.fetched = map[int64]bool{10: true, 11: true}
	v.bodyDirty = true
	v.render(100, 40)
	return v
}

// shellCardLine is the first body line of run id's card, or -1.
func shellCardLine(v *chatView, id int64) int {
	for y, a := range v.anchors {
		if a.shell == id {
			return y
		}
	}
	return -1
}

func withClipImage(t *testing.T, data []byte, err error) {
	t.Helper()
	prev := clipImageRead
	clipImageRead = func(context.Context) ([]byte, error) { return data, err }
	t.Cleanup(func() { clipImageRead = prev })
}

var fakePNG = []byte("\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR")

func init() {
	chat, conv := panelKeyProbes[ctxChat], panelKeyProbes[ctxChatConversation]
	chat["alt+x"] = func(t *testing.T) {
		v := chatViewFixture()
		v.composer.SetValue("!not a command")
		v.updateKey(registryKey(t, "alt+x"))
		if !v.shell.prompt || !v.capturesInput() || v.bindingContext() != ctxChatShell {
			t.Fatalf("alt+x: prompt %v, captures %v, context %q", v.shell.prompt, v.capturesInput(), v.bindingContext())
		}
		if v.composer.Value() != "!not a command" {
			t.Fatalf("the prompt touched the draft: %q", v.composer.Value())
		}
	}
	chat["ctrl+v"] = func(t *testing.T) {
		withClipImage(t, fakePNG, nil)
		v := chatViewFixture()
		_, cmd := v.updateKey(registryKey(t, "ctrl+v"))
		msg, ok := runCmd(t, cmd, 10*time.Second).(chatImageClipMsg)
		if !ok || string(msg.data) != string(fakePNG) || msg.chatID != 1 {
			t.Fatalf("ctrl+v answered %#v, want the clipboard's image", msg)
		}
	}
	conv["a"] = func(t *testing.T) {
		v := shellFixture(t)
		// Unmark the marked run first: the press must reach the daemon.
		v.focusConversation()
		v.render(100, 40)
		v.moveCursor(shellCardLine(v, 11))
		_, cmd := v.updateKey(registryKey(t, "a"))
		if cmd == nil {
			t.Fatalf("a on a finished run's card sent nothing (note %q)", v.note)
		}
	}
	panelKeyProbes[ctxChatShell] = map[string]func(*testing.T){
		"enter": func(t *testing.T) {
			v := chatViewFixture()
			v.client = offlineClient()
			v.openShellPrompt()
			v.shell.buf = "ls"
			_, cmd := v.updateKey(registryKey(t, "enter"))
			if cmd == nil || v.shell.prompt || !v.shell.busy {
				t.Fatalf("enter: cmd %v, prompt %v, busy %v", cmd != nil, v.shell.prompt, v.shell.busy)
			}
		},
		"esc": func(t *testing.T) {
			v := chatViewFixture()
			v.openShellPrompt()
			v.shell.buf = "ls"
			_, cmd := v.updateKey(registryKey(t, "esc"))
			if cmd != nil || v.shell.prompt || v.shell.buf != "" {
				t.Fatalf("esc: cmd %v, prompt %v, buf %q", cmd != nil, v.shell.prompt, v.shell.buf)
			}
		},
	}
}

// TestChatShellPromptTypes: the prompt owns the keyboard — a letter goes to
// the command, not the draft — and a paste lands in it as one line.
func TestChatShellPromptTypes(t *testing.T) {
	v := chatViewFixture()
	v.openShellPrompt()
	pressAll(t, v, "l", "s")
	v.paste(" -la\nrm -rf /")
	if v.shell.buf != "ls -la rm -rf /" || v.composer.Value() != "" {
		t.Fatalf("buf %q, draft %q", v.shell.buf, v.composer.Value())
	}
	if !strings.Contains(ansi.Strip(v.render(100, 30)), "$ ls -la rm -rf /") {
		t.Fatalf("the prompt is not drawn:\n%s", ansi.Strip(v.render(100, 30)))
	}
}

// TestChatShellRefusedOnAnEndedChat: no prompt on a terminal chat, and the
// footer does not offer the key there.
func TestChatShellRefusedOnAnEndedChat(t *testing.T) {
	v := chatViewFixture()
	v.chat.State = "closed"
	v.updateKey(registryKey(t, "alt+x"))
	if v.shell.prompt || !v.noteBad {
		t.Fatalf("an ended chat opened the prompt (note %q)", v.note)
	}
	for _, b := range v.liveBindings(bindings) {
		if b.op == "shell_command" {
			t.Fatal("the footer offers the shell on an ended chat")
		}
	}
	for _, state := range []string{"idle", "running", "awaiting_input"} {
		v := chatViewFixture()
		v.chat.State = state
		if !v.shellAvailable() {
			t.Errorf("the shell is not offered on a %s chat", state)
		}
	}
}

// TestChatShellRefusalIsTheNote: a 409 from the daemon is shown, and no card
// appears.
func TestChatShellRefusalIsTheNote(t *testing.T) {
	v := chatViewFixture()
	v.shell.busy = true
	v.applyShellStarted(chatShellStartedMsg{chatID: 1, err: &apiclient.Error{Status: 409, Code: "invalid_state", Message: "this chat already has a shell command running"}})
	if v.shell.busy || !v.noteBad || !strings.Contains(v.note, "already has a shell command running") || len(v.shell.runs) != 0 {
		t.Fatalf("busy %v, note %q (bad %v), runs %d", v.shell.busy, v.note, v.noteBad, len(v.shell.runs))
	}
}

// TestChatShellCardsSitAfterTheirTurn: each card is drawn after the turn
// that was newest when it started, says its exit code and its mark, and folds
// a long output until enter unfolds it.
func TestChatShellCardsSitAfterTheirTurn(t *testing.T) {
	v := shellFixture(t)
	body := bodyText(v)
	first, second, third := strings.Index(body, "second question"), strings.Index(body, "$ go test"), strings.Index(body, "$ git status")
	if first < 0 || second < 0 || third < 0 || second > first || third < strings.Index(body, "third question") {
		t.Fatalf("cards out of place:\n%s", body)
	}
	for _, want := range []string{"exit 1", "exit 0 · attached to next prompt", "│ line 4", "… 2 more lines", "output truncated", "│ clean"} {
		if !strings.Contains(body, want) {
			t.Errorf("the body lacks %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "│ line 6") {
		t.Fatalf("a folded card drew its whole output:\n%s", body)
	}
	v.focusConversation()
	v.render(100, 40)
	v.moveCursor(shellCardLine(v, 10))
	v.updateKey(registryKey(t, "enter"))
	if body := bodyText(v); !strings.Contains(body, "│ line 6") || strings.Contains(body, "more lines") {
		t.Fatalf("enter did not unfold the card:\n%s", body)
	}

	// Taken by a turn, the card says which.
	turn := 4
	v.shell.runs[1].Attach, v.shell.runs[1].AttachedTurn = false, &turn
	v.bodyDirty = true
	if body := bodyText(v); !strings.Contains(body, "sent with turn 4") {
		t.Fatalf("a taken run does not say so:\n%s", body)
	}
	v.moveCursor(shellCardLine(v, 11))
	if _, cmd := v.updateKey(registryKey(t, "a")); cmd != nil || !strings.Contains(v.note, "already sent") {
		t.Fatalf("a re-attached a taken run (note %q)", v.note)
	}
}

// TestChatShellRunningCardAndEvent: a running card fetches no output, and
// chat.shell_changed re-reads the runs rather than the whole chat.
func TestChatShellRunningCardAndEvent(t *testing.T) {
	v := chatViewFixture()
	v.client = offlineClient()
	cmd := v.applyShellRuns(chatShellRunsMsg{chatID: 1, runs: []apiclient.ChatShellRun{
		{ID: 3, ChatID: 1, Command: "sleep 9", State: apiclient.ChatShellRunning},
	}})
	if cmd != nil || v.shell.fetched[3] {
		t.Fatal("a running run's output was fetched")
	}
	if body := bodyText(v); !strings.Contains(body, "$ sleep 9") || !strings.Contains(body, "running…") {
		t.Fatalf("the running card:\n%s", body)
	}
	ch := make(chan apiclient.Note)
	got := v.applyChatNote(chatNoteMsg{chatID: 1, next: ch, note: apiclient.EventNote{Event: apiclient.Event{Type: chatShellChangedEvent}}})
	msgs := batchMsgs(t, got)
	if _, ok := msgs["shell"]; !ok || msgs["load"] {
		t.Fatalf("chat.shell_changed ran %v, want the runs re-read alone", msgs)
	}
}

// batchMsgs runs every command of a batch except the stream read, and says
// which kinds of request it made.
func batchMsgs(t *testing.T, cmd tea.Cmd) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	var walk func(tea.Cmd)
	walk = func(c tea.Cmd) {
		if c == nil {
			return
		}
		done := make(chan tea.Msg, 1)
		go func() { done <- c() }()
		select {
		case msg := <-done:
			switch m := msg.(type) {
			case tea.BatchMsg:
				for _, c := range m {
					walk(c)
				}
			case chatShellRunsMsg:
				out["shell"] = true
			case chatLoadedMsg:
				out["load"] = true
			}
		case <-time.After(200 * time.Millisecond):
			// The stream read blocks on its channel: it is not a request.
		}
	}
	walk(cmd)
	return out
}

// TestChatImagePasteFallsBackToText: no image on the clipboard is today's
// text paste, silently.
func TestChatImagePasteFallsBackToText(t *testing.T) {
	withClipImage(t, nil, clipimage.ErrNoImage)
	v := chatViewFixture()
	msg := runCmd(t, v.pasteImageCmd(), 10*time.Second)
	if _, ok := msg.(chatImageClipMsg); ok {
		t.Fatal("no image still uploaded one")
	}
	if v.note != "" {
		t.Fatalf("the fallback left a note: %q", v.note)
	}
}

// TestChatImageRefusedForAnAgentWithout: an agent that says it takes no
// images is refused before the upload, by name; a daemon refusal inserts
// nothing; an accepted image puts its chip at the cursor.
func TestChatImageRefusedForAnAgentWithout(t *testing.T) {
	no := false
	v := chatViewFixture()
	v.client = offlineClient()
	v.agents = map[string]apiclient.Agent{"claude": {Name: "claude", SupportsImages: &no}}
	if cmd := v.applyImageClip(chatImageClipMsg{chatID: 1, data: fakePNG}); cmd != nil {
		t.Fatal("an agent without images was uploaded to")
	}
	if !v.noteBad || !strings.Contains(v.note, "claude cannot take images") {
		t.Fatalf("note %q", v.note)
	}

	v = chatViewFixture()
	v.composer.SetValue("look at ")
	v.composer.CursorEnd()
	v.applyImageAdded(chatImageAddedMsg{chatID: 1, err: &apiclient.Error{Status: 422, Code: "validation_failed", Message: "codex cannot take images"}})
	if v.composer.Value() != "look at " || !strings.Contains(v.note, "codex cannot take images") {
		t.Fatalf("a refusal: draft %q, note %q", v.composer.Value(), v.note)
	}
	v.applyImageAdded(chatImageAddedMsg{chatID: 1, image: &apiclient.ChatImage{Number: 2, Chip: "[image 2]"}})
	if v.composer.Value() != "look at [image 2]" {
		t.Fatalf("the chip went to %q", v.composer.Value())
	}
}

// TestChatImagePathPaste: a pasted path to an image file uploads the file
// and never types the path; one that cannot be read pastes as text.
func TestChatImagePathPaste(t *testing.T) {
	prev := readImageFile
	t.Cleanup(func() { readImageFile = prev })
	readImageFile = func(string) ([]byte, error) { return fakePNG, nil }
	path := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(path, fakePNG, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, ok := clipimage.ImagePath(path); !ok {
		t.Fatalf("clipimage does not take %q as an image path", path)
	} else {
		path = got
	}
	v := chatViewFixture()
	msg, ok := runCmd(t, v.paste(path), 10*time.Second).(chatImageClipMsg)
	if !ok || msg.name != "shot.png" || v.composer.Value() != "" {
		t.Fatalf("a pasted image path answered %#v, draft %q", msg, v.composer.Value())
	}
	readImageFile = func(string) ([]byte, error) { return nil, errors.New("gone") }
	back := runCmd(t, v.paste(path), 10*time.Second)
	v.update(back)
	if v.composer.Value() != path {
		t.Fatalf("an unreadable path pasted %q", v.composer.Value())
	}
}

// TestChatShellCardStripsControls (review F1): a progress meter's bare
// carriage returns, a shift-out and a bell never reach the terminal; the
// line shows what a terminal would have left, and tabs still expand.
func TestChatShellCardStripsControls(t *testing.T) {
	v := shellFixture(t)
	v.shell.out[11] = "progress 10%\rprogress 100%\n\x0eshifted\x07bell\x1b[31mred\x1b[0m\tab\r\n"
	v.bodyDirty = true
	v.render(100, 40)
	var card []string
	for _, l := range v.shellCardLines(v.shell.runs[1], 100) {
		card = append(card, ansi.Strip(l))
	}
	joined := strings.Join(card, "\n")
	for _, bad := range []string{"\r", "\x0e", "\x07", "\x1b", "progress 10%"} {
		if strings.Contains(joined, bad) {
			t.Errorf("the card kept %q:\n%q", bad, joined)
		}
	}
	for _, want := range []string{"│ progress 100%", "│ shiftedbellred    ab"} {
		if !strings.Contains(joined, want) {
			t.Errorf("the card lacks %q:\n%q", want, joined)
		}
	}
}

// TestChatImagePathPastesAsTextWithoutTheCapability (review F2): in a chat
// whose agent takes no images, a dropped image file's path is pasted as the
// text it is — the agent can still read the file by path — and no note is
// left.
func TestChatImagePathPastesAsTextWithoutTheCapability(t *testing.T) {
	prev := readImageFile
	t.Cleanup(func() { readImageFile = prev })
	readImageFile = func(string) ([]byte, error) {
		t.Error("the image file was read for an agent that takes no images")
		return fakePNG, nil
	}
	path := filepath.Join(t.TempDir(), "shot.png")
	if err := os.WriteFile(path, fakePNG, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, ok := clipimage.ImagePath(path); !ok {
		t.Fatalf("clipimage does not take %q as an image path", path)
	}
	no := false
	v := chatViewFixture()
	v.agents = map[string]apiclient.Agent{"claude": {Name: "claude", SupportsImages: &no}}
	if v.chat.Agent != "claude" {
		v.agents[v.chat.Agent] = apiclient.Agent{Name: v.chat.Agent, SupportsImages: &no}
	}
	if cmd := v.paste(path); cmd != nil {
		if msg, ok := runCmd(t, cmd, 10*time.Second).(chatImageClipMsg); ok {
			t.Fatalf("the path went to the upload: %#v", msg)
		}
	}
	if v.composer.Value() != path || v.noteBad {
		t.Fatalf("draft %q, note %q (bad %v)", v.composer.Value(), v.note, v.noteBad)
	}
}
