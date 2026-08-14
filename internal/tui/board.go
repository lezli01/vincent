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

	filter    textinput.Model
	filtering bool

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
		now:     time.Now,
		filter:  fi,
		bell:    ringBell,
		actions: &actionBar{},
		tbl:     table.New(table.WithFocused(true)),
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
	cmds := []tea.Cmd{b.loadCmd(), b.infoCmd()}
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
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		tasks, err := client.ListTasks(ctx, apiclient.ListTasksOptions{})
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
	case tea.KeyPressMsg:
		return b.updateKey(msg)
	}
	return b, nil
}

// target is the row under the cursor as the action bar sees it.
func (b *board) target() taskActions {
	id, ok := b.selected()
	if !ok {
		return taskActions{}
	}
	for _, t := range b.visible() {
		if t.ID == id {
			return taskActions{id: t.ID, state: t.State, actions: t.AvailableActions}
		}
	}
	return taskActions{id: id}
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
	}

	// §15's action keys act on the row under the cursor. A key the daemon
	// does not offer for this task falls through to the table, so j/k keep
	// moving on a row that cannot be skipped.
	if cmd, handled := b.actions.handleKey(msg.String(), b.client, b.target()); handled {
		return b, cmd
	}

	var cmd tea.Cmd
	b.tbl, cmd = b.tbl.Update(msg)
	b.rememberSelection()
	return b, cmd
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
	switch delta := line - cursorLine; {
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
	b.rememberSelection()
}

// visible is the filtered, sorted task list currently on screen.
func (b *board) visible() []apiclient.Task {
	tasks := filterTasks(b.tasks, b.filter.Value())
	sorted := make([]apiclient.Task, len(tasks))
	copy(sorted, tasks)
	sortTasks(sorted)
	return sorted
}

// selected reports the task id under the cursor.
func (b *board) selected() (int64, bool) {
	rows := b.visible()
	i := b.tbl.Cursor()
	if i < 0 || i >= len(rows) {
		return 0, false
	}
	return rows[i].ID, true
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

// restoreSelection moves the cursor back onto the remembered task.
func (b *board) restoreSelection(rows []apiclient.Task) {
	if b.selectedID == 0 {
		return
	}
	for i := range rows {
		if rows[i].ID == b.selectedID {
			b.tbl.SetCursor(i)
			return
		}
	}
	// The task is gone (archived, or filtered out): fall back to the top
	// rather than leaving the cursor pointing at an unrelated row.
	b.tbl.SetCursor(0)
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

	rows := b.visible()
	if body, ok := b.emptyBody(rows); ok {
		sb.WriteString(body)
		return sb.String()
	}

	cols, set := boardColumns(b.width)
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
		b.tbl.SetCursor(0)
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

func (b *board) rowsFor(tasks []apiclient.Task, set columnSet) []table.Row {
	now := b.now()
	out := make([]table.Row, 0, len(tasks))
	for _, t := range tasks {
		row := table.Row{strconv.FormatInt(t.ID, 10)}
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
		row = append(row, t.Title, renderBoardState(t), formatStep(t, set.stepName), elapsed)
		if set.cost {
			row = append(row, formatCost(t.CostUSD))
		}
		out = append(out, row)
	}
	return out
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
	parts := make([]string, 0, len(b.info.Agents))
	for _, a := range b.info.Agents {
		switch {
		case !a.Available:
			parts = append(parts, styleBad.Render(a.Name+" ✗"))
		case a.NotAuthenticated():
			// Present but unable to run a step (§9.5) — the board's one-glance
			// summary must not read as healthy.
			parts = append(parts, styleWarn.Render(a.Name+" ⚠"))
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
func (b *board) emptyBody(rows []apiclient.Task) (string, bool) {
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
