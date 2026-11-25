package tui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func init() {
	panelKeyProbes[ctxChat]["alt+o"] = func(t *testing.T) {
		v := chatViewFixture()
		v.updateKey(registryKey(t, "alt+o"))
		if !v.convFocus {
			t.Fatal("alt+o left the keyboard in the composer")
		}
	}
	panelKeyProbes[ctxChatConversation]["alt+o"] = func(t *testing.T) {
		v := chatViewFixture()
		v.focusConversation()
		v.updateKey(registryKey(t, "alt+o"))
		if v.convFocus {
			t.Fatal("alt+o did not give the keyboard back to the composer")
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

// #1157: a line longer than its field scrolls with the cursor, so what is
// typed past the edge — and the cursor itself — stays on screen, and moving
// back to the start brings the start back.
func TestLineEditScrollsHorizontally(t *testing.T) {
	var e lineEdit
	e.Set("go test ./internal/tui/ -run TestLineEditScrollsHorizontally")
	visible := func(want string) {
		t.Helper()
		got := ansi.Strip(e.ViewIn(20))
		if w := ansi.StringWidth(got); w > 20 {
			t.Fatalf("%q is %d cells wide, past the field's 20", got, w)
		}
		if !strings.Contains(got, want) {
			t.Fatalf("the field shows %q, want it to show %q", got, want)
		}
	}
	visible("Horizontally█")
	if got := ansi.Strip(e.ViewIn(20)); !strings.HasPrefix(got, "…") {
		t.Errorf("a field scrolled right shows no mark of the text before it: %q", got)
	}
	pressLine(&e, "home")
	visible("go test")
	if got := ansi.Strip(e.ViewIn(20)); !strings.HasSuffix(got, "…") {
		t.Errorf("a field cut on the right shows no mark of the text after it: %q", got)
	}
	// Walking right one character at a time keeps the cursor in view.
	for range len("go test ./internal/tui/ -run") {
		pressLine(&e, "right")
		got := ansi.Strip(e.ViewIn(20))
		if ansi.StringWidth(got) > 20 {
			t.Fatalf("%q overflows", got)
		}
		cur := string(e.r[e.pos])
		if !strings.Contains(got, cur) {
			t.Fatalf("at %d the cursor's %q is off the field %q", e.pos, cur, got)
		}
	}
	short := lineEdit{}
	short.Set("ls")
	if got, want := short.ViewIn(20), short.View(); got != want {
		t.Errorf("a line that fits drew %q, want %q", got, want)
	}
}

// #1157: the shell prompt and the find line draw through the scrolling
// field, so a long command still shows its end and the cursor.
func TestShellPromptLineScrolls(t *testing.T) {
	v := shellFixture(t)
	v.openShellPrompt()
	v.shell.buf.Set("for f in $(git ls-files); do wc -l \"$f\"; done | sort -n | tail -20")
	got := ansi.Strip(v.shellPromptLine(40))
	if ansi.StringWidth(got) > 40 || !strings.Contains(got, "tail -20█") {
		t.Fatalf("the prompt drew %q", got)
	}
}
