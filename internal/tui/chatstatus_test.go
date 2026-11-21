package tui

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
)

func statusFixture() (*apiclient.Chat, []apiclient.ChatTurn) {
	ptr := func(n int64) *int64 { return &n }
	cost := func(f float64) *float64 { return &f }
	chat := &apiclient.Chat{
		ID: 7, Agent: "claude", Model: "opus", Effort: "high", PermissionMode: "restricted",
		Branch: "vincent/chat-7-polish", State: "idle",
	}
	turns := []apiclient.ChatTurn{
		{Seq: 1, State: "done", CostUSD: cost(0.25), ContextTokens: ptr(40_000), ContextWindow: ptr(200_000)},
		{Seq: 2, State: "done", CostUSD: cost(0.17), ContextTokens: ptr(122_000), ContextWindow: ptr(200_000)},
		{Seq: 3, State: "running"},
	}
	return chat, turns
}

// TestChatStatusLineRendersEveryField: each field the config reference names
// reaches the line, with the header's own figures behind the two computed
// ones.
func TestChatStatusLineRendersEveryField(t *testing.T) {
	chat, turns := statusFixture()
	for tmpl, want := range map[string]string{
		"{{.Model}}":          "opus",
		"{{.Effort}}":         "high",
		"{{.Agent}}":          "claude",
		"{{.PermissionMode}}": "restricted",
		"{{.Branch}}":         "vincent/chat-7-polish",
		"{{.State}}":          "idle",
		"{{.Cost}}":           "$0.42",
		"{{.ContextPercent}}": "61",
		"{{.Turns}}":          "3",
		"{{.Model}} · {{.ContextPercent}}% · {{.Cost}}":         "opus · 61% · $0.42",
		"{{if gt .ContextPercent 50}}!{{end}} {{.Turns}} turns": "! 3 turns",
	} {
		if got := chatStatusLine(tmpl, chat, turns); got != want {
			t.Errorf("chatStatusLine(%q) = %q, want %q", tmpl, got, want)
		}
	}
}

// TestChatStatusLineCoversTheConfigFields runs a template naming every field
// of config.ChatStatusFields, so a field added there cannot go unfilled here
// unnoticed: the fixture gives each a non-zero value.
func TestChatStatusLineCoversTheConfigFields(t *testing.T) {
	chat, turns := statusFixture()
	f := reflect.ValueOf(chatStatusFields(chat, turns))
	for i := range f.NumField() {
		if f.Field(i).IsZero() {
			t.Errorf("%s is not filled from the chat", f.Type().Field(i).Name)
		}
	}
	if err := config.ValidateChatStatusLine("{{.Model}}{{.Turns}}"); err != nil {
		t.Errorf("the daemon's validator refuses fields the renderer fills: %v", err)
	}
}

// TestChatStatusLineUnreportedFiguresAreEmpty: nothing is estimated. No cost
// reported is an empty cost, and no context figure is zero percent.
func TestChatStatusLineUnreportedFiguresAreEmpty(t *testing.T) {
	chat := &apiclient.Chat{Agent: "copilot", TurnCount: 4}
	got := chatStatusLine("[{{.Cost}}] {{.ContextPercent}} {{.Turns}}", chat, nil)
	if got != "[] 0 4" {
		t.Errorf("chatStatusLine = %q, want an empty cost, 0%% and the chat's own turn count", got)
	}
	// A context size with no window has no share to state.
	n := int64(5000)
	got = chatStatusLine("{{.ContextPercent}}", chat, []apiclient.ChatTurn{{ContextTokens: &n}})
	if got != "0" {
		t.Errorf("a size without a window rendered %q, want 0", got)
	}
}

// TestChatStatusLineFallsBackToNothing: an empty template, a chat that has
// not loaded and a template that cannot run all give "", which the header
// reads as "draw the gauge".
func TestChatStatusLineFallsBackToNothing(t *testing.T) {
	chat, turns := statusFixture()
	for name, got := range map[string]string{
		"empty template": chatStatusLine("", chat, turns),
		"nil chat":       chatStatusLine("{{.Model}}", nil, turns),
		"parse error":    chatStatusLine("{{.Model", chat, turns),
		"unknown field":  chatStatusLine("{{.Model}} {{.Tokens}}", chat, turns),
		"unknown func":   chatStatusLine("{{upper .Model}}", chat, turns),
	} {
		if got != "" {
			t.Errorf("%s: chatStatusLine = %q, want \"\"", name, got)
		}
	}
}

// TestChatStatusLineIsOnePlainLine: a branch name is agent-reachable text and
// a template can print a break of its own; neither a line break nor an
// escape sequence reaches the header.
func TestChatStatusLineIsOnePlainLine(t *testing.T) {
	chat, turns := statusFixture()
	chat.Branch = "evil\x1b[2J\x1b]0;title\x07\nbranch\ttab"
	chat.Model = "op\rus\x00"
	got := chatStatusLine(`{{.Branch}} {{printf "a\nb"}} {{.Model}}`, chat, turns)
	for _, r := range got {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			t.Fatalf("a control character %q survived: %q", r, got)
		}
	}
	if strings.Contains(got, "[2J") || strings.Contains(got, "title") {
		t.Errorf("an escape sequence's body survived as text: %q", got)
	}
	if got != "evil branch tab a b op us" {
		t.Errorf("chatStatusLine = %q, want the text on one line", got)
	}
}

// TestChatStatusPrefs: both preferences are off until a config installs them, and
// follow every config after that.
func TestChatStatusPrefs(t *testing.T) {
	t.Cleanup(func() { setChatPrefs("", false) })
	setChatPrefs("", false)
	if chatStatusTemplate() != "" || chatVimEnabled() {
		t.Error("the chat preferences are on without a config")
	}
	setChatPrefs("{{.Model}}", true)
	if chatStatusTemplate() != "{{.Model}}" || !chatVimEnabled() {
		t.Errorf("setChatPrefs did not install: %q, %v", chatStatusTemplate(), chatVimEnabled())
	}
	setChatPrefs("", false)
	if chatStatusTemplate() != "" || chatVimEnabled() {
		t.Error("setChatPrefs did not turn the preferences back off")
	}
}
