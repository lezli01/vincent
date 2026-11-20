package tui

import (
	"context"
	"errors"
	"path/filepath"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/chatexport"
	"github.com/lezli01/vincent/internal/fileswap"
)

// The export form (task 159, §15): a root popup that exports one chat to a
// file. The daemon renders the document (GET /v1/chats/{id}/export) and the
// TUI only writes the bytes, because the file is the client's — which is
// also why the path is resolved against the TUI's own working directory.
//
// It lives on the root beside the project picker rather than in either view
// that opens it, so the chat workspace and the chats board share one form,
// and it owns the keyboard while it is up: the chat composer cannot see a key
// typed into the path field.

// exportFilePerm is the written file's mode: a document meant to be shared,
// written as an editor would write it.
const exportFilePerm = 0o644

// exportTimeout bounds one fetch: a diff is read through the worktree and
// can be large, so it is the diff pane's budget rather than a plain call's.
const exportTimeout = diffTimeout

var errEmptyExportPath = errors.New("the path is empty")

// openChatExportMsg asks the root to open the form for one chat.
type openChatExportMsg struct {
	id    int64
	title string
}

func openChatExportCmd(id int64, title string) tea.Cmd {
	return func() tea.Msg { return openChatExportMsg{id: id, title: title} }
}

// chatExportDoneMsg is a fetch-and-write's outcome. seq names the attempt so
// an answer for a form since closed or resubmitted is dropped.
type chatExportDoneMsg struct {
	seq  int
	path string
	body []byte
	err  error
}

// The form's rows, in order.
const (
	exportRowFormat = iota
	exportRowDiff
	exportRowPath
	exportRowCount
)

// exportFormats is the format row's cycle.
var exportFormats = []chatexport.Format{chatexport.FormatMarkdown, chatexport.FormatHTML, chatexport.FormatJSON}

// chatExportForm is the popup.
type chatExportForm struct {
	id    int64
	title string
	// cwd is the TUI's working directory, which a relative path is resolved
	// against; empty when it could not be read, leaving the process's own.
	cwd      string
	format   int
	withDiff bool
	path     textField
	// pathEdited records that the human typed into the path, after which a
	// format change swaps only its extension rather than re-suggesting it.
	pathEdited bool
	row        int

	busy bool
	seq  int
	err  error
	// written is the path the last export landed at; body is what was
	// written, kept for the copy-the-Markdown choice. While written is set
	// the form offers its copy choices and choice is the highlighted one.
	written string
	body    []byte
	choice  int
}

func newChatExportForm(id int64, title, cwd string) *chatExportForm {
	f := &chatExportForm{id: id, title: title, cwd: cwd, row: exportRowFormat, path: newTextField()}
	f.path.SetValue(f.suggested())
	f.path.CursorEnd()
	return f
}

func (f *chatExportForm) chosen() chatexport.Format { return exportFormats[f.format] }

// suggested is the prefilled path: `./chat-<id>-<slug>.<ext>`, the name the
// daemon's Content-Disposition suggests, in the working directory.
func (f *chatExportForm) suggested() string {
	return "." + string(filepath.Separator) + chatexport.Filename(f.id, f.title, f.chosen())
}

// cycleFormat moves the format and keeps the path's extension in step.
func (f *chatExportForm) cycleFormat(step int) {
	f.format = (f.format + step + len(exportFormats)) % len(exportFormats)
	if !f.pathEdited {
		f.path.SetValue(f.suggested())
		f.path.CursorEnd()
		return
	}
	p := f.path.Value()
	ext := filepath.Ext(p)
	for _, known := range exportFormats {
		if strings.EqualFold(ext, "."+known.Ext()) {
			f.path.SetValue(strings.TrimSuffix(p, ext) + "." + f.chosen().Ext())
			f.path.CursorEnd()
			return
		}
	}
}

// resolved is the path the file is written to.
func (f *chatExportForm) resolved() string {
	p := strings.TrimSpace(f.path.Value())
	if p == "" || filepath.IsAbs(p) || f.cwd == "" {
		return p
	}
	return filepath.Join(f.cwd, p)
}

func (f *chatExportForm) setRow(r int) tea.Cmd {
	f.row = (r + exportRowCount) % exportRowCount
	if f.row == exportRowPath {
		return f.path.Focus()
	}
	f.path.Blur()
	return nil
}

// copyChoices are the offers once the file is written: its path, and for
// Markdown the document itself.
func (f *chatExportForm) copyChoices() []string {
	if f.chosen() == chatexport.FormatMarkdown {
		return []string{"copy the path", "copy the Markdown"}
	}
	return []string{"copy the path"}
}

// update handles one key. done reports the form should close; cmd is the
// work to run.
func (f *chatExportForm) update(msg tea.KeyPressMsg, client *apiclient.Client) (done bool, cmd tea.Cmd) {
	if f.written != "" {
		return f.updateWritten(msg)
	}
	switch msg.String() {
	case "esc":
		return true, nil
	case "enter":
		return false, f.submit(client)
	case "tab", "down":
		return false, f.setRow(f.row + 1)
	case "shift+tab", "up":
		return false, f.setRow(f.row - 1)
	}
	switch f.row {
	case exportRowFormat:
		switch msg.String() {
		case "left":
			f.cycleFormat(-1)
		case "right", "space":
			f.cycleFormat(1)
		}
		return false, nil
	case exportRowDiff:
		switch msg.String() {
		case "left", "right", "space":
			f.withDiff = !f.withDiff
		}
		return false, nil
	}
	before := f.path.Value()
	var c tea.Cmd
	f.path, c = f.path.Update(msg)
	if f.path.Value() != before {
		f.pathEdited = true
	}
	return false, c
}

// updateWritten is the form after a successful write: its copy choices.
func (f *chatExportForm) updateWritten(msg tea.KeyPressMsg) (bool, tea.Cmd) {
	n := len(f.copyChoices())
	switch msg.String() {
	case "esc":
		return true, nil
	case "up", "shift+tab":
		f.choice = (f.choice - 1 + n) % n
	case "down", "tab":
		f.choice = (f.choice + 1) % n
	case "enter":
		if f.choice == 1 {
			return true, writeClipboardCmd("markdown", string(f.body))
		}
		return true, writeClipboardCmd("path", f.written)
	}
	return false, nil
}

// paste types into the path field when it has the focus.
func (f *chatExportForm) paste(text string) tea.Cmd {
	if f.written != "" || f.row != exportRowPath {
		return nil
	}
	var cmd tea.Cmd
	f.path, cmd = f.path.Update(tea.PasteMsg{Content: text})
	f.pathEdited = true
	return cmd
}

// submit fetches the export and writes it. The write is atomic (fileswap),
// since a half-written export is worse than none.
func (f *chatExportForm) submit(client *apiclient.Client) tea.Cmd {
	path := f.resolved()
	if path == "" {
		f.err = errEmptyExportPath
		return nil
	}
	if client == nil || f.busy {
		return nil
	}
	f.busy, f.err = true, nil
	f.seq++
	seq, id := f.seq, f.id
	opts := apiclient.ChatExportOptions{Format: string(f.chosen()), WithDiff: f.withDiff}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), exportTimeout)
		defer cancel()
		exp, err := client.ChatExport(ctx, id, opts)
		if err != nil {
			return chatExportDoneMsg{seq: seq, path: path, err: err}
		}
		if err := fileswap.WriteFile(path, exp.Body, exportFilePerm); err != nil {
			return chatExportDoneMsg{seq: seq, path: path, err: err}
		}
		return chatExportDoneMsg{seq: seq, path: path, body: exp.Body}
	}
}

// land records a fetch-and-write's outcome.
func (f *chatExportForm) land(msg chatExportDoneMsg) {
	if msg.seq != f.seq {
		return
	}
	f.busy = false
	if msg.err != nil {
		f.err = msg.err
		return
	}
	f.written, f.body, f.choice = msg.path, msg.body, 0
	f.path.Blur()
}

// render draws the form box for overlaying.
func (f *chatExportForm) render(w, h int) string {
	inner := max(w-2, 10)
	lines := make([]string, 0, h)
	lines = append(lines, styleDim.Render(ansi.Truncate("  chat "+strconv.FormatInt(f.id, 10)+" "+f.title, inner, "…")), "")
	mark := func(r int) string {
		if f.written == "" && f.row == r {
			return styleFocus.Render("› ")
		}
		return "  "
	}
	formats := make([]string, 0, len(exportFormats))
	for i, x := range exportFormats {
		if i == f.format {
			formats = append(formats, styleTitle.Render("["+string(x)+"]"))
		} else {
			formats = append(formats, styleDim.Render(" "+string(x)+" "))
		}
	}
	lines = append(lines, mark(exportRowFormat)+"format        "+strings.Join(formats, " "))
	diff := "[ ] off"
	if f.withDiff {
		diff = "[x] on"
	}
	lines = append(lines, mark(exportRowDiff)+"include diff  "+diff)
	f.path.SetWidth(max(inner-16, 1))
	lines = append(lines, indentRows(mark(exportRowPath)+"path          ", f.path.rows())...)
	lines = append(lines, "")
	switch {
	case f.busy:
		lines = append(lines, styleDim.Render("  exporting…"))
	case f.err != nil:
		lines = append(lines, styleBad.Render(ansi.Truncate("  export failed: "+f.err.Error(), inner, "…")),
			styleDim.Render("  fix the path and press enter to try again · esc closes"))
	case f.written != "":
		lines = append(lines, styleOK.Render(ansi.Truncate("  wrote "+f.written, inner, "…")))
		for i, c := range f.copyChoices() {
			if i == f.choice {
				lines = append(lines, styleFocus.Render("› ")+styleTitle.Render(c))
			} else {
				lines = append(lines, "  "+styleDim.Render(c))
			}
		}
		lines = append(lines, styleDim.Render("  enter copies · esc closes"))
	default:
		lines = append(lines, styleDim.Render("  ↑/↓ move · space changes · enter exports · esc closes"))
	}
	return frame("export chat", strings.Join(lines, "\n"), w, h, true)
}
