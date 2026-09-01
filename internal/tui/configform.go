package tui

import (
	"context"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The daemon view's config editor (task 060).
//
// It is a takeover rather than a floating popup: while it is open it owns the
// keyboard, which is what `capturesInput` on the view reports, and the daemon
// view renders it in place of its blocks. That is the property that matters —
// the view's single-key globals (R, f, j/k) would otherwise fire into the text
// field on the first keystroke.
//
// Four keys are gated behind an explicit confirmation: notify.command,
// environment.*, agents.*.path and listen. They are the keys that decide what
// the daemon executes or exposes, and agents already run full-auto by default
// (§16) — a stray keystroke must not change the argv the daemon spawns as you.

// configSavedMsg is the answer to a PATCH /v1/config. cfg is the
// configuration in force afterwards, which is not always what was written:
// `listen` is pinned until restart.
type configSavedMsg struct {
	path string
	cfg  apiclient.Config
	err  error
}

// configForm edits one key.
type configForm struct {
	key configKey
	// choice indexes key.choices for kindBool and kindEnum; input holds the
	// text for every other kind.
	choice int
	input  textField
	// confirming is the second step a dangerous key needs. It is entered on
	// save and left by y (apply) or esc (back to the field).
	confirming bool
	saving     bool
	// err is the daemon's refusal, rendered against the field. It survives
	// until the next save so the value that caused it is still on screen.
	err string
}

// newConfigForm opens the editor on one key, seeded with the value in force.
func newConfigForm(k configKey, cur apiclient.Config) *configForm {
	f := &configForm{key: k}
	value := k.read(cur)
	switch k.kind {
	case kindBool, kindEnum:
		f.choice = indexOf(k.choices, value)
	default:
		in := newTextField()
		in.SetPrompt("")
		in.SetValue(value)
		in.Focus()
		// The cursor lands at the end, on the value someone is about to edit,
		// rather than at column zero in front of it.
		in.CursorEnd()
		f.input = in
	}
	return f
}

// value is what the form would submit.
func (f *configForm) value() string {
	switch f.key.kind {
	case kindBool, kindEnum:
		if f.choice >= 0 && f.choice < len(f.key.choices) {
			return f.key.choices[f.choice]
		}
		return ""
	default:
		return f.input.Value()
	}
}

// update routes a keypress. It returns a command when the form asks the
// daemon for something, and reports whether the form is done with itself.
func (f *configForm) update(msg tea.KeyPressMsg, client *apiclient.Client) (cmd tea.Cmd, done bool) {
	if msg.String() == "esc" {
		if f.confirming {
			// Back to the field, not out of the form: someone who reads the
			// confirmation and changes their mind about the *value* should
			// not have to reopen the editor.
			f.confirming = false
			return nil, false
		}
		return nil, true
	}
	if f.confirming {
		switch msg.String() {
		case "y", "Y":
			f.confirming = false
			return f.save(client), false
		}
		return nil, false
	}
	switch f.key.kind {
	case kindBool, kindEnum:
		switch msg.String() {
		case "left", "h", "up", "k":
			f.choice = wrapIndex(f.choice-1, len(f.key.choices))
			return nil, false
		case "right", "l", "down", "j", " ", "space", "tab":
			f.choice = wrapIndex(f.choice+1, len(f.key.choices))
			return nil, false
		}
	}
	switch msg.String() {
	case "enter", "ctrl+s":
		if f.key.dangerous {
			f.confirming = true
			return nil, false
		}
		return f.save(client), false
	}
	if f.key.kind != kindBool && f.key.kind != kindEnum {
		var cmd tea.Cmd
		f.input, cmd = f.input.Update(msg)
		return cmd, false
	}
	return nil, false
}

// save builds the patch and sends it. A value this side can already tell is
// wrong is refused here, without a round trip; everything else is the
// daemon's to reject, and its message is what renders.
func (f *configForm) save(client *apiclient.Client) tea.Cmd {
	patch, err := f.key.write(f.value())
	if err != nil {
		f.err = err.Error()
		return nil
	}
	if client == nil {
		f.err = "the daemon is not reachable"
		return nil
	}
	f.err = ""
	f.saving = true
	path := f.key.path
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		cfg, err := client.PatchConfig(ctx, patch)
		return configSavedMsg{path: path, cfg: cfg, err: err}
	}
}

// render draws the editor. It is deliberately plain: one key, one field, the
// default beside it, and the line that says what happens when it applies. The
// width is the caller's business: every line here is short by construction, so
// nothing wraps and nothing has to be elided.
func (f *configForm) render(width int) []string {
	out := []string{
		" " + styleTitle.Render("edit "+f.key.path),
		"",
	}
	if f.key.help != "" {
		out = append(out, "   "+styleDim.Render(f.key.help))
	}
	out = append(out, "   "+styleDim.Render("default ")+f.key.def())
	out = append(out, "")
	switch f.key.kind {
	case kindBool, kindEnum:
		out = append(out, "   "+f.renderChoices())
	default:
		f.input.SetWidth(max(width-3, 10))
		out = append(out, fieldRows("   ", f.input)...)
		if len(f.key.choices) > 0 {
			out = append(out, "   "+styleDim.Render("accepted: "+strings.Join(f.key.choices, " ")))
		}
	}
	out = append(out, "")
	if f.key.restart {
		// Said before it is applied, not after: `listen` is written to the
		// file and the running daemon keeps the address it bound, so GET
		// /v1/config will go on reporting the old one until a restart.
		out = append(out, "   "+styleWarn.Render(
			"takes effect on the next daemon restart; the running daemon keeps its address"))
	}
	if f.err != "" {
		out = append(out, "   "+styleBad.Render("⚠ "+f.err))
	}
	out = append(out, "")
	switch {
	case f.confirming:
		out = append(out, "   "+styleWarn.Render("this changes what the daemon executes or exposes."),
			"   "+styleWarn.Render("set "+f.key.path+" to ")+f.displayValue()+
				styleWarn.Render("?  y confirm   esc back"))
	case f.saving:
		out = append(out, "   "+styleDim.Render("saving…"))
	default:
		out = append(out, "   "+styleDim.Render(f.keyLine()))
	}
	return out
}

// displayValue renders an empty value as something a reader can see: a
// confirmation that reads "set agents.claude.path to " and stops is not one.
func (f *configForm) displayValue() string {
	v := f.value()
	if strings.TrimSpace(v) == "" {
		return styleDim.Render("(empty)")
	}
	return v
}

func (f *configForm) keyLine() string {
	if f.key.kind == kindBool || f.key.kind == kindEnum {
		return "←/→ choose   enter apply   esc cancel"
	}
	return "enter apply   esc cancel"
}

func (f *configForm) renderChoices() string {
	parts := make([]string, 0, len(f.key.choices))
	for i, c := range f.key.choices {
		if i == f.choice {
			parts = append(parts, styleFocus.Render("["+c+"]"))
			continue
		}
		parts = append(parts, styleDim.Render(" "+c+" "))
	}
	return strings.Join(parts, " ")
}

func indexOf(items []string, want string) int {
	for i, s := range items {
		if s == want {
			return i
		}
	}
	return 0
}

func wrapIndex(i, n int) int {
	if n == 0 {
		return 0
	}
	return ((i % n) + n) % n
}
