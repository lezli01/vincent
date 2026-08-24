package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/table"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
)

const (
	// refreshDebounce coalesces the burst of events a busy daemon emits into
	// one refetch. Loopback SQLite is cheap, but re-rendering per event is
	// visible flicker.
	refreshDebounce = 150 * time.Millisecond
	// elapsedTick re-renders so the elapsed column counts up. It fetches
	// nothing — the board's data comes from events (§13.3, T3.2 done-when:
	// live updates without polling).
	elapsedTick = time.Second
	// bellInterval is the minimum gap between two terminal bells. Several
	// tasks entering awaiting_input at once is one interruption, not five.
	bellInterval = time.Second
)

// Board messages.
type (
	// boardRefreshMsg fires when the debounce window closes.
	boardRefreshMsg struct{}
	// boardLoadedMsg carries a completed task fetch. seq orders concurrent
	// fetches: commands run on their own goroutines, so an older response
	// can land after a newer one and must not clobber it (zero = untracked,
	// for tests that build the message directly).
	boardLoadedMsg struct {
		seq   uint64
		tasks []apiclient.Task
		err   error
	}
	// boardInfoMsg carries the daemon info the header renders.
	boardInfoMsg struct {
		info apiclient.Info
		err  error
	}
	// boardConfigMsg carries the view preferences the daemon relays (§15,
	// task 009). The TUI reads no configuration from disk, so this is where
	// the task table's grouping comes from.
	boardConfigMsg struct {
		board apiclient.ConfigBoard
		err   error
	}
	// boardTickMsg drives the elapsed column.
	boardTickMsg time.Time
	// selectTaskMsg asks the home shell to select a task in the table and
	// open it in the detail panels immediately — the explicit path, used by
	// task creation. Cursor movement goes through the settle window instead.
	selectTaskMsg struct{ id int64 }
)

// board is the §15 home view: every task, live.
type board struct {
	client *apiclient.Client
	// now is injected so elapsed rendering is deterministic under test.
	now func() time.Time

	tasks   []apiclient.Task
	info    apiclient.Info
	infoOK  bool
	loaded  bool
	loadErr error
	// lastLoad stamps the newest successful fetch, so a stale board says how
	// stale it is rather than silently lying.
	lastLoad time.Time

	tbl        table.Model
	selectedID int64
	// laneParent drills the board into one fan-out parent's lanes (§7.6,
	// task 014). Zero is the ordinary board, which hides lanes entirely
	// (decision 13); non-zero shows exactly that parent's lanes, in merge
	// order. It is a lens on the same table rather than a second view: the
	// rows are ordinary tasks and every action key means what it always does.
	laneParent int64

	// marks is the bulk selection (boardmark.go, task 011): the tasks the §6
	// action keys act on instead of the row under the cursor.
	marks markSet

	filter    textinput.Model
	filtering bool

	// group is the grouping the table is rendering (boardgroup.go);
	// configGroup is what the daemon's config asked for. They differ once `g`
	// has been pressed, and groupPinned is what keeps a reconnect's refetch
	// from undoing that press — the config is the starting point, not a
	// setting the daemon re-imposes while someone is looking at the board.
	group       grouping
	configGroup grouping
	groupPinned bool

	// actions drives §15's action keys against the row under the cursor.
	// The shell shares one instance between board and detail — a pending
	// confirmation is the same question wherever the eye lands — and the
	// footer renders it (T3.12).
	actions *actionBar

	refreshPending bool
	ticking        bool

	// loadSeq stamps outgoing fetches; appliedSeq is the newest one
	// installed. A response older than what is on screen is dropped.
	loadSeq    uint64
	appliedSeq uint64

	// bell rings the terminal. Injected so tests can count rings; the
	// default writes BEL straight to stdout, because tea.Printf is
	// unmanaged scrollback output and a no-op under altscreen.
	bell func()
	// lastBellEvent is the highest event id that has rung. Event ids are
	// monotonic, so this alone makes a Last-Event-ID replay silent while a
	// genuinely missed transition still rings.
	lastBellEvent int64
	lastBellAt    time.Time

	width, height int
}

func newBoard() *board {
	fi := textinput.New()
	fi.Placeholder = "filter by id, title, project or state"
	fi.Prompt = "/"
	b := &board{
		now:         time.Now,
		filter:      fi,
		bell:        ringBell,
		actions:     &actionBar{},
		tbl:         table.New(table.WithFocused(true)),
		group:       defaultGrouping(),
		configGroup: defaultGrouping(),
	}
	b.applyStyles()
	return b
}

// applyStyles sets the table styling. Selection is a background, not a
// foreground: renderRow wraps the whole row in the Selected style and
// lipgloss does not rewrite the per-cell colour sequences nested inside it,
// so a selected foreground would simply lose to the state colour.
func (b *board) applyStyles() {
	s := table.DefaultStyles()
	s.Header = s.Header.Bold(true)
	s.Selected = tableSelectedStyle()
	b.tbl.SetStyles(s)
}

// tableSelectedStyle is shared by every table in the TUI so a cursor looks
// the same wherever it is.
func tableSelectedStyle() lipgloss.Style {
	return lipgloss.NewStyle().
		Background(lipgloss.Color("237")).
		Bold(true)
}

func ringBell() {
	// Errors are deliberately ignored: a terminal that will not take a BEL
	// is not a reason to disturb the render loop.
	_, _ = os.Stdout.WriteString("\a")
}

func (b *board) title() string { return "Board" }

// setClient wires the board to a connected daemon and kicks off its initial
// load. Called again on reconnect, which is why the tick is guarded.
func (b *board) setClient(c *apiclient.Client) tea.Cmd {
	b.client = c
	cmds := []tea.Cmd{b.loadCmd(), b.infoCmd(), b.configCmd()}
	if !b.ticking {
		b.ticking = true
		cmds = append(cmds, tickCmd())
	}
	return tea.Batch(cmds...)
}

func tickCmd() tea.Cmd {
	return tea.Tick(elapsedTick, func(t time.Time) tea.Msg { return boardTickMsg(t) })
}

func (b *board) loadCmd() tea.Cmd {
	client := b.client
	if client == nil {
		return nil
	}
	b.loadSeq++
	seq := b.loadSeq
	parent := b.laneParent
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		tasks, err := client.ListTasks(ctx, apiclient.ListTasksOptions{ParentID: parent})
		return boardLoadedMsg{seq: seq, tasks: tasks, err: err}
	}
}

func (b *board) infoCmd() tea.Cmd {
	client := b.client
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		info, err := client.Info(ctx)
		return boardInfoMsg{info: info, err: err}
	}
}

// configCmd fetches the view preferences (§12.3 `tui:`). It rides the connect
// and every reconnect, which is also how a config the human edited while the
// TUI was up reaches the board: the daemon hot-reloads the file but publishes
// no event for it (§13.3), so there is nothing to subscribe to.
func (b *board) configCmd() tea.Cmd {
	client := b.client
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		cfg, err := client.Config(ctx)
		return boardConfigMsg{board: cfg.TUI.Board, err: err}
	}
}

// applyConfig installs the configured grouping. A failed fetch changes
// nothing: the default grouping is a working board, and a config request that
// timed out is not a statement that the human wants a flat table.
func (b *board) applyConfig(msg boardConfigMsg) {
	if msg.err != nil {
		return
	}
	b.configGroup = parseGrouping(msg.board.GroupBy)
	if !b.groupPinned {
		b.group = b.configGroup
	}
}

// scheduleRefresh opens a debounce window, or does nothing if one is open.
func (b *board) scheduleRefresh() tea.Cmd {
	if b.refreshPending {
		return nil
	}
	b.refreshPending = true
	return tea.Tick(refreshDebounce, func(time.Time) tea.Msg { return boardRefreshMsg{} })
}

func (b *board) update(msg tea.Msg) (panel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		b.width, b.height = msg.Width, msg.Height
		return b, nil
	case boardLoadedMsg:
		b.updateLoaded(msg)
		return b, nil
	case boardInfoMsg:
		if msg.err == nil {
			b.info, b.infoOK = msg.info, true
		}
		return b, nil
	case boardConfigMsg:
		b.applyConfig(msg)
		return b, nil
	case boardRefreshMsg:
		b.refreshPending = false
		return b, b.loadCmd()
	case boardTickMsg:
		// Render-only: the elapsed column advances without asking the daemon
		// anything.
		return b, tickCmd()
	case noteMsg:
		return b, b.updateNote(msg.note)
	case actionResultMsg:
		b.actions.applyResult(msg)
		// Refetch immediately rather than through the debounce: a 409 means
		// the row was stale, and leaving it stale invites the same keypress.
		b.refreshPending = false
		return b, b.loadCmd()
	case bulkResultMsg:
		b.actions.applyBulkResult(msg)
		// What the daemon accepted leaves the selection; what it refused stays
		// marked, so a force re-ask or a retry needs no re-selection (task 011).
		b.marks = b.marks.drop(msg.done...)
		b.refreshPending = false
		return b, b.loadCmd()
	case tea.KeyPressMsg:
		return b.updateKey(msg)
	}
	return b, nil
}

// target is what the action bar acts on: the row under the cursor, carrying
// the bulk selection when there is one (task 011). The cursor row travels even
// then — it is what the confirmation for a single task would name, and what the
// palette titles its action section with.
func (b *board) target() taskActions {
	marked := b.markedTargets()
	id, ok := b.selected()
	if !ok {
		return taskActions{marked: marked}
	}
	for _, t := range b.visible() {
		if t.ID == id {
			return taskActions{id: t.ID, state: t.State, actions: t.AvailableActions, marked: marked}
		}
	}
	return taskActions{id: id, marked: marked}
}

func (b *board) updateLoaded(msg boardLoadedMsg) {
	if msg.seq != 0 && msg.seq <= b.appliedSeq {
		return // a slower, older fetch landing after a newer one
	}
	if msg.err != nil {
		// Keep the rows already on screen. A failed refresh is not a lost
		// connection, and blanking a board full of running work because one
		// request failed destroys the view exactly when it matters.
		b.loadErr = msg.err
		return
	}
	b.loadErr = nil
	b.loaded = true
	b.lastLoad = b.now()
	b.tasks = msg.tasks
	b.appliedSeq = msg.seq
	// A mark for a task the daemon no longer lists — archived away, or whose
	// project was removed — would be counted in the panel title and dispatched
	// to a 404. Only a *successful* load prunes: a failed refresh is not news
	// about which tasks exist.
	b.marks = b.marks.keep(msg.tasks)
}

// updateNote reacts to the event stream. Every task event schedules a
// refetch; entering awaiting_input additionally rings the bell.
func (b *board) updateNote(n apiclient.Note) tea.Cmd {
	ev, ok := n.(apiclient.EventNote)
	if !ok {
		return nil
	}
	var cmds []tea.Cmd
	if b.ringsFor(ev.Event) {
		bell := b.bell
		cmds = append(cmds, func() tea.Msg {
			bell()
			return nil
		})
	}
	if isTaskEvent(ev.Event.Type) {
		cmds = append(cmds, b.scheduleRefresh())
	}
	if ev.Event.Type == eventAgentQuotaChanged {
		// The header's agent badges come from /v1/info, not from the task
		// list, so this refetches that rather than the rows (task 026).
		cmds = append(cmds, b.infoCmd())
	}
	return tea.Batch(cmds...)
}

func isTaskEvent(t string) bool {
	return strings.HasPrefix(t, "task.") || strings.HasPrefix(t, "project.")
}

// eventWorkflowRegistryChanged is the durable event the daemon writes after
// any registry scope reloads (§13.3). Its payload is empty — it names no
// scope — so the only honest reaction is to refetch the whole registry.
const eventWorkflowRegistryChanged = "workflow.registry_changed"

// ringsFor decides whether one event should ring the terminal bell.
//
// Only a transition *into* awaiting_input rings, and only once per event id:
// ids are monotonic, so a Last-Event-ID replay after a reconnect is silent,
// while a transition that genuinely happened while disconnected still rings
// — being told about work that started waiting while you were away is the
// point. The rate limit collapses a burst into one interruption.
func (b *board) ringsFor(ev apiclient.Event) bool {
	if ev.Type != "task.state_changed" || ev.ID <= b.lastBellEvent {
		return false
	}
	var payload struct {
		To string `json:"to"`
	}
	if err := json.Unmarshal(ev.Payload, &payload); err != nil {
		return false
	}
	if payload.To != stateAwaitingInput {
		return false
	}
	// The id advances even when the rate limit swallows the ring, so a
	// later replay of this event cannot ring either.
	b.lastBellEvent = ev.ID
	now := b.now()
	if now.Sub(b.lastBellAt) < bellInterval {
		return false
	}
	b.lastBellAt = now
	return true
}

func (b *board) updateKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	if b.filtering {
		switch msg.String() {
		case "esc":
			b.filtering = false
			b.filter.SetValue("")
			b.filter.Blur()
			return b, nil
		case "enter":
			b.filtering = false
			b.filter.Blur()
			return b, nil
		}
		var cmd tea.Cmd
		b.filter, cmd = b.filter.Update(msg)
		return b, cmd
	}

	// A confirmation owns the keyboard until it is answered.
	if b.actions.capturing() {
		cmd, _ := b.actions.handleKey(msg.String(), b.client, b.target())
		return b, cmd
	}

	switch msg.String() {
	case "/":
		b.filtering = true
		b.filter.Focus()
		return b, nil
	case "esc":
		if b.filter.Value() != "" {
			b.filter.SetValue("")
		}
		return b, nil
	case "space":
		// Mark the row for a bulk action (§15, task 011). The cursor does not
		// move: `space` is a statement about this row, and a human marking a
		// run of rows presses down themselves — auto-advancing would make
		// unmarking a mis-press land on the wrong row.
		b.toggleMark()
		return b, nil
	case "V":
		b.markVisible()
		return b, nil
	case "L":
		// Drill into the selected fan-out parent's lanes, or back out.
		// Lanes are hidden from the board by design; this is the way in.
		if b.laneParent != 0 {
			parent := b.laneParent
			b.laneParent = 0
			b.selectedID = parent
			return b, b.loadCmd()
		}
		if id, ok := b.selected(); ok {
			for _, t := range b.tasks {
				if t.ID == id && t.State == stateAwaitingChildren {
					b.laneParent = id
					b.selectedID = 0
					return b, b.loadCmd()
				}
			}
		}
		return b, nil
	case "g":
		// Cycles the grouping for the session (§15, task 009); the config
		// file stays the starting point and is never written from here. This
		// takes `g` from the table's own undocumented go-to-top alias — `home`
		// still does that, and the registry is what the help promises.
		//
		// The cursor is not touched: selectedID already names the task under
		// it, and the next render puts the cursor back on that task wherever
		// the new layout moved it. Re-reading the cursor here would read an
		// index from the old layout against the new one.
		b.group = b.group.next()
		b.groupPinned = true
		return b, nil
	}

	// §15's action keys act on the row under the cursor. A key the daemon
	// does not offer for this task falls through to the table, so j/k keep
	// moving on a row that cannot be skipped.
	if cmd, handled := b.actions.handleKey(msg.String(), b.client, b.target()); handled {
		return b, cmd
	}

	before := b.tbl.Cursor()
	var cmd tea.Cmd
	b.tbl, cmd = b.tbl.Update(msg)
	b.skipHeaders(travelDir(before, b.tbl.Cursor()))
	b.rememberSelection()
	return b, cmd
}

// travelDir is the direction a cursor move was going in, which is the
// direction a group header has to be stepped over in. A move that went
// nowhere (already at an end) reads as downward, so the first press on the
// top row still lands on a task.
func travelDir(before, after int) int {
	if after < before {
		return -1
	}
	return 1
}

// skipHeaders steps the cursor off a group header, carrying on in the
// direction of travel and turning around at either end. Headers are labels:
// resting on one would empty the detail panels and offer no actions, so j/k
// pass over them as if they were not rows at all.
//
// It moves with MoveUp/MoveDown rather than SetCursor for the reason
// clickRow does: the table's scroll offset is private and only those two keep
// its bookkeeping consistent.
func (b *board) skipHeaders(dir int) {
	rows := b.rows()
	// Bounded by the row count: a turn-around at either end must not become a
	// loop between two headers.
	for range rows {
		i := b.tbl.Cursor()
		if i < 0 || i >= len(rows) || !rows[i].header {
			return
		}
		if dir > 0 {
			b.tbl.MoveDown(1)
		} else {
			b.tbl.MoveUp(1)
		}
		if b.tbl.Cursor() == i {
			dir = -dir // clamped at an end: the tasks are the other way
		}
	}
}

// clickRow selects the table row rendered at the given body line (0 = the
// first row under the column header). The table's scroll offset is private,
// so the clicked line's distance from the *styled cursor row* drives the
// same MoveUp/MoveDown path the keys use — bubbles keeps its own scroll
// bookkeeping consistent that way.
func (b *board) clickRow(line int) {
	marker, _, _ := strings.Cut(tableSelectedStyle().Render("~"), "~")
	if marker == "" {
		return
	}
	rows := strings.Split(b.tbl.View(), "\n")
	if len(rows) < 2 {
		return
	}
	body := rows[1:] // the column header is line 0
	// A table shorter than its pane is padded with blank lines. Clicking one
	// used to select the last row, because the move clamps — so clicking
	// empty space below the list silently changed the selection (T3.8
	// finding). Nothing is a row unless something is rendered on it.
	if line >= len(body) || strings.TrimSpace(ansi.Strip(body[line])) == "" {
		return
	}
	cursorLine := -1
	for i, row := range body {
		if strings.Contains(row, marker) {
			cursorLine = i
			break
		}
	}
	if cursorLine < 0 {
		return
	}
	delta := line - cursorLine
	// A group header is a label, not a row: clicking one leaves the selection
	// alone rather than jumping to a task the click did not name. The clicked
	// row is the cursor's plus the delta — a body line cannot be indexed into
	// the row slice directly, because the table may be scrolled.
	if b.rowAt(b.tbl.Cursor() + delta).header {
		return
	}
	switch {
	case delta > 0:
		b.tbl.MoveDown(delta)
	case delta < 0:
		b.tbl.MoveUp(-delta)
	}
	b.rememberSelection()
}

// paste types into the filter, and only into the filter: a confirmation is
// single-key, and a paste is not an answer to "are you sure?".
func (b *board) paste(text string) tea.Cmd {
	if !b.filtering {
		return nil
	}
	var cmd tea.Cmd
	b.filter, cmd = b.filter.Update(tea.PasteMsg{Content: text})
	return cmd
}

// wheelMove is one wheel notch on the task table: the cursor moves, and the
// panels below follow it through the usual settle window.
func (b *board) wheelMove(delta int) {
	if delta > 0 {
		b.tbl.MoveDown(delta)
	} else {
		b.tbl.MoveUp(-delta)
	}
	b.skipHeaders(delta)
	b.rememberSelection()
}

// rows is the table as it is rendered: filtered, sorted, and — under a
// grouping — interleaved with group headers (boardgroup.go). Every index into
// the table means an index into this slice.
func (b *board) rows() []boardRow {
	tasks := filterTasks(b.tasks, b.filter.Value())
	sorted := make([]apiclient.Task, len(tasks))
	copy(sorted, tasks)
	sortTasks(sorted)
	return groupRows(sorted, b.group)
}

// rowAt is the row at a table index, or a zero row off either end.
func (b *board) rowAt(i int) boardRow {
	rows := b.rows()
	if i < 0 || i >= len(rows) {
		return boardRow{}
	}
	return rows[i]
}

// visible is the tasks on screen in render order, headers dropped: the list
// for anything that walks tasks rather than lines — the attention jump, the
// action bar's target.
func (b *board) visible() []apiclient.Task {
	rows := b.rows()
	out := make([]apiclient.Task, 0, len(rows))
	for _, r := range rows {
		if !r.header {
			out = append(out, r.task)
		}
	}
	return out
}

// selected reports the task id under the cursor.
//
// A cursor sitting on a group header resolves to the first task under it,
// rather than to nothing. Key navigation steps over headers (skipHeaders), so
// this is the frame between a load and the render that places the cursor —
// and answering "no task" there would leave a freshly loaded board with its
// detail panels empty until something moved. A header always has rows beneath
// it, so the scan always lands.
func (b *board) selected() (int64, bool) {
	rows := b.rows()
	i := b.tbl.Cursor()
	if i < 0 {
		return 0, false
	}
	for ; i < len(rows); i++ {
		if !rows[i].header {
			return rows[i].task.ID, true
		}
	}
	return 0, false
}

// hintedProject is the project of the row under the cursor, which is the
// project a new task is most likely for.
func (b *board) hintedProject() int64 {
	id, ok := b.selected()
	if !ok {
		return 0
	}
	for _, t := range b.visible() {
		if t.ID == id {
			return t.ProjectID
		}
	}
	return 0
}

// rememberSelection records the id under the cursor so a refresh that
// reorders rows can put the cursor back on the same task. The table clamps
// its cursor index but never remaps it, so tracking the index alone would
// silently select a different task whenever the sort shifted.
func (b *board) rememberSelection() {
	if id, ok := b.selected(); ok {
		b.selectedID = id
	}
}

// restoreSelection moves the cursor back onto the remembered task — after a
// refresh that reordered the rows, and after `g` regrouped them.
func (b *board) restoreSelection(rows []boardRow) {
	if b.selectedID != 0 {
		for i := range rows {
			if !rows[i].header && rows[i].task.ID == b.selectedID {
				b.tbl.SetCursor(i)
				return
			}
		}
	}
	// The task is gone (archived, or filtered out), or nothing has been
	// selected yet: the first task rather than the first *row*, which under a
	// grouping is a header — a cursor parked on one shows empty panels and no
	// actions.
	b.tbl.SetCursor(firstTaskRow(rows))
}

func (b *board) render(width, height int) string {
	if width > 0 {
		b.width = width
	}
	if height > 0 {
		b.height = height
	}
	var sb strings.Builder
	sb.WriteString(b.headerLine())
	sb.WriteString("\n")
	if line := b.statusLine(); line != "" {
		sb.WriteString(line)
		sb.WriteString("\n")
	}

	rows := b.rows()
	if body, ok := b.emptyBody(rows); ok {
		sb.WriteString(body)
		return sb.String()
	}

	cols, set := boardColumns(b.width, b.group, b.hasMarks())
	// SetColumns re-renders the rows it already holds: when a resize crosses
	// a column breakpoint, yesterday's wider rows meet today's narrower
	// column set and the table indexes out of range. Clear the rows first —
	// the real ones are set right back.
	if len(cols) != len(b.tbl.Columns()) {
		b.tbl.SetRows(nil)
	}
	b.tbl.SetColumns(cols)
	b.tbl.SetRows(b.rowsFor(rows, set))
	b.tbl.SetWidth(b.width)
	// Two lines of chrome above, plus the filter line when it is showing.
	b.tbl.SetHeight(max(3, b.height-b.chromeLines()))
	b.restoreSelection(rows)
	// Passing through an empty row set (the breakpoint clear above, or a
	// board that emptied and refilled) parks the cursor at -1; with rows on
	// screen the cursor belongs on one.
	if b.tbl.Cursor() < 0 && len(rows) > 0 {
		b.tbl.SetCursor(firstTaskRow(rows))
	}
	sb.WriteString(b.tbl.View())
	sb.WriteString("\n")
	sb.WriteString(b.actionLine())
	return sb.String()
}

// actionLine is the board's action affordance: the same gating as the detail
// view's bar, rendered as one dim footer. Triage happens here, so the keys
// have to be here too (§15 lists them as global).
func (b *board) actionLine() string {
	t := b.target()
	if t.id == 0 {
		return ""
	}
	var extra []string
	if t.has(apiclient.ActionAnswer) {
		extra = append(extra, styleAsk.Render("enter → answer"))
	}
	return b.actions.render(t, extra...)
}

func (b *board) chromeLines() int {
	n := 3 // header + the action line + a blank the shell leaves
	if b.statusLine() != "" {
		n++
	}
	return n
}

// firstRowLine is the board-content line the table's first data row lands
// on: the header line, the status line when there is one, then the table's
// own column header. Click math needs *only* what sits above the rows, so it
// cannot borrow chromeLines — that is a height budget and also counts the
// action line rendered below the table. Sharing the two put every click two
// rows high, which read as "rows select from a few lines below themselves"
// (M3 gate finding, macOS).
func (b *board) firstRowLine() int {
	n := 2 // the header line, then the table's column header
	if b.statusLine() != "" {
		n++
	}
	return n
}

// rowsFor renders the table's cells. It and boardColumns share one shape —
// the same optional columns in the same order — and move together; a row that
// disagrees with the column set indexes the table out of range.
func (b *board) rowsFor(rows []boardRow, set columnSet) []table.Row {
	now := b.now()
	// Task titles sit under their headers, one indent per grouping level.
	indent := strings.Repeat(groupIndent, len(b.group))
	out := make([]table.Row, 0, len(rows))
	for _, r := range rows {
		if r.header {
			out = append(out, groupHeaderRow(r, set))
			continue
		}
		t := r.task
		row := make(table.Row, 0, maxBoardColumns)
		if set.mark {
			row = append(row, b.markCell(t.ID))
		}
		row = append(row, strconv.FormatInt(t.ID, 10))
		if set.project {
			row = append(row, t.ProjectName)
		}
		if set.workflow {
			row = append(row, t.Workflow)
		}
		elapsed := "—"
		if d, ok := t.Elapsed(now); ok {
			elapsed = formatElapsed(d)
		}
		row = append(row, indent+t.Title, renderBoardState(t), formatStep(t, set.stepName), elapsed)
		if set.cost {
			row = append(row, formatCost(t.CostUSD))
		}
		out = append(out, row)
	}
	return out
}

// groupHeaderRow renders a group header as a table row: the label in the
// title column — the widest, and the only one that survives every width — and
// every other cell blank, so the header reads as a break in the list rather
// than as a task with missing figures.
func groupHeaderRow(r boardRow, set columnSet) table.Row {
	row := make(table.Row, 0, maxBoardColumns)
	if set.mark {
		// A group is not something a bulk action can name: the marker column is
		// blank on a header, like every other column but the label.
		row = append(row, "")
	}
	row = append(row, "")
	if set.project {
		row = append(row, "")
	}
	if set.workflow {
		row = append(row, "")
	}
	row = append(row, r.headerCell(), "", "", "")
	if set.cost {
		row = append(row, "")
	}
	return row
}

// headerLine reports what the daemon is doing overall (§15): how much work
// is in flight against the cap, how much needs a human, and which adapters
// are actually usable. The counts come from the whole fetched list, not the
// filtered view — a filter must not hide that something needs you.
func (b *board) headerLine() string {
	running := countRunning(b.tasks)
	attention := countAttention(b.tasks)

	limit := "?"
	if b.infoOK {
		limit = strconv.Itoa(b.info.MaxParallelTasks)
	}
	parts := []string{fmt.Sprintf(" %d/%s running", running, limit)}
	if attention > 0 {
		parts = append(parts, styleWarn.Render(
			fmt.Sprintf("%s %d need attention", attentionBadge, attention)))
	} else {
		parts = append(parts, styleDim.Render("0 need attention"))
	}
	if b.infoOK {
		parts = append(parts, b.agentsSummary())
	}
	return strings.Join(parts, styleDim.Render(" · "))
}

func (b *board) agentsSummary() string {
	if len(b.info.Agents) == 0 {
		return styleDim.Render("no adapters")
	}
	now := b.now()
	parts := make([]string, 0, len(b.info.Agents))
	for _, a := range b.info.Agents {
		switch {
		case !a.Available:
			parts = append(parts, styleBad.Render(a.Name+" ✗"))
		case a.NotAuthenticated():
			// Present but unable to run a step (§9.5) — the board's one-glance
			// summary must not read as healthy.
			parts = append(parts, styleWarn.Render(a.Name+" ⚠"))
		case a.QuotaSpent(now):
			// Installed, authenticated, and out of quota until a stated time
			// (task 026). It ranks below the other two because it is
			// temporary and self-clearing, and it ranks above ✓ because a
			// tick here is the answer to "why is nothing running" being wrong.
			// Admission is untouched: this warns, it does not withhold.
			parts = append(parts, styleWarn.Render(a.Name+" "+quotaBadge(a.Quota, now)))
		default:
			parts = append(parts, styleOK.Render(a.Name+" ✓"))
		}
	}
	return strings.Join(parts, " ")
}

// statusLine surfaces a stale board and the filter prompt while it is being
// typed — a committed filter moves to the panel title (§15). A refresh that
// failed says so and says how old the rows are, rather than the board
// quietly showing yesterday's truth.
func (b *board) statusLine() string {
	switch {
	case b.filtering:
		line := " " + b.filter.View()
		if b.loadErr != nil {
			line += styleBad.Render("  ⚠ " + b.staleNote())
		}
		return line
	case b.loadErr != nil:
		return styleBad.Render(" ⚠ " + b.staleNote())
	default:
		return ""
	}
}

// filterActive reports there is a filter to clear — typing or committed.
func (b *board) filterActive() bool {
	return b.filtering || b.filter.Value() != ""
}

// commitFilter leaves the typed filter applied and stops capturing (§15:
// tab commits; only esc clears).
func (b *board) commitFilter() {
	b.filtering = false
	b.filter.Blur()
}

// clearFilter is the esc-stack layer: the filter disappears entirely.
func (b *board) clearFilter() {
	b.filtering = false
	b.filter.SetValue("")
	b.filter.Blur()
}

func (b *board) staleNote() string {
	note := "refresh failed: " + errString(b.loadErr)
	if !b.lastLoad.IsZero() {
		note += " — showing " + b.lastLoad.Local().Format("15:04:05")
	}
	return note
}

// emptyBody distinguishes "nothing exists yet" from "your filter matches
// nothing": different problems with different ways out, and the first-run
// case is the one moment a new user most needs pointing at what to do next.
func (b *board) emptyBody(rows []boardRow) (string, bool) {
	if len(rows) > 0 {
		return "", false
	}
	switch {
	case !b.loaded && b.loadErr == nil:
		return styleDim.Render("\n  loading tasks…\n"), true
	case len(b.tasks) > 0:
		return styleDim.Render(fmt.Sprintf(
			"\n  no tasks match %q — esc to clear the filter\n", b.filter.Value())), true
	default:
		return styleDim.Render("\n  no tasks yet — press n to create one\n"), true
	}
}

// capturesInput reports that the filter field or a pending confirmation has
// the keyboard — typing into either must not reach the shell's global keys.
func (b *board) capturesInput() bool { return b.filtering || b.actions.capturing() }
