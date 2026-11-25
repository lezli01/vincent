package tui

// The status line (#1164): one line for what the TUI has to say, owned by
// the root and drawn as the lead of the footer row, which every screen has.
//
// Before it, the root's line under the header, the action bar's footer lead
// and a dozen view-local notes each kept their own message with their own
// clearing rule, so a dead key's explanation lingered on one screen while
// another wiped its result on a cursor move. Now every view posts here, and
// one rule decides how long a message stays:
//
//   - a note — an explanation ("approve: not available while running") or a
//     hint ("nothing to copy") — is about the place the key was pressed, so
//     the next key press, cursor move (a click or the wheel) or tab change
//     takes it away;
//   - a result or an error — what an action did — is news the human may not
//     have read yet, and stays until a newer message replaces it.
//
// There is no queue: the newest message wins, whichever view posted it.
//
// What is not a message stays where it is: a question that owns the
// keyboard (every y/n prompt, the leave guard, a pending project switch)
// and a persistent banner (the connection badge, the global pause line, the
// keymap warning) are drawn by their owners.
//
// The footer is where the line is drawn because it is on every screen at
// every size already, and the action bar's status had led it since task
// 135.10 decision 2 — "a status or a confirmation leads, cut from the
// right" — so its start, the error, is what survives a narrow terminal.
// Drawing it under the header instead would take a body row from every view
// whenever a message is up and move the layout under the cursor.

// statusKind says how long a status message stays.
type statusKind uint8

const (
	// statusResult stays until a newer message replaces it.
	statusResult statusKind = iota
	// statusNote is dropped by the next key, cursor move or tab change.
	statusNote
)

// statusHolder is the one status line. The root makes it and hands the same
// pointer to every view (statusAware), as it does hyperlinkHolder; a view
// built alone — in a test — makes its own, so it never writes through nil.
// Every write happens on the Update goroutine.
type statusHolder struct {
	text string
	bad  bool
	kind statusKind
}

func newStatusHolder() *statusHolder { return &statusHolder{} }

// result posts what an action did; bad renders it as an error.
func (h *statusHolder) result(text string, bad bool) {
	h.text, h.bad, h.kind = text, bad, statusResult
}

// note posts an explanation or a hint that lasts until the next key, cursor
// move or tab change; bad renders it as a warning.
func (h *statusHolder) note(text string, bad bool) {
	h.text, h.bad, h.kind = text, bad, statusNote
}

// clear empties the line: a progress message whose outcome is shown
// elsewhere ("opening a chat…" once the chat is on screen), or a question
// that has taken over from it.
func (h *statusHolder) clear() { h.text, h.bad, h.kind = "", false, statusResult }

// dropNote empties the line if it holds a note, and leaves a result alone.
func (h *statusHolder) dropNote() {
	if h.kind == statusNote {
		h.clear()
	}
}

// shown is the line's styled text, or "" when nothing is posted.
func (h *statusHolder) shown() string {
	if h == nil || h.text == "" {
		return ""
	}
	if h.bad {
		return styleBad.Render(h.text)
	}
	return styleDim.Render(h.text)
}

// statusAware is a view that posts to the status line; the root hands each
// one its holder.
type statusAware interface {
	setStatusHolder(h *statusHolder)
}

// statusPoster is embedded by every view that posts to the status line: it
// carries the holder and makes one on first use, so a view built outside
// the root still has somewhere to write.
type statusPoster struct{ sl *statusHolder }

func (p *statusPoster) setStatusHolder(h *statusHolder) { p.sl = h }

// status is the line this view posts to.
func (p *statusPoster) status() *statusHolder {
	if p.sl == nil {
		p.sl = newStatusHolder()
	}
	return p.sl
}
