package tui

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatnote"
	"github.com/lezli01/vincent/internal/keymap"
)

// The chat's diff pane (task 169). It is a pane swap on a key, not a tab
// strip (decision 3): the chat_diff key puts the diff where the conversation
// is, the diff then holds the keyboard the way the conversation focus does
// (task 160, chatView.convFocus), and esc gives the keyboard back to the
// composer with the draft exactly as it was.
//
// The view holds nothing the daemon does not: the files, the review marks
// and the note a reject writes all come from the daemon. Only the scope and
// the cursors are this view's.
//
// Nothing here follows the event stream. The diff routes run git per call,
// so the diff is read when the pane opens, on a scope switch, on `r`, after
// a reject, and when a turn ends while the pane is open; the Details pane's
// `changes` row is read on open, when a turn ends and after a reject
// (decision 10).

// chatDiffState is the pane and what it last heard from the daemon.
type chatDiffState struct {
	open bool
	// scope is apiclient.ChatDiffScopeBase or ChatDiffScopeTurn; empty reads
	// as the base.
	scope string
	pane  diffPane
	// loaded is a read of the current scope having been answered, fetching
	// one being in flight, and busy a reject or an accept in flight — one at
	// a time, so a held key cannot queue a second reject behind the first.
	loaded, fetching, busy bool
	// unavailable is the daemon's own reason a scope has no diff (409
	// chat_diff_unavailable): an empty state, never an error box and never a
	// silent fall back to the base (decision 7). err is any other failure.
	unavailable, err string
	// files is the last read's files by path, for what the renderer does not
	// keep: whether a file's hunks can be rejected at all.
	files map[string]apiclient.ChatDiffFile
	// stat is the Details pane's `changes` row (decision 10), nil until read.
	stat *apiclient.ChatDiffStat
}

type (
	// chatDiffMsg is GET /v1/chats/{id}/diff for one scope.
	chatDiffMsg struct {
		chatID int64
		scope  string
		diff   *apiclient.ChatDiff
		err    error
	}
	// chatDiffStatMsg is GET /v1/chats/{id}/diff/stat.
	chatDiffStatMsg struct {
		chatID int64
		stat   *apiclient.ChatDiffStat
		err    error
	}
	// chatHunkRejectedMsg is a reject's answer.
	chatHunkRejectedMsg struct {
		chatID int64
		res    *apiclient.ChatHunkReject
		err    error
	}
	// chatHunkMarkedMsg is an accept's or an unaccept's answer.
	chatHunkMarkedMsg struct {
		chatID   int64
		path, id string
		accepted bool
		err      error
	}
)

// diffScope is the scope in force.
func (v *chatView) diffScope() string {
	if v.diff.scope == apiclient.ChatDiffScopeTurn {
		return apiclient.ChatDiffScopeTurn
	}
	return apiclient.ChatDiffScopeBase
}

// resetDiff forgets a chat's diff for a new open. The scope goes back to the
// base with it: "since the last turn" is a question about one chat's turn.
func (v *chatView) resetDiff() {
	v.diff = chatDiffState{pane: newDiffPane()}
}

// openDiff swaps the conversation for the diff and reads it. The draft is
// untouched — the composer only stops taking keys — but it is saved now: a
// reject prepends its note to the draft the daemon holds, so that draft
// should be what the reader typed.
func (v *chatView) openDiff() tea.Cmd {
	if v.chat == nil {
		return nil
	}
	v.focusComposer()
	v.hideInline("")
	v.hideFiles()
	v.hideSkills()
	v.composer.Blur()
	v.diff.open = true
	return tea.Batch(v.flushDraft(), v.diffFetchCmd())
}

// closeDiff gives the pane back to the conversation and the keyboard back to
// the composer.
func (v *chatView) closeDiff() {
	v.diff.open = false
	v.composer.Focus()
	v.bodyDirty = true
}

// diffFetchCmd reads the diff for the scope in force.
func (v *chatView) diffFetchCmd() tea.Cmd {
	client, id, scope := v.client, v.chatID, v.diffScope()
	if client == nil || id == 0 {
		return nil
	}
	v.diff.fetching = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
		defer cancel()
		d, err := client.ChatDiff(ctx, id, scope)
		return chatDiffMsg{chatID: id, scope: scope, diff: d, err: err}
	}
}

// diffStatCmd reads the Details pane's `changes` row. A chat with no branch
// has no worktree to count, so nothing is asked.
func (v *chatView) diffStatCmd() tea.Cmd {
	client, id := v.client, v.chatID
	if client == nil || id == 0 {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
		defer cancel()
		s, err := client.ChatDiffStat(ctx, id)
		return chatDiffStatMsg{chatID: id, stat: s, err: err}
	}
}

// turnEndedDiff is what a turn ending re-reads (decision 10): the `changes`
// row always, the diff only while it is on screen.
func (v *chatView) turnEndedDiff() tea.Cmd {
	if v.diff.open {
		return tea.Batch(v.diffStatCmd(), v.diffFetchCmd())
	}
	// A closed pane's content is stale now; the next open reads it anyway.
	return v.diffStatCmd()
}

func (v *chatView) applyDiffStat(msg chatDiffStatMsg) {
	if msg.chatID != v.chatID {
		return
	}
	// A failed read — a chat with no directory, say — leaves no row rather
	// than a stale one.
	v.diff.stat = nil
	if msg.err == nil {
		v.diff.stat = msg.stat
	}
}

// applyDiff folds a read in. An answer for a scope the reader has since left
// is dropped: the read for the scope in force is already on its way.
func (v *chatView) applyDiff(msg chatDiffMsg) {
	if msg.chatID != v.chatID || msg.scope != v.diffScope() {
		return
	}
	d := &v.diff
	d.fetching, d.loaded = false, true
	d.unavailable, d.err = "", ""
	if msg.err != nil {
		d.files = nil
		d.pane.setFiles(nil, v.now())
		var apiErr *apiclient.Error
		if errors.As(msg.err, &apiErr) && apiErr.Code == apiclient.CodeChatDiffUnavailable {
			d.unavailable = sanitizeText(apiErr.Message)
			return
		}
		d.err = errString(msg.err)
		return
	}
	if msg.diff == nil {
		return
	}
	d.files = make(map[string]apiclient.ChatDiffFile, len(msg.diff.Files))
	in := make([]diffFileInput, 0, len(msg.diff.Files))
	for _, f := range msg.diff.Files {
		d.files[f.Path] = f
		file := diffFileInput{
			path: sanitizeText(f.Path), status: chatDiffStatusLetter(f.Status), added: f.Added, removed: f.Removed, binary: f.Binary,
			note: chatDiffFileNote(f),
		}
		for _, h := range f.Hunks {
			file.hunks = append(file.hunks, diffHunkInput{
				id: h.Hash, header: sanitizeText(h.Header),
				// Without the patch's trailing newline, which would draw as
				// an empty last line of every hunk.
				body:     strings.Split(sanitizeText(strings.TrimSuffix(h.Body, "\n")), "\n"),
				accepted: h.Accepted,
			})
		}
		in = append(in, file)
	}
	d.pane.setFiles(in, v.now())
}

// chatDiffFileNote says why a file's hunks cannot be rejected one at a time
// (decision 6), and is empty for a file whose hunks can.
func chatDiffFileNote(f apiclient.ChatDiffFile) string {
	if f.Rejectable {
		return ""
	}
	switch f.NotRejectable {
	case apiclient.ChatDiffNotRejectableBinary:
		return "binary file — not rejectable per hunk"
	case apiclient.ChatDiffNotRejectableRenamed:
		return "renamed from " + sanitizeText(f.OldPath) + " — not rejectable per hunk"
	case apiclient.ChatDiffNotRejectableModeOnly:
		return "mode change only — not rejectable per hunk"
	case apiclient.ChatDiffNotRejectableGitlink:
		return "nested git repository — not rejectable here; remove the directory yourself"
	case apiclient.ChatDiffNotRejectableEmpty:
		return "empty file — nothing to reject per hunk"
	default:
		return "not rejectable per hunk"
	}
}

// diffTarget is the hunk `x` and `a` act on: the one under the hunk cursor,
// while the file cursor is still on its file. A reader who walked the file
// list somewhere else is not asked about a hunk they left behind — and on a
// file that cannot be rejected per hunk the reason is the answer.
func (v *chatView) diffTarget() (diffHunkRef, bool) {
	d := &v.diff
	if !d.loaded || d.fetching {
		return diffHunkRef{}, false
	}
	ref, ok := d.pane.selectedHunk()
	// Ungrouped, the file cursor's key is the file's path.
	at := d.pane.cursorPath
	if ok && ref.path == at {
		return ref, true
	}
	if f, known := d.files[at]; known && !f.Rejectable {
		v.note, v.noteBad = chatDiffFileNote(f), false
		return diffHunkRef{}, false
	}
	v.note, v.noteBad = "no hunk selected — ] and [ move between the hunks", false
	return diffHunkRef{}, false
}

// rejectHunkCmd reverts the selected hunk in the chat's directory.
func (v *chatView) rejectHunkCmd() tea.Cmd {
	ref, ok := v.diffTarget()
	client, id, scope := v.client, v.chatID, v.diffScope()
	if !ok || client == nil || v.diff.busy {
		return nil
	}
	v.diff.busy = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), diffTimeout)
		defer cancel()
		res, err := client.RejectChatHunk(ctx, id, scope, ref.path, ref.id)
		return chatHunkRejectedMsg{chatID: id, res: res, err: err}
	}
}

// acceptHunkCmd marks the selected hunk reviewed, or clears the mark of one
// that already is (decision 9). The mark is the daemon's; the renderer's copy
// flips only when the daemon has answered.
func (v *chatView) acceptHunkCmd() tea.Cmd {
	ref, ok := v.diffTarget()
	client, id, scope := v.client, v.chatID, v.diffScope()
	if !ok || client == nil || v.diff.busy {
		return nil
	}
	v.diff.busy = true
	want := !ref.accepted
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		var err error
		if want {
			err = client.AcceptChatHunk(ctx, id, scope, ref.path, ref.id)
		} else {
			err = client.UnacceptChatHunk(ctx, id, ref.path, ref.id)
		}
		return chatHunkMarkedMsg{chatID: id, path: ref.path, id: ref.id, accepted: want, err: err}
	}
}

// diffRefused turns a reject's or an accept's refusal into what the reader
// is told (decision 7), and re-reads the diff when the refusal is that the
// hunk is no longer the one on screen.
func (v *chatView) diffRefused(err error) tea.Cmd {
	v.noteBad = true
	var apiErr *apiclient.Error
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusConflict {
		v.note = errString(err)
		return nil
	}
	switch apiErr.Code {
	case apiclient.CodeChatTurnLive:
		v.note = "a turn is running — wait for it to end, or cancel it first"
	case apiclient.CodeChatHunkStale:
		v.note = "the diff changed under that hunk — it has been read again"
		return v.diffFetchCmd()
	default:
		// chat_hunk_conflict and chat_hunk_not_rejectable say it themselves.
		v.note = sanitizeText(apiErr.Message)
	}
	return nil
}

func (v *chatView) applyHunkRejected(msg chatHunkRejectedMsg) tea.Cmd {
	if msg.chatID != v.chatID {
		return nil
	}
	v.diff.busy = false
	if msg.err != nil {
		return v.diffRefused(msg.err)
	}
	v.note, v.noteBad = "hunk rejected — a note for the agent opens your draft", false
	var save tea.Cmd
	if msg.res != nil {
		save = v.adoptRejectDraft(msg.res.Draft, msg.res.Note)
		if !msg.res.NoteAdded {
			v.note, v.noteBad = "hunk rejected — "+cmp.Or(sanitizeText(msg.res.Warning), "no note was added to the draft"), true
		}
	}
	// The worktree changed: the diff and the `changes` row are both stale.
	return tea.Batch(save, v.diffFetchCmd(), v.diffStatCmd())
}

// adoptRejectDraft puts a reject's resulting draft in the composer (decision
// 8) without taking back anything typed here (task 164). note is the line
// the daemon added to its draft, "" when it added none.
//
// When the composer is clean — exactly the stored draft — the answer replaces
// it. When it holds an edit the daemon has not seen, the same line is added
// to the edit the way the daemon added it to its own draft (chatnote.Add),
// and the result is saved, so neither the note nor the edit is lost; the
// chat.draft_changed that follows finds the composer dirty and leaves it be.
// The line is added rather than recovered by comparing the two drafts: a
// second reject extends the block in place, so the daemon's draft does not
// end with the one before it (review F5). A queued message being edited is
// not the draft, so it is left alone and the stored draft arrives when the
// edit ends.
func (v *chatView) adoptRejectDraft(draft, note string) tea.Cmd {
	if !v.draft.loaded || v.editingQueued != 0 {
		return nil
	}
	if v.draftClean() {
		v.draft.saved = draft
		v.composer.SetValue(draft)
		return nil
	}
	v.draft.saved = draft
	if note != "" {
		v.composer.SetValue(chatnote.Add(v.composer.Value(), note))
	}
	// The edit here wins over the stored draft, as it would against any
	// remote one, and now carries the note; the next save says so.
	return v.armDraftSave()
}

func (v *chatView) applyHunkMarked(msg chatHunkMarkedMsg) tea.Cmd {
	if msg.chatID != v.chatID {
		return nil
	}
	v.diff.busy = false
	if msg.err != nil {
		return v.diffRefused(msg.err)
	}
	v.note = ""
	if !v.diff.pane.setHunkAccepted(msg.path, msg.id, msg.accepted) {
		// The diff was re-read meanwhile and the hunk is gone; the daemon's
		// marks are the truth, so read them.
		return v.diffFetchCmd()
	}
	return nil
}

// updateDiffKey is the diff pane's keyboard. It owns every key, as the
// conversation focus does: a press the pane has no meaning for is spent on
// the renderer's own fold, scroll and pan keys rather than typed.
func (v *chatView) updateDiffKey(msg tea.KeyPressMsg) tea.Cmd {
	// The key that opened the pane closes it too, wherever it is bound.
	if key := opKey(keymap.ChatDiff); key != "" && msg.String() == key {
		v.closeDiff()
		return nil
	}
	switch msg.String() {
	case "esc":
		v.closeDiff()
		return nil
	case "ctrl+c":
		return nil
	case "]":
		v.diff.pane.moveHunk(1)
		return nil
	case "[":
		v.diff.pane.moveHunk(-1)
		return nil
	case "x":
		return v.rejectHunkCmd()
	case "a":
		return v.acceptHunkCmd()
	case "s":
		// The scope is view state (decision 3); each scope's diff is the
		// daemon's, so a switch reads it.
		if v.diffScope() == apiclient.ChatDiffScopeBase {
			v.diff.scope = apiclient.ChatDiffScopeTurn
		} else {
			v.diff.scope = apiclient.ChatDiffScopeBase
		}
		v.diff.loaded, v.diff.unavailable, v.diff.err = false, "", ""
		v.note = ""
		return v.diffFetchCmd()
	case "r":
		return tea.Batch(v.diffFetchCmd(), v.diffStatCmd())
	}
	return v.diff.pane.updateKey(msg)
}

// chatDiffStatusLetter is a file's wire status as the letter the shared
// renderer draws on its row.
func chatDiffStatusLetter(status string) byte {
	switch status {
	case "added":
		return 'A'
	case "deleted":
		return 'D'
	case "renamed":
		return 'R'
	}
	return 'M'
}

// chatDiffScopeLabel names a scope the way the pane's head line does.
func chatDiffScopeLabel(scope string) string {
	if scope == apiclient.ChatDiffScopeTurn {
		return "since the last turn"
	}
	return "since the base"
}

// diffBody is the diff where the conversation is drawn: one line naming the
// scope, then the renderer — or the reason there is nothing to render.
func (v *chatView) diffBody(width, height int) []string {
	d := &v.diff
	head := " " + styleTitle.Render(ic("± Diff")) + " " + styleDim.Render(chatDiffScopeLabel(v.diffScope()))
	if d.fetching && d.loaded {
		head += " " + styleDim.Render("· reading…")
	}
	lines := []string{ansi.Truncate(head, max(width, 1), "…")}
	room := max(height-1, 1)
	var body string
	switch {
	case d.unavailable != "":
		// The daemon's reason, as an empty state (decision 7).
		body = styleDim.Render("  no diff " + chatDiffScopeLabel(v.diffScope()) + ": " + d.unavailable)
	case d.err != "":
		body = styleDim.Render("  the diff could not be read: " + d.err)
	case !d.loaded:
		body = styleDim.Render("  loading diff…")
	default:
		body = d.pane.render(width, room)
	}
	lines = append(lines, strings.Split(body, "\n")...)
	for len(lines) < height {
		lines = append(lines, "")
	}
	if len(lines) > height && height > 0 {
		lines = lines[:height]
	}
	return lines
}

// chatChanges is the Details pane's `changes` row (task 169 decision 10):
// the base scope's totals, and the key that opens the diff — read through
// the keymap, so a rebind shows. key is empty where no key opens it: the
// chats board's preview, or an unbound operation.
type chatChanges struct {
	stat *apiclient.ChatDiffStat
	key  string
}

// rows is the row and, under it, the key. Nothing until the totals are read.
func (c chatChanges) rows(row func(k, val string) string) []string {
	if c.stat == nil {
		return nil
	}
	val := "none"
	if c.stat.Files > 0 {
		val = countNoun(c.stat.Files, "file")
		if c.stat.Added+c.stat.Removed > 0 {
			val += " +" + strconv.Itoa(c.stat.Added) + " -" + strconv.Itoa(c.stat.Removed)
		}
	}
	out := []string{row("changes", val)}
	if c.key != "" {
		out = append(out, styleDim.Render(c.key+" opens the diff"))
	}
	return out
}

// changes is the open chat's row.
func (v *chatView) changes() chatChanges {
	return chatChanges{stat: v.diff.stat, key: opKey(keymap.ChatDiff)}
}
