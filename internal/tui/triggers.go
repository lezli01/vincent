package tui

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// The triggers takeover (§15 view 11, task 096.6). It is the view the inward
// signal gets: every file under {config_dir}/triggers, whether it is armed and
// why not, how its source's polls are going, and the delivery ledger of what
// each event became. It is reached from the command palette, the way
// Workflows and Projects are (decision 14).
//
// Like the workflow editor it composes no YAML (decisions 19–21): a create
// sends the starter's inputs, the form and the `space` toggle send edit
// operations carrying the version the last read handed back, and delete sends
// that version too. The daemon owns the file end to end, so a comment an
// author wrote survives every write made from here.
//
// The two dry runs fire nothing. The sample event `T` judges is held in this
// view's memory for the session and never written anywhere — not to tui.json,
// which holds view state, because a vendor payload can carry text nobody
// meant to persist (decision 27).

// Triggers messages.
type (
	triggersRefreshMsg struct{}
	// triggersLoadedMsg carries the list, the served schema and the project
	// names. Only the list failing costs the view its rows; a schema that did
	// not arrive only makes the enable confirmation generic.
	triggersLoadedMsg struct {
		list     apiclient.TriggerList
		schema   *apiclient.TriggerSchema
		projects []apiclient.Project
		err      error
	}
	// triggerLedgerMsg is one ledger read for one trigger id.
	triggerLedgerMsg struct {
		id   string
		rows []apiclient.TriggerDelivery
		err  error
	}
	// triggerTickMsg re-reads the list and the ledger while the view is on
	// screen: poll health is published on transitions only (decision 24), so
	// "last poll" and new ledger rows are this timer's to keep current.
	triggerTickMsg struct{}
	// triggerWrittenMsg is a toggle or a delete landing.
	triggerWrittenMsg struct {
		id      string
		done    string
		deleted bool
		err     error
	}
	// triggerEditedMsg reports that $EDITOR exited on a trigger file.
	triggerEditedMsg struct{ err error }
	// openConfigKeyMsg asks the root to open one config key in the daemon
	// view's editor: the global-off banner's way to the switch it names.
	openConfigKeyMsg struct{ path string }
)

const (
	// triggerTickInterval paces the ledger and poll-health refresh.
	triggerTickInterval = 5 * time.Second
	// triggerLedgerLimit bounds one ledger read: the pane shows the newest
	// deliveries, and the route serves up to 1000 for a script that wants more.
	triggerLedgerLimit = 50
	// triggersEnabledKey is the config key the banner's `B` opens.
	triggersEnabledKey = "triggers.enabled"
)

// triggerFocus says which of the view's two lists the arrows mean — the
// daemon view's list/log split, with tab between them (decision 25).
type triggerFocus int

const (
	trigFocusList triggerFocus = iota
	trigFocusLedger
)

// triggerConfirm is an inline question in front of a write: enabling a
// trigger, or deleting one. run is what `y` sends.
type triggerConfirm struct {
	question string
	warning  []string
	run      func() tea.Cmd
}

// triggersView is §15's view 11.
type triggersView struct {
	client *apiclient.Client
	exec   execFunc
	now    func() time.Time

	list     apiclient.TriggerList
	schema   *apiclient.TriggerSchema
	projects []apiclient.Project
	loaded   bool
	loadErr  error

	// cursor indexes visible(); selectedID is what a reload restores it to,
	// so a refresh that reorders or filters the rows never moves the
	// selection off the trigger somebody was looking at.
	cursor     int
	selectedID string

	filter    textField
	filtering bool

	focus        triggerFocus
	ledgerID     string
	ledger       []apiclient.TriggerDelivery
	ledgerErr    error
	ledgerCursor int

	confirm *triggerConfirm
	form    *trigFormLayer
	create  *trigCreateForm
	dry     *trigDryRun
	// samples is each trigger's dry-run sample event, for this session only
	// (decision 27). It is deliberately a field and never a tuiState member.
	samples map[string]string

	note string
	err  string

	// onScreen is whether the view is the active one: the tick re-arms only
	// while it is.
	onScreen       bool
	refreshPending bool
	width, height  int
}

func newTriggersView() *triggersView {
	fi := newTextField()
	fi.SetPlaceholder("filter by id, source, action or project")
	fi.SetPrompt("/")
	return &triggersView{
		exec:    tea.ExecProcess,
		now:     time.Now,
		filter:  fi,
		samples: map[string]string{},
	}
}

func (v *triggersView) title() string { return "Triggers" }

func (v *triggersView) setClient(c *apiclient.Client) tea.Cmd {
	v.client = c
	return v.loadCmd()
}

// capturesInput is true while anything on the view holds the keyboard: the
// filter, a question, the form's field or overlay, the create prompt, or a dry
// run. A question captures too, so `n` answers it rather than opening the
// new-task form behind it.
func (v *triggersView) capturesInput() bool {
	switch {
	case v.filtering, v.confirm != nil, v.create != nil, v.dry != nil:
		return true
	case v.form != nil:
		return v.form.capturing()
	}
	return false
}

// paste hands pasted text to the field with the keyboard.
func (v *triggersView) paste(text string) tea.Cmd {
	var cmd tea.Cmd
	switch {
	case v.create != nil:
		f := v.create.field()
		if f == nil {
			return nil
		}
		*f, cmd = f.Update(tea.PasteMsg{Content: text})
	case v.form != nil && v.form.input != nil:
		*v.form.input, cmd = v.form.input.Update(tea.PasteMsg{Content: text})
	case v.filtering:
		v.filter, cmd = v.filter.Update(tea.PasteMsg{Content: text})
	}
	return cmd
}

// bindingContext names the layer with the keyboard for the footer, the
// palette and the ? overlay.
func (v *triggersView) bindingContext() bindingContext {
	switch {
	case v.create != nil:
		return ctxTriggerCreate
	case v.dry != nil:
		return ctxTriggerDryRun
	case v.form != nil:
		return ctxTriggerForm
	case v.focus == trigFocusLedger:
		return ctxTriggerLedger
	}
	return ctxTriggers
}

// hintedProject lets `n` open the new-task form on the selected trigger's
// project.
func (v *triggersView) hintedProject() int64 {
	if s, ok := v.current(); ok {
		return s.ProjectID
	}
	return 0
}

func (v *triggersView) loadCmd() tea.Cmd {
	client := v.client
	if client == nil {
		return nil
	}
	haveSchema := v.schema != nil
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		list, err := client.Triggers(ctx)
		if err != nil {
			return triggersLoadedMsg{err: err}
		}
		msg := triggersLoadedMsg{list: list}
		if !haveSchema {
			if s, err := client.TriggerSchema(ctx); err == nil {
				msg.schema = &s
			}
		}
		if projects, err := client.ListProjects(ctx); err == nil {
			msg.projects = projects
		}
		return msg
	}
}

// ledgerCmd reads the selected trigger's ledger.
func (v *triggersView) ledgerCmd() tea.Cmd {
	client := v.client
	s, ok := v.current()
	if client == nil || !ok {
		return nil
	}
	id := s.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		rows, err := client.TriggerDeliveries(ctx, id, triggerLedgerLimit)
		return triggerLedgerMsg{id: id, rows: rows, err: err}
	}
}

func (v *triggersView) tickCmd() tea.Cmd {
	return tea.Tick(triggerTickInterval, func(time.Time) tea.Msg { return triggerTickMsg{} })
}

func (v *triggersView) scheduleRefresh() tea.Cmd {
	if v.refreshPending {
		return nil
	}
	v.refreshPending = true
	return tea.Tick(refreshDebounce, func(time.Time) tea.Msg { return triggersRefreshMsg{} })
}

func (v *triggersView) update(msg tea.Msg) (panel, tea.Cmd) {
	if cmd, handled := v.updateLayerMsg(msg); handled {
		return v, cmd
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width, v.height = msg.Width, msg.Height
		return v, nil
	case viewActivatedMsg:
		if msg.id != viewTriggers {
			return v, nil
		}
		v.onScreen = true
		return v, tea.Batch(v.loadCmd(), v.tickCmd())
	case viewDeactivatedMsg:
		if msg.id == viewTriggers {
			v.onScreen = false
		}
		return v, nil
	case triggerTickMsg:
		if !v.onScreen {
			// A tea.Tick cannot be cancelled, so it stops by not re-arming.
			return v, nil
		}
		return v, tea.Batch(v.loadCmd(), v.tickCmd())
	case triggersRefreshMsg:
		v.refreshPending = false
		return v, v.loadCmd()
	case triggersLoadedMsg:
		return v, v.applyLoaded(msg)
	case triggerLedgerMsg:
		v.applyLedger(msg)
		return v, nil
	case triggerWrittenMsg:
		return v, v.applyWritten(msg)
	case triggerEditedMsg:
		if msg.err != nil {
			v.err = "editor: " + errString(msg.err)
		}
		// The daemon's watcher reloads the registry after a save, and
		// nothing announces it on the stream: re-read now, and once more
		// after the watcher has had a moment.
		return v, tea.Batch(v.loadCmd(), tea.Tick(time.Second, func(time.Time) tea.Msg {
			return triggersRefreshMsg{}
		}))
	case noteMsg:
		return v, v.updateNote(msg.note)
	case tea.KeyPressMsg:
		return v.updateKey(msg)
	}
	return v, nil
}

// updateNote re-reads on the trigger events (a fire adds a ledger row, a poll
// changing health changes a row) and on project changes, which rename the
// project column.
func (v *triggersView) updateNote(n apiclient.Note) tea.Cmd {
	ev, ok := n.(apiclient.EventNote)
	if !ok {
		return nil
	}
	if strings.HasPrefix(ev.Event.Type, "trigger.") || strings.HasPrefix(ev.Event.Type, "project.") {
		return v.scheduleRefresh()
	}
	return nil
}

// applyLoaded keeps the last-good list behind a failed refresh, restores the
// selection by id, and re-reads the ledger for whatever is selected now.
func (v *triggersView) applyLoaded(msg triggersLoadedMsg) tea.Cmd {
	if msg.err != nil {
		v.loadErr = msg.err
		return nil
	}
	v.loadErr = nil
	v.loaded = true
	v.list = msg.list
	if msg.schema != nil {
		v.schema = msg.schema
	}
	if msg.projects != nil {
		v.projects = msg.projects
	}
	v.restoreSelection()
	return v.ledgerCmd()
}

func (v *triggersView) applyLedger(msg triggerLedgerMsg) {
	s, ok := v.current()
	if !ok || s.ID != msg.id {
		// An answer for a trigger the cursor has since left.
		return
	}
	if msg.err != nil {
		v.ledgerErr = msg.err
		return
	}
	if v.ledgerID != msg.id {
		v.ledgerCursor = 0
	}
	v.ledgerID, v.ledger, v.ledgerErr = msg.id, msg.rows, nil
	v.ledgerCursor = min(v.ledgerCursor, max(len(v.ledger)-1, 0))
}

// applyWritten lands a toggle or a delete. A stale version says so and
// re-reads, which is the only honest reaction: the file is not what the
// question on screen was about any more.
func (v *triggersView) applyWritten(msg triggerWrittenMsg) tea.Cmd {
	if msg.err != nil {
		if isStale(msg.err) {
			v.err = msg.id + " changed on disk since it was read — re-read it; try again"
		} else {
			v.err = errString(msg.err)
		}
		v.note = ""
		return v.loadCmd()
	}
	v.err, v.note = "", msg.done
	if msg.deleted && v.selectedID == msg.id {
		v.selectedID = ""
	}
	return v.loadCmd()
}

// isStale reports a version-token conflict: a 409 carrying the current
// version in its details.
func isStale(err error) bool {
	var apiErr *apiclient.Error
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict && apiErr.Details["version"] != ""
}

func (v *triggersView) updateKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	switch {
	case v.create != nil:
		return v, v.updateCreateKey(msg)
	case v.dry != nil:
		return v, v.updateDryKey(msg)
	case v.form != nil:
		return v, v.updateFormKey(msg)
	case v.confirm != nil:
		return v, v.updateConfirm(msg)
	case v.filtering:
		return v, v.updateFilter(msg)
	case v.focus == trigFocusLedger:
		return v.updateLedgerKey(msg)
	}
	switch msg.String() {
	case "up", "k":
		return v, v.move(-1)
	case "down", "j":
		return v, v.move(1)
	case "enter", "i":
		return v, v.openForm()
	case opKey(keymap.Add):
		v.openCreate()
		return v, nil
	case opKey(keymap.Editor):
		return v, v.editCmd()
	case opKey(keymap.Delete):
		v.askDelete()
		return v, nil
	case "space", " ":
		return v, v.toggle()
	case opKey(keymap.Refresh):
		v.err, v.note = "", ""
		return v, v.loadCmd()
	case opKey(keymap.Filter):
		v.filtering = true
		return v, v.filter.Focus()
	case "tab":
		v.focus = trigFocusLedger
		return v, v.ledgerCmd()
	case "T":
		return v, v.openTest()
	case "X":
		return v, v.openPoll()
	case "B":
		return v, func() tea.Msg { return openConfigKeyMsg{path: triggersEnabledKey} }
	case "esc":
		// One layer per press (§15): a note or a committed filter clears
		// first; with nothing left, the takeover closes.
		if v.err != "" || v.note != "" || v.filter.Value() != "" {
			v.err, v.note = "", ""
			v.filter.SetValue("")
			v.restoreSelection()
			return v, nil
		}
		return v, func() tea.Msg { return selectViewMsg{id: viewHome} }
	}
	return v, nil
}

func (v *triggersView) updateLedgerKey(msg tea.KeyPressMsg) (panel, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		v.ledgerCursor = max(v.ledgerCursor-1, 0)
	case "down", "j":
		v.ledgerCursor = min(v.ledgerCursor+1, max(len(v.ledger)-1, 0))
	case "enter":
		return v, v.openDelivery()
	case "tab", "esc":
		v.focus = trigFocusList
	case opKey(keymap.Refresh):
		v.err, v.note = "", ""
		return v, v.loadCmd()
	}
	return v, nil
}

// openDelivery opens the task a delivery created or acted on. A delivery
// that touched no task says so rather than doing nothing.
func (v *triggersView) openDelivery() tea.Cmd {
	if v.ledgerCursor < 0 || v.ledgerCursor >= len(v.ledger) {
		return nil
	}
	d := v.ledger[v.ledgerCursor]
	if d.TaskID == nil {
		v.err = "this delivery was " + d.Outcome + " and touched no task"
		return nil
	}
	v.err = ""
	id := *d.TaskID
	return func() tea.Msg { return selectTaskMsg{id: id} }
}

func (v *triggersView) updateFilter(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		v.filtering = false
		v.filter.SetValue("")
		v.filter.Blur()
		v.restoreSelection()
		return v.ledgerCmd()
	case "enter":
		v.filtering = false
		v.filter.Blur()
		return v.ledgerCmd()
	}
	var cmd tea.Cmd
	v.filter, cmd = v.filter.Update(msg)
	v.restoreSelection()
	return cmd
}

func (v *triggersView) updateConfirm(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "y", "Y":
		c := v.confirm
		v.confirm = nil
		return c.run()
	case "n", "N", "esc":
		v.confirm = nil
	}
	// Any other key leaves the question standing: a write that starts agents
	// is not something a stray press should answer either way.
	return nil
}

// move walks the visible rows and re-reads the ledger for the new selection.
func (v *triggersView) move(delta int) tea.Cmd {
	rows := v.visible()
	if len(rows) == 0 {
		return nil
	}
	next := min(max(v.cursor+delta, 0), len(rows)-1)
	if next == v.cursor {
		return nil
	}
	v.cursor = next
	v.selectedID = rows[next].ID
	v.ledger, v.ledgerErr, v.ledgerCursor = nil, nil, 0
	return v.ledgerCmd()
}

// visible is the filtered list, in the registry's id order.
func (v *triggersView) visible() []apiclient.TriggerSummary {
	q := strings.ToLower(strings.TrimSpace(v.filter.Value()))
	if q == "" {
		return v.list.Triggers
	}
	out := make([]apiclient.TriggerSummary, 0, len(v.list.Triggers))
	for _, s := range v.list.Triggers {
		hay := strings.ToLower(s.ID + " " + s.SourceType + " " + s.ActionType + " " + v.projectName(s.ProjectID))
		if strings.Contains(hay, q) {
			out = append(out, s)
		}
	}
	return out
}

func (v *triggersView) current() (apiclient.TriggerSummary, bool) {
	rows := v.visible()
	if v.cursor < 0 || v.cursor >= len(rows) {
		return apiclient.TriggerSummary{}, false
	}
	return rows[v.cursor], true
}

// restoreSelection puts the cursor back on selectedID, or clamps it when that
// trigger is gone or filtered out.
func (v *triggersView) restoreSelection() {
	rows := v.visible()
	for i := range rows {
		if rows[i].ID == v.selectedID {
			v.cursor = i
			return
		}
	}
	v.cursor = min(max(v.cursor, 0), max(len(rows)-1, 0))
	if v.cursor < len(rows) {
		v.selectedID = rows[v.cursor].ID
	}
}

func (v *triggersView) projectName(id int64) string {
	for _, p := range v.projects {
		if p.ID == id {
			return p.Name
		}
	}
	if id == 0 {
		return ""
	}
	return "#" + strconv.FormatInt(id, 10)
}

// editCmd opens the selected trigger's file in $EDITOR. The escape hatch for
// a file the form cannot load, and for an edit the form has no row for.
func (v *triggersView) editCmd() tea.Cmd {
	s, ok := v.current()
	if !ok || s.File == "" {
		return nil
	}
	v.err, v.note = "", ""
	return openEditorPath(v.exec, s.File, func(err error) tea.Msg { return triggerEditedMsg{err: err} })
}

// toggle flips `enabled`. Disabling never asks (decision 19). Enabling asks
// with the served schema's warning and what this trigger's on_fire means for
// it, because that is the difference between a paused task and a running
// agent.
func (v *triggersView) toggle() tea.Cmd {
	s, ok := v.current()
	if !ok {
		return nil
	}
	if !s.Valid {
		v.err = s.ID + " does not validate, so it cannot be switched — fix it with e first"
		return nil
	}
	v.err, v.note = "", ""
	if s.Enabled {
		return v.patchEnabledCmd(s, false)
	}
	v.confirm = &triggerConfirm{
		question: "enable " + s.ID + "?",
		warning:  v.enableWarning(s),
		run:      func() tea.Cmd { return v.patchEnabledCmd(s, true) },
	}
	return nil
}

// enableWarning is the served warning for `enabled: true`, followed by the
// consequence for this trigger. A schema that never arrived still asks.
func (v *triggersView) enableWarning(s apiclient.TriggerSummary) []string {
	warn := "an enabled trigger acts on every event that passes its filter, with no keypress"
	if v.schema != nil {
		if w, ok := dangerousWarning(v.schema.TopLevel, "enabled", "true"); ok {
			warn = w
		}
	}
	out := []string{warn}
	switch s.OnFire {
	case "create":
		out = append(out, "on_fire is create: each task starts running as soon as a slot is free")
	default:
		out = append(out, "on_fire is propose: each task is created paused, for you to resume")
	}
	if !v.list.Enabled {
		out = append(out, "triggers.enabled is off, so nothing polls until it is turned on")
	}
	return out
}

func (v *triggersView) patchEnabledCmd(s apiclient.TriggerSummary, on bool) tea.Cmd {
	client := v.client
	if client == nil {
		v.err = "not connected"
		return nil
	}
	value, done := "false", "disabled "+s.ID
	if on {
		value, done = "true", "enabled "+s.ID
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		_, err := client.PatchTrigger(ctx, s.ID, s.Version, []apiclient.WorkflowOp{{
			Op: apiclient.WorkflowOpSet, Path: "enabled", Value: value,
		}})
		return triggerWrittenMsg{id: s.ID, done: done, err: err}
	}
}

// askDelete puts the delete behind a question that says what goes and what
// stays (decision 21).
func (v *triggersView) askDelete() {
	s, ok := v.current()
	if !ok {
		return
	}
	v.err, v.note = "", ""
	client := v.client
	v.confirm = &triggerConfirm{
		question: "delete trigger " + s.ID + "?",
		warning: []string{
			"its file is removed and its poll cursor dropped; its delivery ledger is kept,",
			"so a trigger re-created with this id cannot fire an event that already fired",
		},
		run: func() tea.Cmd {
			if client == nil {
				v.err = "not connected"
				return nil
			}
			return func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
				defer cancel()
				err := client.DeleteTrigger(ctx, s.ID, s.Version)
				return triggerWrittenMsg{id: s.ID, done: "deleted " + s.ID, deleted: true, err: err}
			}
		},
	}
}

// dangerousWarning finds a field's warning for one value in a served field
// list (decision 19).
func dangerousWarning(fields []apiclient.TriggerSchemaField, name, value string) (string, bool) {
	for _, f := range fields {
		if f.Name != name {
			continue
		}
		for _, d := range f.Dangerous {
			if d.Value == value {
				return d.Warning, true
			}
		}
	}
	return "", false
}
