package tui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// The issue form (§15 views 12 and 13, task 130.12): create an issue, or edit
// one. It is a full pane over the list or the detail, reached with `n` from
// either and with `i` for the issue under the cursor.
//
// It is modelled on the project form rather than the new-task form for that
// form's reason: an edit PATCHes only what changed, at the version the form
// read, and the daemon owns every rule worth having. Which rows may change is
// the issue DTO's `editable` list — the form never keeps its own copy of
// "imported ⇒ title, body and labels are locked" (task 130 decision 14.2).

// ifRow identifies one line of the issue form.
type ifRow int

const (
	ifProject ifRow = iota
	ifTitle
	ifBody
	ifLabels
	ifKind
	ifPriority
	ifSave
	ifRowCount
)

func (r ifRow) label() string {
	switch r {
	case ifProject:
		return "project"
	case ifTitle:
		return "title"
	case ifBody:
		return "description"
	case ifLabels:
		return "labels"
	case ifKind:
		return "kind"
	case ifPriority:
		return "priority"
	case ifSave:
		return "save"
	case ifRowCount:
	}
	return ""
}

// field is the `editable` name the row's value travels under, "" for the
// rows that are not an issue field.
func (r ifRow) field() string {
	switch r {
	case ifTitle:
		return "title"
	case ifBody:
		return "body"
	case ifLabels:
		return "labels"
	case ifKind:
		return "kind"
	case ifPriority:
		return "priority"
	case ifProject, ifSave, ifRowCount:
	}
	return ""
}

// issueKinds is the suggested kind vocabulary (task 130 decision 4). A
// suggestion, not a list: the picker takes free text, and so does the daemon.
var issueKinds = []string{"bug", "feature", "task", "chore", "question", "docs"}

// Issue-form messages.
type (
	issueFormCatalogMsg struct {
		projectID int64
		labels    []apiclient.IssueLabel
		projects  []apiclient.Project
		err       error
	}
	issueFormBodyMsg struct {
		text string
		err  error
	}
	issueFormSavedMsg struct {
		issue apiclient.Issue
		err   error
	}
	issueFormReloadedMsg struct {
		issue apiclient.Issue
		err   error
	}
	// issueFormClosedMsg asks the owning view to drop the form. saved is the
	// issue a save returned, nil when the form was closed without one.
	issueFormClosedMsg struct {
		saved   *apiclient.Issue
		created bool
	}
)

// issueForm creates or edits one issue.
type issueForm struct {
	client *apiclient.Client
	exec   execFunc
	// original is nil when creating. When editing it is the PATCH base: the
	// version sent, and what "changed" is measured against.
	original *apiclient.Issue

	projectID int64
	projects  []apiclient.Project
	catalog   []apiclient.IssueLabel

	title    textField
	body     textPane
	labels   []string
	kind     string
	priority int

	cursor  ifRow
	editing bool
	pick    *picker

	// idemKey is generated once per opened form, so a ctrl+s retried after
	// a timeout replays the create rather than filing the issue twice.
	idemKey string

	// confirming is the discard prompt esc raises over unsaved changes.
	confirming bool
	saving     bool
	err        string
	// stale is set by a 409: R re-reads the issue and keeps the edits that
	// still apply. current is the issue an issue_changed 409 carried, which
	// R rebases onto without a second read.
	stale   bool
	current *apiclient.Issue
}

func newIssueForm(client *apiclient.Client, exec execFunc, original *apiclient.Issue, projectID int64) *issueForm {
	f := &issueForm{client: client, exec: exec, title: newTextField(), body: newTextPane(), projectID: projectID}
	f.title.SetPlaceholder("what is wrong, or what is wanted")
	if original != nil {
		f.adopt(*original)
		f.cursor = ifTitle
	} else {
		f.idemKey = newIdempotencyKey()
		f.cursor = ifTitle
	}
	return f
}

// adopt takes iss as the form's base and shows its values.
func (f *issueForm) adopt(iss apiclient.Issue) {
	f.original = &iss
	f.projectID = iss.ProjectID
	f.title.SetValue(iss.Title)
	f.body.SetValue(iss.Body)
	f.labels = slices.Clone(iss.Labels)
	f.kind = iss.Kind
	f.priority = iss.Priority
}

// newIdempotencyKey is a random key for one create. A failure to read
// randomness leaves it empty, which only costs the replay guarantee.
func newIdempotencyKey() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	return "tui-issue-" + hex.EncodeToString(b)
}

func (f *issueForm) creating() bool { return f.original == nil }

// editable reports whether a row may change. Creating, everything may; on an
// edit the daemon's `editable` list decides.
func (f *issueForm) editable(r ifRow) bool {
	if f.creating() || r.field() == "" {
		return true
	}
	return slices.Contains(f.original.Editable, r.field())
}

func (f *issueForm) init() tea.Cmd { return f.catalogCmd(f.projectID) }

// catalogCmd fetches the project's label catalogue and, creating, the
// projects the read-only project row names its project from.
func (f *issueForm) catalogCmd(projectID int64) tea.Cmd {
	client, creating := f.client, f.creating()
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		msg := issueFormCatalogMsg{projectID: projectID}
		if creating {
			msg.projects, msg.err = client.ListProjects(ctx)
		}
		if projectID != 0 && msg.err == nil {
			msg.labels, msg.err = client.ListIssueLabels(ctx, projectID)
		}
		return msg
	}
}

func (f *issueForm) capturesInput() bool { return true }

func (f *issueForm) paste(text string) tea.Cmd {
	if f.pick != nil {
		return f.pick.paste(text)
	}
	if !f.editing || f.cursor != ifTitle {
		return nil
	}
	var cmd tea.Cmd
	f.title, cmd = f.title.Update(tea.PasteMsg{Content: text})
	return cmd
}

// update handles the form's own messages; ok is false for one it does not
// know.
func (f *issueForm) update(msg tea.Msg) (cmd tea.Cmd, ok bool) {
	switch msg := msg.(type) {
	case issueFormCatalogMsg:
		if msg.projectID == f.projectID {
			if msg.err != nil {
				f.err = "labels: " + errString(msg.err)
			}
			f.catalog = msg.labels
			if msg.projects != nil {
				f.projects = msg.projects
			}
		}
		return nil, true
	case issueFormBodyMsg:
		if msg.err != nil {
			f.err = "description: " + errString(msg.err)
		} else {
			f.err = ""
			f.body.SetValue(msg.text)
		}
		return nil, true
	case issueFormSavedMsg:
		return f.applySaved(msg), true
	case issueFormReloadedMsg:
		f.applyReloaded(msg)
		return nil, true
	case tea.KeyPressMsg:
		return f.updateKey(msg), true
	}
	return nil, false
}

func (f *issueForm) updateKey(msg tea.KeyPressMsg) tea.Cmd {
	switch {
	case f.confirming:
		f.confirming = false
		if s := msg.String(); s == "y" || s == "Y" {
			return f.close(nil, false)
		}
		return nil
	case f.pick != nil:
		return f.updatePicking(msg)
	case f.editing:
		return f.updateEditing(msg)
	}
	switch msg.String() {
	case "up", "k", "shift+tab":
		f.move(-1)
	case "down", "j", "tab":
		f.move(1)
	case "enter":
		return f.activate()
	case opKey(keymap.Editor):
		if f.cursor == ifBody {
			return f.openEditor()
		}
	case opKey(keymap.Refresh):
		return f.reloadCmd()
	case "ctrl+s":
		return f.submit()
	case "esc":
		if f.dirty() {
			f.confirming = true
			return nil
		}
		return f.close(nil, false)
	}
	return nil
}

func (f *issueForm) close(saved *apiclient.Issue, created bool) tea.Cmd {
	return func() tea.Msg { return issueFormClosedMsg{saved: saved, created: created} }
}

// move walks the rows. The project row is never landed on: creating, it
// shows the selected project read-only — a switch is the only way to change
// it (task 132.13, decision 9) — and editing, it is not drawn at all, since
// an issue's project is where it lives, not a field.
func (f *issueForm) move(delta int) {
	next := min(max(int(f.cursor)+delta, int(ifTitle)), int(ifRowCount)-1)
	f.cursor = ifRow(next)
}

func (f *issueForm) activate() tea.Cmd {
	if !f.editable(f.cursor) {
		f.err = f.cursor.label() + " is mirrored from GitHub — it changes there, not here"
		return nil
	}
	switch f.cursor {
	case ifProject:
		// Read-only: the selection is the project (task 132.13).
	case ifTitle:
		f.editing = true
		return f.title.Focus()
	case ifBody:
		f.editing = true
		return f.body.Focus()
	case ifLabels:
		f.pick = newPicker(int(ifLabels), "labels", f.labelOptions(), true, "")
		f.pick.multi = true
	case ifKind:
		opts := []pickerOption{{value: "", label: "(none)"}}
		kinds := slices.Clone(issueKinds)
		if f.kind != "" && !slices.Contains(kinds, f.kind) {
			kinds = append(kinds, f.kind)
		}
		for _, k := range kinds {
			opts = append(opts, pickerOption{value: k, label: k})
		}
		f.pick = newPicker(int(ifKind), "kind", opts, true, f.kind)
	case ifPriority:
		// The scale is Linear's, inverted against a task's (task 130
		// decision 4): 1 is the most urgent and 0 is no priority at all.
		opts := make([]pickerOption, 0, 5)
		for _, p := range []int{0, 1, 2, 3, 4} {
			opts = append(opts, pickerOption{value: strconv.Itoa(p), label: issuePriorityLabel(p)})
		}
		f.pick = newPicker(int(ifPriority), "priority", opts, false, strconv.Itoa(f.priority))
	case ifSave:
		return f.submit()
	case ifRowCount:
	}
	return nil
}

// labelOptions is the project's catalogue plus any label already on the
// issue that the catalogue does not carry, each ticked when it is set.
func (f *issueForm) labelOptions() []pickerOption {
	names := make([]string, 0, len(f.catalog)+len(f.labels))
	for _, l := range f.catalog {
		names = append(names, l.Name)
	}
	for _, l := range f.labels {
		if !slices.Contains(names, l) {
			names = append(names, l)
		}
	}
	out := make([]pickerOption, 0, len(names))
	for _, n := range names {
		out = append(out, pickerOption{value: n, label: n, selected: slices.Contains(f.labels, n)})
	}
	return out
}

func (f *issueForm) updateEditing(msg tea.KeyPressMsg) tea.Cmd {
	if f.cursor == ifBody {
		switch msg.String() {
		case "esc":
			f.editing = false
			f.body.Blur()
			return nil
		case "ctrl+s":
			f.editing = false
			f.body.Blur()
			return f.submit()
		}
		var cmd tea.Cmd
		f.body, cmd = f.body.Update(msg)
		return cmd
	}
	switch msg.String() {
	case "esc", "enter":
		f.editing = false
		f.title.Blur()
		return nil
	case "ctrl+s":
		f.editing = false
		f.title.Blur()
		return f.submit()
	}
	var cmd tea.Cmd
	f.title, cmd = f.title.Update(msg)
	return cmd
}

func (f *issueForm) updatePicking(msg tea.KeyPressMsg) tea.Cmd {
	res := f.pick.update(msg)
	if res.chosen {
		switch ifRow(f.pick.row) {
		case ifLabels:
			f.toggleLabel(res.value, res.free)
		case ifKind:
			f.kind = res.value
		case ifPriority:
			if p, err := strconv.Atoi(res.value); err == nil {
				f.priority = p
			}
		case ifProject, ifTitle, ifBody, ifSave, ifRowCount:
		}
	}
	if res.closed {
		f.pick = nil
	}
	return res.cmd
}

// toggleLabel flips a label from the list, or adds one typed as free text
// (which a second toggle never removes: typing a label means wanting it).
func (f *issueForm) toggleLabel(name string, free bool) {
	if i := slices.Index(f.labels, name); i >= 0 {
		if !free {
			f.labels = slices.Delete(f.labels, i, i+1)
		}
	} else {
		f.labels = append(f.labels, name)
	}
	if f.pick != nil && !free {
		for i := range f.pick.options {
			f.pick.options[i].selected = slices.Contains(f.labels, f.pick.options[i].value)
		}
	}
}

// openEditor hands the description to $EDITOR, seeded with what is typed.
func (f *issueForm) openEditor() tea.Cmd {
	if !f.editable(ifBody) {
		f.err = "description is mirrored from GitHub — it changes there, not here"
		return nil
	}
	name := "new-issue"
	if f.original != nil {
		name = "issue" + strconv.FormatInt(f.original.ID, 10)
	}
	cmd, err := editTextCmd(f.exec, name, ".md", f.body.Value(), func(edited string, err error) tea.Msg {
		return issueFormBodyMsg{text: edited, err: err}
	})
	if err != nil {
		f.err = "description: " + errString(err)
		return nil
	}
	return cmd
}

// dirty reports whether closing would lose anything typed.
func (f *issueForm) dirty() bool {
	if f.creating() {
		return strings.TrimSpace(f.title.Value()) != "" || strings.TrimSpace(f.body.Value()) != "" ||
			len(f.labels) > 0 || f.kind != "" || f.priority != 0
	}
	p := f.patch()
	return p.Title != nil || p.Body != nil || len(p.AddLabels) > 0 || len(p.RemoveLabels) > 0 ||
		p.Kind != nil || p.Priority != nil
}

func (f *issueForm) submit() tea.Cmd {
	f.err = ""
	if f.saving {
		return nil
	}
	if f.client == nil {
		f.err = "not connected"
		return nil
	}
	if f.creating() {
		if f.projectID == 0 {
			f.err = "no project is selected"
			return nil
		}
		client, req, key := f.client, f.createRequest(), f.idemKey
		f.saving = true
		return func() tea.Msg {
			ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
			defer cancel()
			iss, err := client.CreateIssue(ctx, req, key)
			return issueFormSavedMsg{issue: iss, err: err}
		}
	}
	if !f.dirty() {
		return f.close(nil, false)
	}
	client, id, p := f.client, f.original.ID, f.patch()
	f.saving = true
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), actionTimeout)
		defer cancel()
		iss, err := client.PatchIssue(ctx, id, p)
		return issueFormSavedMsg{issue: iss, err: err}
	}
}

func (f *issueForm) createRequest() apiclient.CreateIssueRequest {
	return apiclient.CreateIssueRequest{
		ProjectID: f.projectID,
		Title:     strings.TrimSpace(f.title.Value()),
		Body:      f.body.Value(),
		Labels:    slices.Clone(f.labels),
		Kind:      f.kind,
		Priority:  f.priority,
	}
}

// patch is the version the form read plus only the fields that differ from
// it, and only those the daemon lists as editable. Labels travel as a diff,
// so a label another writer added meanwhile is not removed by this one.
func (f *issueForm) patch() apiclient.IssuePatch {
	o := f.original
	p := apiclient.IssuePatch{Version: o.Version}
	if v := strings.TrimSpace(f.title.Value()); v != o.Title && f.editable(ifTitle) {
		p.Title = ptr(v)
	}
	if v := f.body.Value(); v != o.Body && f.editable(ifBody) {
		p.Body = ptr(v)
	}
	if f.editable(ifLabels) {
		for _, l := range f.labels {
			if !slices.Contains(o.Labels, l) {
				p.AddLabels = append(p.AddLabels, l)
			}
		}
		for _, l := range o.Labels {
			if !slices.Contains(f.labels, l) {
				p.RemoveLabels = append(p.RemoveLabels, l)
			}
		}
	}
	if f.kind != o.Kind && f.editable(ifKind) {
		p.Kind = ptr(f.kind)
	}
	if f.priority != o.Priority && f.editable(ifPriority) {
		p.Priority = ptr(f.priority)
	}
	return p
}

func (f *issueForm) applySaved(msg issueFormSavedMsg) tea.Cmd {
	f.saving = false
	if msg.err == nil {
		saved := msg.issue
		return f.close(&saved, f.creating())
	}
	reason, current, conflict := apiclient.IssueConflict(msg.err)
	switch {
	case conflict && reason == apiclient.IssueReasonChanged:
		f.stale, f.current = true, current
		f.err = "the issue changed since this form read it — " + opKey(keymap.Refresh) +
			" takes the current version and keeps your edits where they still apply"
	case conflict && reason == apiclient.IssueReasonMirrored:
		// The editable list said otherwise when the form opened: the issue
		// was imported between the read and the save.
		f.stale = true
		f.err = "this issue is now mirrored from GitHub, so its title, description and labels change there; " +
			opKey(keymap.Refresh) + " re-reads it"
	default:
		f.err = errString(msg.err)
	}
	return nil
}

// reloadCmd re-reads the issue under edit. Creating, there is nothing to
// re-read.
func (f *issueForm) reloadCmd() tea.Cmd {
	if f.creating() || f.client == nil {
		return nil
	}
	if cur := f.current; cur != nil {
		f.current = nil
		return func() tea.Msg { return issueFormReloadedMsg{issue: *cur} }
	}
	client, id := f.client, f.original.ID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		iss, err := client.GetIssue(ctx, id, "")
		return issueFormReloadedMsg{issue: iss, err: err}
	}
}

// applyReloaded rebases the form onto the issue as it is now. A field the
// user changed keeps their value if it is still editable; one they left alone
// takes the new value. Their label additions and removals are replayed onto
// the new set rather than the old set being restored.
func (f *issueForm) applyReloaded(msg issueFormReloadedMsg) {
	if msg.err != nil {
		f.err = "re-read: " + errString(msg.err)
		return
	}
	old, cur := *f.original, msg.issue
	title, body := strings.TrimSpace(f.title.Value()), f.body.Value()
	labels, kind, priority := f.labels, f.kind, f.priority
	f.adopt(cur)
	keep := func(r ifRow, changed bool) bool { return changed && f.editable(r) }
	if keep(ifTitle, title != old.Title) {
		f.title.SetValue(title)
	}
	if keep(ifBody, body != old.Body) {
		f.body.SetValue(body)
	}
	if f.editable(ifLabels) {
		for _, l := range labels {
			if !slices.Contains(old.Labels, l) && !slices.Contains(f.labels, l) {
				f.labels = append(f.labels, l)
			}
		}
		f.labels = slices.DeleteFunc(f.labels, func(l string) bool {
			return slices.Contains(old.Labels, l) && !slices.Contains(labels, l)
		})
	}
	if keep(ifKind, kind != old.Kind) {
		f.kind = kind
	}
	if keep(ifPriority, priority != old.Priority) {
		f.priority = priority
	}
	f.stale = false
	f.err = ""
	if f.dirty() {
		f.err = "re-read at version " + strconv.FormatInt(cur.Version, 10) + " — your edits are kept; ctrl+s saves them"
	}
}

// --- rendering ---

func (f *issueForm) heading() string {
	if f.creating() {
		return "New issue"
	}
	return "Edit issue #" + strconv.FormatInt(f.original.ID, 10)
}

func (f *issueForm) projectName() string {
	for _, p := range f.projects {
		if p.ID == f.projectID {
			return p.Name
		}
	}
	if f.projectID == 0 {
		return ""
	}
	return "project #" + strconv.FormatInt(f.projectID, 10)
}

func (f *issueForm) render(width, height int) string {
	lines := []string{" " + styleTitle.Render(f.heading()), ""}
	for r := ifProject; r < ifRowCount; r++ {
		if r == ifProject && !f.creating() {
			continue
		}
		lines = append(lines, f.rowLines(r, width)...)
		if f.pick != nil && ifRow(f.pick.row) == r {
			f.pick.setWidth(width - 4)
			lines = append(lines, f.pick.renderBody()...)
		}
	}
	lines = append(lines, "")
	switch {
	case f.confirming:
		lines = append(lines, styleWarn.Render("  discard your changes? y discards · any other key keeps editing"))
	case f.saving:
		lines = append(lines, styleDim.Render("  saving…"))
	case f.err != "":
		style := styleBad
		if !f.stale && strings.HasPrefix(f.err, "re-read at") {
			style = styleWarn
		}
		lines = append(lines, style.Render("  "+f.err))
	}
	lines = append(lines, styleDim.Render("  ↑↓ move · enter edit · "+opKey(keymap.Editor)+
		" $EDITOR (description) · ctrl+s save · esc close"))
	for i, l := range lines {
		lines[i] = ansi.Truncate(l, width, "…")
	}
	return strings.Join(window(lines, 0, max(height, 1)), "\n")
}

func (f *issueForm) rowLines(r ifRow, width int) []string {
	marker := "  "
	if r == f.cursor {
		marker = styleFocus.Render("› ")
	}
	head := marker + styleDim.Render(padRight(r.label(), 13))
	var value string
	switch r {
	case ifProject:
		value = firstNonEmpty(f.projectName(), styleDim.Render("(no project selected)")) +
			"  " + styleDim.Render("· the selected project")
	case ifTitle:
		if f.editing && r == f.cursor {
			f.title.SetWidth(max(width-17, 10))
			return indentRows(head, f.title.rows())
		}
		value = firstNonEmpty(strings.TrimSpace(f.title.Value()), styleDim.Render("(required)"))
	case ifBody:
		if f.editing && r == f.cursor {
			f.body.SetSize(max(width-6, 10), 8)
			return append([]string{head}, indentRows("    ", strings.Split(f.body.View(), "\n"))...)
		}
		value = bodyPreview(f.body.Value())
	case ifLabels:
		value = firstNonEmpty(strings.Join(f.labels, ", "), styleDim.Render("none"))
	case ifKind:
		value = firstNonEmpty(f.kind, styleDim.Render("none"))
	case ifPriority:
		value = issuePriorityLabel(f.priority)
	case ifSave:
		return []string{marker + styleKey.Render("save")}
	case ifRowCount:
	}
	if !f.editable(r) {
		value += "  " + styleDim.Render("· mirrored from GitHub")
	}
	return []string{head + value}
}

// bodyPreview is the description's first line and how many follow.
func bodyPreview(body string) string {
	body = strings.TrimSpace(body)
	if body == "" {
		return styleDim.Render("(empty — enter types, " + opKey(keymap.Editor) + " opens $EDITOR)")
	}
	lines := strings.Split(body, "\n")
	if len(lines) == 1 {
		return lines[0]
	}
	return lines[0] + styleDim.Render("  (+"+plural(len(lines)-1, "line", "lines")+")")
}
