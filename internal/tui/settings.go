package tui

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/lezli01/vincent/internal/apiclient"
	"github.com/lezli01/vincent/internal/keymap"
)

// The Settings screen (task 152): every key config.yaml carries, on a screen of
// its own. It is opened from the command palette and the Daemon screen, and has
// no main tab: settings are visited, not lived in.
//
// A change applies the moment it is made, the way the daemon already treats the
// file, so there is no save step. The keys that decide what the daemon executes
// or exposes are the exception (task 060): every path to changing one, arrows
// and reset included, stops at the confirmation first.

// branchPreviewDebounce is how long typing must rest before a template is
// rendered by the daemon.
const branchPreviewDebounce = 150 * time.Millisecond

// branchTemplateFields are the fields the template editor can insert.
var branchTemplateFields = []string{"ID", "Slug", "Title", "BaseBranch", "Project.Name"}

type (
	// settingsInfoMsg is the daemon's identity: the slots in use, the adapters
	// found and where config.yaml lives.
	settingsInfoMsg struct {
		info apiclient.Info
		err  error
	}
	// settingsEditedMsg says $EDITOR closed on config.yaml.
	settingsEditedMsg struct{ err error }
	// branchPreviewDueMsg says typing may have rested on template.
	branchPreviewDueMsg struct{ template string }
	// branchPreviewMsg is the daemon's rendering of a template.
	branchPreviewMsg struct {
		template string
		preview  apiclient.BranchPreview
		err      error
	}
)

// settingsChange is a value on its way to the daemon.
type settingsChange struct {
	key   configKey
	value string
}

// settingsRow is one setting as the screen lists it.
type settingsRow struct {
	category, section int
	item              settingsItem
	key               configKey
}

// settingsView is the screen.
type settingsView struct {
	client *apiclient.Client
	exec   execFunc
	now    func() time.Time

	keys []configKey

	config    apiclient.Config
	configOK  bool
	configErr error
	info      apiclient.Info
	infoOK    bool

	// category is the entry selected on the left, and selected the setting's
	// path under the cursor.
	category int
	selected string

	search    textField
	searching bool

	// form is the open editor, nil when none is. While it is open it owns the
	// keyboard, and it is what asks before a dangerous key applies.
	form *configForm
	// bindings is the key-bindings screen (#1079), nil when it is not open.
	bindings *keyBindings
	// fieldAt is the template field tab inserts next.
	fieldAt int
	// preview is the daemon's rendering of the template being edited.
	preview    apiclient.BranchPreview
	previewFor string
	previewErr string

	// shown holds a value on its way to the daemon, so an arrow press answers at
	// once. saving is the change in flight, and queued the latest press behind
	// it: holding an arrow sends the value it ends on, not every step.
	shown  map[string]string
	saving *settingsChange
	queued *settingsChange

	// searchRows, navHits and paneHits are the last wide render's layout,
	// which a click is read against (#1065).
	searchRows int
	navHits    []settingsNavHit
	paneHits   []settingsHit

	note    string
	noteBad bool
	// undo is the last saved change and the value it replaced, which `u`
	// writes back (#1057); undoing marks that write, which is not itself
	// undoable. burstPath and burstWas are the value a run of arrow presses
	// started from, so a held arrow is undone in one step, not one value.
	undo                *settingsUndo
	undoing             bool
	burstPath, burstWas string
	// pendingKey is a key another screen asked to open before the config landed.
	pendingKey string

	// visible is whether the screen is the active one.
	visible bool

	width, height, termW int
}

func newSettingsView() *settingsView {
	search := newTextField()
	search.SetPlaceholder("search every setting by name, key or what it does")
	search.SetPrompt("")
	v := &settingsView{
		exec: tea.ExecProcess, now: time.Now, keys: configKeys(), search: search, shown: map[string]string{},
	}
	v.selectFirst()
	return v
}

func (v *settingsView) title() string { return "Settings" }

// setClient re-reads what is on screen after a reconnect.
func (v *settingsView) setClient(c *apiclient.Client) tea.Cmd {
	v.client = c
	if !v.visible {
		return nil
	}
	return tea.Batch(v.configCmd(), v.infoCmd())
}

// promptKeys are the answers while a confirmation is waiting: enter does not
// apply there, and the status bar must not say it does.
func (v *settingsView) promptKeys() promptAnswers {
	if v.form != nil && v.form.confirming {
		return promptHint("y", "apply", "n", "go back")
	}
	return nil
}

// frameless reports that the screen draws its own panes.
func (v *settingsView) frameless() bool {
	return v.termW >= mgmtWideMinW && v.height >= formWideMinH
}

// capturesInput is true while a text field owns the keyboard.
func (v *settingsView) capturesInput() bool {
	// Taking a key press as a binding owns the keyboard: the key meant for
	// the binding must not run whatever it already means.
	return v.form != nil || v.searching || v.bindings != nil && v.bindings.capturing
}

// bindingContext names the layer that owns the keyboard.
func (v *settingsView) bindingContext() bindingContext {
	if v.form != nil {
		return ctxConfigEdit
	}
	if v.bindings != nil {
		return ctxKeyBindings
	}
	return ctxSettings
}

func (v *settingsView) key(path string) (configKey, bool) {
	i := slices.IndexFunc(v.keys, func(k configKey) bool { return k.path == path })
	if i < 0 {
		return configKey{}, false
	}
	return v.keys[i], true
}

// rows is what the cursor walks: the selected category, or every match.
func (v *settingsView) rows() []settingsRow {
	query := strings.ToLower(strings.TrimSpace(v.search.Value()))
	var out []settingsRow
	for c, category := range settingsCatalog {
		if query == "" && c != v.category {
			continue
		}
		for s, section := range category.sections {
			for _, item := range section.items {
				k, ok := v.key(item.path)
				if !ok || (query != "" && !settingMatches(query, category.name, item, k)) {
					continue
				}
				out = append(out, settingsRow{c, s, item, k})
			}
		}
	}
	return out
}

// settingMatches reports whether a search finds one setting.
func settingMatches(query, category string, item settingsItem, k configKey) bool {
	text := strings.ToLower(strings.Join([]string{category, item.label, item.desc, item.path, k.label, k.help}, " "))
	return strings.Contains(text, query)
}

func (v *settingsView) selectFirst() {
	v.selected = ""
	if rows := v.rows(); len(rows) > 0 {
		v.selected = rows[0].item.path
	}
}

// current is the setting under the cursor.
func (v *settingsView) current() (settingsRow, bool) {
	rows := v.rows()
	i := slices.IndexFunc(rows, func(r settingsRow) bool { return r.item.path == v.selected })
	if i < 0 {
		return settingsRow{}, false
	}
	return rows[i], true
}

// value is what a key is set to, counting a change still on its way.
func (v *settingsView) value(k configKey) string {
	if s, ok := v.shown[k.path]; ok {
		return s
	}
	return k.read(v.config)
}

func (v *settingsView) infoCmd() tea.Cmd {
	client := v.client
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		info, err := client.Info(ctx)
		return settingsInfoMsg{info: info, err: err}
	}
}

func (v *settingsView) configCmd() tea.Cmd {
	client := v.client
	if client == nil {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		cfg, err := client.Config(ctx)
		return daemonConfigMsg{config: cfg, err: err}
	}
}

func (v *settingsView) update(msg tea.Msg) (panel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		v.width, v.height, v.termW = msg.Width, msg.Height, msg.Width
	case viewActivatedMsg:
		if msg.id == viewSettings {
			v.visible = true
			return v, tea.Batch(v.configCmd(), v.infoCmd())
		}
	case viewDeactivatedMsg:
		if msg.id == viewSettings {
			v.visible = false
		}
	case daemonConfigMsg:
		v.applyConfig(msg)
	case settingsInfoMsg:
		if msg.err == nil {
			v.info, v.infoOK = msg.info, true
		}
	case configSavedMsg:
		return v, v.applySaved(msg)
	case openConfigKeyMsg:
		v.openKey(msg.path)
		return v, v.armPreview()
	case settingsEditedMsg:
		v.note, v.noteBad = "config.yaml closed", false
		if msg.err != nil {
			v.note, v.noteBad = "could not open the editor: "+errString(msg.err), true
		}
		return v, v.configCmd()
	case branchPreviewDueMsg:
		return v, v.previewCmd(msg.template)
	case branchPreviewMsg:
		v.applyPreview(msg)
	case tea.PasteMsg:
		return v, v.paste(msg.Content)
	case tea.KeyPressMsg:
		return v, v.updateKey(msg)
	case tea.MouseClickMsg:
		return v, v.updateClick(msg)
	}
	return v, nil
}

// paste types into the field that has the keyboard.
func (v *settingsView) paste(text string) tea.Cmd {
	var cmd tea.Cmd
	switch {
	case v.form != nil && v.form.key.kind != kindBool && v.form.key.kind != kindEnum && !v.form.confirming:
		v.form.input, cmd = v.form.input.Update(tea.PasteMsg{Content: text})
		return tea.Batch(cmd, v.armPreview())
	case v.searching:
		v.search, cmd = v.search.Update(tea.PasteMsg{Content: text})
		v.selectFirst()
	}
	return cmd
}

func (v *settingsView) applyConfig(msg daemonConfigMsg) {
	if msg.err != nil {
		v.configErr = msg.err
		return
	}
	v.config, v.configOK, v.configErr = msg.config, true, nil
	if v.pendingKey != "" && v.form == nil {
		v.openKey(v.pendingKey)
	}
}

// openKey moves to one key and opens its editor: how another screen sends
// somebody to a switch it can name but must not flip. A config that has not
// loaded yet opens the key when it lands.
func (v *settingsView) openKey(path string) {
	if !v.goTo(path) {
		return
	}
	if !v.configOK {
		v.pendingKey = path
		return
	}
	v.pendingKey = ""
	v.openForm(path)
}

// goTo puts the cursor on one setting, in its own category, so a change
// made from elsewhere happens on screen.
func (v *settingsView) goTo(path string) bool {
	c, _, ok := settingsLocate(path)
	if !ok {
		return false
	}
	v.clearSearch()
	v.category, v.selected = c, path
	return true
}

// settingsLocate finds a setting's category and catalog entry.
func settingsLocate(path string) (int, settingsItem, bool) {
	for c, category := range settingsCatalog {
		for _, section := range category.sections {
			for _, item := range section.items {
				if item.path == path {
					return c, item, true
				}
			}
		}
	}
	return 0, settingsItem{}, false
}

func (v *settingsView) openForm(path string) {
	if path == tuiKeysPath {
		// #1079: a list to press keys into, not a line of pairs to type.
		v.bindings = newKeyBindings(v.config)
		return
	}
	if k, ok := v.key(path); ok {
		v.form = newConfigForm(k, v.config)
		v.fieldAt, v.preview, v.previewFor, v.previewErr = 0, apiclient.BranchPreview{}, "", ""
	}
}

// applySaved lands the answer to a PATCH. A refusal keeps the editor open with
// the message against the field; a success adopts what the daemon reports,
// which is what is in force rather than what was asked for.
func (v *settingsView) applySaved(msg configSavedMsg) tea.Cmd {
	if v.bindings != nil && msg.path == tuiKeysPath && v.bindings.saving {
		v.applyKeysSaved(msg)
		return nil
	}
	if v.form != nil && msg.path == v.form.key.path {
		v.form.saving = false
		if msg.err != nil {
			v.form.err = errString(msg.err)
			v.undoing = false // #1137: an undo that failed is not pending
			return nil
		}
		v.form = nil
		v.adopt(msg)
		return nil
	}
	if v.saving == nil || msg.path != v.saving.key.path {
		if msg.err == nil {
			v.config, v.configOK = msg.cfg, true
		}
		return nil
	}
	v.saving = nil
	if msg.err != nil {
		delete(v.shown, msg.path)
		v.queued, v.undoing = nil, false
		v.note, v.noteBad = errString(msg.err), true
		return nil
	}
	v.adopt(msg)
	if next := v.queued; next != nil {
		v.queued = nil
		return v.send(*next)
	}
	return nil
}

func (v *settingsView) adopt(msg configSavedMsg) {
	delete(v.shown, msg.path)
	k, known := v.key(msg.path)
	was := ""
	if known {
		was = k.read(v.config)
		if v.burstPath == msg.path {
			was = v.burstWas
		}
	}
	v.config, v.configOK, v.configErr = msg.cfg, true, nil
	v.note, v.noteBad = "saved", false
	if known && k.restart {
		v.note = "saved, applies after the daemon restarts"
	}
	if v.queued != nil && v.queued.key.path == msg.path {
		return // a held arrow is still on its way: the burst is one change
	}
	v.burstPath = ""
	switch {
	case v.undoing:
		v.undoing, v.undo = false, nil
		// Named, since the undo may have been asked for elsewhere (#1137).
		_, item, _ := settingsLocate(msg.path)
		v.note += " · " + valueOr(item.label, msg.path) + " put back"
	case known && was != k.read(v.config):
		// What it was, and the key that puts it back (#1057).
		v.undo = &settingsUndo{key: k, was: was}
		v.note += " · was " + valueOr(oneLine(was), "(unset)") + " · u puts it back"
	}
}

func (v *settingsView) updateKey(msg tea.KeyPressMsg) tea.Cmd {
	if v.bindings != nil {
		return v.updateBindings(msg)
	}
	if v.form != nil {
		return v.updateForm(msg)
	}
	if v.searching {
		return v.updateSearch(msg)
	}
	switch msg.String() {
	case "esc":
		if v.search.Value() != "" {
			v.clearSearch()
			return nil
		}
		return backToTab
	case "up", "k":
		v.moveCursor(-1)
	case "down", "j":
		v.moveCursor(1)
	case "left", "h":
		return v.change(-1)
	case "right", "l", " ", "space":
		return v.change(1)
	case "tab":
		v.moveCategory(1)
	case "shift+tab":
		v.moveCategory(-1)
	case "enter":
		if v.configOK {
			v.openForm(v.selected)
			return v.armPreview()
		}
	case opKey(keymap.Filter):
		v.searching = true
		return v.search.Focus()
	case "backspace":
		return v.reset()
	case "u":
		return v.undoLast()
	case opKey(keymap.Editor):
		return v.openFile()
	case opKey(keymap.Refresh):
		return tea.Batch(v.configCmd(), v.infoCmd())
	}
	return nil
}

// updateSearch is the search box: typing narrows, the arrows act on the results.
func (v *settingsView) updateSearch(msg tea.KeyPressMsg) tea.Cmd {
	switch msg.String() {
	case "esc":
		v.clearSearch()
		return nil
	case "enter":
		v.searching = false
		v.search.Blur()
		return nil
	case "up":
		v.moveCursor(-1)
		return nil
	case "down":
		v.moveCursor(1)
		return nil
	case "left":
		return v.change(-1)
	case "right":
		return v.change(1)
	}
	var cmd tea.Cmd
	v.search, cmd = v.search.Update(msg)
	v.selectFirst()
	return cmd
}

func (v *settingsView) clearSearch() {
	v.searching = false
	v.search.Blur()
	if v.search.Value() != "" {
		v.search.SetValue("")
		v.selectFirst()
	}
}

// updateForm is the open editor. tab inserts a field into a branch template,
// and every change to the text asks the daemon what the template now makes.
func (v *settingsView) updateForm(msg tea.KeyPressMsg) tea.Cmd {
	f := v.form
	if _, no := confirmAnswer(msg.String()); msg.String() == "esc" || f.confirming && no {
		// #1137: esc (or n, #1162) steps back from an undo waiting at a
		// dangerous key's confirmation; whatever is saved from the form
		// afterwards is a change of its own, not the undo.
		v.undoing = false
	}
	if msg.String() == "tab" && f.key.path == "branch_template" && !f.confirming {
		f.input.SetValue(f.input.Value() + "{{." + branchTemplateFields[v.fieldAt] + "}}")
		f.input.CursorEnd()
		v.fieldAt = (v.fieldAt + 1) % len(branchTemplateFields)
		return v.armPreview()
	}
	cmd, done := f.update(msg, v.client)
	if done {
		v.form = nil
		return cmd
	}
	return tea.Batch(cmd, v.armPreview())
}

// armPreview waits for typing in a branch template to rest.
func (v *settingsView) armPreview() tea.Cmd {
	if v.form == nil || v.form.key.path != "branch_template" || v.form.input.Value() == v.previewFor {
		return nil
	}
	template := v.form.input.Value()
	return tea.Tick(branchPreviewDebounce, func(time.Time) tea.Msg { return branchPreviewDueMsg{template} })
}

// previewCmd asks the daemon to render the template if it is still the one on
// screen. Branch naming is the daemon's: the screen never renders one itself.
func (v *settingsView) previewCmd(template string) tea.Cmd {
	client := v.client
	if client == nil || v.form == nil || v.form.key.path != "branch_template" || v.form.input.Value() != template {
		return nil
	}
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		preview, err := client.PreviewBranchTemplate(ctx, template)
		return branchPreviewMsg{template: template, preview: preview, err: err}
	}
}

func (v *settingsView) applyPreview(msg branchPreviewMsg) {
	if v.form == nil || v.form.key.path != "branch_template" || v.form.input.Value() != msg.template {
		return
	}
	v.preview, v.previewFor, v.previewErr = msg.preview, msg.template, ""
	if msg.err != nil {
		v.previewErr = errString(msg.err)
	}
}

// moveCursor walks the settings, clamped at both ends.
func (v *settingsView) moveCursor(delta int) {
	rows := v.rows()
	if len(rows) == 0 {
		return
	}
	i := slices.IndexFunc(rows, func(r settingsRow) bool { return r.item.path == v.selected })
	v.selected = rows[min(max(i+delta, 0), len(rows)-1)].item.path
}

// moveCategory steps through the list on the left, wrapping.
func (v *settingsView) moveCategory(delta int) {
	v.clearSearch()
	n := len(settingsCatalog)
	v.category = (v.category + delta + n) % n
	v.selectFirst()
	v.note = ""
}

// nextValue is the value one arrow press away, for a key the arrows can change.
func nextValue(item settingsItem, k configKey, current string, delta int) (string, bool) {
	switch {
	case k.kind == kindBool || k.kind == kindEnum:
		return k.choices[wrapIndex(indexOf(k.choices, current)+delta, len(k.choices))], true
	case k.kind == kindInt:
		n, err := strconv.Atoi(strings.TrimSpace(current))
		if err != nil {
			return "", false
		}
		return strconv.Itoa(max(n+delta, 0)), true
	case len(item.presets) > 0:
		at := slices.IndexFunc(item.presets, func(p string) bool { return sameDuration(p, current) })
		if at < 0 {
			return item.presets[0], true
		}
		return item.presets[wrapIndex(at+delta, len(item.presets))], true
	}
	return "", false
}

// sameDuration compares two spellings of a duration: 1h and 1h0m0s are one value.
func sameDuration(a, b string) bool {
	x, errA := time.ParseDuration(a)
	y, errB := time.ParseDuration(b)
	return errA == nil && errB == nil && x == y
}

// change is an arrow press on the selected setting.
func (v *settingsView) change(delta int) tea.Cmd {
	row, ok := v.current()
	if !ok || !v.configOK {
		return nil
	}
	value, ok := nextValue(row.item, row.key, v.value(row.key), delta)
	if !ok {
		v.note, v.noteBad = "press enter to edit this setting", false
		return nil
	}
	return v.apply(row.key, value)
}

// reset puts the selected setting back to its built-in default.
func (v *settingsView) reset() tea.Cmd {
	row, ok := v.current()
	if !ok || !v.configOK || row.key.def() == v.value(row.key) {
		return nil
	}
	return v.apply(row.key, row.key.def())
}

// settingsUndo is one change `u` can take back.
type settingsUndo struct {
	key configKey
	was string
}

// undoLast is `u`: the last saved change's setting back to what it was.
func (v *settingsView) undoLast() tea.Cmd {
	u := v.undo
	if u == nil || !v.configOK {
		v.note, v.noteBad = "nothing to undo", false
		return nil
	}
	v.undoing = true
	// #1137: u may be pressed in another category, or under a search that
	// hides the setting; go to it, so the write is not made off screen.
	if !v.goTo(u.key.path) {
		v.selected = u.key.path
	}
	return v.apply(u.key, u.was)
}

// apply sends a value, or stops at the confirmation for a dangerous key.
func (v *settingsView) apply(k configKey, value string) tea.Cmd {
	if v.saving == nil && (v.burstPath != k.path || v.queued == nil) {
		v.burstPath, v.burstWas = k.path, v.value(k)
	}
	if k.dangerous {
		v.openForm(k.path)
		v.form.setValue(value)
		v.form.confirming = true
		return nil
	}
	change := settingsChange{k, value}
	v.shown[k.path] = value
	v.note = ""
	if v.saving != nil {
		v.queued = &change
		return nil
	}
	return v.send(change)
}

// send writes one change.
func (v *settingsView) send(change settingsChange) tea.Cmd {
	patch, err := change.key.write(change.value)
	if err == nil && v.client == nil {
		err = errDaemonUnreachable
	}
	if err != nil {
		delete(v.shown, change.key.path)
		v.undoing = false
		v.note, v.noteBad = errString(err), true
		return nil
	}
	v.saving = &change
	client, path := v.client, change.key.path
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), loadTimeout)
		defer cancel()
		cfg, err := client.PatchConfig(ctx, patch)
		return configSavedMsg{path: path, cfg: cfg, err: err}
	}
}

// openFile hands config.yaml to $EDITOR. The daemon watches the file, reloads
// a valid edit and keeps the last good configuration otherwise.
func (v *settingsView) openFile() tea.Cmd {
	if !v.infoOK || v.info.ConfigPath == "" {
		v.note, v.noteBad = "the daemon has not said where config.yaml is yet", true
		return nil
	}
	return openEditorPath(v.exec, v.info.ConfigPath, func(err error) tea.Msg { return settingsEditedMsg{err} })
}

// changedIn counts the settings of one category that differ from their default.
func (v *settingsView) changedIn(category int) int {
	n := 0
	for _, section := range settingsCatalog[category].sections {
		for _, item := range section.items {
			if k, ok := v.key(item.path); ok && v.configOK && k.def() != v.value(k) {
				n++
			}
		}
	}
	return n
}

// settingsNavHit is one category's rows in the list on the left.
type settingsNavHit struct {
	y0, y1, category int
}

// settingsHit is one drawn line of a setting: where it is, which setting, and
// its text, so a click on its stepper's − or + can be told from one on its
// label.
type settingsHit struct {
	y, x0 int
	path  string
	text  string
}

// updateClick is a click on the layout (#1065): a category selects it, a
// setting selects it, and a click on the selected setting's − or + changes
// it as ←/→ do. The narrow layout's settings take the same clicks (#1144).
// The key bindings list is not this layout, so it takes none.
func (v *settingsView) updateClick(msg tea.MouseClickMsg) tea.Cmd {
	if msg.Button != tea.MouseLeft || v.form != nil || v.bindings != nil {
		return nil
	}
	if v.frameless() && msg.X < settingsNavW {
		for _, h := range v.navHits {
			if msg.Y >= h.y0 && msg.Y < h.y1 {
				v.clearSearch()
				v.category = h.category
				v.selectFirst()
				v.note = ""
				return nil
			}
		}
		return nil
	}
	for _, h := range v.paneHits {
		if msg.Y != h.y {
			continue
		}
		if h.path == v.selected {
			if at := msg.X - h.x0; at >= 0 {
				r := []rune(h.text)
				if at < len(r) {
					switch r[at] {
					case '−':
						return v.change(-1)
					case '+':
						return v.change(1)
					}
				}
			}
			return nil
		}
		v.selected = h.path
		return nil
	}
	return nil
}
