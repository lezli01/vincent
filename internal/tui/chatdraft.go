package tui

// A chat's draft and stash, kept in the daemon, and the recall of earlier
// prompts (task 164, #1012, spec §5.5, §15).
//
// The daemon owns the draft (decision 1): the composer is a view of
// `chats.draft`, saved on a debounce while typing and flushed at once when
// the chat is left. Two rules keep a remote change from eating a keystroke.
// The composer takes the daemon's draft only while it still holds exactly the
// text this client last wrote, with no save in flight. And a read is ordered
// against this client's saves by `draft_updated_at`: a read stamped before
// the last save the daemon acknowledged was taken before that save committed,
// however late it arrives, so it is never taken for a newer draft (review
// F1).

import (
	"context"
	"encoding/json"
	"strings"
	"time"
	"unicode/utf8"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatstate"
	"github.com/lezli01/vincent/internal/keymap"
)

// chatDraftDebounce is how long the composer rests before its text is saved.
const chatDraftDebounce = 500 * time.Millisecond

// chatDraftExitTimeout bounds the synchronous save a quitting TUI makes.
const chatDraftExitTimeout = 2 * time.Second

type (
	// chatDraftTickMsg is the debounce firing for generation gen.
	chatDraftTickMsg struct {
		chatID int64
		gen    int
	}
	// chatDraftSavedMsg reports PUT /v1/chats/{id}/draft.
	chatDraftSavedMsg struct {
		chatID int64
		// updatedAt is the write's draft_updated_at, which orders later
		// reads against it (review F1).
		updatedAt *time.Time
		err       error
	}
	// chatDraftFetchedMsg is GET /v1/chats/{id}/draft, the re-read a
	// chat.draft_changed asks for.
	chatDraftFetchedMsg struct {
		chatID int64
		draft  *apiclient.ChatDraft
		err    error
	}
	// chatPromptsMsg is GET /v1/chats/prompts for the history picker.
	chatPromptsMsg struct {
		chatID  int64
		query   string
		prompts []apiclient.ChatPrompt
		err     error
	}
)

// chatDraftState is the view's half of the draft. It is reset by every open.
type chatDraftState struct {
	// loaded is set once the chat's stored draft has been put in the
	// composer for this visit.
	loaded bool
	// saved is the draft text this client last wrote (or loaded): the
	// composer is "dirty" when it differs.
	saved string
	// stash is the chat's stash slot as last read or written, and
	// stashDirty a stash this client changed and has not yet written.
	stash      string
	stashDirty bool
	// gen numbers the debounce ticks; only the newest one saves.
	gen int
	// inFlight is a PUT on its way, and again a save asked for meanwhile:
	// writes are serialized so an older one can never land last.
	inFlight bool
	again    bool
	// stashInFlight says the PUT in flight carries the stash, so a failed
	// one marks the stash dirty again rather than losing it (review F2).
	stashInFlight bool
	// failed is a save the daemon refused, which keeps the composer dirty so
	// a remote draft cannot replace text that was never stored.
	failed bool
	// writtenAt is draft_updated_at of the newest save the daemon
	// acknowledged. A read stamped earlier was taken before that save
	// committed, and is stale however late it arrives (review F1).
	writtenAt *time.Time
}

// chatRecall is `↑`/`↓` walking this chat's earlier prompts (decision 4).
// idx is the prompt in the composer, newest first; -1 is not recalling.
type chatRecall struct {
	idx     int
	prompts []string
}

// active reports whether the composer holds a recalled prompt.
func (r *chatRecall) active() bool { return r.idx >= 0 }

// recallPrompts lists the chat's own prompts, newest first, with a repeat of
// the prompt just after it skipped so `↑` never seems to do nothing.
func (v *chatView) recallPrompts() []string {
	var out []string
	for i := len(v.turns) - 1; i >= 0; i-- {
		p := v.turns[i].Prompt
		if p == "" || (len(out) > 0 && out[len(out)-1] == p) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// recallUp is `↑` on an empty composer with no queued message, or `↑` again
// while recalling: the next older prompt. It reports false when there is
// nothing to recall, so `↑` falls through to the composer.
func (v *chatView) recallUp() bool {
	if !v.recall.active() {
		if v.composer.Value() != "" {
			return false
		}
		v.recall.prompts = v.recallPrompts()
		if len(v.recall.prompts) == 0 {
			return false
		}
		v.recall.idx = 0
	} else if v.recall.idx+1 < len(v.recall.prompts) {
		v.recall.idx++
	}
	// A copy: editing it never touches the turn it came from.
	v.composer.SetValue(v.recall.prompts[v.recall.idx])
	return true
}

// recallDown is `↓` while recalling: the next newer prompt, and past the
// newest an empty composer, which ends recall.
func (v *chatView) recallDown() {
	v.recall.idx--
	if v.recall.idx < 0 {
		v.composer.SetValue("")
		return
	}
	v.composer.SetValue(v.recall.prompts[v.recall.idx])
}

// endRecall leaves recall mode, keeping whatever is in the composer.
func (v *chatView) endRecall() { v.recall.idx = -1 }

// resetDraft forgets the previous chat's draft state for a new open.
func (v *chatView) resetDraft() {
	v.draft = chatDraftState{}
	v.recall = chatRecall{idx: -1}
	v.history = chatHistoryPicker{}
}

// applyLoadedDraft folds a loaded chat's draft and stash into the view. The
// first load of a visit restores the stored draft unless the composer
// already holds text (a first message handed back by the new-chat form).
// Later loads replace the composer only when it is clean — exactly the
// text this client last wrote — so no echo, of this client's save or of an
// older one, can take a keystroke back.
func (v *chatView) applyLoadedDraft(c *apiclient.Chat) {
	if c == nil {
		return
	}
	v.applyRemoteDraft(c.Draft, c.Stash, c.DraftUpdatedAt)
}

// applyRemoteDraft is applyLoadedDraft for a draft and stash read at
// updatedAt. A read older than the last save this client had acknowledged
// was taken before that save committed — a reload racing a send's PUT, say —
// so neither its draft nor its stash is newer than what this client holds.
func (v *chatView) applyRemoteDraft(draft, stash string, updatedAt *time.Time) {
	if w := v.draft.writtenAt; w != nil && (updatedAt == nil || updatedAt.Before(*w)) {
		return
	}
	// A stash being written is this client's until the write is answered.
	if !v.draft.stashDirty && !v.draft.inFlight {
		v.draft.stash = stash
	}
	if !v.draft.loaded {
		v.draft.loaded = true
		v.draft.saved = draft
		if v.composer.Value() == "" && v.editingQueued == 0 {
			v.composer.SetValue(draft)
		}
		return
	}
	if draft == v.draft.saved || !v.draftClean() {
		return
	}
	v.draft.saved = draft
	v.composer.SetValue(draft)
}

// draftClean reports whether the composer holds exactly what this client last
// stored, with nothing pending, so a remote draft may replace it.
func (v *chatView) draftClean() bool {
	return v.editingQueued == 0 && !v.recall.active() && !v.draft.inFlight && !v.draft.failed &&
		v.composer.Value() == v.draft.saved
}

// draftEdited is every keystroke or paste that reached the composer: it ends
// recall when the text changed (decision 4) and arms the debounce.
func (v *chatView) draftEdited(before string) tea.Cmd {
	if v.composer.Value() == before {
		return nil
	}
	v.endRecall()
	return v.armDraftSave()
}

// armDraftSave starts a fresh debounce: only the newest tick saves.
func (v *chatView) armDraftSave() tea.Cmd {
	if v.client == nil || v.chatID == 0 {
		return nil
	}
	v.draft.gen++
	id, gen := v.chatID, v.draft.gen
	return tea.Tick(chatDraftDebounce, func(time.Time) tea.Msg { return chatDraftTickMsg{chatID: id, gen: gen} })
}

func (v *chatView) applyDraftTick(msg chatDraftTickMsg) tea.Cmd {
	if msg.chatID != v.chatID || msg.gen != v.draft.gen {
		return nil
	}
	return v.flushDraft()
}

// draftUpdate is the write the composer needs, or nil when the daemon
// already holds it. A queued message being edited is not the draft, and a
// terminal chat keeps none.
func (v *chatView) draftUpdate() *apiclient.ChatDraftUpdate {
	if !v.draftStorable() {
		return nil
	}
	var u apiclient.ChatDraftUpdate
	if text := v.composer.Value(); text != v.draft.saved || v.draft.failed {
		u.Draft = &text
	}
	if v.draft.stashDirty {
		stash := v.draft.stash
		u.Stash = &stash
	}
	if u.Draft == nil && u.Stash == nil {
		return nil
	}
	return &u
}

// draftStorable reports whether the composer is the chat's draft, which the
// daemon can keep: not before the chat's draft has loaded, not while it holds
// a queued message being edited, and not on a chat that has ended.
func (v *chatView) draftStorable() bool {
	if v.chatID == 0 || !v.draft.loaded || v.editingQueued != 0 {
		return false
	}
	return v.chat == nil || !chatstate.Terminal(chatstate.State(v.chat.State))
}

// flushDraft writes the draft now, one PUT at a time.
func (v *chatView) flushDraft() tea.Cmd {
	if v.draft.inFlight {
		v.draft.again = true
		return nil
	}
	u := v.draftUpdate()
	client, id := v.client, v.chatID
	if u == nil || client == nil {
		return nil
	}
	if u.Draft != nil {
		v.draft.saved = *u.Draft
	}
	v.draft.stashInFlight = u.Stash != nil
	v.draft.stashDirty, v.draft.failed = false, false
	v.draft.inFlight, v.draft.again = true, false
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		d, err := client.PutChatDraft(ctx, id, *u)
		msg := chatDraftSavedMsg{chatID: id, err: err}
		if d != nil {
			msg.updatedAt = d.UpdatedAt
		}
		return msg
	}
}

// chatDraftChangedEvent is a chat's draft or stash being written (spec
// §13.3, task 164).
const chatDraftChangedEvent = "chat.draft_changed"

// draftEventCmd answers a chat.draft_changed: the draft alone is re-read
// (review F5), and not at all when the event is the echo of the write this
// client last had acknowledged — its draft_updated_at is that write's.
func (v *chatView) draftEventCmd(payload json.RawMessage) tea.Cmd {
	client, id := v.client, v.chatID
	if client == nil || id == 0 {
		return nil
	}
	var ev struct {
		DraftUpdatedAt *time.Time `json:"draft_updated_at"`
	}
	if json.Unmarshal(payload, &ev) == nil && ev.DraftUpdatedAt != nil &&
		v.draft.writtenAt != nil && ev.DraftUpdatedAt.Equal(*v.draft.writtenAt) {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		d, err := client.ChatDraft(ctx, id)
		return chatDraftFetchedMsg{chatID: id, draft: d, err: err}
	}
}

// applyDraftFetched folds a re-read draft in under applyRemoteDraft's rules.
// A failed read changes nothing: the next event or load tries again.
func (v *chatView) applyDraftFetched(msg chatDraftFetchedMsg) {
	if msg.chatID != v.chatID || msg.err != nil || msg.draft == nil {
		return
	}
	v.applyRemoteDraft(msg.draft.Draft, msg.draft.Stash, msg.draft.UpdatedAt)
}

func (v *chatView) applyDraftSaved(msg chatDraftSavedMsg) tea.Cmd {
	if msg.chatID != v.chatID {
		return nil
	}
	v.draft.inFlight = false
	stashSent := v.draft.stashInFlight
	v.draft.stashInFlight = false
	if msg.err != nil {
		// The draft is re-sent because failed is set; the stash the same
		// way, by being dirty again — the composer no longer holds it, so a
		// stash that is not retried is gone (review F2).
		v.draft.failed = true
		v.draft.stashDirty = v.draft.stashDirty || stashSent
		v.note, v.noteBad = "the draft was not saved: "+errString(msg.err), true
		return nil
	}
	if at := msg.updatedAt; at != nil && (v.draft.writtenAt == nil || at.After(*v.draft.writtenAt)) {
		v.draft.writtenAt = at
	}
	if v.draft.again {
		return v.flushDraft()
	}
	return nil
}

// leaveDraft is the flush on leaving a chat — esc, another chat opening, a
// project switch, or any other view taking over, the palette's included. It is fire-and-forget: the view is moving on, and the
// result is for a chat it no longer shows.
func (v *chatView) leaveDraft() tea.Cmd {
	u := v.draftUpdate()
	client, id := v.client, v.chatID
	if u == nil || client == nil {
		return nil
	}
	if u.Draft != nil {
		v.draft.saved = *u.Draft
	}
	v.draft.stashDirty = false
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		_, _ = client.PutChatDraft(ctx, id, *u)
		return nil
	}
}

// flushDraftSync is the save a quitting TUI makes after the program has
// stopped, when no command can run any more.
func (v *chatView) flushDraftSync() {
	u := v.draftUpdate()
	if u == nil || v.client == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), chatDraftExitTimeout)
	defer cancel()
	_, _ = v.client.PutChatDraft(ctx, v.chatID, *u)
}

// toggleStash is alt+s (decision 2): a non-empty composer goes to the stash
// slot, replacing what was there, and the composer empties; an empty one
// takes the stash back.
func (v *chatView) toggleStash() tea.Cmd {
	if v.chat == nil || !v.draft.loaded {
		v.note, v.noteBad = "this chat has not loaded", true
		return nil
	}
	if chatstate.Terminal(chatstate.State(v.chat.State)) {
		v.note, v.noteBad = "this chat has ended — it keeps no draft", true
		return nil
	}
	if v.editingQueued != 0 {
		v.note, v.noteBad = "finish editing the queued message first", true
		return nil
	}
	v.endRecall()
	text := v.composer.Value()
	switch {
	case strings.TrimSpace(text) != "":
		replaced := v.draft.stash != ""
		v.draft.stash, v.draft.stashDirty = text, true
		v.composer.SetValue("")
		v.note, v.noteBad = "draft stashed — "+opKey(keymap.Stash)+" on an empty composer brings it back", false
		if replaced {
			v.note += " (the previous stash was replaced)"
		}
	case v.draft.stash != "":
		v.composer.SetValue(v.draft.stash)
		v.draft.stash, v.draft.stashDirty = "", true
		v.note, v.noteBad = "stash restored", false
	default:
		v.note, v.noteBad = "nothing to stash", false
		return nil
	}
	return v.flushDraft()
}

// chatHistoryPicker is alt+h's list of every prompt the project's chats sent
// (decision 3). It owns the keyboard while open.
type chatHistoryPicker struct {
	open    bool
	loading bool
	query   string
	items   []apiclient.ChatPrompt
	cursor  int
	err     string
	// drawn is the rows the last render drew, for a click (#1144).
	drawn []clickRow
}

func (p *chatHistoryPicker) clickRows() []clickRow { return p.drawn }

// pointAt is a click on a row (#1144).
func (p *chatHistoryPicker) pointAt(i int) bool {
	if i < 0 || i >= len(p.items) {
		return false
	}
	p.cursor = i
	return true
}

// chatHistoryRows is how many prompts the picker shows at once.
const chatHistoryRows = 8

// chatHistoryLimit is how many prompts one fetch asks for.
const chatHistoryLimit = 100

func (v *chatView) openHistory() tea.Cmd {
	if v.chat == nil {
		v.note, v.noteBad = "this chat has not loaded", true
		return nil
	}
	v.history = chatHistoryPicker{open: true}
	return v.fetchHistory()
}

func (v *chatView) fetchHistory() tea.Cmd {
	client, id := v.client, v.chatID
	if client == nil || v.chat == nil {
		return nil
	}
	pid, q := v.chat.ProjectID, v.history.query
	v.history.loading = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		ps, err := client.ChatPrompts(ctx, pid, q, chatHistoryLimit)
		return chatPromptsMsg{chatID: id, query: q, prompts: ps, err: err}
	}
}

func (v *chatView) applyPrompts(msg chatPromptsMsg) {
	// A late answer for a query since changed is not this list's.
	if msg.chatID != v.chatID || !v.history.open || msg.query != v.history.query {
		return
	}
	v.history.loading = false
	if msg.err != nil {
		v.history.err = errString(msg.err)
		return
	}
	v.history.err = ""
	v.history.items = msg.prompts
	v.history.cursor = min(v.history.cursor, max(len(msg.prompts)-1, 0))
}

// updateHistoryKey is the open picker's keyboard. enter inserts the prompt
// at the composer's cursor and sends nothing; esc leaves the draft as it was.
func (v *chatView) updateHistoryKey(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		v.history = chatHistoryPicker{}
		return nil
	case "up":
		v.history.cursor = max(v.history.cursor-1, 0)
		return nil
	case "down":
		v.history.cursor = min(v.history.cursor+1, max(len(v.history.items)-1, 0))
		return nil
	case "enter":
		if len(v.history.items) == 0 {
			return nil
		}
		text := v.history.items[v.history.cursor].Prompt
		v.history = chatHistoryPicker{}
		before := v.composer.Value()
		v.composer.InsertString(text)
		return v.draftEdited(before)
	case "backspace":
		if v.history.query == "" {
			return nil
		}
		r := []rune(v.history.query)
		v.history.query = string(r[:len(r)-1])
		v.history.cursor = 0
		return v.fetchHistory()
	}
	if msg.Text != "" {
		v.history.query += msg.Text
		v.history.cursor = 0
		return v.fetchHistory()
	}
	return nil
}

// renderHistory draws the picker where the inline lists draw: over the
// composer, out of the body's budget.
func (v *chatView) renderHistory(width int) []string {
	if !v.history.open {
		return nil
	}
	w := max(width-4, 10)
	head := "prompt history · " + v.history.query + "▏"
	switch {
	case v.history.err != "":
		head += "  " + v.history.err
	case v.history.loading && len(v.history.items) == 0:
		head += "  loading…"
	case strings.TrimSpace(v.history.query) != "" && !historyNarrows(v.history.query):
		head += "  (narrows from a word's third character)"
	}
	out := []string{"", " " + styleDim.Render(truncateRunes(head, w+2))}
	if len(v.history.items) == 0 && !v.history.loading {
		out = append(out, "   "+styleDim.Render("no earlier prompts"))
	}
	v.history.drawn = v.history.drawn[:0]
	start := 0
	if v.history.cursor >= chatHistoryRows {
		start = v.history.cursor - chatHistoryRows + 1
	}
	for i := start; i < len(v.history.items) && i < start+chatHistoryRows; i++ {
		p := v.history.items[i]
		line := strings.Join(strings.Fields(p.Prompt), " ")
		line = truncateRunes(line, w-ansi.StringWidth(p.ChatTitle)-5)
		row := line + styleDim.Render("  · "+p.ChatTitle)
		cursor := "   "
		if i == v.history.cursor {
			cursor = " ▸ "
		}
		v.history.drawn = append(v.history.drawn, drawnRow(row, i))
		out = append(out, cursor+row)
	}
	out = append(out, " "+styleDim.Render("enter inserts · esc closes · type to narrow"))
	return out
}

// historyNarrows reports whether the daemon narrows a prompt-history query:
// once any word has three characters (task 164 decision 6, review F4).
func historyNarrows(query string) bool {
	for _, w := range strings.Fields(query) {
		if utf8.RuneCountInString(w) >= 3 {
			return true
		}
	}
	return false
}

// truncateRunes cuts s to at most n runes, marking the cut.
func truncateRunes(s string, n int) string {
	r := []rune(s)
	if n < 1 {
		return ""
	}
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// updateKeyCmd is updateKey's command alone, for a caller that keeps the view.
func (v *chatView) updateKeyCmd(msg tea.KeyPressMsg) tea.Cmd {
	_, cmd := v.updateKey(msg)
	return cmd
}
