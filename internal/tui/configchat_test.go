package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// TestConfigEditorWritesTheChatKeys is task 170's two settings through the
// real handlers: the status line is typed, vim is chosen, and both reach
// config.yaml and the view's own copy of the configuration.
func TestConfigEditorWritesTheChatKeys(t *testing.T) {
	h := newConfigLive(t)
	const tmpl = "{{.Model}} · {{.ContextPercent}}%"

	h.open(t, "tui.chat.status_line")
	h.view.form.input.SetValue(tmpl)
	h.press(t, "enter")
	if h.view.form != nil {
		t.Fatalf("the editor stayed open after saving the status line: %+v", h.view.form)
	}
	if got := h.view.config.TUI.Chat.StatusLine; got != tmpl {
		t.Errorf("the view holds status line %q, want %q", got, tmpl)
	}

	h.open(t, "tui.chat.vim")
	for h.view.form.value() != "true" {
		h.press(t, "right")
	}
	h.press(t, "enter")
	if !h.view.config.TUI.Chat.Vim {
		t.Error("the view did not adopt tui.chat.vim")
	}
	file := h.file(t)
	if !strings.Contains(file, "vim: true") || !strings.Contains(file, "status_line: ") {
		t.Errorf("config.yaml does not carry tui.chat:\n%s", file)
	}
	if h.view.config.TUI.Chat.StatusLine != tmpl {
		t.Error("saving vim dropped the status line")
	}
}

// TestConfigEditorShowsAChatStatusLineRefusal: a template naming a field no
// chat has comes back as the daemon's sentence against the field, and nothing
// is saved.
func TestConfigEditorShowsAChatStatusLineRefusal(t *testing.T) {
	h := newConfigLive(t)
	h.open(t, "tui.chat.status_line")
	h.view.form.input.SetValue("{{.Tokens}}")
	h.press(t, "enter")
	if h.view.config.TUI.Chat.StatusLine != "" {
		t.Errorf("a refused template was adopted: %q", h.view.config.TUI.Chat.StatusLine)
	}
	if strings.Contains(h.file(t), "Tokens") {
		t.Error("a refused template reached config.yaml")
	}
}

// TestSettingsShowsTheChatKeys: both rows are on the Interface page under
// their own labels.
func TestSettingsShowsTheChatKeys(t *testing.T) {
	v := settingsFixture(t)
	for _, path := range []string{"tui.chat.status_line", "tui.chat.vim"} {
		mustSetting(t, v, path)
	}
	v.openKey("tui.chat.vim")
	screen := ansi.Strip(v.render(180, 44))
	for _, want := range []string{"Status line", "Vim keys"} {
		if !strings.Contains(screen, want) {
			t.Errorf("the settings screen does not show %q:\n%s", want, screen)
		}
	}
}
