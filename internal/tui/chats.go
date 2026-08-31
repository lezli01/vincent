package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
)

// The chats board (§15, task 067, closing task 063.2).
//
// It is a second board, not a filter on the first. A chat runs no workflow,
// waits for no scheduler slot and has no §6 action set, so it shares none of
// the task board's columns and none of its keys beyond the four every table
// in this TUI has. Decision row 29 says a chat never appears on the task
// board; this is where it appears instead.
//
// Attention is this board's own: an awaiting_input chat is sorted to the top
// and badged in this header, and nowhere else. `!` and the home board's
// needs-attention count stay task-only (task 067 decision 4).

// Chats board messages.
type (
	chatsRefreshMsg struct{}
	// chatsLoadedMsg is one whole board: every chat, plus the project names
	// the headings read from.
	chatsLoadedMsg struct {
		chats []apiclient.Chat
		names map[int64]string
		// projectsListed reports that the load reached the project
		// registry, so an empty names map means "none registered" rather
		// than "not asked yet" or "the listing failed".
		projectsListed bool
		err            error
	}
	// chatArchivedMsg reports a completed archive, or the refusal that came
	// back instead — a dirty worktree answers 409 and is offered the force.
	chatArchivedMsg struct {
		id  int64
		err error
	}
	// openChatMsg asks the root to open one chat's workspace. It goes through
	// the root rather than straight to the view because the view is not
	// active yet, and an inactive view receives nothing.
	openChatMsg struct{ id int64 }
)

// archivePrompt is the inline confirmation an archive takes. force is set on
// the second pass, after the daemon refused a dirty worktree.
type archivePrompt struct {
	id    int64
	title string
	force bool
	text  string
}

// chatsView is the chats board.
type chatsView struct {
	client  *apiclient.Client
	now     func() time.Time
	dataDir string

	chats []apiclient.Chat
	names map[int64]string
	// projectsListed is what makes an empty names map readable: only a load
	// that actually reached the project registry may be quoted as "there is
	// no project here".
	projectsListed bool

	loaded   bool
	loading  bool
	lastLoad time.Time
	loadErr  string

	cursor     int
	selectedID int64

	filter    textinput.Model
	filtering bool

	folds   foldSet
	confirm *archivePrompt

	// create is the new-chat form, a layer over this board rather than a
	// seventh view: it is opened from here, it returns here, and it has no
	// meaning anywhere else. `n` on the chats board makes a chat; `n`
	// everywhere else still makes a task.
	create *newChatForm

	note    string
	noteBad bool

	width, height int
}

func newChatsView() *chatsView {
	fi := textinput.New()
	fi.Placeholder = "filter by title, agent or branch"
	fi.Prompt = "/"
	return &chatsView{now: time.Now, filter: fi, names: map[int64]string{}}
}

func (v *chatsView) title() string { return "Chats" }

func (v *chatsView) setClient(c *apiclient.Client) tea.Cmd {
	v.client = c
	return v.loadCmd()
}

func (v *chatsView) setDataDir(dir string) {
	v.dataDir = dir
	v.folds = loadChatFolds(dir)
}

func (v *chatsView) capturesInput() bool {
	if v.create != nil {
		return v.create.capturesInput()
	}
	return v.filtering
}

func (v *chatsView) paste(text string) tea.Cmd {
	if v.create != nil {
		return v.create.paste(text)
	}
	if !v.filtering {
		return nil
	}
	var cmd tea.Cmd
	v.filter, cmd = v.filter.Update(tea.PasteMsg{Content: text})
	return cmd
}

// bindingContext names the layer that has the keyboard, so the footer and the
// ? overlay describe the keys that actually work.
func (v *chatsView) bindingContext() bindingContext {
	if v.create != nil {
		return ctxNewChat
	}
	return ctxChats
}

// hintedProject opens the new-chat form on the project the cursor stands in.
func (v *chatsView) hintedProject() int64 {
	if c, ok := v.current(); ok {
		return c.ProjectID
	}
	return 0
}

func (v *chatsView) update(msg tea.Msg) (panel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width, v.height = msg.Width, msg.Height
		return v, nil
	case viewActivatedMsg:
		if msg.id == viewChats {
			// A board that was off-screen through a burst of events opens on
			// what it last fetched; refetching on activation is what keeps
			// "off-screen" from meaning "stale".
			return v, v.loadCmd()
		}
		return v, nil
	case chatsRefreshMsg:
		return v, v.loadCmd()
	case chatsLoadedMsg:
		v.applyLoaded(msg)
		return v, nil
	case newChatFieldsMsg:
		// The new-chat form's own fetch landing. The form is a layer over
		// this board rather than a view, so it has no `update` for anything
		// but keys: this is its only message entry point, and without this
		// case the projects the daemon listed are dropped and both pickers
		// stay empty for the life of the form (issue #279). A draft
		// discarded before its fetch landed must not be resurrected by it,
		// so a closed form drops the message rather than reopening.
		if v.create != nil {
			v.create.applyFields(msg)
		}
		return v, nil
	case chatArchivedMsg:
		return v, v.applyArchived(msg)
	case chatCreatedMsg:
		return v, v.applyCreated(msg)
	case noteMsg:
		// A chat.* event is this board's reconciler tick. Task events are
		// none of its business and are dropped here, which is the same
		// separation in the other direction as the task board dropping
		// chat.* — one entity per board, both ways (decision row 29).
		return v, v.applyNote(msg)
	case tea.KeyPressMsg:
		return v.updateKey(msg)
	}
	return v, nil
}

// applyNote reloads when a chat event says the board changed.
func (v *chatsView) applyNote(msg noteMsg) tea.Cmd {
	ev, ok := msg.note.(apiclient.EventNote)
	if !ok || !strings.HasPrefix(ev.Event.Type, "chat.") {
		return nil
	}
	return v.loadCmd()
}

// loadCmd fetches every chat and the project names the headings read from.
func (v *chatsView) loadCmd() tea.Cmd {
	client := v.client
	if client == nil {
		return nil
	}
	v.loading = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		chats, err := client.ListChats(ctx, 0)
		if err != nil {
			return chatsLoadedMsg{err: err}
		}
		names := map[int64]string{}
		listed := false
		// A project listing that fails is not a board that fails: the
		// headings fall back to the id, which is still a stable grouping.
		if projects, perr := client.ListProjects(ctx); perr == nil {
			listed = true
			for _, p := range projects {
				names[p.ID] = p.Name
			}
		}
		return chatsLoadedMsg{chats: chats, names: names, projectsListed: listed}
	}
}

func (v *chatsView) applyLoaded(msg chatsLoadedMsg) {
	v.loading = false
	if msg.err != nil {
		v.loadErr = errString(msg.err)
		return
	}
	v.loadErr = ""
	v.loaded = true
	v.lastLoad = v.now()
	sortChats(msg.chats)
	v.chats, v.names = msg.chats, msg.names
	v.projectsListed = msg.projectsListed
	v.folds = pruneChatFolds(v.folds, v.chats, v.names)
	v.restoreSelection()
}

func (v *chatsView) applyArchived(msg chatArchivedMsg) tea.Cmd {
	if msg.err != nil {
		// A dirty worktree is a refusal to re-offer with the force, not an
		// error to swallow: the same shape the task board's archive takes.
		if isChatWorktreeDirty(msg.err) {
			v.confirm = &archivePrompt{
				id: msg.id, force: true,
				text: "worktree has local changes — archive anyway and lose them? (y/n)",
			}
			return nil
		}
		v.note, v.noteBad = errString(msg.err), true
		return nil
	}
	v.note, v.noteBad = "chat archived", false
	return v.loadCmd()
}

func (v *chatsView) applyCreated(msg chatCreatedMsg) tea.Cmd {
	if msg.err != nil {
		if v.create != nil {
			v.create.applyFailure(msg.err)
		}
		return nil
	}
	v.create = nil
	v.selectedID = msg.chat.ID
	// Straight into the workspace: the human asked for a conversation, and
	// landing them on the board to press enter on the row they just made
	// would be a step for nothing.
	id := msg.chat.ID
	return tea.Batch(v.loadCmd(), func() tea.Msg { return openChatMsg{id: id} })
}

// rows is the board as it is currently laid out.
func (v *chatsView) rows() []chatRow {
	return groupChatRows(filterChats(v.chats, v.filter.Value()), v.names, v.folds)
}

func (v *chatsView) current() (apiclient.Chat, bool) {
	rows := v.rows()
	if v.cursor < 0 || v.cursor >= len(rows) || rows[v.cursor].chat == nil {
		return apiclient.Chat{}, false
	}
	return *rows[v.cursor].chat, true
}

func (v *chatsView) restoreSelection() {
	rows := v.rows()
	if v.selectedID != 0 {
		for i, r := range rows {
			if r.chat != nil && r.chat.ID == v.selectedID {
				v.cursor = i
				return
			}
		}
	}
	v.cursor = min(v.cursor, max(len(rows)-1, 0))
	v.moveCursor(0)
}

// moveCursor walks by delta, skipping the headings that cannot be selected.
// delta 0 lands on the nearest selectable row, which is what a re-layout
// needs after a fold.
func (v *chatsView) moveCursor(delta int) {
	rows := v.rows()
	if len(rows) == 0 {
		v.cursor = 0
		return
	}
	i := min(max(v.cursor+delta, 0), len(rows)-1)
	step := 1
	if delta < 0 {
		step = -1
	}
	for j := i; j >= 0 && j < len(rows); j += step {
		if rows[j].selectable() {
			v.cursor = j
			v.rememberSelection()
			return
		}
	}
	// Nothing selectable in that direction: stay where the walk started.
	for j := i; j >= 0 && j < len(rows); j -= step {
		if rows[j].selectable() {
			v.cursor = j
			v.rememberSelection()
			return
		}
	}
	v.cursor = i
}

func (v *chatsView) rememberSelection() {
	if c, ok := v.current(); ok {
		v.selectedID = c.ID
	}
}

// saveFolds persists the chats board's own fold set. A write failure is a
// note, never a refusal to fold: the fold already happened on screen.
func (v *chatsView) saveFolds() tea.Cmd {
	dir, folds := v.dataDir, v.folds
	if dir == "" {
		return nil
	}
	return func() tea.Msg {
		if err := writeChatFolds(dir, folds); err != nil {
			return noteMsg{}
		}
		return nil
	}
}

// isChatWorktreeDirty reports the 409 an archive gets when the chat's
// worktree has local changes (§10). It is the same refusal a task's archive
// takes, read off the same `reason` detail — the block-reason vocabulary is
// shared, so the client reads one name here too.
func isChatWorktreeDirty(err error) bool {
	var apiErr *apiclient.Error
	return errors.As(err, &apiErr) && apiErr.Details["reason"] == "worktree_dirty"
}

// updateKey is the chats board's keyboard. The layers answer in order: the
// new-chat form, the confirmation, the filter, then the board itself.
func (v *chatsView) updateKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	if v.create != nil {
		cmd, done := v.create.update(msg, v.client)
		if done {
			v.create = nil
		}
		return v, cmd
	}
	if v.confirm != nil {
		return v, v.updateConfirm(msg)
	}
	if v.filtering {
		return v, v.updateFilter(msg)
	}
	v.note = ""
	switch msg.String() {
	case "esc":
		if v.filter.Value() != "" {
			v.filter.SetValue("")
			v.restoreSelection()
			return v, nil
		}
		return v, func() tea.Msg { return selectViewMsg{id: viewHome} }
	case "up", "k":
		v.moveCursor(-1)
	case "down", "j":
		v.moveCursor(1)
	case "/":
		v.filtering = true
		v.filter.Focus()
		return v, nil
	case "left":
		return v, v.collapseAtCursor()
	case "right":
		return v, v.expandAtCursor()
	case "enter":
		if c, ok := v.current(); ok {
			id := c.ID
			return v, func() tea.Msg { return openChatMsg{id: id} }
		}
	case "n":
		// A chat needs a project, and the form offers no way to register
		// one: opening it on an installation that has none is a dead end
		// whose only exit is `esc` (issue #279). Refuse only on a positive
		// answer — a board that has not listed the projects yet, or whose
		// listing failed, knows nothing and opens the form as before.
		if v.projectsListed && len(v.names) == 0 {
			v.note, v.noteBad = "register a project first — the Projects view (4) adds one", true
			return v, nil
		}
		v.create = newNewChatForm(v.client, v.hintedProject())
		return v, v.create.init()
	case "a":
		if c, ok := v.current(); ok {
			v.confirm = &archivePrompt{
				id: c.ID, title: c.Title,
				text: fmt.Sprintf("archive %q and remove its worktree? (y/n)", c.Title),
			}
		}
	case "r":
		return v, v.loadCmd()
	}
	return v, nil
}

func (v *chatsView) updateConfirm(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "y", "Y":
		p := *v.confirm
		v.confirm = nil
		return v.archiveCmd(p.id, p.force)
	default:
		v.confirm = nil
		return nil
	}
}

func (v *chatsView) updateFilter(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "enter":
		v.filtering = false
		v.filter.Blur()
		v.restoreSelection()
		return nil
	case "esc":
		v.filtering = false
		v.filter.Blur()
		v.filter.SetValue("")
		v.restoreSelection()
		return nil
	}
	var cmd tea.Cmd
	v.filter, cmd = v.filter.Update(msg)
	v.restoreSelection()
	return cmd
}

func (v *chatsView) archiveCmd(id int64, force bool) tea.Cmd {
	client := v.client
	if client == nil {
		v.note, v.noteBad = "not connected", true
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		_, err := client.ArchiveChat(ctx, id, force)
		return chatArchivedMsg{id: id, err: err}
	}
}

// collapseAtCursor folds the group the cursor is in, naming the row rather
// than an index: the layout changes under the fold, and reading a cursor
// index from the old layout against the new one selects a different chat.
func (v *chatsView) collapseAtCursor() tea.Cmd {
	rows := v.rows()
	if v.cursor < 0 || v.cursor >= len(rows) {
		return nil
	}
	path := rows[v.cursor].path
	if len(path) == 0 || v.folds.has(path) {
		return nil
	}
	v.folds = v.folds.with(path)
	v.focusHeader(path)
	return v.saveFolds()
}

func (v *chatsView) expandAtCursor() tea.Cmd {
	rows := v.rows()
	if v.cursor < 0 || v.cursor >= len(rows) {
		return nil
	}
	path := rows[v.cursor].path
	if len(path) == 0 || !v.folds.has(path) {
		return nil
	}
	v.folds = v.folds.without(path)
	v.focusHeader(path)
	return v.saveFolds()
}

// focusHeader puts the cursor back on the heading it was working on, in the
// layout the fold produced.
func (v *chatsView) focusHeader(path foldPath) {
	for i, r := range v.rows() {
		if r.header && r.path.equal(path) {
			v.cursor = i
			return
		}
	}
	v.restoreSelection()
}
