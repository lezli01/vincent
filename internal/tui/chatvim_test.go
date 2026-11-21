package tui

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
)

// vimText is the fixture the tables edit: words and punctuation, an empty
// line, an indented line, and a last line shorter than the first.
const vimText = "foo bar.baz\n\n  two words\nlast"

// vimHarness is the layer over a textarea, routed the way the chat view
// routes it: the layer sees every key first, and one it does not handle goes
// to the textarea as it would with the layer off.
type vimHarness struct {
	t   *testing.T
	ta  textarea.Model
	vim *chatVim
}

func newVimHarness(t *testing.T, text string, width int) *vimHarness {
	t.Helper()
	ta := textarea.New()
	ta.Prompt = ""
	ta.ShowLineNumbers = false
	ta.MaxContentHeight = editorContentCap
	ta.SetWidth(width)
	ta.SetHeight(6)
	ta.Focus()
	ta.SetValue(text)
	return &vimHarness{t: t, ta: ta, vim: newChatVim()}
}

// normalAt is a harness in normal mode with the cursor on row, col.
func normalAt(t *testing.T, text string, row, col int) *vimHarness {
	t.Helper()
	h := newVimHarness(t, text, 60)
	h.vim.state = vimNormal
	vimPlace(&h.ta, vimPos{row, col})
	if h.ta.Line() != row || h.ta.Column() != col {
		t.Fatalf("fixture cursor is %d,%d, want %d,%d", h.ta.Line(), h.ta.Column(), row, col)
	}
	return h
}

// vimKey reads one token of a key sequence: `<esc>`, `<enter>`, `<left>`,
// `<bs>`, `<c-w>`, `<a-x>`, or a character.
func vimKey(tok string) tea.KeyPressMsg {
	switch tok {
	case "<esc>":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "<enter>":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "<left>":
		return tea.KeyPressMsg{Code: tea.KeyLeft}
	case "<bs>":
		return tea.KeyPressMsg{Code: tea.KeyBackspace}
	case "<c-w>":
		return tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl}
	case "<a-x>":
		return tea.KeyPressMsg{Code: 'x', Text: "x", Mod: tea.ModAlt}
	}
	r := []rune(tok)[0]
	msg := tea.KeyPressMsg{Code: r, Text: tok}
	if r >= 'A' && r <= 'Z' {
		msg.Code, msg.ShiftedCode, msg.Mod = r+('a'-'A'), r, tea.ModShift
	}
	return msg
}

func vimTokens(seq string) []string {
	var out []string
	for seq != "" {
		if seq[0] == '<' {
			if end := strings.IndexByte(seq, '>'); end > 0 {
				out = append(out, seq[:end+1])
				seq = seq[end+1:]
				continue
			}
		}
		r := []rune(seq)[0]
		out = append(out, string(r))
		seq = seq[len(string(r)):]
	}
	return out
}

// press sends one key and reports whether the layer handled it.
func (h *vimHarness) press(tok string) bool {
	h.t.Helper()
	msg := vimKey(tok)
	handled, cmd := h.vim.update(&h.ta, msg)
	if cmd != nil {
		h.t.Errorf("%s returned a command", tok)
	}
	if !handled && msg.Code != tea.KeyEnter {
		h.ta, _ = h.ta.Update(msg)
	}
	return handled
}

func (h *vimHarness) keys(seq string) {
	h.t.Helper()
	for _, tok := range vimTokens(seq) {
		h.press(tok)
	}
}

func (h *vimHarness) want(text string, row, col int) {
	h.t.Helper()
	if got := h.ta.Value(); got != text {
		h.t.Errorf("text = %q, want %q", got, text)
	}
	if h.ta.Line() != row || h.ta.Column() != col {
		h.t.Errorf("cursor = %d,%d, want %d,%d", h.ta.Line(), h.ta.Column(), row, col)
	}
}

// TestChatVimMotions: every motion key, from a stated cursor to the one vim
// would leave. None of them changes the text.
func TestChatVimMotions(t *testing.T) {
	cases := []struct {
		name     string
		row, col int
		keys     string
		wantRow  int
		wantCol  int
	}{
		{"h", 0, 3, "h", 0, 2},
		{"h at the line's start", 0, 0, "h", 0, 0},
		{"l", 0, 0, "l", 0, 1},
		{"l stops on the last character", 0, 10, "l", 0, 10},
		{"j", 0, 5, "j", 1, 0},
		{"j keeps the column it aimed for", 0, 5, "jj", 2, 5},
		{"j clamps to a shorter line", 0, 5, "jjj", 3, 3},
		{"j on the last line", 3, 1, "j", 3, 1},
		{"k", 2, 3, "k", 1, 0},
		{"k keeps the column it aimed for", 2, 6, "kk", 0, 6},
		{"k on the first line", 0, 4, "k", 0, 4},
		{"a horizontal move forgets the column", 0, 5, "jhj", 2, 0},
		{"w", 0, 0, "w", 0, 4},
		{"w stops at punctuation", 0, 0, "ww", 0, 7},
		{"w leaves punctuation", 0, 0, "www", 0, 8},
		{"w stops on an empty line", 0, 0, "wwww", 1, 0},
		{"w skips indentation", 0, 0, "wwwww", 2, 2},
		{"w onto the last line", 0, 0, "wwwwwww", 3, 0},
		{"w at the end of the text", 3, 0, "w", 3, 3},
		{"b", 3, 0, "b", 2, 6},
		{"b inside a word", 2, 8, "b", 2, 6},
		{"b stops on an empty line", 2, 2, "b", 1, 0},
		{"b from an empty line", 1, 0, "b", 0, 8},
		{"b stops at punctuation", 0, 8, "b", 0, 7},
		{"b at the start of the text", 0, 0, "b", 0, 0},
		{"e", 0, 0, "e", 0, 2},
		{"e from a word's end", 0, 2, "e", 0, 6},
		{"e stops at punctuation", 0, 6, "e", 0, 7},
		{"e passes an empty line", 0, 10, "e", 2, 4},
		{"e at the end of the text", 3, 3, "e", 3, 3},
		{"0", 2, 5, "0", 2, 0},
		{"$", 0, 0, "$", 0, 10},
		{"$ on an empty line", 1, 0, "$", 1, 0},
		{"gg", 3, 2, "gg", 0, 0},
		{"G", 0, 4, "G", 3, 0},
		{"g then another key is nothing", 3, 2, "gx", 3, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := normalAt(t, vimText, tc.row, tc.col)
			h.keys(tc.keys)
			h.want(vimText, tc.wantRow, tc.wantCol)
			if h.vim.mode() != vimNormal {
				t.Error("a motion left normal mode")
			}
		})
	}
	t.Run("gg and G land on the first non-blank", func(t *testing.T) {
		h := normalAt(t, "  a\nb\n  c", 1, 0)
		h.keys("G")
		h.want("  a\nb\n  c", 2, 2)
		h.keys("gg")
		h.want("  a\nb\n  c", 0, 2)
	})
}

// TestChatVimEdits: every key that changes the text or the mode, against the
// text and cursor it leaves. The text is compared exactly — it is what a
// draft would persist.
func TestChatVimEdits(t *testing.T) {
	const rest = "\n\n  two words\nlast"
	cases := []struct {
		name     string
		row, col int
		keys     string
		want     string
		wantRow  int
		wantCol  int
	}{
		// Entering insert mode.
		{"i", 0, 4, "iX<esc>", "foo Xbar.baz" + rest, 0, 4},
		{"a", 0, 2, "aX<esc>", "fooX bar.baz" + rest, 0, 3},
		{"a on an empty line", 1, 0, "aX<esc>", "foo bar.baz\nX\n  two words\nlast", 1, 0},
		{"I", 2, 6, "IX<esc>", "foo bar.baz\n\n  Xtwo words\nlast", 2, 2},
		{"A", 0, 0, "AX<esc>", "foo bar.bazX" + rest, 0, 11},
		{"o", 0, 3, "oX<esc>", "foo bar.baz\nX" + rest, 1, 0},
		{"o on the last line", 3, 1, "oX<esc>", vimText + "\nX", 4, 0},
		{"O", 0, 3, "OX<esc>", "X\nfoo bar.baz" + rest, 0, 0},
		{"O in the middle", 2, 3, "OX<esc>", "foo bar.baz\n\nX\n  two words\nlast", 2, 0},

		// x.
		{"x", 0, 3, "x", "foobar.baz" + rest, 0, 3},
		{"x on the last character", 0, 10, "x", "foo bar.ba" + rest, 0, 9},
		{"x on an empty line", 1, 0, "x", vimText, 1, 0},

		// d with each motion, and doubled.
		{"dh", 0, 3, "dh", "fo bar.baz" + rest, 0, 2},
		{"dl", 0, 0, "dl", "oo bar.baz" + rest, 0, 0},
		{"dw", 0, 0, "dw", "bar.baz" + rest, 0, 0},
		{"dw on a line's last word keeps the line break", 0, 8, "dw", "foo bar." + rest, 0, 7},
		{"db", 0, 7, "db", "foo .baz" + rest, 0, 4},
		{"de", 0, 0, "de", " bar.baz" + rest, 0, 0},
		{"d0", 0, 4, "d0", "bar.baz" + rest, 0, 0},
		{"d$", 0, 4, "d$", "foo " + rest, 0, 3},
		{"d$ on an empty line keeps the line", 1, 0, "d$", vimText, 1, 0},
		{"dj", 0, 2, "dj", "  two words\nlast", 0, 2},
		{"dj on the last line is cancelled", 3, 0, "dj", vimText, 3, 0},
		{"dk", 3, 0, "dk", "foo bar.baz\n", 1, 0},
		{"dgg", 2, 3, "dgg", "last", 0, 0},
		{"dG", 2, 3, "dG", "foo bar.baz\n", 1, 0},
		{"dd", 0, 5, "dd", "\n  two words\nlast", 0, 0},
		{"dd on the last line", 3, 2, "dd", "foo bar.baz\n\n  two words", 2, 2},

		// c with each motion, and doubled.
		{"ch", 0, 4, "chX<esc>", "fooXbar.baz" + rest, 0, 3},
		{"cl", 0, 0, "clX<esc>", "Xoo bar.baz" + rest, 0, 0},
		{"cw changes the word, not the blank after it", 0, 0, "cwX<esc>", "X bar.baz" + rest, 0, 0},
		{"cw on a word's last character", 0, 2, "cwX<esc>", "foX bar.baz" + rest, 0, 2},
		{"cw on a blank", 0, 3, "cwX<esc>", "fooXbar.baz" + rest, 0, 3},
		{"cb", 0, 7, "cbX<esc>", "foo X.baz" + rest, 0, 4},
		{"ce", 0, 0, "ceX<esc>", "X bar.baz" + rest, 0, 0},
		{"c0", 0, 4, "c0X<esc>", "Xbar.baz" + rest, 0, 0},
		{"c$", 0, 4, "c$X<esc>", "foo X" + rest, 0, 4},
		{"cj", 0, 0, "cjX<esc>", "X\n  two words\nlast", 0, 0},
		{"ck", 3, 0, "ckX<esc>", "foo bar.baz\n\nX", 2, 0},
		{"cgg", 1, 0, "cggX<esc>", "X\n  two words\nlast", 0, 0},
		{"cG", 2, 0, "cGX<esc>", "foo bar.baz\n\nX", 2, 0},
		{"cc", 2, 4, "ccX<esc>", "foo bar.baz\n\nX\nlast", 2, 0},

		// y with each motion, and doubled, shown by what p puts back.
		{"yh", 0, 1, "yhp", "ffoo bar.baz" + rest, 0, 1},
		{"yl", 0, 0, "ylp", "ffoo bar.baz" + rest, 0, 1},
		{"yw", 0, 0, "ywp", "ffoo oo bar.baz" + rest, 0, 4},
		{"yb moves to the span's start", 0, 4, "ybp", "ffoo oo bar.baz" + rest, 0, 4},
		{"ye", 0, 0, "yep", "ffoooo bar.baz" + rest, 0, 3},
		{"y0", 0, 3, "y0$p", "foo bar.bazfoo" + rest, 0, 13},
		{"y$", 0, 8, "y$0p", "fbazoo bar.baz" + rest, 0, 3},
		{"yj", 0, 0, "yjjp", "foo bar.baz\n\nfoo bar.baz\n\n  two words\nlast", 2, 0},
		{"yk", 1, 0, "ykp", "foo bar.baz\nfoo bar.baz\n\n\n  two words\nlast", 1, 0},
		{"ygg", 1, 0, "yggGp", vimText + "\nfoo bar.baz\n", 4, 0},
		{"yG", 2, 0, "yGggp", "foo bar.baz\n  two words\nlast\n\n  two words\nlast", 1, 2},
		{"yy", 0, 3, "yyp", "foo bar.baz\nfoo bar.baz" + rest, 1, 0},
		{"yy of an empty line", 1, 0, "yyp", "foo bar.baz\n\n\n  two words\nlast", 2, 0},
		{"y changes nothing", 0, 0, "yw", vimText, 0, 0},

		// p.
		{"p with nothing yanked", 0, 0, "p", vimText, 0, 0},
		{"p after x swaps two characters", 0, 0, "xp", "ofo bar.baz" + rest, 0, 1},
		{"p after dd moves a line down", 0, 0, "ddp", "\nfoo bar.baz\n  two words\nlast", 1, 0},
		{"p on an empty line", 0, 0, "ywjp", "foo bar.baz\nfoo \n  two words\nlast", 1, 3},

		// u.
		{"u with nothing to undo", 0, 3, "u", vimText, 0, 3},
		{"u after x", 0, 3, "xu", vimText, 0, 3},
		{"u undoes one change", 0, 0, "xxu", "oo bar.baz" + rest, 0, 0},
		{"u twice", 0, 0, "xxuu", vimText, 0, 0},
		{"u after dd", 2, 4, "ddu", vimText, 2, 4},
		{"u after p", 0, 0, "yypu", vimText, 0, 0},
		{"u takes an insert session whole", 0, 4, "iXYZ<esc>u", vimText, 0, 4},
		{"u takes o and its typing whole", 0, 3, "oXY<esc>u", vimText, 0, 3},
		{"u takes c and its typing whole", 0, 0, "cwXY<esc>u", vimText, 0, 0},
		{"u after an insert that typed nothing undoes the change before it", 0, 0, "xi<esc>u", vimText, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := normalAt(t, vimText, tc.row, tc.col)
			h.keys(tc.keys)
			h.want(tc.want, tc.wantRow, tc.wantCol)
			if h.vim.mode() != vimNormal {
				t.Error("the sequence did not end in normal mode")
			}
		})
	}
	t.Run("dd on the only line leaves an empty composer", func(t *testing.T) {
		h := normalAt(t, "only", 0, 2)
		h.keys("dd")
		h.want("", 0, 0)
		h.keys("p")
		h.want("\nonly", 1, 0)
	})
	t.Run("I on a line of blanks goes after them", func(t *testing.T) {
		h := normalAt(t, "   ", 0, 0)
		h.keys("IX<esc>")
		h.want("   X", 0, 3)
	})
}

// TestChatVimInsertKeysEnterInsertMode: i a I A o O and c leave the layer in
// insert mode, where it handles nothing but esc.
func TestChatVimInsertKeysEnterInsertMode(t *testing.T) {
	for _, keys := range []string{"i", "a", "I", "A", "o", "O", "cw", "cc", "c$"} {
		h := normalAt(t, vimText, 0, 4)
		h.keys(keys)
		if h.vim.mode() != vimInsert || h.vim.label() != "INSERT" {
			t.Errorf("%s left the layer in %s", keys, h.vim.label())
		}
		if h.press("x") {
			t.Errorf("after %s the layer handled a typed character", keys)
		}
	}
}

// TestChatVimStartsInInsert: a new layer types, so a hand without the habit
// loses nothing; esc is the one key it takes.
func TestChatVimStartsInInsert(t *testing.T) {
	h := newVimHarness(t, "", 60)
	if h.vim.mode() != vimInsert || h.vim.label() != "INSERT" {
		t.Fatalf("a new layer is in %s", h.vim.label())
	}
	for _, tok := range []string{"h", "d", "<enter>", "<c-w>", "<a-x>", "<left>", "<bs>"} {
		if h.press(tok) {
			t.Errorf("insert mode handled %s", tok)
		}
	}
	h.ta.SetValue("")
	h.keys("hi dd")
	h.want("hi dd", 0, 5)
	if !h.press("<esc>") {
		t.Error("insert mode did not handle esc")
	}
	if h.vim.mode() != vimNormal || h.vim.label() != "NORMAL" {
		t.Errorf("esc left the layer in %s", h.vim.label())
	}
	// The cursor steps back onto the last character typed.
	h.want("hi dd", 0, 4)
	// And the first session is an undo step like any other.
	h.keys("u")
	h.want("", 0, 0)
}

// TestChatVimNormalModeContract is what the chat view is wired against: which
// keys normal mode takes, and which it leaves for the caller.
func TestChatVimNormalModeContract(t *testing.T) {
	t.Run("esc with nothing pending is the caller's", func(t *testing.T) {
		h := normalAt(t, vimText, 0, 0)
		if h.press("<esc>") {
			t.Error("normal mode handled an esc with nothing pending")
		}
		if h.vim.mode() != vimNormal {
			t.Error("esc left normal mode")
		}
	})
	t.Run("esc clears a pending operator", func(t *testing.T) {
		for _, pending := range []string{"d", "c", "y", "g", "dg"} {
			h := normalAt(t, vimText, 0, 0)
			h.keys(pending)
			if !h.press("<esc>") {
				t.Errorf("esc with %s pending was not handled", pending)
			}
			if h.press("<esc>") {
				t.Errorf("a second esc after %s was handled; nothing was pending", pending)
			}
			// The operator is gone: w only moves.
			h.keys("w")
			h.want(vimText, 0, 4)
		}
	})
	t.Run("enter is the caller's in both modes", func(t *testing.T) {
		h := normalAt(t, vimText, 0, 0)
		if h.press("<enter>") {
			t.Error("normal mode handled enter")
		}
		h.keys("d")
		if h.press("<enter>") {
			t.Error("normal mode handled enter with an operator pending")
		}
		h.keys("w")
		h.want(vimText, 0, 4) // the operator did not survive it
	})
	t.Run("ctrl and alt keys are the caller's", func(t *testing.T) {
		h := normalAt(t, vimText, 0, 4)
		for _, tok := range []string{"<c-w>", "<a-x>"} {
			msg := vimKey(tok)
			if handled, _ := h.vim.update(&h.ta, msg); handled {
				t.Errorf("normal mode handled %s", tok)
			}
		}
		h.want(vimText, 0, 4)
	})
	t.Run("non-printing keys are the caller's", func(t *testing.T) {
		h := normalAt(t, vimText, 0, 4)
		if h.press("<left>") {
			t.Error("normal mode handled an arrow key")
		}
		h.want(vimText, 0, 3)
	})
	t.Run("any other printable key is swallowed", func(t *testing.T) {
		h := normalAt(t, vimText, 0, 4)
		for _, tok := range []string{"z", "q", " ", "5", ":", "/", "v", "P", "X", "é"} {
			if !h.press(tok) {
				t.Errorf("normal mode did not handle %q", tok)
			}
		}
		h.want(vimText, 0, 4)
		if h.vim.mode() != vimNormal {
			t.Error("an unbound key left normal mode")
		}
	})
	t.Run("an unbound key cancels a pending operator", func(t *testing.T) {
		h := normalAt(t, vimText, 0, 0)
		h.keys("dzw")
		h.want(vimText, 0, 4)
	})
}

// TestChatVimReset: back to insert, with nothing pending, yanked or undoable.
func TestChatVimReset(t *testing.T) {
	h := normalAt(t, vimText, 0, 0)
	h.keys("yyxd") // a yank, an undo step, and a pending operator
	h.vim.reset()
	if h.vim.mode() != vimInsert || h.vim.label() != "INSERT" {
		t.Fatalf("reset left the layer in %s", h.vim.label())
	}
	const after = "oo bar.baz\n\n  two words\nlast"
	h.keys("<esc>")
	h.keys("w")
	h.want(after, 0, 3) // w moved: no operator was pending
	h.keys("p")
	h.want(after, 0, 3) // nothing to put
	h.keys("u")
	h.want(after, 0, 3) // nothing to undo
}

// TestChatVimPlacesTheCursorAcrossWrappedLines: the textarea moves vertically
// by wrapped row, the layer by logical line. In a composer narrow enough to
// wrap every line, each landing is still the logical one.
func TestChatVimPlacesTheCursorAcrossWrappedLines(t *testing.T) {
	long := strings.Repeat("word ", 9) + "end"
	text := long + "\n" + long + "\n\n" + long
	h := newVimHarness(t, text, 12)
	h.vim.state = vimNormal
	vimPlace(&h.ta, vimPos{0, 0})

	h.keys("G")
	h.want(text, 3, 0)
	h.keys("$")
	h.want(text, 3, len(long)-1)
	h.keys("k")
	h.want(text, 2, 0)
	h.keys("k")
	h.want(text, 1, len(long)-1)
	h.keys("gg")
	h.want(text, 0, 0)
	h.keys("jjj")
	h.want(text, 3, 0)
	h.keys("kdd")
	h.want(long+"\n"+long+"\n"+long, 2, 0)
	h.keys("u")
	h.want(text, 2, 0)
	for row := range 4 {
		for _, col := range []int{0, 7, len(long) - 1} {
			if row == 2 {
				col = 0
			}
			vimPlace(&h.ta, vimPos{row, col})
			h.want(text, row, col)
		}
	}
}

// TestChatVimUndoIsBounded: the stack keeps the newest steps.
func TestChatVimUndoIsBounded(t *testing.T) {
	text := strings.Repeat("x", vimUndoDepth+20)
	h := normalAt(t, text, 0, 0)
	for range vimUndoDepth + 10 {
		h.keys("x")
	}
	for range vimUndoDepth + 10 {
		h.keys("u")
	}
	if got := len(h.ta.Value()); got != vimUndoDepth+10 {
		t.Errorf("after undoing everything kept the text is %d long, want %d", got, vimUndoDepth+10)
	}
}
