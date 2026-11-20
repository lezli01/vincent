package tui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

// TestChatSettingsPickerMarksBothCurrentValues: alt+p's list shows what the
// chat runs at now for each field, the effort included, and opens with the
// cursor on the current model (review F4). It had matched the effort rows
// against nothing, so the chat's effort was never shown.
func TestChatSettingsPickerMarksBothCurrentValues(t *testing.T) {
	a := apiclient.Agent{
		Name:    "claude",
		Models:  []apiclient.AgentOption{{Value: "haiku"}, {Value: "opus"}},
		Efforts: []apiclient.AgentOption{{Value: "low"}, {Value: "high"}},
	}
	current := func(p *chatSettingsPicker) []string {
		var out []string
		for _, r := range p.rows {
			if r.current {
				out = append(out, r.field+"="+r.value)
			}
		}
		return out
	}

	p := newChatSettingsPicker(a, &apiclient.Chat{Model: "opus", Effort: "high"})
	if got := strings.Join(current(p), ","); got != "model=opus,effort=high" {
		t.Errorf("current rows = %s, want model=opus,effort=high", got)
	}
	if r := p.rows[p.cursor]; r.field != "model" || r.value != "opus" {
		t.Errorf("cursor on %s=%s, want the current model", r.field, r.value)
	}
	text := ansi.Strip(strings.Join(p.render(80), "\n"))
	for _, want := range []string{"• effort: high", "• model: opus"} {
		if !strings.Contains(text, want) {
			t.Errorf("picker does not mark %q:\n%s", want, text)
		}
	}

	// A chat on the adapter's default model, at a chosen effort: the
	// default model row and the effort are both marked.
	p = newChatSettingsPicker(a, &apiclient.Chat{Effort: "low"})
	if got := strings.Join(current(p), ","); got != "model=,effort=low" {
		t.Errorf("current rows = %s, want model=,effort=low", got)
	}
	if r := p.rows[p.cursor]; r.field != "model" || r.value != "" {
		t.Errorf("cursor on %s=%s, want the default model row", r.field, r.value)
	}
}
