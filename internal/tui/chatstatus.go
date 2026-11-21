package tui

import (
	"fmt"
	"strings"
	"sync/atomic"
	"text/template"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/config"
)

// The chat view's two preferences from `tui.chat` (task 170), and the status
// line one of them draws.
//
// Both are held the way tui.icons is (theme.go): package-level atomics filled
// from every config the TUI reads, because the chat view is not the only
// place that learns of a config — a save in the settings view and a hot
// reload land at the root — and a preference read through a field would be
// one more thing each of those has to push.

// chatStatusTmpl is tui.chat.status_line; chatVimOn is tui.chat.vim.
var (
	chatStatusTmpl atomic.Pointer[string]
	chatVimOn      atomic.Bool
)

// setChatPrefs installs tui.chat. An older daemon's zero answer is no status
// line and no vim layer, which is the shipped chat.
func setChatPrefs(statusLine string, vim bool) {
	chatStatusTmpl.Store(&statusLine)
	chatVimOn.Store(vim)
}

// chatStatusTemplate is the status line template in force, "" for none.
func chatStatusTemplate() string {
	if p := chatStatusTmpl.Load(); p != nil {
		return *p
	}
	return ""
}

// chatVimEnabled reports whether the composer's modal layer is on.
func chatVimEnabled() bool { return chatVimOn.Load() }

// chatStatusLine executes a status line template over a chat and its turns
// and returns one line of plain text for the header: no style, no escape
// sequence, no line break — the caller styles and truncates it.
//
// "" is "draw the header as it ships": an empty template, a chat that has not
// loaded, and a template that fails. The daemon validated the template before
// serving it (config.ValidateChatStatusLine), so a failure here is a daemon
// and a client that disagree about the fields, and the honest fallback is the
// gauge the line would have replaced rather than an error in the header.
func chatStatusLine(tmpl string, chat *apiclient.Chat, turns []apiclient.ChatTurn) string {
	if tmpl == "" || chat == nil {
		return ""
	}
	t, err := template.New("status_line").Option("missingkey=error").Parse(tmpl)
	if err != nil {
		return ""
	}
	var b strings.Builder
	if err := t.Execute(&b, chatStatusFields(chat, turns)); err != nil {
		return ""
	}
	// The chat's own strings are agent- and human-supplied — a branch, a
	// model id — and a template can emit a break of its own with printf, so
	// the result is cleaned, not the inputs.
	return oneLine(b.String())
}

// chatStatusFields fills the template's fields from what the chat view
// already holds. The figures are the header's own: the context share is
// chatContext's, as the gauge draws it, and the cost is chatUsage's sum of
// what turns reported.
func chatStatusFields(chat *apiclient.Chat, turns []apiclient.ChatTurn) config.ChatStatusFields {
	f := config.ChatStatusFields{
		Model:          chat.Model,
		Effort:         chat.Effort,
		Agent:          chat.Agent,
		PermissionMode: chat.PermissionMode,
		Branch:         chat.Branch,
		State:          chat.State,
		// A chat read from a list carries its count and no turns; an open
		// one carries the turns, which are the fresher of the two.
		Turns: max(len(turns), chat.TurnCount),
	}
	// No compact command is passed: which prompt compacts is per agent and
	// the chat view's to know, so after a compaction the share is the last
	// one reported until the next turn reports the new size.
	if tokens, window, ok := chatContext(turns, ""); ok && window > 0 {
		f.ContextPercent = int(tokens * 100 / window)
	}
	// Only a reported cost, as the turn headings do: copilot spends premium
	// requests, never dollars, and `$0.00` would be a claim it never made.
	if _, cost, priced := chatUsage(turns); priced {
		f.Cost = fmt.Sprintf("$%.2f", cost)
	}
	return f
}
