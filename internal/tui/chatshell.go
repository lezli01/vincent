package tui

import (
	"context"
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
)

// Shell passthrough in the chat workspace (task 170).
//
// The `shell_command` operation opens a one-line prompt over the composer;
// enter hands the line to POST /v1/chats/{id}/shell, which runs it in the
// chat's directory. Every run the daemon holds is drawn as a card in the
// conversation, after the turn that was the newest when it started, and the
// conversation's own cursor is how a card is picked: enter folds or unfolds
// its output the way it does a tool call's, and `a` marks a finished run for
// the next prompt.
//
// The composer itself is never read for a leading `!`: a message that starts
// with one is sent as typed (task 124 decision 9). The prompt is a separate
// layer precisely so that the composer's text stays uninterpreted.
//
// Nothing here is the client's own state but the prompt's buffer and which
// cards are unfolded: the runs, their output and their marks are re-read from
// the daemon on every chat.shell_changed event.

// chatShellFold is how many output lines a folded card draws.
const chatShellFold = 4

// chatShellChangedEvent is the durable event the daemon publishes whenever a
// run starts, ends or is marked.
const chatShellChangedEvent = "chat.shell_changed"

// chatShellState is the view's half of the feature.
type chatShellState struct {
	// prompt is the one-line prompt on screen, and buf what is typed in it.
	prompt bool
	buf    lineEdit
	// recall is how far back ↑ has walked the commands already run (#1078),
	// 0 for none; draft is what was typed before the walk began.
	recall int
	draft  string
	// busy is a start in flight, so a second enter does not run it twice.
	busy bool
	// runs is GET /v1/chats/{id}/shell, oldest first.
	runs []apiclient.ChatShellRun
	// out is each ended run's output, fetched once; fetched marks a fetch
	// issued, so a run whose output came back empty is not asked again.
	out     map[int64]string
	fetched map[int64]bool
	// expanded is the cards drawn whole rather than folded.
	expanded map[int64]bool
}

// Shell messages.
type (
	chatShellRunsMsg struct {
		chatID int64
		runs   []apiclient.ChatShellRun
		err    error
	}
	chatShellStartedMsg struct {
		chatID int64
		run    *apiclient.ChatShellRun
		err    error
	}
	chatShellOutputMsg struct {
		chatID int64
		runID  int64
		out    string
		err    error
	}
	chatShellAttachedMsg struct {
		chatID int64
		run    *apiclient.ChatShellRun
		err    error
	}
)

// resetShell forgets the previous chat's runs; the open's own fetch refills.
func (v *chatView) resetShell() {
	v.shell = chatShellState{}
}

// shellRunsCmd re-reads the chat's runs.
func (v *chatView) shellRunsCmd() tea.Cmd {
	client, id := v.client, v.chatID
	if client == nil || id == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		runs, err := client.ChatShellRuns(ctx, id)
		return chatShellRunsMsg{chatID: id, runs: runs, err: err}
	}
}

// applyShellRuns adopts a listing and fetches the output of every run that
// has ended since the last one. A failed listing keeps the cards as they
// were: a daemon that could not answer is not one that has no runs.
func (v *chatView) applyShellRuns(msg chatShellRunsMsg) tea.Cmd {
	if msg.chatID != v.chatID || msg.err != nil {
		return nil
	}
	v.shell.runs = msg.runs
	v.bodyDirty, v.grew = true, true
	return v.fetchShellOutputs()
}

// fetchShellOutputs asks once for each ended run's output. A running run's
// is not asked for: it is still being written, and the card says running.
func (v *chatView) fetchShellOutputs() tea.Cmd {
	client, id := v.client, v.chatID
	if client == nil {
		return nil
	}
	var cmds []tea.Cmd
	for _, r := range v.shell.runs {
		if r.State == apiclient.ChatShellRunning || v.shell.fetched[r.ID] {
			continue
		}
		if v.shell.fetched == nil {
			v.shell.fetched = map[int64]bool{}
		}
		v.shell.fetched[r.ID] = true
		runID := r.ID
		cmds = append(cmds, func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
			defer cancel()
			out, err := client.ChatShellOutput(ctx, id, runID)
			return chatShellOutputMsg{chatID: id, runID: runID, out: out, err: err}
		})
	}
	return tea.Batch(cmds...)
}

func (v *chatView) applyShellOutput(msg chatShellOutputMsg) {
	if msg.chatID != v.chatID || msg.err != nil {
		return
	}
	if v.shell.out == nil {
		v.shell.out = map[int64]string{}
	}
	v.shell.out[msg.runID] = msg.out
	v.bodyDirty = true
}

// shellAvailable is where the prompt is offered: on a chat that is still
// alive, whatever its turn is doing. The daemon refuses a terminal chat with
// a 409; this says so without the round trip.
func (v *chatView) shellAvailable() bool {
	return v.chat != nil && !chatstate.Terminal(chatstate.State(v.chat.State))
}

// openShellPrompt raises the prompt, or says why it cannot.
func (v *chatView) openShellPrompt() {
	if v.chat == nil {
		return
	}
	if !v.shellAvailable() {
		v.note, v.noteBad = "this chat has ended — no command runs here", true
		return
	}
	v.hideInline("")
	v.hideFiles()
	v.shell.prompt = true
	v.shell.buf.Set("")
	v.shell.recall, v.shell.draft = 0, ""
}

// updateShellPromptKey is the prompt's keyboard. It owns every key, as the
// search line does: a printable one types, enter runs, esc closes.
func (v *chatView) updateShellPromptKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		v.shell.prompt = false
		v.shell.buf.Set("")
		return nil
	case "enter":
		command := strings.TrimSpace(v.shell.buf.String())
		if command == "" || v.shell.busy {
			return nil
		}
		v.shell.prompt = false
		v.shell.buf.Set("")
		return v.runShellCmd(command)
	case "up":
		v.recallShell(1)
		return nil
	case "down":
		v.recallShell(-1)
		return nil
	case "ctrl+c":
		return nil
	}
	v.shell.buf.Key(msg)
	return nil
}

// recallShell walks the commands this chat already ran, newest first (↑),
// and back towards what was being typed (↓) (#1078). Repeats of the same
// command in a row are one step.
func (v *chatView) recallShell(delta int) {
	var past []string
	for i := len(v.shell.runs) - 1; i >= 0; i-- {
		c := v.shell.runs[i].Command
		if c != "" && (len(past) == 0 || past[len(past)-1] != c) {
			past = append(past, c)
		}
	}
	at := min(max(v.shell.recall+delta, 0), len(past))
	if at == v.shell.recall {
		return
	}
	if v.shell.recall == 0 {
		v.shell.draft = v.shell.buf.String()
	}
	v.shell.recall = at
	if at == 0 {
		v.shell.buf.Set(v.shell.draft)
		return
	}
	v.shell.buf.Set(past[at-1])
}

// pasteShell is a paste while the prompt is up: one line of it, because the
// prompt is one line and a newline would otherwise be a second command the
// human never saw.
//
// The prompt is one line drawn raw, so line breaks and tabs become spaces
// and every other control character and escape sequence is dropped (review
// F9): a pasted ESC would otherwise reach the terminal and throw off the
// width the prompt is truncated to.
func (v *chatView) pasteShell(text string) {
	text = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(text)
	v.shell.buf.Insert(strings.ReplaceAll(sanitizeText(text), "\t", " "))
}

func (v *chatView) runShellCmd(command string) tea.Cmd {
	client, id := v.client, v.chatID
	if client == nil || id == 0 {
		return nil
	}
	v.shell.busy = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		run, err := client.RunChatShell(ctx, id, command)
		return chatShellStartedMsg{chatID: id, run: run, err: err}
	}
}

// applyShellStarted shows the new card at once — the event that follows
// re-reads the list anyway — or the daemon's refusal as the note.
func (v *chatView) applyShellStarted(msg chatShellStartedMsg) tea.Cmd {
	if msg.chatID != v.chatID {
		return nil
	}
	v.shell.busy = false
	if msg.err != nil {
		v.note, v.noteBad = "shell: "+errString(msg.err), true
		return nil
	}
	if msg.run != nil {
		v.upsertShellRun(*msg.run)
	}
	v.follow() // the card the human just asked for is the thing to show
	return v.fetchShellOutputs()
}

func (v *chatView) upsertShellRun(run apiclient.ChatShellRun) {
	for i := range v.shell.runs {
		if v.shell.runs[i].ID == run.ID {
			v.shell.runs[i] = run
			v.bodyDirty = true
			return
		}
	}
	v.shell.runs = append(v.shell.runs, run)
	v.bodyDirty, v.grew = true, true
}

func (v *chatView) shellRun(id int64) (apiclient.ChatShellRun, bool) {
	for _, r := range v.shell.runs {
		if r.ID == id {
			return r, true
		}
	}
	return apiclient.ChatShellRun{}, false
}

// toggleShellAttachAt marks or unmarks the run under body line y for the
// next prompt. Only a finished run no turn has taken can be marked: a
// running one has no output to send yet, and a taken one has been sent.
func (v *chatView) toggleShellAttachAt(y int) tea.Cmd {
	a := anchorAt(v.anchors, y)
	run, ok := v.shellRun(a.shell)
	if !ok {
		v.note, v.noteBad = "move the cursor onto a shell command's card to attach it", false
		return nil
	}
	switch {
	case run.AttachedTurn != nil:
		v.note, v.noteBad = fmt.Sprintf("already sent with turn %d", *run.AttachedTurn), false
		return nil
	case run.State == apiclient.ChatShellRunning:
		v.note, v.noteBad = "the command is still running — attach it once it ends", false
		return nil
	}
	client, id, attach := v.client, v.chatID, !run.Attach
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		got, err := client.AttachChatShell(ctx, id, run.ID, attach)
		return chatShellAttachedMsg{chatID: id, run: got, err: err}
	}
}

func (v *chatView) applyShellAttached(msg chatShellAttachedMsg) {
	if msg.chatID != v.chatID {
		return
	}
	if msg.err != nil {
		v.note, v.noteBad = "shell: "+errString(msg.err), true
		return
	}
	if msg.run != nil {
		v.upsertShellRun(*msg.run)
		v.rebuildBody()
	}
}

// toggleShellAt folds or unfolds the card under body line y, reporting
// whether the line was a card's.
func (v *chatView) toggleShellAt(y int) bool {
	id := anchorAt(v.anchors, y).shell
	if id == 0 {
		return false
	}
	if v.shell.expanded == nil {
		v.shell.expanded = map[int64]bool{}
	}
	if v.shell.expanded[id] {
		delete(v.shell.expanded, id)
	} else {
		v.shell.expanded[id] = true
	}
	v.rebuildBody()
	return true
}

// shellPlacement groups the runs by the turn they are drawn after: the
// newest loaded turn whose seq is at most the run's AfterTurn, or 0 — before
// the first turn — when there is none. Within a group the daemon's order,
// oldest first, is kept, which is started order.
func (v *chatView) shellPlacement() map[int][]apiclient.ChatShellRun {
	if len(v.shell.runs) == 0 {
		return nil
	}
	at := make(map[int][]apiclient.ChatShellRun, len(v.shell.runs))
	for _, r := range v.shell.runs {
		place := 0
		for i := range v.turns {
			if v.turns[i].Seq <= r.AfterTurn {
				place = v.turns[i].Seq
			}
		}
		at[place] = append(at[place], r)
	}
	return at
}

// shellCardLines draws one run: the command, its state, and its output,
// folded past chatShellFold lines unless the card is expanded.
func (v *chatView) shellCardLines(r apiclient.ChatShellRun, width int) []string {
	w := max(width-4, 1)
	lines := []string{" " + ansi.Truncate(styleKey.Render("$ ")+oneLine(r.Command), max(width-1, 1), "…")}
	status := chatShellStatus(r)
	if note := chatShellMark(r); note != "" {
		status += styleDim.Render(" · " + note)
	}
	lines = append(lines, "   "+status)
	if r.State == apiclient.ChatShellRunning {
		return lines
	}
	raw, got := v.shell.out[r.ID]
	out := strings.TrimRight(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	if out == "" {
		if got {
			lines = append(lines, "   "+styleDim.Render("(no output)"))
		}
		return lines
	}
	rows := strings.Split(out, "\n")
	shown := rows
	folded := !v.shell.expanded[r.ID] && len(rows) > chatShellFold
	if folded {
		shown = rows[:chatShellFold]
	}
	for _, l := range shown {
		l = strings.ReplaceAll(shellOutputLine(l), "\t", "    ")
		lines = append(lines, "   "+styleDim.Render("│ ")+ansi.Truncate(l, w-2, "…"))
	}
	if folded {
		hint := "… " + plural(len(rows)-chatShellFold, "more line", "more lines")
		if v.convFocus {
			hint += " — enter expands"
		}
		lines = append(lines, "   "+styleDim.Render(hint))
	}
	if r.Truncated {
		lines = append(lines, "   "+styleWarn.Render("output truncated — the daemon kept only the first part"))
	}
	return lines
}

// shellOutputLine is one line of a run's output as a terminal would have
// left it: only the text after its last bare carriage return (progress
// meters redraw that way), with every escape sequence and control
// character removed, as everything else from an agent or a command is.
func shellOutputLine(l string) string {
	l = strings.TrimRight(l, "\r")
	if i := strings.LastIndexByte(l, '\r'); i >= 0 {
		l = l[i+1:]
	}
	return sanitizeText(l)
}

// chatShellStatus is the run's state, or its exit code once it exited.
func chatShellStatus(r apiclient.ChatShellRun) string {
	switch r.State {
	case apiclient.ChatShellRunning:
		return styleDim.Render("running…")
	case apiclient.ChatShellExited:
		if r.ExitCode != nil {
			if *r.ExitCode == 0 {
				return styleDim.Render("exit 0")
			}
			return styleBad.Render(fmt.Sprintf("exit %d", *r.ExitCode))
		}
		return styleDim.Render("exited")
	case apiclient.ChatShellTimedOut:
		return styleBad.Render("timed out")
	case apiclient.ChatShellFailed:
		return styleBad.Render("failed to start")
	}
	return styleWarn.Render(strings.ReplaceAll(r.State, "_", " "))
}

// chatShellMark is what the card says about sending the run to the agent.
func chatShellMark(r apiclient.ChatShellRun) string {
	switch {
	case r.AttachedTurn != nil:
		return fmt.Sprintf("sent with turn %d", *r.AttachedTurn)
	case r.Attach:
		return "attached to next prompt"
	}
	return ""
}

// shellPromptLine is the prompt as drawn over the composer.
func (v *chatView) shellPromptLine(width int) string {
	return lineField(styleKey.Render("$ "), &v.shell.buf, "  enter run · esc cancel", width)
}
