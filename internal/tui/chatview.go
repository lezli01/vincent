package tui

import (
	"context"
	"errors"
	"strings"
	"time"

	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The chat workspace (§15, task 067, closing task 063.2): turn history above,
// a live tail of the running turn, a composer below.
//
// It is taskview.go's structural sibling and none of its code: a task
// workspace is a timeline of steps with a diff pane and a §6 action bar, and
// a chat has one turn at a time, no diff to review mid-conversation and three
// actions. What the two do share is the §7.4 answer popup, which is reused
// rather than forked — the request is the same request.

// Chat workspace messages.
type (
	// chatLoadedMsg is the chat and its whole conversation.
	chatLoadedMsg struct {
		id    int64
		chat  *apiclient.Chat
		turns []apiclient.ChatTurn
		err   error
	}
	// chatTranscriptMsg is one finished turn's rendered record, or the live
	// turn's scrollback plus the offset the live tail resumes at.
	chatTranscriptMsg struct {
		chatID  int64
		seq     int
		records []apiclient.TranscriptRecord
		// next is the X-Next-Offset the fetch reported. Chunks at or before
		// it are already in records; that is the whole seam (§13.3).
		next int64
		err  error
	}
	// chatSentMsg reports POST /v1/chats/{id}/send.
	chatSentMsg struct {
		chatID int64
		turn   *apiclient.ChatTurn
		err    error
	}
	// chatAnsweredMsg reports POST /v1/chats/{id}/answer.
	chatAnsweredMsg struct {
		chatID int64
		err    error
	}
	// chatCanceledMsg reports POST /v1/chats/{id}/cancel.
	chatCanceledMsg struct {
		chatID int64
		err    error
	}
)

// chatView is the workspace.
type chatView struct {
	client *apiclient.Client
	now    func() time.Time

	chatID int64
	chat   *apiclient.Chat
	turns  []apiclient.ChatTurn

	// scrollback is the running turn's transcript as fetched, and liveFrom is
	// the offset past which the stream's chunks are new. Together they are
	// the catch-up seam: fetch, then keep every chunk whose offset is past
	// liveFrom. Without it a reconnect double-prints the tail.
	scrollback []string
	liveFrom   int64
	liveTurn   int64

	composer textarea.Model
	form     *answerForm

	// stream is the per-chat SSE subscription's cancel, held so opening a
	// second chat does not leave the first one's stream running.
	streamStop context.CancelFunc

	note    string
	noteBad bool
	loadErr string

	width, height int
}

func newChatView() *chatView {
	ta := textarea.New()
	ta.Placeholder = "message… (enter sends, shift+enter for a newline)"
	ta.SetHeight(3)
	return &chatView{now: time.Now, composer: ta}
}

func (v *chatView) title() string {
	if v.chat != nil {
		return "Chat: " + v.chat.Title
	}
	return "Chat"
}

func (v *chatView) setClient(c *apiclient.Client) tea.Cmd {
	v.client = c
	if v.chatID == 0 {
		return nil
	}
	return v.open(v.chatID)
}

// capturesInput is true whenever the composer has the keyboard, which is
// almost always: this screen is a text field with a conversation above it, so
// the single-key global bindings must not fire while someone types "q".
func (v *chatView) capturesInput() bool {
	if v.form != nil {
		return v.form.capturing()
	}
	return v.composer.Focused()
}

func (v *chatView) paste(text string) tea.Cmd {
	if v.form != nil {
		return v.form.paste(text)
	}
	var cmd tea.Cmd
	v.composer, cmd = v.composer.Update(tea.PasteMsg{Content: text})
	return cmd
}

func (v *chatView) bindingContext() bindingContext {
	if v.form != nil {
		return ctxForm
	}
	return ctxChat
}

func (v *chatView) hintedProject() int64 {
	if v.chat != nil {
		return v.chat.ProjectID
	}
	return 0
}

// open points the workspace at a chat: it loads the conversation and
// subscribes to that chat's stream, dropping any previous subscription.
func (v *chatView) open(id int64) tea.Cmd {
	if v.streamStop != nil {
		v.streamStop()
		v.streamStop = nil
	}
	v.chatID = id
	v.chat, v.turns = nil, nil
	v.scrollback, v.liveFrom, v.liveTurn = nil, 0, 0
	v.note, v.loadErr = "", ""
	v.composer.SetValue("")
	v.composer.Focus()
	return tea.Batch(v.loadCmd(), v.streamCmd())
}

func (v *chatView) loadCmd() tea.Cmd {
	client, id := v.client, v.chatID
	if client == nil || id == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		chat, turns, err := client.GetChat(ctx, id)
		return chatLoadedMsg{id: id, chat: chat, turns: turns, err: err}
	}
}

// streamCmd subscribes to GET /v1/chats/{id}/events and republishes every
// note as a tea.Msg. It is the per-task subscription's twin: durable events
// re-render the header and the turn list, live chunks extend the tail.
func (v *chatView) streamCmd() tea.Cmd {
	client, id := v.client, v.chatID
	if client == nil || id == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	v.streamStop = cancel
	ch := client.StreamChat(ctx, id, apiclient.StreamOptions{})
	return func() tea.Msg {
		note, ok := <-ch
		if !ok {
			return nil
		}
		return chatNoteMsg{chatID: id, note: note, next: ch}
	}
}

// chatNoteMsg is one note off the per-chat stream, carrying the channel so
// the next read is scheduled without re-subscribing.
type chatNoteMsg struct {
	chatID int64
	note   apiclient.Note
	next   <-chan apiclient.Note
}

// readNext schedules the next read off an already-open subscription.
func readNextChatNote(id int64, ch <-chan apiclient.Note) tea.Cmd {
	return func() tea.Msg {
		note, ok := <-ch
		if !ok {
			return nil
		}
		return chatNoteMsg{chatID: id, note: note, next: ch}
	}
}

func (v *chatView) update(msg tea.Msg) (panel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width, v.height = msg.Width, msg.Height
		v.composer.SetWidth(max(msg.Width-4, 10))
		return v, nil
	case openChatMsg:
		return v, v.open(msg.id)
	case viewActivatedMsg:
		if msg.id == viewChat && v.chatID != 0 {
			return v, v.loadCmd()
		}
		return v, nil
	case chatLoadedMsg:
		v.applyLoaded(msg)
		return v, v.fetchLiveTranscript()
	case chatTranscriptMsg:
		v.applyTranscript(msg)
		return v, nil
	case chatNoteMsg:
		return v, v.applyChatNote(msg)
	case chatSentMsg:
		return v, v.applySent(msg)
	case chatAnsweredMsg:
		return v, v.applyAnswered(msg)
	case chatCanceledMsg:
		return v, v.applyCanceled(msg)
	case tea.KeyPressMsg:
		return v.updateKey(msg)
	}
	return v, nil
}

func (v *chatView) applyLoaded(msg chatLoadedMsg) {
	if msg.id != v.chatID {
		return
	}
	if msg.err != nil {
		v.loadErr = errString(msg.err)
		return
	}
	v.loadErr = ""
	v.chat, v.turns = msg.chat, msg.turns
	// The popup follows the chat's own state: it opens when the daemon says
	// awaiting_input and closes when it stops saying so, which is what makes
	// an answer given elsewhere — the CLI, curl — close it here too.
	v.syncForm()
}

// syncForm opens, keeps or closes the §7.4 popup to match the chat's state.
func (v *chatView) syncForm() {
	if v.chat == nil || v.chat.State != "awaiting_input" {
		v.form = nil
		return
	}
	req, ok, err := v.chat.PendingRequest()
	if err != nil || !ok {
		v.form = nil
		return
	}
	if v.form != nil && v.form.sameRequest(req) {
		return
	}
	v.form = newAnswerForm(req)
}

// runningTurn is the turn currently producing output, if any.
func (v *chatView) runningTurn() *apiclient.ChatTurn {
	for i := range v.turns {
		if v.turns[i].State == "running" {
			return &v.turns[i]
		}
	}
	return nil
}

// fetchLiveTranscript fetches the running turn's transcript so the tail has
// scrollback, and records the offset the stream resumes past.
func (v *chatView) fetchLiveTranscript() tea.Cmd {
	turn := v.runningTurn()
	client, id := v.client, v.chatID
	if turn == nil || client == nil {
		return nil
	}
	if turn.ID == v.liveTurn && v.liveFrom > 0 {
		return nil // already seamed onto this turn
	}
	seq := turn.Seq
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		records, next, err := client.ChatTurnTranscript(ctx, id, seq, apiclient.TranscriptOptions{})
		return chatTranscriptMsg{chatID: id, seq: seq, records: records, next: next, err: err}
	}
}

func (v *chatView) applyTranscript(msg chatTranscriptMsg) {
	if msg.chatID != v.chatID {
		return
	}
	if msg.err != nil {
		// A transcript that is gone to retention is not an error worth a
		// banner: the finished turns still render from ResultText, and a
		// running turn's tail simply starts empty.
		return
	}
	turn := v.runningTurn()
	if turn == nil || turn.Seq != msg.seq {
		return
	}
	v.liveTurn, v.liveFrom = turn.ID, msg.next
	v.scrollback = transcriptLines(msg.records)
}

// applyChatNote folds one stream note in and schedules the next read.
func (v *chatView) applyChatNote(msg chatNoteMsg) tea.Cmd {
	if msg.chatID != v.chatID {
		return nil // a stale subscription from a chat we left
	}
	next := readNextChatNote(msg.chatID, msg.next)
	switch note := msg.note.(type) {
	case apiclient.EventNote:
		if strings.HasPrefix(note.Event.Type, "chat.") {
			return tea.Batch(next, v.loadCmd())
		}
	case apiclient.OutputNote:
		// The seam: a chunk at or before the offset the transcript fetch
		// reported is one the fetch already returned. Dropping it here is
		// what keeps the tail from double-printing a line across a reconnect
		// or across a turn boundary.
		if note.TurnID == v.liveTurn && note.Offset <= v.liveFrom {
			return next
		}
		if line := outputNoteLine(note); line != "" {
			v.scrollback = append(v.scrollback, line)
		}
	}
	return next
}

func (v *chatView) applySent(msg chatSentMsg) tea.Cmd {
	if msg.err != nil {
		// A 409 chat_cap_reached is a refusal, never a queued turn and never
		// a spinner: the daemon will not run this turn, and a client that
		// showed one pending would be inventing state (§11, decision 1).
		var apiErr *apiclient.Error
		if errors.As(msg.err, &apiErr) && apiErr.Code == "chat_cap_reached" {
			v.note, v.noteBad = "too many chats are running — "+
				"finish or cancel one and send again", true
			return nil
		}
		v.note, v.noteBad = errString(msg.err), true
		return nil
	}
	v.note = ""
	v.scrollback, v.liveFrom, v.liveTurn = nil, 0, 0
	return v.loadCmd()
}

func (v *chatView) applyAnswered(msg chatAnsweredMsg) tea.Cmd {
	if msg.err != nil {
		if v.form != nil {
			v.form.err = errString(msg.err)
			v.form.submitting = false
		}
		return nil
	}
	v.form = nil
	return v.loadCmd()
}

func (v *chatView) applyCanceled(msg chatCanceledMsg) tea.Cmd {
	if msg.err != nil {
		v.note, v.noteBad = errString(msg.err), true
		return nil
	}
	v.note, v.noteBad = "turn canceled", false
	return v.loadCmd()
}

func (v *chatView) updateKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	if v.form != nil {
		cmd, exit := v.form.updateWith(msg, v.answerCmd)
		if exit {
			v.form = nil
		}
		return v, cmd
	}
	switch msg.String() {
	case "esc":
		return v, func() tea.Msg { return selectViewMsg{id: viewChats} }
	case "ctrl+c":
		return v, nil
	case "enter":
		return v, v.sendCmd()
	case "ctrl+x":
		return v, v.cancelCmd()
	}
	var cmd tea.Cmd
	v.composer, cmd = v.composer.Update(msg)
	return v, cmd
}

func (v *chatView) sendCmd() tea.Cmd {
	message := strings.TrimSpace(v.composer.Value())
	if message == "" {
		return nil
	}
	client, id := v.client, v.chatID
	if client == nil {
		v.note, v.noteBad = "not connected", true
		return nil
	}
	v.composer.SetValue("")
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		turn, err := client.SendChat(ctx, id, message)
		return chatSentMsg{chatID: id, turn: turn, err: err}
	}
}

func (v *chatView) answerCmd(resp apiclient.InputResponse) tea.Cmd {
	client, id := v.client, v.chatID
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		return chatAnsweredMsg{chatID: id, err: client.AnswerChat(ctx, id, resp)}
	}
}

func (v *chatView) cancelCmd() tea.Cmd {
	client, id := v.client, v.chatID
	if client == nil || v.runningTurn() == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		return chatCanceledMsg{chatID: id, err: client.CancelChat(ctx, id)}
	}
}
