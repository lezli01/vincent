package tui

import (
	"context"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/reasons"
)

// The chat queue's keys (task 156). They live on the chat surface, beside the
// composer that owns every letter, so each is modified or only means
// something when the composer is empty.
const (
	// chatQueueEditKey edits the newest queued message when the composer is
	// empty (task 156 decision 5).
	chatQueueEditKey = "up"
	// chatQueueDeleteKey deletes the queued message being edited.
	chatQueueDeleteKey = "ctrl+d"
	// chatQueueResumeKey and chatQueueClearKey act on a paused queue.
	chatQueueResumeKey = "alt+r"
	chatQueueClearKey  = "alt+c"
)

// chatQueueDoneMsg reports a queue action: an edit, a delete, a resume or a
// clear. note is what the composer says on success.
type chatQueueDoneMsg struct {
	chatID int64
	note   string
	err    error
}

// queueWanted reports whether the chat has a queue worth fetching.
func queueWanted(c *apiclient.Chat) bool {
	return c != nil && (c.QueuedCount > 0 || c.QueuePaused)
}

// queueDiscardNote is what an ending that deletes the chat's queue — archive,
// close, hand-off (task 156 decision 6) — adds to its confirmation: how many
// queued messages go with it. Empty when the queue is.
func queueDiscardNote(c *apiclient.Chat) string {
	if c == nil || c.QueuedCount <= 0 {
		return ""
	}
	return " — " + countNoun(c.QueuedCount, "queued message") + " will be discarded"
}

// queueAction runs fn against the daemon and reports it as a
// chatQueueDoneMsg.
func (v *chatView) queueAction(note string, fn func(ctx context.Context, c *apiclient.Client, id int64) error) tea.Cmd {
	client, id := v.client, v.chatID
	if client == nil {
		v.note, v.noteBad = "not connected", true
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		return chatQueueDoneMsg{chatID: id, note: note, err: fn(ctx, client, id)}
	}
}

func (v *chatView) applyQueueDone(msg chatQueueDoneMsg) tea.Cmd {
	if msg.chatID != v.chatID {
		return nil
	}
	if msg.err != nil {
		v.note, v.noteBad = errString(msg.err), true
	} else {
		v.note, v.noteBad = msg.note, false
	}
	return v.loadCmd()
}

// newestQueued is the queued message `↑` edits: the one sent last.
func (v *chatView) newestQueued() *apiclient.ChatQueuedMessage {
	if v.queue == nil || len(v.queue.Messages) == 0 {
		return nil
	}
	return &v.queue.Messages[len(v.queue.Messages)-1]
}

// startQueueEdit puts the newest queued message in the composer. It reports
// false when there is nothing to edit, so `↑` falls through to the composer.
func (v *chatView) startQueueEdit() bool {
	m := v.newestQueued()
	if m == nil || v.composer.Value() != "" {
		return false
	}
	v.editingQueued = m.ID
	v.composer.SetValue(m.Prompt)
	v.note, v.noteBad = "editing the queued message — enter saves, "+
		chatQueueDeleteKey+" deletes it, esc stops editing", false
	return true
}

// stopQueueEdit leaves the edit state and empties the composer.
func (v *chatView) stopQueueEdit() {
	v.editingQueued = 0
	v.composer.SetValue("")
	v.note = ""
}

func (v *chatView) saveQueueEditCmd() tea.Cmd {
	qid, text := v.editingQueued, v.composer.Value()
	if text == "" {
		return v.deleteQueuedCmd()
	}
	v.stopQueueEdit()
	return v.queueAction("queued message saved", func(ctx context.Context, c *apiclient.Client, id int64) error {
		_, err := c.EditChatQueued(ctx, id, qid, text)
		return err
	})
}

func (v *chatView) deleteQueuedCmd() tea.Cmd {
	qid := v.editingQueued
	v.stopQueueEdit()
	return v.queueAction("queued message deleted", func(ctx context.Context, c *apiclient.Client, id int64) error {
		return c.DeleteChatQueued(ctx, id, qid)
	})
}

// resumeQueueCmd resumes a paused queue, and starts the queue of an idle chat
// that holds one whether or not it is paused: a chat can be left idle with an
// unpaused queue — a crash between recovery's two writes, a resume racing a
// turn's ending — and nothing else would ever drain it (review F4).
func (v *chatView) resumeQueueCmd() tea.Cmd {
	stranded := v.chat != nil && v.chat.State == string(chatstate.Idle) && v.chat.QueuedCount > 0
	if v.chat == nil || !v.chat.QueuePaused && !stranded {
		v.note, v.noteBad = "the queue is not paused", false
		return nil
	}
	return v.queueAction("queue resumed", func(ctx context.Context, c *apiclient.Client, id int64) error {
		_, err := c.ResumeChatQueue(ctx, id)
		return err
	})
}

func (v *chatView) clearQueueCmd() tea.Cmd {
	if !queueWanted(v.chat) {
		v.note, v.noteBad = "nothing is queued", false
		return nil
	}
	return v.queueAction("queue cleared", func(ctx context.Context, c *apiclient.Client, id int64) error {
		return c.ClearChatQueue(ctx, id)
	})
}

// steerNote is what the steer key says (task 156 decision 4): no adapter can
// steer a running turn yet, so the key names the reason rather than doing
// something else under its name.
func (v *chatView) steerNote() {
	if v.chat == nil {
		return
	}
	reason := v.chat.SteerReason
	if v.chat.CanSteer || reason == "" {
		reason = "steering is unavailable here"
	}
	v.note, v.noteBad = reason, true
}

// queueLines renders the queue below the conversation: one faded `You` card
// per queued message, marked queued, and the paused card when a turn's ending
// stopped the queue from draining (task 156).
func (v *chatView) queueLines(width int) []string {
	if v.queue == nil {
		return nil
	}
	var lines []string
	for i := range v.queue.Messages {
		m := &v.queue.Messages[i]
		label := "queued"
		if m.ID == v.editingQueued {
			label = "queued · editing"
		}
		lines = append(lines, "")
		for _, l := range promptCardLines(m.Prompt, label, width) {
			lines = append(lines, styleDim.Render(ansi.Strip(l)))
		}
	}
	stranded := !v.queue.Paused && len(v.queue.Messages) > 0 &&
		v.chat != nil && v.chat.State == string(chatstate.Idle)
	if stranded {
		// An idle chat whose queue is not paused has nothing that will run
		// it (review F4); say how to start it.
		text := fmt.Sprintf("%s waiting with no turn running. %s starts the next · %s clear queue",
			countNoun(len(v.queue.Messages), "queued message"), chatQueueResumeKey, chatQueueClearKey)
		lines = append(lines, "")
		for _, l := range wrapText(text, max(width-1, 1), wrapOpts{indent: 2}) {
			lines = append(lines, " "+styleWarn.Render(l))
		}
	}
	if v.queue.Paused && len(v.queue.Messages) > 0 {
		why := v.queue.PauseReason
		if title := reasons.Explain(why).Title; title != "" && title != why {
			why = title + " (" + v.queue.PauseReason + ")"
		}
		text := fmt.Sprintf("queue paused — the last turn ended %s, so %d queued message(s) wait. "+
			"%s resume queue · %s clear queue", why, len(v.queue.Messages), chatQueueResumeKey, chatQueueClearKey)
		lines = append(lines, "")
		for _, l := range wrapText(text, max(width-1, 1), wrapOpts{indent: 2}) {
			lines = append(lines, " "+styleWarn.Render(l))
		}
	}
	return lines
}
