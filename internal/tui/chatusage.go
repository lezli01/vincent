package tui

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/keymap"
)

// A chat's usage on screen (task 157): what each turn spent in its heading,
// how full the agent's context is on the conversation pane's edge, and the
// conversation's totals over the conversation when the details pane is not
// there to carry them. Every figure is the daemon's — a field the adapter did
// not report draws nothing, and nothing here is estimated.

// chatGaugeHot is the share of the context window past which the gauge
// warns, and offers compaction where the agent has a command for it.
const chatGaugeHot = 80

// chatGaugeCells is how many cells the gauge's bar has.
const chatGaugeCells = 5

// chatTurnUsage is a finished turn's spend, in the heading's order: vincent's
// own wall clock, the tokens it processed and produced, and its cost. A part
// the adapter did not report is left out, and a running turn has none yet.
func chatTurnUsage(t apiclient.ChatTurn) []string {
	if t.State == "running" || t.EndedAt == nil {
		return nil
	}
	var parts []string
	if t.DurationMS != nil {
		parts = append(parts, formatAgentDuration(*t.DurationMS))
	}
	in, out := chatTurnInput(t), t.OutputTokens
	switch {
	case in > 0 && out > 0:
		parts = append(parts, chatTokens(in)+"↑ "+chatTokens(out)+"↓")
	case in > 0:
		parts = append(parts, chatTokens(in)+"↑")
	case out > 0:
		parts = append(parts, chatTokens(out)+"↓")
	}
	// Only a reported cost: copilot spends premium requests, never dollars,
	// and a `$0.00` there would be a claim it never made (§9.9).
	if t.CostUSD != nil {
		parts = append(parts, fmt.Sprintf("$%.2f", *t.CostUSD))
	}
	return parts
}

// chatTurnInput is what a turn processed: its input and the cache it read
// and wrote, which claude's and copilot's input leaves out (task 157
// decision 3). An adapter whose input already counts the cache records no
// cache counts, so nothing is counted twice.
func chatTurnInput(t apiclient.ChatTurn) int64 {
	n := t.InputTokens
	if t.CacheReadTokens != nil {
		n += *t.CacheReadTokens
	}
	if t.CacheCreationTokens != nil {
		n += *t.CacheCreationTokens
	}
	return n
}

// chatContext is the latest context figure a conversation has: the prompt
// size of the last turn that reported one, and its window (0 when the run did
// not report one). ok is false when no turn reported a context size.
//
// A compact turn — one whose prompt is the agent's compact command, compact
// — that reported no size ends the walk with ok false: claude reports none
// for a local command, and every figure before it measured a context the
// compaction has just discarded, so the gauge stays empty until a turn
// reports the new size rather than repeat the old one and invite another
// compact (review F2).
func chatContext(turns []apiclient.ChatTurn, compact string) (tokens, window int64, ok bool) {
	for i := len(turns) - 1; i >= 0; i-- {
		t := turns[i]
		if t.ContextTokens == nil {
			if isCompactPrompt(t.Prompt, compact) {
				return 0, 0, false
			}
			continue
		}
		if t.ContextWindow != nil {
			window = *t.ContextWindow
		}
		return *t.ContextTokens, window, true
	}
	return 0, 0, false
}

// isCompactPrompt reports whether prompt is the compact command, alone or
// with the instructions claude's /compact takes after it.
func isCompactPrompt(prompt, compact string) bool {
	if compact == "" {
		return false
	}
	rest, found := strings.CutPrefix(strings.TrimSpace(prompt), compact)
	return found && (rest == "" || rest[0] == ' ' || rest[0] == '\t' || rest[0] == '\n')
}

// chatGauge is the context gauge for a conversation: `ctx ▰▰▰▱▱ 61%` wide,
// `ctx 61%` narrow, and `ctx 122k` when the run reported no window. "" when no
// turn reported a context size. hot is past chatGaugeHot.
//
// It never relies on colour alone (§15 Colour): past the threshold it turns
// yellow *and* carries a `!`, so the warning survives NO_COLOR and a 16-colour
// terminal. compactCmd is the agent's compact command (see chatContext), and
// compact the key that sends it, named in the warning only where the agent
// can compact ("" otherwise).
func chatGauge(turns []apiclient.ChatTurn, wide bool, compactCmd, compact string) (text string, hot bool) {
	tokens, window, ok := chatContext(turns, compactCmd)
	if !ok {
		return "", false
	}
	if window <= 0 {
		return styleDim.Render("ctx " + chatTokens(tokens)), false
	}
	pct := int(tokens * 100 / window)
	hot = pct > chatGaugeHot
	label := "ctx "
	if wide {
		filled := min(max((pct*chatGaugeCells+50)/100, 0), chatGaugeCells)
		label += ic(strings.Repeat("▰", filled)+strings.Repeat("▱", chatGaugeCells-filled)) + " "
	}
	label += strconv.Itoa(pct) + "%"
	if !hot {
		return styleDim.Render(label), false
	}
	label = ic("! ") + label
	if compact != "" && wide {
		label += " · " + compact + " compact"
	}
	return styleWarn.Bold(true).Render(label), true
}

// chatTotalsLine is the conversation's totals, the figures the details pane
// shows, for a terminal too narrow to show that pane (task 157). "" when no
// turn reported tokens or a cost.
func chatTotalsLine(turns []apiclient.ChatTurn) string {
	tokens, cost, priced := chatUsage(turns)
	var parts []string
	if tokens > 0 {
		parts = append(parts, chatTokens(tokens)+" tokens")
	}
	if priced {
		parts = append(parts, fmt.Sprintf("$%.2f", cost))
	}
	if len(parts) == 0 {
		return ""
	}
	return styleDim.Render(ic("Σ ") + strings.Join(parts, "  ·  "))
}

// compactCommand is the chat's agent's compact command, "" when it has none
// or the agent list has not answered.
func (v *chatView) compactCommand() string {
	if v.chat == nil {
		return ""
	}
	return v.compactCmds[v.chat.Agent]
}

// gaugeCompactKey is the key the gauge's warning names: the compact key, on
// an agent that can compact.
func (v *chatView) gaugeCompactKey() string {
	if v.compactCommand() == "" {
		return ""
	}
	return opKey(keymap.Compact)
}

// askCompact puts the compact confirmation up, or says why it cannot.
func (v *chatView) askCompact() {
	chat := v.chat
	switch {
	case chat == nil:
		return
	case v.compactCommand() == "":
		v.note, v.noteBad = chat.Agent+" has no compact command", true
	case chatstate.Terminal(chatstate.State(chat.State)):
		v.note, v.noteBad = sendRefusal(chatstate.State(chat.State)), true
	case chat.State != string(chatstate.Idle):
		// A live chat would queue an ordinary message (task 156), but a
		// compact belongs to the conversation as it stands now, so it waits
		// for the agent rather than queueing behind it.
		v.note, v.noteBad = "compact once the agent is idle", true
	default:
		v.compacting = true
		v.note = ""
		v.hideInline("")
		v.hideFiles()
	}
}

// compactQuestion is the compact confirmation's text.
func (v *chatView) compactQuestion() string {
	return fmt.Sprintf("send %s to %s? it compacts the conversation's context", v.compactCommand(), v.chat.Agent)
}

// chatAgentsMsg carries each adapter's compact command, by name.
// It also carries every adapter's row, by name, for the settings picker
// (task 162).
type chatAgentsMsg struct {
	compact map[string]string
	agents  map[string]apiclient.Agent
}

// loadAgentsCmd reads GET /v1/agents for the compact commands. A failed read
// leaves the map as it was: no command offered is the safe answer.
func (v *chatView) loadAgentsCmd() tea.Cmd {
	client := v.client
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		agents, err := client.ListAgents(ctx, false)
		if err != nil {
			return nil
		}
		out := make(map[string]string, len(agents))
		rows := make(map[string]apiclient.Agent, len(agents))
		for _, a := range agents {
			rows[a.Name] = a
			if a.CompactCommand != nil && *a.CompactCommand != "" {
				out[a.Name] = *a.CompactCommand
			}
		}
		return chatAgentsMsg{compact: out, agents: rows}
	}
}
