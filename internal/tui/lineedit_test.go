package tui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
)

func init() {
	panelKeyProbes[ctxChat]["alt+i"] = func(t *testing.T) {
		v := chatViewFixture()
		v.updateKey(registryKey(t, "alt+i"))
		if !v.convFocus {
			t.Fatal("alt+i left the keyboard in the composer")
		}
	}
	panelKeyProbes[ctxChatConversation]["alt+i"] = func(t *testing.T) {
		v := chatViewFixture()
		v.focusConversation()
		v.updateKey(registryKey(t, "alt+i"))
		if v.convFocus {
			t.Fatal("alt+i did not give the keyboard back to the composer")
		}
	}
}

func pressLine(e *lineEdit, keys ...string) {
	for _, k := range keys {
		e.Key(synthKey(k))
	}
}

// #1078: the find line and the shell prompt move and delete like a field.
func TestLineEditMovesAndDeletes(t *testing.T) {
	var e lineEdit
	e.Set("git log --oneline")
	pressLine(&e, "alt+left", "ctrl+w")
	if e.String() != "git --oneline" {
		t.Fatalf("ctrl+w after alt+left left %q", e.String())
	}
	pressLine(&e, "home", "right", "right", "right")
	e.Key(tea.KeyPressMsg{Code: 'X', Text: "X"})
	if e.String() != "gitX --oneline" {
		t.Fatalf("typing mid-line left %q", e.String())
	}
	pressLine(&e, "ctrl+k")
	if e.String() != "gitX" {
		t.Fatalf("ctrl+k left %q", e.String())
	}
	pressLine(&e, "ctrl+u")
	if e.String() != "" {
		t.Fatalf("ctrl+u left %q", e.String())
	}
	b := lineEdit{max: 3}
	b.Insert("abcdef")
	if b.String() != "abc" {
		t.Fatalf("a bounded line took %q", b.String())
	}
}

// #1078: ↑ walks the commands this chat ran, newest first, and ↓ comes back
// to what was being typed.
func TestShellPromptRecallsEarlierCommands(t *testing.T) {
	v := shellFixture(t)
	v.openShellPrompt()
	v.shell.buf.Set("ls")
	v.updateShellPromptKey(synthKey("up"))
	if got := v.shell.buf.String(); got != "git status" {
		t.Fatalf("↑ recalled %q, want the newest command", got)
	}
	v.updateShellPromptKey(synthKey("up"))
	v.updateShellPromptKey(synthKey("up"))
	if got := v.shell.buf.String(); got != "go test ./..." {
		t.Fatalf("↑↑↑ recalled %q, want the oldest command", got)
	}
	v.updateShellPromptKey(synthKey("down"))
	v.updateShellPromptKey(synthKey("down"))
	if got := v.shell.buf.String(); got != "ls" {
		t.Fatalf("↓ back past the newest left %q, want the draft", got)
	}
}
